package upload

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"io"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// maxPNGChunk is the PNG specification's maximum chunk length (2^31-1).
const maxPNGChunk = 1<<31 - 1

// maxPNGDimension bounds accepted PNG width and height; App Store
// screenshots are far smaller.
const maxPNGDimension = 1 << 16

// ValidateImage checks a screenshot before upload: it must be a PNG or JPEG,
// a PNG must have no alpha channel or transparency chunk, and the pixel
// size must be accepted by display. Only the image header is decoded.
func ValidateImage(r io.Reader, display applemeta.DisplayType) error {
	br := bufio.NewReader(r)
	head, err := br.Peek(len(pngSignature))
	if err != nil {
		return fmt.Errorf("%w: image too short: %w", tool.ErrInvalidArgs, err)
	}
	var w, h int
	if bytes.Equal(head, pngSignature) {
		if w, h, err = pngHeader(br); err != nil {
			return err
		}
	} else {
		if head[0] != 0xFF || head[1] != 0xD8 {
			return fmt.Errorf("%w: screenshots must be PNG or JPEG", tool.ErrInvalidArgs)
		}
		cfg, err := jpeg.DecodeConfig(br)
		if err != nil {
			return fmt.Errorf("%w: decode jpeg: %w", tool.ErrInvalidArgs, err)
		}
		w, h = cfg.Width, cfg.Height
	}
	if _, ok := applemeta.LookupDisplay(display); !ok {
		return fmt.Errorf("%w: unknown display type %q", tool.ErrInvalidArgs, display)
	}
	if !applemeta.ValidPixels(display, w, h) {
		return fmt.Errorf("%w: %dx%d is not an accepted size for %s", tool.ErrInvalidArgs, w, h, display)
	}
	return nil
}

// pngHeader returns the PNG size and rejects transparency: IHDR color types
// with alpha (4 gray+alpha, 6 RGBA) or a tRNS chunk before the image data.
func pngHeader(br *bufio.Reader) (int, int, error) {
	// Signature (8) + IHDR length/type (8) + IHDR data (13) + CRC (4).
	peek, err := br.Peek(33)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: png header: %w", tool.ErrInvalidArgs, err)
	}
	if string(peek[12:16]) != "IHDR" {
		return 0, 0, fmt.Errorf("%w: png header: first chunk is not IHDR", tool.ErrInvalidArgs)
	}
	w, h := binary.BigEndian.Uint32(peek[16:20]), binary.BigEndian.Uint32(peek[20:24])
	if w == 0 || h == 0 || w > maxPNGDimension || h > maxPNGDimension {
		return 0, 0, fmt.Errorf("%w: png size %dx%d", tool.ErrInvalidArgs, w, h)
	}
	if ct := peek[25]; ct == 4 || ct == 6 {
		return 0, 0, fmt.Errorf("%w: png has an alpha channel; App Store screenshots must be opaque", tool.ErrInvalidArgs)
	}
	// Walk chunks up to IDAT looking for tRNS (transparency on RGB/gray/palette).
	if _, err := br.Discard(len(pngSignature)); err != nil {
		return 0, 0, fmt.Errorf("%w: read png: %w", tool.ErrInvalidArgs, err)
	}
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return 0, 0, fmt.Errorf("%w: read png chunk: %w", tool.ErrInvalidArgs, err)
		}
		n := binary.BigEndian.Uint32(hdr[:4])
		switch string(hdr[4:]) {
		case "tRNS":
			return 0, 0, fmt.Errorf("%w: png has transparency (tRNS); App Store screenshots must be opaque", tool.ErrInvalidArgs)
		case "IDAT", "IEND":
			return int(w), int(h), nil
		}
		if n > maxPNGChunk {
			return 0, 0, fmt.Errorf("%w: png chunk of %d bytes", tool.ErrInvalidArgs, n)
		}
		// Skip the chunk data and its CRC.
		if _, err := br.Discard(int(n) + 4); err != nil {
			return 0, 0, fmt.Errorf("%w: read png chunk: %w", tool.ErrInvalidArgs, err)
		}
	}
}
