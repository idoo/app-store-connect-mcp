package screenshots

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/hamn/internal/spec"
	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
	"github.com/idoo/hamn/internal/upload"
)

var (
	specOnce sync.Once
	specIdx  *spec.Index
	specErr  error
)

// jres is a JSON:API resource for fixtures. Rels values are "type:id" or a
// slice of them.
type jres struct {
	Type, ID string
	Attrs    map[string]any
	Rels     map[string]any
}

func (r jres) raw() map[string]any {
	m := map[string]any{"type": r.Type, "id": r.ID}
	if r.Attrs != nil {
		m["attributes"] = r.Attrs
	}
	if len(r.Rels) > 0 {
		rels := map[string]any{}
		for name, v := range r.Rels {
			ident := func(s string) map[string]string {
				typ, id, _ := strings.Cut(s, ":")
				return map[string]string{"type": typ, "id": id}
			}
			switch v := v.(type) {
			case string:
				rels[name] = map[string]any{"data": ident(v)}
			case []string:
				data := []map[string]string{}
				for _, s := range v {
					data = append(data, ident(s))
				}
				rels[name] = map[string]any{"data": data}
			}
		}
		m["relationships"] = rels
	}
	return m
}

func doc(t *testing.T, data any, included ...jres) []byte {
	t.Helper()
	d := map[string]any{}
	switch v := data.(type) {
	case jres:
		d["data"] = v.raw()
	case []jres:
		list := []map[string]any{}
		for _, r := range v {
			list = append(list, r.raw())
		}
		d["data"] = list
	}
	if len(included) > 0 {
		inc := []map[string]any{}
		for _, r := range included {
			inc = append(inc, r.raw())
		}
		d["included"] = inc
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pngChunk encodes one PNG chunk with its CRC.
func pngChunk(typ string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(append(b, typ...), data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}

// pngImage builds a header-only w×h PNG (IHDR, tEXt, IEND): validation
// reads only the header, so no pixels are encoded. alpha selects the RGBA
// color type; seed varies the content (and checksum).
func pngImage(t *testing.T, w, h int, alpha bool, seed uint8) []byte {
	t.Helper()
	colorType := byte(0) // gray
	if alpha {
		colorType = 6 // RGBA
	}
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(w))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(h))
	ihdr = append(ihdr, 8, colorType, 0, 0, 0)
	b := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
	b = append(b, pngChunk("tEXt", []byte{'s', 'e', 'e', 'd', 0, '0' + seed%10, 'a' + seed/10%26})...)
	return append(b, pngChunk("IEND", nil)...)
}

// fake is a stateful App Store Connect fake for screenshot sets.
type fake struct {
	t        *testing.T
	k        *tooltest.Kit
	mu       sync.Mutex
	locs     map[string]string // locale -> localization id
	sets     map[string][]*fakeSet
	shots    map[string]*fakeShot
	previews map[string][]*fakePreviewSet
	next     int
}

type fakeSet struct {
	id, display string
	shots       []string
}

type fakeShot struct {
	id, fileName, checksum, state string
}

type fakePreviewSet struct {
	id, previewType string
	previews        []*fakeShot
}

func (f *fakeSet) res() jres {
	ids := []string{}
	for _, s := range f.shots {
		ids = append(ids, "appScreenshots:"+s)
	}
	return jres{Type: "appScreenshotSets", ID: f.id, Attrs: map[string]any{"screenshotDisplayType": f.display}, Rels: map[string]any{"appScreenshots": ids}}
}

func (s *fakeShot) res(typ string) jres {
	attrs := map[string]any{
		"fileName": s.fileName, "sourceFileChecksum": s.checksum,
		"assetDeliveryState": map[string]any{"state": s.state},
		"imageAsset":         map[string]any{"templateUrl": "https://is1-ssl.mzstatic.com/image/" + s.id + "/{w}x{h}bb.{f}", "width": 1290, "height": 2796},
	}
	if typ == "appPreviews" {
		attrs["videoDeliveryState"] = map[string]any{"state": s.state}
	}
	return jres{Type: typ, ID: s.id, Attrs: attrs}
}

func newKit(t *testing.T) (*tooltest.Kit, *fake) {
	t.Helper()
	k := tooltest.New(t)
	k.Resolve.Apps = []tool.App{{ID: "app1", Name: "Hjul", BundleID: "se.hjul", Platforms: []tool.Platform{tool.IOS}}}
	k.Resolve.AppLocales = []string{"en-US", "de-DE"}
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{
		tool.VersionEditable: {ID: "ver1", String: "1.4", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"},
	}
	f := &fake{
		t: t, k: k, locs: map[string]string{"en-US": "loc-en", "de-DE": "loc-de"},
		sets: map[string][]*fakeSet{}, shots: map[string]*fakeShot{}, previews: map[string][]*fakePreviewSet{},
	}
	f.routes()
	return k, f
}

func (f *fake) id(prefix string) string {
	f.next++
	return fmt.Sprintf("%s%d", prefix, f.next)
}

// addShot seeds a remote screenshot in loc's display set.
func (f *fake) addShot(loc, display, fileName, checksum string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var set *fakeSet
	for _, s := range f.sets[loc] {
		if s.display == display {
			set = s
		}
	}
	if set == nil {
		set = &fakeSet{id: f.id("set"), display: display}
		f.sets[loc] = append(f.sets[loc], set)
	}
	id := f.id("shot")
	f.shots[id] = &fakeShot{id: id, fileName: fileName, checksum: checksum, state: "COMPLETE"}
	set.shots = append(set.shots, id)
	return id
}

func body(req tool.Request) map[string]any {
	b, _ := json.Marshal(req.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	d, _ := m["data"].(map[string]any)
	return d
}

func (f *fake) routes() {
	t, k := f.t, f.k
	k.ASC.HandleFunc("GET", "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", func(tool.Request) tooltest.Fixture {
		var data []jres
		for _, l := range []string{"de-DE", "en-US"} {
			data = append(data, jres{Type: "appStoreVersionLocalizations", ID: f.locs[l], Attrs: map[string]any{"locale": l}})
		}
		return tooltest.Fixture{Status: 200, Body: doc(t, data)}
	})
	k.ASC.HandleFunc("GET", "/v1/appStoreVersionLocalizations/{id}/appScreenshotSets", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		loc := strings.Split(req.Path, "/")[3]
		data := []jres{}
		var inc []jres
		for _, s := range f.sets[loc] {
			data = append(data, s.res())
			for _, id := range s.shots {
				inc = append(inc, f.shots[id].res("appScreenshots"))
			}
		}
		return tooltest.Fixture{Status: 200, Body: doc(t, data, inc...)}
	})
	k.ASC.HandleFunc("GET", "/v1/appStoreVersionLocalizations/{id}/appPreviewSets", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		loc := strings.Split(req.Path, "/")[3]
		data := []jres{}
		var inc []jres
		for _, s := range f.previews[loc] {
			if pt := req.Query.Get("filter[previewType]"); pt != "" && pt != s.previewType {
				continue
			}
			ids := []string{}
			for _, p := range s.previews {
				ids = append(ids, "appPreviews:"+p.id)
				inc = append(inc, jres{Type: "appPreviews", ID: p.id, Attrs: map[string]any{"fileName": p.fileName, "sourceFileChecksum": p.checksum}})
			}
			data = append(data, jres{Type: "appPreviewSets", ID: s.id, Attrs: map[string]any{"previewType": s.previewType}, Rels: map[string]any{"appPreviews": ids}})
		}
		return tooltest.Fixture{Status: 200, Body: doc(t, data, inc...)}
	})
	k.ASC.HandleFunc("POST", "/v1/appScreenshotSets", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		d := body(req)
		display := d["attributes"].(map[string]any)["screenshotDisplayType"].(string)
		loc := d["relationships"].(map[string]any)["appStoreVersionLocalization"].(map[string]any)["data"].(map[string]any)["id"].(string)
		s := &fakeSet{id: f.id("set"), display: display}
		f.sets[loc] = append(f.sets[loc], s)
		return tooltest.Fixture{Status: 201, Body: doc(t, s.res())}
	})
	reserve := func(typ, parentRel, prefix string) func(tool.Request) tooltest.Fixture {
		return func(req tool.Request) tooltest.Fixture {
			f.mu.Lock()
			defer f.mu.Unlock()
			d := body(req)
			attrs := d["attributes"].(map[string]any)
			name := attrs["fileName"].(string)
			size := attrs["fileSize"].(float64)
			parent := d["relationships"].(map[string]any)[parentRel].(map[string]any)["data"].(map[string]any)["id"].(string)
			s := &fakeShot{id: f.id(prefix), fileName: name, state: "AWAITING_UPLOAD"}
			f.shots[s.id] = s
			if typ == "appScreenshots" {
				for _, sets := range f.sets {
					for _, set := range sets {
						if set.id == parent {
							set.shots = append(set.shots, s.id)
						}
					}
				}
			} else {
				for _, sets := range f.previews {
					for _, set := range sets {
						if set.id == parent {
							set.previews = append(set.previews, s)
						}
					}
				}
			}
			u := "https://upload.example.com/" + s.id + "?sig=x"
			k.Fetch.Handle("PUT", u, 200, nil)
			r := jres{Type: typ, ID: s.id, Attrs: map[string]any{
				"fileName": name, "fileSize": size,
				"uploadOperations": []map[string]any{{
					"method": "PUT", "url": u, "length": size, "offset": 0,
					"requestHeaders": []map[string]string{{"name": "Content-Type", "value": "image/png"}},
				}},
			}}
			return tooltest.Fixture{Status: 201, Body: doc(t, r)}
		}
	}
	commit := func(typ string) func(tool.Request) tooltest.Fixture {
		return func(req tool.Request) tooltest.Fixture {
			f.mu.Lock()
			defer f.mu.Unlock()
			id := path.Base(req.Path)
			s := f.shots[id]
			s.checksum = body(req)["attributes"].(map[string]any)["sourceFileChecksum"].(string)
			s.state = "UPLOAD_COMPLETE"
			return tooltest.Fixture{Status: 200, Body: doc(t, s.res(typ))}
		}
	}
	k.ASC.HandleFunc("POST", "/v1/appScreenshots", reserve("appScreenshots", "appScreenshotSet", "shot"))
	k.ASC.HandleFunc("PATCH", "/v1/appScreenshots/{id}", commit("appScreenshots"))
	k.ASC.HandleFunc("GET", "/v1/appScreenshots/{id}", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		s := f.shots[path.Base(req.Path)]
		s.state = "COMPLETE"
		return tooltest.Fixture{Status: 200, Body: doc(t, s.res("appScreenshots"))}
	})
	k.ASC.HandleFunc("DELETE", "/v1/appScreenshots/{id}", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := path.Base(req.Path)
		for _, sets := range f.sets {
			for _, set := range sets {
				set.shots = slices.DeleteFunc(set.shots, func(s string) bool { return s == id })
			}
		}
		delete(f.shots, id)
		return tooltest.Fixture{Status: 204}
	})
	k.ASC.HandleFunc("PATCH", "/v1/appScreenshotSets/{id}/relationships/appScreenshots", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		b, _ := json.Marshal(req.Body)
		var m struct {
			Data []struct{ ID string } `json:"data"`
		}
		_ = json.Unmarshal(b, &m)
		setID := strings.Split(req.Path, "/")[3]
		for _, sets := range f.sets {
			for _, set := range sets {
				if set.id == setID {
					set.shots = nil
					for _, d := range m.Data {
						set.shots = append(set.shots, d.ID)
					}
				}
			}
		}
		return tooltest.Fixture{Status: 204}
	})
	k.ASC.HandleFunc("POST", "/v1/appPreviewSets", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		d := body(req)
		pt := d["attributes"].(map[string]any)["previewType"].(string)
		loc := d["relationships"].(map[string]any)["appStoreVersionLocalization"].(map[string]any)["data"].(map[string]any)["id"].(string)
		s := &fakePreviewSet{id: f.id("pset"), previewType: pt}
		f.previews[loc] = append(f.previews[loc], s)
		return tooltest.Fixture{Status: 201, Body: doc(t, jres{Type: "appPreviewSets", ID: s.id, Attrs: map[string]any{"previewType": pt}})}
	})
	k.ASC.HandleFunc("POST", "/v1/appPreviews", reserve("appPreviews", "appPreviewSet", "appPreview"))
	k.ASC.HandleFunc("PATCH", "/v1/appPreviews/{id}", commit("appPreviews"))
	k.ASC.HandleFunc("GET", "/v1/appPreviews/{id}", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		s := f.shots[path.Base(req.Path)]
		s.state = "COMPLETE"
		return tooltest.Fixture{Status: 200, Body: doc(t, s.res("appPreviews"))}
	})
}

