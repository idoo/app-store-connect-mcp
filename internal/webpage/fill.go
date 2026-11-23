package webpage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// SecretKind reports a control kind whose value from the page is sent but
// never shown unless the caller answers it: a hidden control or a password
// field.
func SecretKind(kind string) bool {
	return kind == KindHidden || kind == "password"
}

// entry is one name and value of the entry list, before encoding.
type entry struct {
	name, value string
	file        *File // set for a file part; value is then the file name
	hidden      bool  // the page's own secret value, sent but never shown
	answered    bool  // a hidden control whose value the caller gave
}

// Fill merges a into the form's defaults and encodes the body a browser
// would send: the successful controls in document order, a disabled control
// never, a box only when ticked, the named submitter only. Problems name
// every answer that fits no field or breaks the field's own rules
// (required, an option that isn't offered, maxlength, a file for a control
// that takes none); with problems Filled is the zero value.
func (f Form) Fill(a Answers) (Filled, []Problem) {
	var problems []Problem
	problem := func(field, msg string) { problems = append(problems, Problem{Field: field, Message: msg}) }

	values := f.assignValues(a.Values, problem)
	files := f.assignFiles(a.Files, problem)
	submitter := f.submitter(a.Submitter, problem)

	var entries []entry
	for i, fd := range f.Fields {
		if fd.Disabled {
			continue
		}
		vals, answered := values[i]
		if !answered {
			vals = fd.Values
		}
		switch fd.Kind {
		case KindSubmit:
			if i == submitter {
				entries = append(entries, entry{name: fd.Name, value: first(fd.Values)})
			}
		case KindFile:
			if file, ok := files[i]; ok {
				entries = append(entries, entry{name: fd.Name, value: file.Name, file: &file})
				continue
			}
			if fd.Required {
				problem(fd.Name, "is required: give a file")
			}
			if f.Enctype == enctypeMultipart {
				entries = append(entries, entry{name: fd.Name, file: &File{}})
			}
		case KindCheckbox, KindRadio, KindSelect:
			if answered {
				checkChoices(fd, vals, problem)
			}
			chosen := 0
			allEmpty := true
			for _, o := range fd.Options {
				if !o.Disabled && slices.Contains(vals, o.Value) {
					entries = append(entries, entry{name: fd.Name, value: o.Value})
					chosen++
					allEmpty = allEmpty && o.Value == ""
				}
			}
			if fd.Required && (chosen == 0 || fd.Kind == KindSelect && allEmpty) {
				problem(fd.Name, "is required: choose an option")
			}
		default:
			if len(vals) > 1 {
				problem(fd.Name, "takes one value, got "+strconv.Itoa(len(vals)))
			}
			v := first(vals)
			if answered && fd.Kind != KindTextarea && fd.Kind != KindHidden {
				v = strings.NewReplacer("\r", "", "\n", "").Replace(v)
			}
			if fd.Required && v == "" {
				problem(fd.Name, "is required")
			}
			if answered && fd.MaxLength > 0 {
				if n := utf16Len(toLF(v)); n > fd.MaxLength {
					problem(fd.Name, "is "+strconv.Itoa(n)+" characters long, at most "+strconv.Itoa(fd.MaxLength)+" allowed")
				}
			}
			entries = append(entries, entry{
				name:     fd.Name,
				value:    v,
				hidden:   !answered && SecretKind(fd.Kind) && (fd.Kind == KindHidden || v != ""),
				answered: answered && fd.Kind == KindHidden,
			})
		}
	}
	if len(problems) > 0 {
		return Filled{}, problems
	}

	for i := range entries {
		entries[i].name = toCRLF(entries[i].name)
		if entries[i].file == nil {
			entries[i].value = toCRLF(entries[i].value)
		}
	}
	out := Filled{Pairs: make([]Pair, 0, len(entries))}
	for _, e := range entries {
		out.Pairs = append(out.Pairs, Pair{Name: e.name, Value: e.value, File: e.file != nil, Hidden: e.hidden, Answered: e.answered})
	}
	if f.Enctype == enctypeMultipart {
		boundary, err := newBoundary()
		if err != nil {
			return Filled{}, []Problem{{Message: "can't make a multipart boundary: " + err.Error()}}
		}
		out.ContentType = enctypeMultipart + "; boundary=" + boundary
		out.Body = encodeMultipart(entries, boundary)
	} else {
		out.ContentType = enctypeURLEncoded
		out.Body = encodeURLEncoded(entries)
	}
	return out, nil
}

// assignValues maps each answered name to the fields it fills: one field
// takes all its values; fields sharing a name take one value each, in
// order. A hidden field beside a visible one of the same name is left to
// its default.
func (f Form) assignValues(answers map[string][]string, problem func(string, string)) map[int][]string {
	out := map[int][]string{}
	for _, name := range sortedKeys(answers) {
		vals := answers[name]
		var targets []int
		named, file := false, false
		for i, fd := range f.Fields {
			if fd.Name != name {
				continue
			}
			named = true
			switch fd.Kind {
			case KindFile:
				file = true
			case KindSubmit:
			default:
				targets = append(targets, i)
			}
		}
		switch {
		case !named:
			problem(name, "the form has no field by this name")
			continue
		case len(targets) == 0 && file:
			problem(name, "takes a file, not a value")
			continue
		case len(targets) == 0:
			problem(name, "is a submit control: name it as the submitter")
			continue
		}
		if visible := keepIf(targets, func(i int) bool { return f.Fields[i].Kind != KindHidden }); len(visible) > 0 {
			targets = visible
		}
		enabled := keepIf(targets, func(i int) bool { return !f.Fields[i].Disabled })
		if len(enabled) == 0 {
			problem(name, "is disabled")
			continue
		}
		switch {
		case len(enabled) == 1:
			out[enabled[0]] = vals
		case len(vals) == len(enabled):
			for k, i := range enabled {
				out[i] = vals[k : k+1]
			}
		default:
			problem(name, strconv.Itoa(len(enabled))+" fields share this name: give one value for each")
		}
	}
	return out
}

