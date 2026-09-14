package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
	"github.com/idoo/hamn/internal/upload"
)

// shotsFake is a stateful screenshot-set fake: reservations add AWAITING
// screenshots, commits store the checksum, deletes and reorders edit sets.
type shotsFake struct {
	t     *testing.T
	k     *tooltest.Kit
	mu    sync.Mutex
	locs  map[string]string // locale -> localization id
	sets  map[string][]*shotSet
	shots map[string]*shot
	next  int
}

type shotSet struct {
	id, display string
	shots       []string
}

type shot struct{ id, fileName, checksum, state string }

func (s *shotSet) res() res {
	ids := []string{}
	for _, id := range s.shots {
		ids = append(ids, "appScreenshots:"+id)
	}
	return res{Type: "appScreenshotSets", ID: s.id, Attrs: map[string]any{"screenshotDisplayType": s.display}, Rels: map[string]any{"appScreenshots": ids}}
}

func (s *shot) res() res {
	return res{Type: "appScreenshots", ID: s.id, Attrs: map[string]any{
		"fileName": s.fileName, "sourceFileChecksum": s.checksum,
		"assetDeliveryState": map[string]any{"state": s.state},
	}}
}

func (f *shotsFake) newID(prefix string) string {
	f.next++
	return fmt.Sprintf("%s%d", prefix, f.next)
}

// setByID returns the set with id; f.mu must be held.
func (f *shotsFake) setByID(id string) *shotSet {
	for _, sets := range f.sets {
		for _, s := range sets {
			if s.id == id {
				return s
			}
		}
	}
	return nil
}

func relID(d map[string]any, rel string) string {
	r, _ := d["relationships"].(map[string]any)[rel].(map[string]any)
	data, _ := r["data"].(map[string]any)
	id, _ := data["id"].(string)
	return id
}

func screenshotsKit(t *testing.T) (*tooltest.Kit, *shotsFake) {
	t.Helper()
	k := newKit(t)
	k.Resolve.AppLocales = []string{"en-US", "de-DE"}
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{
		tool.VersionEditable: {ID: "ver1", String: "1.4", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"},
	}
	f := &shotsFake{t: t, k: k, locs: map[string]string{"en-US": "loc-en", "de-DE": "loc-de"}, sets: map[string][]*shotSet{}, shots: map[string]*shot{}}
	asc := k.ASC
	asc.HandleFunc(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", func(tool.Request) tooltest.Fixture {
		var data []res
		for _, l := range []string{"de-DE", "en-US"} {
			data = append(data, res{Type: "appStoreVersionLocalizations", ID: f.locs[l], Attrs: map[string]any{"locale": l}})
		}
		return tooltest.Fixture{Status: http.StatusOK, Body: jsonDoc(t, data)}
	})
	asc.HandleFunc(http.MethodGet, "/v1/appStoreVersionLocalizations/{id}/appScreenshotSets", func(r tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		data := []res{}
		var inc []res
		for _, s := range f.sets[strings.Split(r.Path, "/")[3]] {
			data = append(data, s.res())
			for _, id := range s.shots {
				inc = append(inc, f.shots[id].res())
			}
		}
		return tooltest.Fixture{Status: http.StatusOK, Body: jsonDoc(t, data, inc...)}
	})
	asc.HandleFunc(http.MethodPost, "/v1/appScreenshotSets", func(r tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		d := reqBody(r)
		display, _ := d["attributes"].(map[string]any)["screenshotDisplayType"].(string)
		s := &shotSet{id: f.newID("set"), display: display}
		loc := relID(d, "appStoreVersionLocalization")
		f.sets[loc] = append(f.sets[loc], s)
		return tooltest.Fixture{Status: http.StatusCreated, Body: jsonDoc(t, s.res())}
	})
	asc.HandleFunc(http.MethodPost, "/v1/appScreenshots", func(r tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		d := reqBody(r)
		attrs, _ := d["attributes"].(map[string]any)
		name, _ := attrs["fileName"].(string)
		size, _ := attrs["fileSize"].(float64)
		s := &shot{id: f.newID("shot"), fileName: name, state: "AWAITING_UPLOAD"}
		f.shots[s.id] = s
		if set := f.setByID(relID(d, "appScreenshotSet")); set != nil {
			set.shots = append(set.shots, s.id)
		}
		u := "https://upload.example.com/" + s.id + "?sig=x"
		k.Fetch.Handle(http.MethodPut, u, http.StatusOK, nil)
		return tooltest.Fixture{Status: http.StatusCreated, Body: jsonDoc(t, res{Type: "appScreenshots", ID: s.id, Attrs: map[string]any{
			"fileName": name, "fileSize": size,
			"uploadOperations": []map[string]any{{
				"method": "PUT", "url": u, "length": size, "offset": 0,
				"requestHeaders": []map[string]string{{"name": "Content-Type", "value": "image/png"}},
			}},
		}})}
	})
	asc.HandleFunc(http.MethodPatch, "/v1/appScreenshots/{id}", func(r tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		s := f.shots[path.Base(r.Path)]
		s.checksum, _ = reqBody(r)["attributes"].(map[string]any)["sourceFileChecksum"].(string)
		s.state = "UPLOAD_COMPLETE"
		return tooltest.Fixture{Status: http.StatusOK, Body: jsonDoc(t, s.res())}
	})
	asc.HandleFunc(http.MethodGet, "/v1/appScreenshots/{id}", func(r tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		s := f.shots[path.Base(r.Path)]
		s.state = "COMPLETE"
		return tooltest.Fixture{Status: http.StatusOK, Body: jsonDoc(t, s.res())}
	})
	asc.HandleFunc(http.MethodDelete, "/v1/appScreenshots/{id}", func(r tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := path.Base(r.Path)
		for _, sets := range f.sets {
			for _, set := range sets {
				set.shots = slices.DeleteFunc(set.shots, func(s string) bool { return s == id })
			}
		}
		delete(f.shots, id)
		return tooltest.Fixture{Status: http.StatusNoContent}
	})
	asc.HandleFunc(http.MethodPatch, "/v1/appScreenshotSets/{id}/relationships/appScreenshots", func(r tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		b, _ := json.Marshal(r.Body)
		var m struct {
			Data []struct{ ID string } `json:"data"`
		}
		_ = json.Unmarshal(b, &m)
		if set := f.setByID(strings.Split(r.Path, "/")[3]); set != nil {
			set.shots = nil
			for _, d := range m.Data {
				set.shots = append(set.shots, d.ID)
			}
		}
		return tooltest.Fixture{Status: http.StatusNoContent}
	})
	return k, f
}

