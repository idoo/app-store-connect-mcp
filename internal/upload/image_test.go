package upload

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// pngChunk encodes one PNG chunk with its CRC.
func pngChunk(typ string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(append(b, typ...), data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}

// headerPNG builds a w×h PNG of colorType with only the chunks the
// validator reads (IHDR, extra chunks, IEND): no pixel data to encode.
func headerPNG(w, h int, colorType byte, extra ...[]byte) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(w))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(h))
	ihdr = append(ihdr, 8, colorType, 0, 0, 0)
	b := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
	for _, c := range extra {
		b = append(b, c...)
	}
	return append(b, pngChunk("IEND", nil)...)
}

// headerJPEG builds a JPEG with just SOI, a JFIF APP0, a baseline SOF0 frame
// header for w×h and EOI: enough for jpeg.DecodeConfig.
func headerJPEG(w, h int) []byte {
	b := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	b = append(b, 0xFF, 0xC0, 0x00, 0x0B, 0x08)
	b = binary.BigEndian.AppendUint16(b, uint16(h))
	b = binary.BigEndian.AppendUint16(b, uint16(w))
	return append(b, 0x01, 0x01, 0x11, 0x00, 0xFF, 0xD9)
}

func TestValidateImage(t *testing.T) {
	opaquePNG := headerPNG(1280, 800, 2)
	pal := headerPNG(1280, 800, 3, pngChunk("PLTE", []byte{0, 0, 0, 255, 255, 255}), pngChunk("tRNS", []byte{0}))
	cases := []struct {
		name    string
		data    []byte
		display applemeta.DisplayType
		want    string
	}{
		{"opaque png", opaquePNG, applemeta.AppDesktop, ""},
		{"jpeg", headerJPEG(1280, 800), applemeta.AppDesktop, ""},
		{"alpha png", headerPNG(1280, 800, 6), applemeta.AppDesktop, "alpha channel"},
		{"trns png", pal, applemeta.AppDesktop, "tRNS"},
		{"wrong size", opaquePNG, applemeta.AppIPhone65, "not an accepted size"},
		{"gif", []byte("GIF89a\x04\x00\x04\x00\x00\x00\x00;"), applemeta.AppDesktop, "PNG or JPEG"},
		{"unknown display", opaquePNG, "APP_FRIDGE", "unknown display"},
		{"truncated", opaquePNG[:20], applemeta.AppDesktop, "png header"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateImage(bytes.NewReader(c.data), c.display)
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			mustContain(t, err, c.want)
			if !errors.Is(err, tool.ErrInvalidArgs) {
				t.Errorf("not invalid_arguments: %v", err)
			}
		})
	}
}
