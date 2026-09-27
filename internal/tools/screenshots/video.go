package screenshots

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
)

// videoInfo is what the MP4/QuickTime container says about a video.
type videoInfo struct {
	Seconds       float64
	Width, Height int
}

// maxBoxes bounds the boxes probeVideo visits, so a hostile file can't spin.
const maxBoxes = 4096

var errNoMovie = errors.New("no moov/mvhd box")

// probeVideo reads duration (moov/mvhd) and the first video track's display
// size (moov/trak/tkhd, rotation applied) without decoding: it seeks past
// mdat, so peak memory is one header box.
func probeVideo(r io.ReadSeeker, size int64) (videoInfo, error) {
	var info videoInfo
	visited := 0
	var walk func(start, end int64, depth int) error
	walk = func(start, end int64, depth int) error {
		for off := start; off+8 <= end; {
			if visited++; visited > maxBoxes {
				return errors.New("too many boxes")
			}
			var h [16]byte
			if _, err := r.Seek(off, io.SeekStart); err != nil {
				return err
			}
			if _, err := io.ReadFull(r, h[:8]); err != nil {
				return err
			}
			boxSize, typ, hdr := int64(binary.BigEndian.Uint32(h[:4])), string(h[4:8]), int64(8)
			switch boxSize {
			case 0:
				boxSize = end - off
			case 1:
				if _, err := io.ReadFull(r, h[8:16]); err != nil {
					return err
				}
				boxSize, hdr = int64(binary.BigEndian.Uint64(h[8:16])), 16 //nolint:gosec // bounds-checked below
			}
			if boxSize < hdr || off+boxSize > end {
				return fmt.Errorf("box %q overruns its parent", typ)
			}
			body, bodyLen := off+hdr, boxSize-hdr
			switch {
			case typ == "moov" && depth == 0, typ == "trak" && depth == 1:
				if err := walk(body, off+boxSize, depth+1); err != nil {
					return err
				}
			case typ == "mvhd" && depth == 1:
				if err := readMvhd(r, bodyLen, &info); err != nil {
					return err
				}
			case typ == "tkhd" && depth == 2 && info.Width == 0:
				if err := readTkhd(r, bodyLen, &info); err != nil {
					return err
				}
			}
			off += boxSize
		}
		return nil
	}
	if err := walk(0, size, 0); err != nil {
		return info, err
	}
	if info.Seconds == 0 {
		return info, errNoMovie
	}
	return info, nil
}

func readMvhd(r io.Reader, n int64, info *videoInfo) error {
	if n < 32 || n > 1<<10 {
		return errors.New("bad mvhd")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	var scale, dur uint64
	if b[0] == 1 {
		scale, dur = uint64(binary.BigEndian.Uint32(b[20:24])), binary.BigEndian.Uint64(b[24:32])
	} else {
		scale, dur = uint64(binary.BigEndian.Uint32(b[12:16])), uint64(binary.BigEndian.Uint32(b[16:20]))
	}
	if scale == 0 {
		return errors.New("mvhd timescale is 0")
	}
	info.Seconds = float64(dur) / float64(scale)
	return nil
}

func readTkhd(r io.Reader, n int64, info *videoInfo) error {
	if n < 84 || n > 1<<10 {
		return errors.New("bad tkhd")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	matrix, wh := 40, 76
	if b[0] == 1 {
		if n < 96 {
			return errors.New("bad tkhd")
		}
		matrix, wh = 52, 88
	}
	w := int(binary.BigEndian.Uint32(b[wh:wh+4]) >> 16)
	h := int(binary.BigEndian.Uint32(b[wh+4:wh+8]) >> 16)
	if w == 0 || h == 0 {
		return nil // audio track
	}
	if a, bb := binary.BigEndian.Uint32(b[matrix:matrix+4]), binary.BigEndian.Uint32(b[matrix+4:matrix+8]); a == 0 && bb != 0 {
		w, h = h, w // rotated 90°
	}
	info.Width, info.Height = w, h
	return nil
}

var frameCodeRE = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2}):(\d{2})$`)

// checkPreview validates a preview before anything is reserved: the poster
// frame code format, and (when the container is readable) duration, poster
// time within the video and resolution. Format, duration and poster errors
// are returned; a resolution mismatch or an unreadable container is a
// warning, since Apple re-checks after upload.
func checkPreview(r io.ReadSeeker, size int64, pvType, frameCode string) (warnings []string, err error) {
	var poster float64
	if frameCode != "" {
		m := frameCodeRE.FindStringSubmatch(frameCode)
		if m == nil {
			return nil, fmt.Errorf("%w: preview_frame_time_code %q must be HH:MM:SS:FF, e.g. 00:00:05:00", tool.ErrInvalidArgs, frameCode)
		}
		hh, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		ss, _ := strconv.Atoi(m[3])
		poster = float64(hh*3600 + mm*60 + ss)
	}
	info, perr := probeVideo(r, size)
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind video: %w", err)
	}
	if perr != nil {
		return []string{"couldn't read the video's duration and size (" + perr.Error() + "); Apple checks them after upload"}, nil
	}
	if info.Seconds < applemeta.PreviewMinSeconds || info.Seconds > applemeta.PreviewMaxSeconds {
		return nil, fmt.Errorf("%w: preview is %.1f s; Apple accepts %d–%d s", tool.ErrInvalidArgs, info.Seconds, applemeta.PreviewMinSeconds, applemeta.PreviewMaxSeconds)
	}
	if poster > info.Seconds {
		return nil, fmt.Errorf("%w: preview_frame_time_code %s is past the end of the %.1f s video", tool.ErrInvalidArgs, frameCode, info.Seconds)
	}
	sizes := applemeta.PreviewSizes(applemeta.PreviewType(pvType))
	if len(sizes) == 0 || info.Width == 0 {
		return nil, nil
	}
	for _, s := range sizes {
		if (s.W == info.Width && s.H == info.Height) || (s.W == info.Height && s.H == info.Width) {
			return nil, nil
		}
	}
	return []string{fmt.Sprintf("video is %dx%d but %s previews are %v (either orientation); Apple may reject it after processing", info.Width, info.Height, pvType, sizes)}, nil
}
