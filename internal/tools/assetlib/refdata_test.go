package assetlib

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func refData(t *testing.T) *RefData {
	t.Helper()
	raw, err := os.ReadFile("testdata/refdata.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseRefData(raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func encodePNG(t *testing.T, w, h int, alpha bool) []byte {
	t.Helper()
	var img image.Image
	if alpha {
		m := image.NewNRGBA(image.Rect(0, 0, w, h))
		m.SetNRGBA(0, 0, color.NRGBA{A: 10})
		img = m
	} else {
		img = image.NewGray(image.Rect(0, 0, w, h))
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func inspect(t *testing.T, name string, b []byte) FileInfo {
	t.Helper()
	f, err := Inspect(name, bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestInspect(t *testing.T) {
	f := inspect(t, "dir/Shot.PNG", encodePNG(t, 64, 32, true))
	if f.Name != "Shot.PNG" || f.Ext != ".png" || f.Width != 64 || f.Height != 32 || !f.Alpha || f.Video {
		t.Errorf("png = %+v", f)
	}
	if f := inspect(t, "opaque.png", encodePNG(t, 8, 9, false)); f.Alpha || f.Width != 8 || f.Height != 9 {
		t.Errorf("opaque png = %+v", f)
	}
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, image.NewGray(image.Rect(0, 0, 40, 30)), nil); err != nil {
		t.Fatal(err)
	}
	if f := inspect(t, "a.jpg", jb.Bytes()); f.Width != 40 || f.Height != 30 || f.Alpha {
		t.Errorf("jpeg = %+v", f)
	}
	if f := inspect(t, "clip.MOV", []byte("moov")); !f.Video || f.Ext != ".mov" || f.Size != 4 {
		t.Errorf("video = %+v", f)
	}
	if _, err := Inspect("x.gif", bytes.NewReader([]byte("GIF89a....")), 10); err == nil {
		t.Error("gif accepted")
	}
}

func TestRefDataAlpha(t *testing.T) {
	r := refData(t)
	if !r.AlphaAllowed("MAC_PROFILE") {
		t.Error("Mac must allow alpha")
	}
	if r.AlphaAllowed("IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE") {
		t.Error("iPhone must not allow alpha")
	}
	mac := FileInfo{Name: "m.png", Ext: ".png", Width: 2880, Height: 1800, Alpha: true, Size: 1000}
	if id, probs := r.Check(TypeScreenshot, "MAC_PROFILE", mac); id == "" {
		t.Errorf("Mac alpha rejected: %v", probs)
	}
	phone := FileInfo{Name: "p.png", Ext: ".png", Width: 1320, Height: 2868, Alpha: true, Size: 1000}
	id, probs := r.Check(TypeScreenshot, "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", phone)
	if id != "" || len(probs) != 1 || !strings.Contains(probs[0], "alpha") {
		t.Errorf("iPhone alpha: id %q problems %v", id, probs)
	}
	phone.Alpha = false
	if id, probs := r.Check(TypeScreenshot, "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", phone); id == "" {
		t.Errorf("opaque iPhone rejected: %v", probs)
	}
}

func TestRefDataHeaderAndSearch(t *testing.T) {
	r := refData(t)
	f := FileInfo{Name: "hero.png", Ext: ".png", Width: 5244, Height: 2950, Size: 1 << 20}
	for _, pt := range []string{"PRODUCT_PAGE_HEADER_ASSET", "APP_STORE_SEARCH_RESULTS_ASSET"} {
		if got := r.Groups(pt, f); !slices.Equal(got, []string{"DEFAULT_PROFILE"}) {
			t.Errorf("%s groups = %v", pt, got)
		}
		if id, probs := r.Check(pt, "DEFAULT_PROFILE", f); id == "" {
			t.Errorf("%s rejects 5244x2950: %v", pt, probs)
		}
	}
	if got := r.Groups(TypeScreenshot, f); len(got) != 0 {
		t.Errorf("5244x2950 fits screenshot groups %v", got)
	}
	f.Width = 5000
	_, probs := r.Check("PRODUCT_PAGE_HEADER_ASSET", "DEFAULT_PROFILE", f)
	if len(probs) == 0 || !strings.Contains(probs[0], "5244x2950") {
		t.Errorf("wrong size problems = %v", probs)
	}
	f = FileInfo{Name: "hero.gif", Ext: ".gif", Width: 5244, Height: 2950}
	if _, probs := r.Check("PRODUCT_PAGE_HEADER_ASSET", "DEFAULT_PROFILE", f); len(probs) != 1 || !strings.Contains(probs[0], "extension") {
		t.Errorf("extension problems = %v", probs)
	}
}

func TestRefDataGroupsAndLimits(t *testing.T) {
	r := refData(t)
	got := r.Groups(TypeScreenshot, FileInfo{Ext: ".png", Width: 1320, Height: 2868})
	if !slices.Equal(got, []string{"IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"}) {
		t.Errorf("6.9in groups = %v", got)
	}
	if got := r.Groups(TypePreview, FileInfo{Ext: ".mov", Video: true, Size: 10}); !slices.Contains(got, "MAC_PROFILE") {
		t.Errorf("video preview groups = %v", got)
	}
	if n := r.MaxCount("APP_STORE_VERSIONS", TypeScreenshot, "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"); n != 10 {
		t.Errorf("max count = %d, want 10", n)
	}
	if n := r.MaxCount("APP_STORE_VERSIONS", TypeScreenshot, "NOPE"); n != 0 {
		t.Errorf("unknown max count = %d", n)
	}
}

func TestLoadRefDataUncappedAndCached(t *testing.T) {
	raw, err := os.ReadFile("testdata/refdata.json")
	if err != nil {
		t.Fatal(err)
	}
	k := tooltest.New(t)
	k.Env.Profile.Name = "load-refdata-test"
	k.ASC.Handle(http.MethodGet, "/v1/appAssetLibraryRefData", http.StatusOK, string(raw))
	for range 2 {
		r, err := LoadRefData(t.Context(), k.Env)
		if err != nil || !r.AlphaAllowed("MAC_PROFILE") {
			t.Fatalf("load: %v", err)
		}
	}
	reqs := k.ASC.RequestsTo(http.MethodGet, "/v1/appAssetLibraryRefData")
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1 (cached)", len(reqs))
	}
	if q := reqs[0].Query; q.Has("limit") {
		t.Errorf("query %v sends a limit", q)
	}
	if reqs[0].Accept != "application/json" { // Apple answers 406 to the gzip default
		t.Errorf("Accept = %q", reqs[0].Accept)
	}
}

func TestUsable(t *testing.T) {
	for _, s := range []string{"PREPARE_FOR_SUBMISSION", "APPROVED", "COMPLETE"} {
		if !Usable(s) {
			t.Errorf("%s not usable", s)
		}
	}
	for _, s := range []string{"AWAITING_UPLOAD", "UPLOAD_COMPLETE", "FAILED", "REJECTED", "ARCHIVED", ""} {
		if Usable(s) {
			t.Errorf("%s usable", s)
		}
	}
}

func TestRefDataGroupPlatform(t *testing.T) {
	r := refData(t)
	for g, want := range map[string]string{
		"MAC_PROFILE": "MAC_APP_STORE", "DEFAULT_PROFILE": "ANY",
		"IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE": "IPHONE_APP_STORE", "NOPE": "",
	} {
		if got := r.GroupPlatform(g); got != want {
			t.Errorf("GroupPlatform(%s) = %q, want %q", g, got, want)
		}
	}
}