// phonePNG encodes an opaque 1290×2796 (6.9" iPhone) PNG; seed varies the
// checksum.
func phonePNG(t *testing.T, seed uint8) []byte {
	t.Helper()
	g := image.NewGray(image.Rect(0, 0, 1290, 2796))
	g.SetGray(0, 0, color.Gray{Y: seed})
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Job 5: "upload new screenshots" from a fastlane snapshot directory
// (<locale>/*.png, display inferred from pixels) in at most 3 calls: a Read
// plan, then the sync that applies it. Remote en-US holds 02-map (same
// checksum, kept) before an outdated shot (deleted), so 01-home is uploaded
// and the set reordered.
func TestJobReplaceScreenshotsFromFastlaneDir(t *testing.T) {
	k, f := screenshotsKit(t)
	files := map[string][]byte{
		"fastlane/screenshots/en-US/01-home.png": phonePNG(t, 1),
		"fastlane/screenshots/en-US/02-map.png":  phonePNG(t, 2),
		"fastlane/screenshots/de-DE/01-home.png": phonePNG(t, 3),
	}
	for rel, data := range files {
		if err := k.Files.WriteFile(rel, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mapSum, err := upload.FileMD5(bytes.NewReader(files["fastlane/screenshots/en-US/02-map.png"]))
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.sets["loc-en"] = []*shotSet{{id: "set-en", display: "APP_IPHONE_67", shots: []string{"shot-map", "shot-old"}}}
	f.shots["shot-map"] = &shot{id: "shot-map", fileName: "02-map.png", checksum: mapSum, state: "COMPLETE"}
	f.shots["shot-old"] = &shot{id: "shot-old", fileName: "old.png", checksum: "0000", state: "COMPLETE"}
	f.mu.Unlock()

	j := connect(t, k)
	plan := j.call("screenshots_plan", `{"app":"Hjul","dir":"fastlane/screenshots"}`)
	planID, _ := plan["plan_id"].(string)
	if planID == "" {
		t.Fatalf("screenshots_plan returned no plan_id: %v", plan)
	}
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("screenshots_plan sent %d mutations", n)
	}
	out := j.call("screenshots_sync", fmt.Sprintf(`{"app":"Hjul","dir":"fastlane/screenshots","plan_id":%q}`, planID))
	j.done(3)
	if j.calls[0].class != tool.Read {
		t.Errorf("first call %s is class %v, want Read", j.calls[0].name, j.calls[0].class)
	}
	if out["uploaded"] != float64(2) || out["skipped"] != float64(1) || out["deleted"] != float64(1) || out["failed"] != float64(0) {
		t.Errorf("sync = %v", out)
	}

	if n := len(k.ASC.RequestsTo(http.MethodPost, "/v1/appScreenshots")); n != 2 {
		t.Errorf("%d upload reservations, want 2", n)
	}
	commits := k.ASC.RequestsTo(http.MethodPatch, "/v1/appScreenshots/{id}")
	if len(commits) != 2 {
		t.Errorf("%d upload commits, want 2", len(commits))
	}
	for _, c := range commits {
		if attrs, _ := reqBody(c)["attributes"].(map[string]any); attrs["uploaded"] != true || attrs["sourceFileChecksum"] == "" {
			t.Errorf("commit %s lacks uploaded/checksum: %v", c.Path, attrs)
		}
	}
	if len(k.ASC.RequestsTo(http.MethodDelete, "/v1/appScreenshots/{id}")) != 1 {
		t.Errorf("old screenshot not deleted: %v", k.ASC.Mutations())
	}
	if len(k.ASC.RequestsTo(http.MethodPatch, "/v1/appScreenshotSets/{id}/relationships/appScreenshots")) == 0 {
		t.Errorf("sets were not reordered: %v", k.ASC.Mutations())
	}

	// Remote state now mirrors the directory, in file order.
	f.mu.Lock()
	defer f.mu.Unlock()
	names := func(loc string) []string {
		var out []string
		for _, s := range f.sets[loc] {
			for _, id := range s.shots {
				out = append(out, s.display+"/"+f.shots[id].fileName)
			}
		}
		return out
	}
	if got, want := names("loc-en"), []string{"APP_IPHONE_67/01-home.png", "APP_IPHONE_67/02-map.png"}; !slices.Equal(got, want) {
		t.Errorf("en-US = %v, want %v", got, want)
	}
	if got, want := names("loc-de"), []string{"APP_IPHONE_67/01-home.png"}; !slices.Equal(got, want) {
		t.Errorf("de-DE = %v, want %v", got, want)
	}
}
