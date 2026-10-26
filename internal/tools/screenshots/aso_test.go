package screenshots

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func box(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(b, typ...), body...)
}

// mp4 builds a minimal MP4: ftyp, moov{mvhd, trak{tkhd}} and an mdat, with
// duration seconds at timescale 600 and a w×h video track.
func mp4(seconds float64, w, h int) []byte {
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:], 600)
	binary.BigEndian.PutUint32(mvhd[16:], uint32(seconds*600))
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[40:], 0x00010000) // identity matrix a
	binary.BigEndian.PutUint32(tkhd[76:], uint32(w)<<16)
	binary.BigEndian.PutUint32(tkhd[80:], uint32(h)<<16)
	audio := make([]byte, 84) // audio track: no size
	return slices.Concat(
		box("ftyp", []byte("isom\x00\x00\x02\x00")),
		box("mdat", bytes.Repeat([]byte{0xAB}, 64)), // mdat before moov, as in many exports
		box("moov", box("mvhd", mvhd), box("trak", box("tkhd", audio)), box("trak", box("tkhd", tkhd))),
	)
}

func TestProbeVideo(t *testing.T) {
	b := mp4(21.5, 886, 1920)
	info, err := probeVideo(bytes.NewReader(b), int64(len(b)))
	if err != nil || info.Seconds != 21.5 || info.Width != 886 || info.Height != 1920 {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if _, err := probeVideo(bytes.NewReader([]byte("not a movie")), 11); err == nil {
		t.Error("garbage parsed")
	}
	bad := box("moov", box("mvhd", make([]byte, 100)))
	bad[3] = 0xFF // size overruns the file
	if _, err := probeVideo(bytes.NewReader(bad), int64(len(bad))); err == nil {
		t.Error("overrunning box accepted")
	}
}

func TestPreviewChecksBeforeUpload(t *testing.T) {
	tests := []struct {
		name, frame string
		video       []byte
		wantErr     string
		wantWarn    string
	}{
		{name: "too short", video: mp4(10, 886, 1920), wantErr: "10.0 s"},
		{name: "too long", video: mp4(31, 886, 1920), wantErr: "31.0 s"},
		{name: "bad frame code", frame: "5s", video: mp4(20, 886, 1920), wantErr: "HH:MM:SS:FF"},
		{name: "poster past end", frame: "00:00:25:00", video: mp4(20, 886, 1920), wantErr: "past the end"},
		{name: "wrong resolution warns", frame: "00:00:05:00", video: mp4(20, 1000, 1000), wantWarn: "1000x1000"},
		{name: "landscape ok", video: mp4(20, 1920, 886)},
		{name: "unreadable container warns", video: []byte("bytes"), wantWarn: "couldn't read"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, _ := newKit(t)
			write(t, k, "media/p.mp4", tc.video)
			args := `{"locale":"en-US","file":"media/p.mp4","preview_type":"6.9"`
			if tc.frame != "" {
				args += `,"preview_frame_time_code":"` + tc.frame + `"`
			}
			res := call(t, k, "previews_upload", args+"}")
			if tc.wantErr != "" {
				b := tooltest.MustFail(t, res, "invalid_arguments")
				if !strings.Contains(b.Message, tc.wantErr) {
					t.Errorf("message = %q", b.Message)
				}
				if len(k.ASC.Mutations()) != 0 {
					t.Errorf("rejected preview still sent %v", mutations(k))
				}
				return
			}
			tooltest.MustOK(t, res)
			w := strings.Join(res.Meta.Warnings, "\n")
			if tc.wantWarn == "" && w != "" || !strings.Contains(w, tc.wantWarn) {
				t.Errorf("warnings = %q, want %q", w, tc.wantWarn)
			}
		})
	}
}

func TestPlanNaturalOrderHintsAndFoldedLocale(t *testing.T) {
	k, _ := newKit(t)
	for i, name := range []string{"10.png", "2.png", "1.png"} {
		write(t, k, "shots/en-US/"+name, pngImage(t, 1290, 2796, false, uint8(i+1)))
	}
	write(t, k, "shots/en-US/iPhone X-01.png", pngImage(t, 1125, 2436, false, 7)) // hint: legacy 5.8" slot
	write(t, k, "shots/de-CH/1.png", pngImage(t, 1290, 2796, false, 8))
	res := tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`))
	p := tooltest.Decode[planOut](t, res)
	if p.Status != statusPlanned {
		t.Fatalf("plan = %+v", p)
	}
	byKey := map[string][]string{}
	for _, s := range p.Sets {
		for _, u := range s.Uploads {
			byKey[s.Locale+" "+string(s.Display)] = append(byKey[s.Locale+" "+string(s.Display)], u.Rel)
		}
	}
	if got := byKey["en-US APP_IPHONE_67"]; !slices.Equal(got, []string{"en-US/1.png", "en-US/2.png", "en-US/10.png"}) {
		t.Errorf("order = %v", got)
	}
	if got := byKey["en-US "+string(applemeta.AppIPhone58)]; len(got) != 1 {
		t.Errorf("file-name hint ignored: %v", byKey)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "de-CH/ → de-DE") {
		t.Errorf("warnings = %v", p.Warnings)
	}
}

func TestNaturalCompare(t *testing.T) {
	in := []string{"a10", "a2", "a02", "b1", "a1x", "a1"}
	slices.SortFunc(in, naturalCompare)
	if want := []string{"a1", "a1x", "a2", "a02", "a10", "b1"}; !slices.Equal(in, want) {
		t.Errorf("sorted = %v want %v", in, want)
	}
}

func TestListCoverage(t *testing.T) {
	k, f := newKit(t)
	f.addShot("loc-en", "APP_IPHONE_61", "01.png", "abc")
	res := tooltest.MustOK(t, call(t, k, "screenshots_list", `{}`))
	c := tooltest.Decode[listOut](t, res).Coverage
	if c == nil || c.PrimaryLocale != "en-US" {
		t.Fatalf("coverage = %+v", c)
	}
	if !slices.Equal(c.FallsBackToPrimary, []string{"de-DE"}) {
		t.Errorf("falls back = %v", c.FallsBackToPrimary)
	}
	if len(c.MissingRequired) != 1 || c.MissingRequired[0].Locale != "en-US" ||
		!slices.Equal(c.MissingRequired[0].OneOf, []applemeta.DisplayType{applemeta.AppIPhone67, applemeta.AppIPhone65}) {
		t.Errorf("missing = %+v", c.MissingRequired)
	}
	if !strings.Contains(c.Note, "show the en-US ones") || !strings.Contains(c.Note, "iPad") {
		t.Errorf("note = %q", c.Note)
	}
}