// assignFiles maps each file to the first enabled file field of its name.
func (f Form) assignFiles(files map[string]File, problem func(string, string)) map[int]File {
	out := map[int]File{}
	for _, name := range sortedKeys(files) {
		named, fileField, target := false, false, -1
		for i, fd := range f.Fields {
			if fd.Name != name {
				continue
			}
			named = true
			if fd.Kind == KindFile {
				fileField = true
				if !fd.Disabled && target < 0 {
					target = i
				}
			}
		}
		switch {
		case !named:
			problem(name, "the form has no field by this name")
		case !fileField:
			problem(name, "takes no file")
		case target < 0:
			problem(name, "is disabled")
		case f.Enctype != enctypeMultipart:
			problem(name, "the form does not send files")
		default:
			out[target] = files[name]
		}
	}
	return out
}

// submitter finds the submit field named by s ("name" or "name=value");
// -1 when s is empty or names none.
func (f Form) submitter(s string, problem func(string, string)) int {
	if s == "" {
		return -1
	}
	match := func(name string, value *string) []int {
		var out []int
		for i, fd := range f.Fields {
			if fd.Kind == KindSubmit && fd.Name == name && (value == nil || first(fd.Values) == *value) {
				out = append(out, i)
			}
		}
		return out
	}
	cands := match(s, nil)
	if len(cands) == 0 {
		if k := strings.LastIndexByte(s, '='); k >= 0 {
			v := s[k+1:]
			cands = match(s[:k], &v)
		}
	}
	if len(cands) == 0 {
		problem(s, "the form has no submit control by this name")
		return -1
	}
	for _, i := range cands[1:] {
		if first(f.Fields[i].Values) != first(f.Fields[cands[0]].Values) {
			problem(s, "several submit controls share this name: give name=value")
			return -1
		}
	}
	for _, i := range cands {
		if !f.Fields[i].Disabled {
			return i
		}
	}
	problem(s, "is disabled")
	return -1
}

// checkChoices reports answered values a radio, checkbox or select can't
// take.
func checkChoices(fd Field, vals []string, problem func(string, string)) {
	if len(vals) > 1 && (fd.Kind == KindRadio || fd.Kind == KindSelect && !fd.Multiple) {
		problem(fd.Name, "takes one value, got "+strconv.Itoa(len(vals)))
	}
	for _, v := range vals {
		offered := slices.ContainsFunc(fd.Options, func(o Option) bool { return !o.Disabled && o.Value == v })
		if !offered {
			problem(fd.Name, strconv.Quote(v)+" is not an offered option")
		}
	}
}

func first(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func keepIf(idx []int, keep func(int) bool) []int {
	var out []int
	for _, i := range idx {
		if keep(i) {
			out = append(out, i)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// utf16Len counts s in UTF-16 code units, as maxlength does.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// toLF turns CRLF and lone CR into LF.
func toLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// toCRLF turns every line break into CRLF, as form submission does.
func toCRLF(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.ReplaceAll(toLF(s), "\n", "\r\n")
}

// encodeURLEncoded is the application/x-www-form-urlencoded serializer.
func encodeURLEncoded(entries []entry) []byte {
	var b bytes.Buffer
	for i, e := range entries {
		if i > 0 {
			b.WriteByte('&')
		}
		formEscape(&b, e.name)
		b.WriteByte('=')
		formEscape(&b, e.value)
	}
	return b.Bytes()
}

func formEscape(b *bytes.Buffer, s string) {
	const hexDigits = "0123456789ABCDEF"
	for i := range len(s) {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
	}
}

func newBoundary() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "----FormBoundary" + hex.EncodeToString(raw[:]), nil
}

var headerEscaper = strings.NewReplacer(`"`, "%22", "\r", "%0D", "\n", "%0A")

// encodeMultipart is the multipart/form-data encoder.
func encodeMultipart(entries []entry, boundary string) []byte {
	var b bytes.Buffer
	for _, e := range entries {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString(`Content-Disposition: form-data; name="` + headerEscaper.Replace(e.name) + `"`)
		if e.file != nil {
			b.WriteString(`; filename="` + headerEscaper.Replace(e.file.Name) + `"` + "\r\n")
			b.WriteString("Content-Type: " + fileMIME(e.file.MIME) + "\r\n\r\n")
			b.Write(e.file.Data)
		} else {
			b.WriteString("\r\n\r\n")
			b.WriteString(e.value)
		}
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}

func fileMIME(m string) string {
	m = strings.TrimSpace(m)
	if m == "" || strings.ContainsAny(m, "\r\n") {
		return "application/octet-stream"
	}
	return m
}
