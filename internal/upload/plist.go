package upload

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// maxPlistDepth bounds nesting of arrays and dictionaries.
const maxPlistDepth = 64

var errPlist = errors.New("plist")

// ParsePlist decodes a binary (bplist00) or XML property list. Values map to
// map[string]any, []any, string, int64, float64, bool and []byte; dates are
// returned as strings (XML) or float64 seconds since 2001 (binary).
func ParsePlist(b []byte) (any, error) {
	if bytes.HasPrefix(b, []byte("bplist00")) {
		return parseBinaryPlist(b)
	}
	return parseXMLPlist(b)
}

// ---- binary ----

type bplist struct {
	data    []byte
	offsets []uint64
	refSize uint64
	active  []bool
}

func parseBinaryPlist(b []byte) (any, error) {
	const header, trailer = 8, 32
	if len(b) < header+trailer {
		return nil, fmt.Errorf("%w: binary plist too short", errPlist)
	}
	t := b[len(b)-trailer:]
	offSize, refSize := uint64(t[6]), uint64(t[7])
	numObjects := binary.BigEndian.Uint64(t[8:16])
	top := binary.BigEndian.Uint64(t[16:24])
	tableAt := binary.BigEndian.Uint64(t[24:32])
	body := uint64(len(b) - trailer) //nolint:gosec // len(b) ≥ header+trailer, so non-negative
	switch {
	case !validIntSize(offSize) || !validIntSize(refSize):
		return nil, fmt.Errorf("%w: bad offset/ref size %d/%d", errPlist, offSize, refSize)
	case numObjects == 0 || numObjects > body || top >= numObjects:
		return nil, fmt.Errorf("%w: bad object count %d (top %d)", errPlist, numObjects, top)
	case tableAt < header || tableAt > body || (body-tableAt)/offSize < numObjects:
		return nil, fmt.Errorf("%w: offset table out of range", errPlist)
	}
	p := &bplist{data: b[:body], refSize: refSize, offsets: make([]uint64, numObjects), active: make([]bool, numObjects)}
	for i := range numObjects {
		at := tableAt + i*offSize
		p.offsets[i] = beUint(b[at : at+offSize])
	}
	return p.object(top, 0)
}

func validIntSize(n uint64) bool { return n == 1 || n == 2 || n == 4 || n == 8 }

func beUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// bytesAt returns n bytes at off, or an error when out of range.
func (p *bplist) bytesAt(off, n uint64) ([]byte, error) {
	if off > uint64(len(p.data)) || n > uint64(len(p.data))-off {
		return nil, fmt.Errorf("%w: object out of range", errPlist)
	}
	return p.data[off : off+n], nil
}

// length decodes the count of a variable-size object whose marker info is
// info, returning the count and the offset of the payload.
func (p *bplist) length(info byte, off uint64) (uint64, uint64, error) {
	if info != 0x0F {
		return uint64(info), off, nil
	}
	m, err := p.bytesAt(off, 1)
	if err != nil {
		return 0, 0, err
	}
	if m[0]>>4 != 0x1 || m[0]&0x0F > 3 {
		return 0, 0, fmt.Errorf("%w: bad length marker %#x", errPlist, m[0])
	}
	n := uint64(1) << (m[0] & 0x0F)
	v, err := p.bytesAt(off+1, n)
	if err != nil {
		return 0, 0, err
	}
	return beUint(v), off + 1 + n, nil
}

func (p *bplist) refs(off, count uint64) ([]uint64, error) {
	if count > uint64(len(p.data)) {
		return nil, fmt.Errorf("%w: collection too large", errPlist)
	}
	raw, err := p.bytesAt(off, count*p.refSize)
	if err != nil {
		return nil, err
	}
	out := make([]uint64, count)
	for i := range count {
		out[i] = beUint(raw[i*p.refSize : (i+1)*p.refSize])
	}
	return out, nil
}