// write puts a file into the kit's project root.
func write(t *testing.T, k *tooltest.Kit, rel string, data []byte) {
	t.Helper()
	if err := k.Files.WriteFile(rel, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func md5Of(t *testing.T, data []byte) string {
	t.Helper()
	s, err := upload.FileMD5(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// mutations renders the non-GET request log as "METHOD path".
func mutations(k *tooltest.Kit) []string {
	var out []string
	for _, r := range k.ASC.Mutations() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func call(t *testing.T, k *tooltest.Kit, name, args string) *tool.Result {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tooltest.Call(t, tl, k.Env, args)
}

type planResult struct {
	Status   string     `json:"status"`
	Summary  summaryOut `json:"summary"`
	Sets     []setPlan  `json:"sets"`
	Problems []problem  `json:"problems"`
	PlanID   string     `json:"plan_id"`
}

func TestPlanFastlaneLayoutDetectsDisplay(t *testing.T) {
	k, _ := newKit(t)
	write(t, k, "shots/en-US/01-home.png", pngImage(t, 1290, 2796, false, 1))
	write(t, k, "shots/en-US/README.txt", []byte("ignored"))
	res := tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`))
	p := tooltest.Decode[planResult](t, res)
	if p.Status != statusPlanned || len(p.Sets) != 1 {
		t.Fatalf("plan = %+v", p)
	}
	s := p.Sets[0]
	if s.Display != "APP_IPHONE_67" || s.Locale != "en-US" || !s.CreateSet || len(s.Uploads) != 1 || s.Uploads[0].Rel != "en-US/01-home.png" {
		t.Errorf("set = %+v", s)
	}
	if p.PlanID == "" || len(res.Next) != 1 || res.Next[0].Tool != "screenshots_sync" || res.Next[0].Args["plan_id"] != p.PlanID {
		t.Errorf("plan id / next = %q %+v", p.PlanID, res.Next)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Errorf("plan mutated: %v", mutations(k))
	}
}

func TestPlanRejectsAlphaAndReportsAmbiguousAndUnknown(t *testing.T) {
	k, _ := newKit(t)
	write(t, k, "shots/en-US/01.png", pngImage(t, 1290, 2796, true, 1))      // alpha
	write(t, k, "shots/en-US/02.png", pngImage(t, 1125, 2436, false, 2))     // 6.1" and 5.8"
	write(t, k, "shots/xx-YY/01.png", pngImage(t, 1290, 2796, false, 3))     // unknown locale
	write(t, k, "shots/en-US/6.9/03.png", pngImage(t, 1290, 2796, false, 4)) // explicit display: fine
	res := tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`))
	p := tooltest.Decode[planResult](t, res)
	if p.Status != statusInvalid || p.PlanID != "" || len(k.Plans.Proposed) != 0 {
		t.Fatalf("status %s plan %q proposed %d", p.Status, p.PlanID, len(k.Plans.Proposed))
	}
	kinds := map[string]problem{}
	for _, pr := range p.Problems {
		kinds[pr.Kind] = pr
	}
	if pr, ok := kinds[problemInvalidImage]; !ok || pr.File != "en-US/01.png" || !strings.Contains(pr.Message, "alpha") {
		t.Errorf("alpha problem = %+v", kinds)
	}
	if pr, ok := kinds[problemAmbiguousSize]; !ok || len(pr.Candidates) < 2 {
		t.Errorf("ambiguous problem = %+v", pr)
	}
	if _, ok := kinds[problemUnknownLocale]; !ok {
		t.Errorf("unknown locale not reported: %+v", p.Problems)
	}
}

func TestPlanSkipsEqualChecksumAndReplaceDeletesEnumerated(t *testing.T) {
	k, f := newKit(t)
	a := pngImage(t, 1290, 2796, false, 1)
	b := pngImage(t, 1290, 2796, false, 2)
	write(t, k, "shots/en-US/01.png", a)
	write(t, k, "shots/en-US/02.png", b)
	keep := f.addShot("loc-en", "APP_IPHONE_67", "old-a.png", md5Of(t, a))
	gone := f.addShot("loc-en", "APP_IPHONE_67", "stale.png", "deadbeef")

	p := tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`)))
	s := p.Sets[0]
	if len(s.Skips) != 1 || s.Skips[0].ID != keep || len(s.Uploads) != 1 || s.Uploads[0].Rel != "en-US/02.png" {
		t.Errorf("skips/uploads = %+v / %+v", s.Skips, s.Uploads)
	}
	if len(s.Deletes) != 1 || s.Deletes[0].ID != gone || s.Deletes[0].FileName != "stale.png" {
		t.Errorf("deletes = %+v", s.Deletes)
	}
	if len(k.Plans.Proposed) != 1 || len(k.Plans.Proposed[0].Irreversible) != 1 {
		t.Errorf("preview = %+v", k.Plans.Proposed)
	}

	// add mode never deletes.
	p = tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots","mode":"add"}`)))
	if s := p.Sets[0]; len(s.Deletes) != 0 || len(s.Uploads) != 1 || s.Reorder {
		t.Errorf("add mode set = %+v", s)
	}
}