func (p *bplist) object(ref uint64, depth int) (any, error) {
	if ref >= uint64(len(p.offsets)) {
		return nil, fmt.Errorf("%w: object ref %d out of range", errPlist, ref)
	}
	if depth > maxPlistDepth || p.active[ref] {
		return nil, fmt.Errorf("%w: nesting too deep or cyclic", errPlist)
	}
	off := p.offsets[ref]
	m, err := p.bytesAt(off, 1)
	if err != nil {
		return nil, err
	}
	kind, info := m[0]>>4, m[0]&0x0F
	off++
	switch kind {
	case 0x0:
		switch info {
		case 0x8:
			return false, nil
		case 0x9:
			return true, nil
		case 0x0:
			return nil, nil
		}
	case 0x1:
		if info > 3 {
			return nil, fmt.Errorf("%w: unsupported integer width", errPlist)
		}
		v, err := p.bytesAt(off, 1<<info)
		if err != nil {
			return nil, err
		}
		return int64(beUint(v)), nil //nolint:gosec // 8-byte plist ints are two's complement
	case 0x2:
		switch info {
		case 2:
			v, err := p.bytesAt(off, 4)
			if err != nil {
				return nil, err
			}
			return float64(math.Float32frombits(binary.BigEndian.Uint32(v))), nil
		case 3:
			v, err := p.bytesAt(off, 8)
			if err != nil {
				return nil, err
			}
			return math.Float64frombits(binary.BigEndian.Uint64(v)), nil
		}
	case 0x3:
		v, err := p.bytesAt(off, 8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(v)), nil
	case 0x4, 0x5, 0x6:
		n, at, err := p.length(info, off)
		if err != nil {
			return nil, err
		}
		if kind == 0x6 {
			if n > uint64(len(p.data))/2 {
				return nil, fmt.Errorf("%w: string too long", errPlist)
			}
			n *= 2
		}
		v, err := p.bytesAt(at, n)
		if err != nil {
			return nil, err
		}
		switch kind {
		case 0x4:
			return bytes.Clone(v), nil
		case 0x5:
			return string(v), nil
		}
		u := make([]uint16, len(v)/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(v[2*i:])
		}
		return string(utf16.Decode(u)), nil
	case 0xA, 0xD:
		n, at, err := p.length(info, off)
		if err != nil {
			return nil, err
		}
		width := n
		if kind == 0xD {
			if n > uint64(len(p.data)) {
				return nil, fmt.Errorf("%w: dictionary too large", errPlist)
			}
			width = 2 * n
		}
		refs, err := p.refs(at, width)
		if err != nil {
			return nil, err
		}
		p.active[ref] = true
		defer func() { p.active[ref] = false }()
		if kind == 0xA {
			arr := make([]any, 0, n)
			for _, r := range refs {
				v, err := p.object(r, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			return arr, nil
		}
		dict := make(map[string]any, n)
		for i := range n {
			k, err := p.object(refs[i], depth+1)
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("%w: dictionary key is %T", errPlist, k)
			}
			v, err := p.object(refs[n+i], depth+1)
			if err != nil {
				return nil, err
			}
			dict[ks] = v
		}
		return dict, nil
	}
	return nil, fmt.Errorf("%w: unsupported object marker %#x", errPlist, m[0])
}

// ---- XML ----

func parseXMLPlist(b []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: no plist element: %w", errPlist, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "plist" {
			return nil, fmt.Errorf("%w: root element <%s>", errPlist, se.Name.Local)
		}
		start, err := nextStart(dec)
		if err != nil {
			return nil, err
		}
		return xmlValue(dec, start, 0)
	}
}

// nextStart returns the next start element, failing on an end element.
func nextStart(dec *xml.Decoder) (xml.StartElement, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return xml.StartElement{}, fmt.Errorf("%w: %w", errPlist, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, nil
		case xml.EndElement:
			return xml.StartElement{}, fmt.Errorf("%w: unexpected </%s>", errPlist, t.Name.Local)
		}
	}
}

func xmlValue(dec *xml.Decoder, se xml.StartElement, depth int) (any, error) {
	if depth > maxPlistDepth {
		return nil, fmt.Errorf("%w: nesting too deep", errPlist)
	}
	switch se.Name.Local {
	case "dict":
		dict := map[string]any{}
		for {
			key, end, err := childOrEnd(dec)
			if err != nil || end {
				return dict, err
			}
			if key.Name.Local != "key" {
				return nil, fmt.Errorf("%w: <%s> where <key> expected", errPlist, key.Name.Local)
			}
			name, err := xmlText(dec)
			if err != nil {
				return nil, err
			}
			vs, err := nextStart(dec)
			if err != nil {
				return nil, err
			}
			v, err := xmlValue(dec, vs, depth+1)
			if err != nil {
				return nil, err
			}
			dict[name] = v
		}
	case "array":
		arr := []any{}
		for {
			child, end, err := childOrEnd(dec)
			if err != nil || end {
				return arr, err
			}
			v, err := xmlValue(dec, child, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, fmt.Errorf("%w: %w", errPlist, err)
		}
		return se.Name.Local == "true", nil
	}
	text, err := xmlText(dec)
	if err != nil {
		return nil, err
	}
	switch se.Name.Local {
	case "string", "date":
		return text, nil
	case "integer":
		v, err := strconv.ParseInt(strings.TrimSpace(text), 0, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: integer %q", errPlist, text)
		}
		return v, nil
	case "real":
		v, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return nil, fmt.Errorf("%w: real %q", errPlist, text)
		}
		return v, nil
	case "data":
		v, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), ""))
		if err != nil {
			return nil, fmt.Errorf("%w: data: %w", errPlist, err)
		}
		return v, nil
	}
	return nil, fmt.Errorf("%w: unsupported element <%s>", errPlist, se.Name.Local)
}

// childOrEnd returns the next child start element, or end=true at the
// parent's end element.
func childOrEnd(dec *xml.Decoder) (xml.StartElement, bool, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return xml.StartElement{}, false, fmt.Errorf("%w: %w", errPlist, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, false, nil
		case xml.EndElement:
			return xml.StartElement{}, true, nil
		}
	}
}

// xmlText reads character data up to the current element's end.
func xmlText(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return "", fmt.Errorf("%w: %w", errPlist, err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		case xml.StartElement:
			return "", fmt.Errorf("%w: unexpected <%s> in text", errPlist, t.Name.Local)
		}
	}
}