func TestPlanNoChanges(t *testing.T) {
	k, f := newKit(t)
	a := pngImage(t, 1290, 2796, false, 1)
	write(t, k, "shots/en-US/01.png", a)
	f.addShot("loc-en", "APP_IPHONE_67", "01.png", md5Of(t, a))
	p := tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`)))
	if p.Status != statusNoChanges || p.PlanID != "" {
		t.Errorf("plan = %+v", p)
	}
	res := tooltest.MustOK(t, call(t, k, "screenshots_sync", `{"dir":"shots"}`))
	if m := tooltest.Decode[map[string]any](t, res); m["already"] != true {
		t.Errorf("sync no-op = %v", m)
	}
}

func TestPlanSyncRoundTripAndStaleRefusal(t *testing.T) {
	k, f := newKit(t)
	a := pngImage(t, 1290, 2796, false, 1)
	b := pngImage(t, 1290, 2796, false, 2)
	write(t, k, "shots/en-US/01.png", b) // new, must come first
	write(t, k, "shots/en-US/02.png", a) // already remote
	write(t, k, "shots/de-DE/01.png", a) // new set
	keep := f.addShot("loc-en", "APP_IPHONE_67", "a.png", md5Of(t, a))
	gone := f.addShot("loc-en", "APP_IPHONE_67", "old.png", "deadbeef")

	p := tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`)))
	if p.Status != statusPlanned || p.Summary.Uploads != 2 || p.Summary.Deletes != 1 || p.Summary.CreateSets != 1 || p.Summary.Reorders != 1 {
		t.Fatalf("summary = %+v", p.Summary)
	}

	// Without confirm a plan with deletes only previews.
	res := tooltest.MustOK(t, call(t, k, "screenshots_sync", `{"dir":"shots"}`))
	if m := tooltest.Decode[map[string]any](t, res); m["status"] != "planned" || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("unconfirmed sync = %v, mutations %v", m, mutations(k))
	}

	// The remote set changes: the first plan is stale.
	f.addShot("loc-en", "APP_IPHONE_67", "other.png", "cafe")
	body := tooltest.MustFail(t, call(t, k, "screenshots_sync", fmt.Sprintf(`{"dir":"shots","plan_id":%q}`, p.PlanID)), "stale_plan")
	_ = body
	if len(k.ASC.Mutations()) != 0 {
		t.Fatalf("stale sync mutated: %v", mutations(k))
	}

	p = tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`)))
	res = tooltest.MustOK(t, call(t, k, "screenshots_sync", fmt.Sprintf(`{"dir":"shots","plan_id":%q}`, p.PlanID)))
	out := tooltest.Decode[syncOut](t, res)
	if out.Status != "synced" || out.Uploaded != 2 || out.Deleted != 2 || out.Skipped != 1 || out.Failed != 0 || out.Pending != 0 {
		t.Fatalf("sync out = %+v", out)
	}
	got := mutations(k)
	want := []string{
		"POST /v1/appScreenshotSets",
		"POST /v1/appScreenshots", "PATCH /v1/appScreenshots/shot6",
		"POST /v1/appScreenshots", "PATCH /v1/appScreenshots/shot7",
		"DELETE /v1/appScreenshots/" + gone, "DELETE /v1/appScreenshots/shot4",
		"PATCH /v1/appScreenshotSets/set1/relationships/appScreenshots",
	}
	if !slices.Equal(got, want) {
		t.Errorf("mutations:\n got %q\nwant %q", got, want)
	}
	// Remote now matches the local order: new upload, then the kept one.
	f.mu.Lock()
	en := f.sets["loc-en"][0].shots
	f.mu.Unlock()
	if len(en) != 2 || en[1] != keep || f.shots[en[0]].fileName != "01.png" {
		t.Errorf("en-US order = %v", en)
	}
	for _, c := range k.Fetch.Calls {
		if !strings.HasPrefix(c, "PUT https://upload.example.com/") {
			t.Errorf("unexpected fetch %s", c)
		}
	}
	if len(k.Fetch.Calls) != 2 {
		t.Errorf("fetch calls = %v", k.Fetch.Calls)
	}
	// A second plan finds nothing to do.
	p = tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", `{"dir":"shots"}`)))
	if p.Status != statusNoChanges {
		t.Errorf("replan = %+v", p)
	}
}

func TestSyncAdditiveWithoutConfirm(t *testing.T) {
	k, _ := newKit(t)
	write(t, k, "shots/en-US/01.png", pngImage(t, 1290, 2796, false, 1))
	res := tooltest.MustOK(t, call(t, k, "screenshots_sync", `{"dir":"shots","mode":"add"}`))
	out := tooltest.Decode[syncOut](t, res)
	if out.Status != "synced" || out.Uploaded != 1 {
		t.Errorf("sync = %+v", out)
	}
}

func TestNoEditableVersion(t *testing.T) {
	k, _ := newKit(t)
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{tool.VersionLive: {ID: "ver0", String: "1.3", State: "READY_FOR_DISTRIBUTION"}}
	write(t, k, "shots/en-US/01.png", pngImage(t, 1290, 2796, false, 1))
	for _, name := range []string{"screenshots_plan", "screenshots_sync"} {
		b := tooltest.MustFail(t, call(t, k, name, `{"dir":"shots"}`), "no_editable_version")
		if len(b.Next) != 1 || b.Next[0].Tool != "release_prepare" {
			t.Errorf("%s next = %+v", name, b.Next)
		}
	}
	b := tooltest.MustFail(t, call(t, k, "previews_upload", `{"locale":"en-US","file":"p.mov"}`), "no_editable_version")
	if len(b.Next) != 1 || b.Next[0].Tool != "release_prepare" {
		t.Errorf("previews next = %+v", b.Next)
	}
}

func TestListWithThumbnails(t *testing.T) {
	k, f := newKit(t)
	id := f.addShot("loc-en", "APP_IPHONE_67", "01.png", "abc")
	k.Fetch.Handle("GET", "https://is1-ssl.mzstatic.com/image/"+id+"/300x650bb.jpg", http.StatusOK, []byte("jpeg"))
	res := tooltest.MustOK(t, call(t, k, "screenshots_list", `{"locales":["en"],"thumbnails":true}`))
	out := tooltest.Decode[listOut](t, res)
	if len(out.Locales) != 1 || out.Locales[0].Locale != "en-US" {
		t.Fatalf("locales = %+v", out.Locales)
	}
	sets := out.Locales[0].ScreenshotSets
	if len(sets) != 1 || sets[0].Display != "APP_IPHONE_67" || len(sets[0].Shots) != 1 {
		t.Fatalf("sets = %+v", sets)
	}
	if s := sets[0].Shots[0]; s.ID != id || s.FileName != "01.png" || s.State != "COMPLETE" || s.Width != 1290 || s.Checksum != "abc" || s.Order != 1 {
		t.Errorf("shot = %+v", s)
	}
	if len(res.Images) != 1 || res.Images[0].MIMEType != "image/jpeg" {
		t.Errorf("images = %+v", res.Images)
	}
	if n := len(k.ASC.Requests()); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
}

func TestPreviewUploadSequence(t *testing.T) {
	k, f := newKit(t)
	video := []byte("not really a movie, but bytes")
	write(t, k, "media/preview.mp4", video)
	res := tooltest.MustOK(t, call(t, k, "previews_upload", `{"locale":"en-US","file":"media/preview.mp4","preview_type":"6.9","preview_frame_time_code":"00:00:05:00"}`))
	out := tooltest.Decode[previewOut](t, res)
	if out.PreviewType != "IPHONE_67" || !out.SetCreated || out.Preview == nil || out.Preview.State != upload.StateComplete {
		t.Fatalf("preview = %+v", out)
	}
	got := mutations(k)
	want := []string{"POST /v1/appPreviewSets", "POST /v1/appPreviews", "PATCH /v1/appPreviews/appPreview2"}
	if !slices.Equal(got, want) {
		t.Errorf("mutations:\n got %q\nwant %q", got, want)
	}
	reserve := k.ASC.RequestsTo("POST", "/v1/appPreviews")[0]
	attrs := body(reserve)["attributes"].(map[string]any)
	if attrs["mimeType"] != "video/mp4" || attrs["previewFrameTimeCode"] != "00:00:05:00" || attrs["fileName"] != "preview.mp4" {
		t.Errorf("reserve attrs = %v", attrs)
	}
	f.mu.Lock()
	n := len(f.previews["loc-en"][0].previews)
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("previews in set = %d", n)
	}
	// Same file again: idempotent no-op.
	before := len(k.ASC.Mutations())
	res = tooltest.MustOK(t, call(t, k, "previews_upload", `{"locale":"en-US","file":"media/preview.mp4","preview_type":"IPHONE_67"}`))
	if m := tooltest.Decode[map[string]any](t, res); m["already"] != true || len(k.ASC.Mutations()) != before {
		t.Errorf("repeat = %v", m)
	}
}

func TestPreviewUploadRejects(t *testing.T) {
	k, _ := newKit(t)
	write(t, k, "media/p.gif", []byte("x"))
	tooltest.MustFail(t, call(t, k, "previews_upload", `{"locale":"en-US","file":"media/p.gif"}`), "invalid_arguments")
	write(t, k, "media/p.mov", []byte("x"))
	b := tooltest.MustFail(t, call(t, k, "previews_upload", `{"locale":"fr-FR","file":"media/p.mov"}`), "missing_localization")
	if len(b.Next) != 1 || b.Next[0].Tool != "metadata_plan" {
		t.Errorf("next = %+v", b.Next)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Errorf("mutated: %v", mutations(k))
	}
}

func TestParsePreviewType(t *testing.T) {
	for in, want := range map[string]string{"": "IPHONE_67", "ipad-13": "IPAD_PRO_3GEN_129", "DESKTOP": "DESKTOP", "mac": "DESKTOP", "iphone_65": "IPHONE_65"} {
		got, err := parsePreviewType(in, tool.IOS)
		if err != nil || got != want {
			t.Errorf("parsePreviewType(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parsePreviewType("watch-ultra", tool.IOS); err == nil {
		t.Error("watch has no preview slot")
	}
}
