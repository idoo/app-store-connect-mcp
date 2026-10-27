package library

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

type fAsset struct {
	id, kind, name, state string
	size, w, h            int
}

type fPlacement struct {
	id, loc, typ, group, asset string
	pos                        int
}

// fake is a stateful Asset Library.
type fake struct {
	t          *testing.T
	k          *tooltest.Kit
	mu         sync.Mutex
	assets     map[string]*fAsset
	placements map[string]*fPlacement
	next       int
	// failPlace, when set, answers the n-th placement POST (1-based) with
	// failStatus.
	failPlace, placeCount, failStatus int
	// hidden placement ids are omitted from asset placement reads (they sit
	// on another parent the tool cannot see).
	hidden map[string]bool
	// headers records every part PUT's headers.
	headers []http.Header
	// onAssetPost, when set, runs inside every asset reservation POST.
	onAssetPost func()
}

func (f *fake) id(prefix string) string {
	f.next++
	return fmt.Sprintf("%s%d", prefix, f.next)
}

type res struct {
	Type, ID string
	Attrs    map[string]any
	Rels     map[string]any
}

func (r res) raw() map[string]any {
	m := map[string]any{"type": r.Type, "id": r.ID, "attributes": r.Attrs}
	if len(r.Rels) > 0 {
		rels := map[string]any{}
		for k, v := range r.Rels {
			typ, id, _ := strings.Cut(v.(string), ":")
			rels[k] = map[string]any{"data": map[string]string{"type": typ, "id": id}}
		}
		m["relationships"] = rels
	}
	return m
}

func doc(t *testing.T, data any) []byte {
	t.Helper()
	var d any
	switch v := data.(type) {
	case res:
		d = v.raw()
	case []res:
		list := []map[string]any{}
		for _, r := range v {
			list = append(list, r.raw())
		}
		d = list
	}
	b, err := json.Marshal(map[string]any{"data": d})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func body(req tool.Request) map[string]any {
	b, _ := json.Marshal(req.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	d, _ := m["data"].(map[string]any)
	return d
}

func relID(d map[string]any, name string) string {
	r, _ := d["relationships"].(map[string]any)[name].(map[string]any)
	if r == nil {
		return ""
	}
	data, _ := r["data"].(map[string]any)
	id, _ := data["id"].(string)
	return id
}

func (a *fAsset) res() res {
	typ := "appAssetLibraryImages"
	if a.kind == "video" {
		typ = "appAssetLibraryVideos"
	}
	return res{Type: typ, ID: a.id, Attrs: map[string]any{"fileName": a.name, "fileSize": a.size, "state": a.state, "width": a.w, "height": a.h}}
}

func (p *fPlacement) res(f *fake) res {
	rel := "image"
	typ := "appAssetLibraryImages"
	if a := f.assets[p.asset]; a != nil && a.kind == "video" {
		rel, typ = "video", "appAssetLibraryVideos"
	}
	return res{Type: "appAssetLibraryPlacements", ID: p.id, Attrs: map[string]any{
		"placementType": p.typ, "placementGroup": p.group, "placementGroupPosition": p.pos, "state": "ACTIVE",
	}, Rels: map[string]any{rel: typ + ":" + p.asset}}
}

// recordFetch wraps the kit's fetcher, recording part headers.
type recordFetch struct {
	f    *fake
	next tool.Fetcher
}

func (r recordFetch) Fetch(ctx context.Context, method, rawURL string, header http.Header, b io.Reader, maxBytes int64) (*tool.FetchResponse, error) {
	r.f.mu.Lock()
	r.f.headers = append(r.f.headers, header.Clone())
	r.f.mu.Unlock()
	return r.next.Fetch(ctx, method, rawURL, header, b, maxBytes)
}

func newKit(t *testing.T, versionState string) (*tooltest.Kit, *fake) {
	t.Helper()
	k := tooltest.New(t)
	k.Env.Profile.Name = "library-test"
	k.Resolve.Apps = []tool.App{{ID: "app1", Name: "Hjul", BundleID: "se.hjul", Platforms: []tool.Platform{tool.IOS}}}
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{
		tool.VersionEditable: {ID: "ver1", String: "1.4", Platform: tool.IOS, State: versionState},
	}
	f := &fake{t: t, k: k, assets: map[string]*fAsset{}, placements: map[string]*fPlacement{}, hidden: map[string]bool{}}
	k.Env.Fetch = recordFetch{f: f, next: k.Fetch}
	f.routes()
	return k, f
}

func (f *fake) routes() {
	t, k := f.t, f.k
	raw, err := os.ReadFile("../assetlib/testdata/refdata.json")
	if err != nil {
		t.Fatal(err)
	}
	k.ASC.Handle("GET", "/v1/appAssetLibraryRefData", 200, string(raw))
	k.ASC.Handle("GET", "/v1/apps/{id}/assetLibrary", 200, `{"data":{"type":"appAssetLibraries","id":"app1"}}`)
	list := func(kind string) func(tool.Request) tooltest.Fixture {
		return func(tool.Request) tooltest.Fixture {
			f.mu.Lock()
			defer f.mu.Unlock()
			out := []res{}
			for _, id := range slices.Sorted(mapsKeys(f.assets)) {
				if a := f.assets[id]; a.kind == kind {
					out = append(out, a.res())
				}
			}
			return tooltest.Fixture{Status: 200, Body: doc(t, out)}
		}
	}
	k.ASC.HandleFunc("GET", "/v1/appAssetLibraries/{id}/images", list("image"))
	k.ASC.HandleFunc("GET", "/v1/appAssetLibraries/{id}/videos", list("video"))
	k.ASC.Handle("GET", "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", 200,
		`{"data":[{"type":"appStoreVersionLocalizations","id":"loc-en","attributes":{"locale":"en-US"}}]}`)
	k.ASC.Handle("GET", "/v1/appEvents/{id}", 200, `{"data":{"type":"appEvents","id":"ev1","attributes":{"eventState":"DRAFT"}}}`)
	k.ASC.Handle("GET", "/v1/appEvents/{id}/localizations", 200,
		`{"data":[{"type":"appEventLocalizations","id":"evloc-en","attributes":{"locale":"en-US"}}]}`)
	locPlacements := func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		loc := strings.Split(req.Path, "/")[3]
		var ps []*fPlacement
		for _, p := range f.placements {
			if p.loc == loc {
				ps = append(ps, p)
			}
		}
		slices.SortFunc(ps, func(a, b *fPlacement) int {
			if a.pos != b.pos {
				return a.pos - b.pos
			}
			return strings.Compare(a.id, b.id)
		})
		out := []res{}
		for _, p := range ps {
			out = append(out, p.res(f))
		}
		return tooltest.Fixture{Status: 200, Body: doc(t, out)}
	}
	k.ASC.HandleFunc("GET", "/v1/appStoreVersionLocalizations/{id}/placements", locPlacements)
	k.ASC.HandleFunc("GET", "/v1/appEventLocalizations/{id}/placements", locPlacements)
	for _, typ := range []string{"appAssetLibraryImages", "appAssetLibraryVideos"} {
		kind := "image"
		if typ == "appAssetLibraryVideos" {
			kind = "video"
		}
		k.ASC.HandleFunc("POST", "/v1/"+typ, func(req tool.Request) tooltest.Fixture {
			f.mu.Lock()
			defer f.mu.Unlock()
			d := body(req)
			attrs := d["attributes"].(map[string]any)
			a := &fAsset{id: f.id("asset"), kind: kind, name: attrs["fileName"].(string), size: int(attrs["fileSize"].(float64)), state: "AWAITING_UPLOAD"}
			f.assets[a.id] = a
			if f.onAssetPost != nil {
				f.onAssetPost()
			}
			u := "https://upload.example.com/" + a.id + "?sig=x"
			k.Fetch.Handle("PUT", u, 200, nil)
			r := a.res()
			r.Attrs["uploadOperations"] = []map[string]any{{
				"method": "PUT", "url": u, "length": a.size, "offset": 0,
				"requestHeaders": []map[string]string{{"name": "Content-Type", "value": "image/png"}},
			}}
			return tooltest.Fixture{Status: 201, Body: doc(t, r)}
		})
		k.ASC.HandleFunc("PATCH", "/v1/"+typ+"/{id}", func(req tool.Request) tooltest.Fixture {
			f.mu.Lock()
			defer f.mu.Unlock()
			a := f.assets[strings.Split(req.Path, "/")[3]]
			attrs := body(req)["attributes"].(map[string]any)
			if attrs["uploaded"] == true {
				a.state = "PREPARE_FOR_SUBMISSION"
			}
			if attrs["archived"] == true {
				a.state = "ARCHIVED"
			}
			return tooltest.Fixture{Status: 200, Body: doc(t, a.res())}
		})
		k.ASC.HandleFunc("GET", "/v1/"+typ+"/{id}", func(req tool.Request) tooltest.Fixture {
			f.mu.Lock()
			defer f.mu.Unlock()
			return tooltest.Fixture{Status: 200, Body: doc(t, f.assets[strings.Split(req.Path, "/")[3]].res())}
		})
		k.ASC.HandleFunc("GET", "/v1/"+typ+"/{id}/placements", func(req tool.Request) tooltest.Fixture {
			f.mu.Lock()
			defer f.mu.Unlock()
			id := strings.Split(req.Path, "/")[3]
			out := []res{}
			for _, pid := range slices.Sorted(mapsKeys(f.placements)) {
				if p := f.placements[pid]; p.asset == id && !f.hidden[pid] {
					out = append(out, p.res(f))
				}
			}
			return tooltest.Fixture{Status: 200, Body: doc(t, out)}
		})
		k.ASC.HandleFunc("DELETE", "/v1/"+typ+"/{id}", func(req tool.Request) tooltest.Fixture {
			f.mu.Lock()
			defer f.mu.Unlock()
			id := strings.Split(req.Path, "/")[3]
			for _, p := range f.placements {
				if p.asset == id {
					return tooltest.Fixture{Status: 409, Body: []byte(`{"errors":[{"status":"409","code":"STATE_ERROR.ASSET_HAS_PLACEMENTS","title":"asset has placements","detail":"delete its placements first"}]}`)}
				}
			}
			delete(f.assets, id)
			return tooltest.Fixture{Status: 204}
		})
	}
	k.ASC.HandleFunc("POST", "/v1/appAssetLibraryPlacements", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.placeCount++
		if f.placeCount == f.failPlace {
			if f.failStatus >= 500 {
				// The real client reports a 5xx on a mutation as outcome_unknown.
				return tooltest.Fixture{Err: fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, &tool.APIError{Status: f.failStatus})}
			}
			return tooltest.Fixture{Status: f.failStatus, Body: []byte(`{"errors":[{"status":"422","code":"ENTITY_ERROR","title":"bad","detail":"refused"}]}`)}
		}
		d := body(req)
		attrs := d["attributes"].(map[string]any)
		asset := relID(d, "image")
		if asset == "" {
			asset = relID(d, "video")
		}
		loc := relID(d, "appStoreVersionLocalization")
		if loc == "" {
			loc = relID(d, "appEventLocalization")
		}
		p := &fPlacement{id: f.id("pl"), loc: loc, typ: attrs["placementType"].(string), group: attrs["placementGroup"].(string), asset: asset, pos: 100 + f.next}
		f.placements[p.id] = p
		return tooltest.Fixture{Status: 201, Body: doc(t, p.res(f))}
	})
	k.ASC.HandleFunc("DELETE", "/v1/appAssetLibraryPlacements/{id}", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.placements, strings.Split(req.Path, "/")[3])
		return tooltest.Fixture{Status: 204}
	})
	k.ASC.HandleFunc("POST", "/v1/appAssetLibraryPlacementOrderingRequests", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		b, _ := json.Marshal(req.Body)
		var m struct {
			Data struct {
				Relationships struct {
					OrderedPlacements struct {
						Data []struct{ ID string } `json:"data"`
					} `json:"orderedPlacements"`
				} `json:"relationships"`
			} `json:"data"`
		}
		_ = json.Unmarshal(b, &m)
		for i, p := range m.Data.Relationships.OrderedPlacements.Data {
			if pl := f.placements[p.ID]; pl != nil {
				pl.pos = i + 1
			}
		}
		return tooltest.Fixture{Status: 201, Body: []byte(`{"data":{"type":"appAssetLibraryPlacementOrderingRequests","id":"ord1"}}`)}
	})
}

func mapsKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// seed adds an asset and (when loc != "") a placement of it.
func (f *fake) seed(name, state, loc, typ, group string, w, h, size int) (assetID, placementID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &fAsset{id: f.id("asset"), kind: "image", name: name, state: state, size: size, w: w, h: h}
	f.assets[a.id] = a
	if loc == "" {
		return a.id, ""
	}
	p := &fPlacement{id: f.id("pl"), loc: loc, typ: typ, group: group, asset: a.id, pos: f.next}
	f.placements[p.id] = p
	return a.id, p.id
}

func pngChunk(typ string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(append(b, typ...), data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}

// pngImage builds a header-only w×h PNG without alpha; seed varies content.
func pngImage(w, h int, seed byte) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(w))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(h))
	ihdr = append(ihdr, 8, 2, 0, 0, 0)
	b := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
	b = append(b, pngChunk("tEXt", []byte{'s', 0, seed})...)
	return append(b, pngChunk("IEND", nil)...)
}

func write(t *testing.T, k *tooltest.Kit, rel string, data []byte) {
	t.Helper()
	if err := k.Files.WriteFile(rel, data, 0o600); err != nil {
		t.Fatal(err)
	}
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
	Status   string         `json:"status"`
	Uploads  []uploadOut    `json:"uploads"`
	Reuse    []reuseOut     `json:"reuse"`
	Already  []alreadyOut   `json:"already"`
	Create   []placementOut `json:"placements_create"`
	Delete   []deleteOut    `json:"placements_delete"`
	Order    []orderOut     `json:"order"`
	Problems []problem      `json:"problems"`
	PlanID   string         `json:"plan_id"`
}

func plan(t *testing.T, k *tooltest.Kit, args string) planResult {
	t.Helper()
	return tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "library_plan", args)))
}

func withPlan(t *testing.T, k *tooltest.Kit, args string) string {
	t.Helper()
	p := plan(t, k, args)
	if p.PlanID == "" {
		t.Fatalf("plan %s has no plan_id: %+v", args, p)
	}
	return strings.TrimSuffix(args, "}") + fmt.Sprintf(`,"plan_id":%q}`, p.PlanID)
}

func mutations(k *tooltest.Kit) []string {
	var out []string
	for _, r := range k.ASC.Mutations() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func count(ms []string, prefix string) int {
	n := 0
	for _, m := range ms {
		if strings.HasPrefix(m, prefix) {
			n++
		}
	}
	return n
}

func threeShots(t *testing.T, k *tooltest.Kit) {
	for i := range 3 {
		write(t, k, fmt.Sprintf("lib/en-US/iphone/0%d.png", i+1), pngImage(1320, 2868, byte('a'+i)))
	}
}

const addArgs = `{"app":"app1","dir":"lib"}`

// Tests 1 and 2: add uploads and places three screenshots with one ordering
// request; a re-run has nothing left to do.
func TestAddThenRerunAlready(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	threeShots(t, k)
	tooltest.MustOK(t, call(t, k, "library_apply", withPlan(t, k, addArgs)))
	ms := mutations(k)
	if count(ms, "POST /v1/appAssetLibraryImages") != 3 || count(ms, "POST /v1/appAssetLibraryPlacements") != 3 ||
		count(ms, "POST /v1/appAssetLibraryPlacementOrderingRequests") != 1 || count(ms, "DELETE") != 0 {
		t.Fatalf("mutations = %v", ms)
	}
	if len(f.placements) != 3 {
		t.Fatalf("placements = %d", len(f.placements))
	}
	p := plan(t, k, addArgs)
	if p.Status != "no_changes" || len(p.Already) != 3 || len(p.Uploads) != 0 || len(p.Create) != 0 || len(p.Order) != 0 {
		t.Fatalf("re-plan = %+v", p)
	}
}

// Test 3: after a failed placement, the re-plan reuses the landed assets.
func TestResumeDoesNotRePostAssets(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	threeShots(t, k)
	f.failPlace, f.failStatus = 2, http.StatusUnprocessableEntity
	r := call(t, k, "library_apply", withPlan(t, k, addArgs))
	if out := tooltest.Decode[applyOut](t, r); out.Status != "partial" {
		t.Fatalf("first apply = %s", tooltest.JSON(t, r.Data))
	}
	p := plan(t, k, addArgs)
	if len(p.Uploads) != 0 || len(p.Reuse) != 3 || len(p.Create) != 1 {
		t.Fatalf("resume plan = %+v", p)
	}
	tooltest.MustOK(t, call(t, k, "library_apply", withPlan(t, k, addArgs)))
	if n := count(mutations(k), "POST /v1/appAssetLibraryImages"); n != 3 {
		t.Fatalf("asset POSTs = %d, want 3", n)
	}
	if len(f.placements) != 3 {
		t.Fatalf("placements = %d", len(f.placements))
	}
}

// Test 4: replace creates, then deletes, then reorders; needs a plan_id and
// refuses a stale plan.
func TestReplaceOrderAndGates(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	_, old := f.seed("old.png", "PREPARE_FOR_SUBMISSION", "loc-en", typeScreenshot, "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", 1320, 2868, 10)
	write(t, k, "lib/en-US/iphone/01.png", pngImage(1320, 2868, 'x'))
	write(t, k, "lib/en-US/iphone/02.png", pngImage(1320, 2868, 'y'))
	args := `{"app":"app1","dir":"lib","mode":"replace"}`
	tooltest.MustFail(t, call(t, k, "library_apply", args), "invalid_arguments")
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("mutations without plan_id = %v", mutations(k))
	}
	stale := withPlan(t, k, args)
	f.seed("other.png", "PREPARE_FOR_SUBMISSION", "loc-en", typeScreenshot, "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", 1320, 2868, 11)
	if r := call(t, k, "library_apply", stale); !r.IsError {
		t.Fatalf("stale plan applied: %s", tooltest.JSON(t, r.Data))
	}
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("stale plan mutated: %v", mutations(k))
	}
	k.Elicit(true)
	tooltest.MustOK(t, call(t, k, "library_apply", withPlan(t, k, args)))
	ms := mutations(k)
	lastCreate, firstDelete, order := -1, -1, -1
	for i, m := range ms {
		switch {
		case m == "POST /v1/appAssetLibraryPlacements":
			lastCreate = i
		case strings.HasPrefix(m, "DELETE /v1/appAssetLibraryPlacements/") && firstDelete < 0:
			firstDelete = i
		case m == "POST /v1/appAssetLibraryPlacementOrderingRequests":
			order = i
		}
	}
	if lastCreate < 0 || firstDelete < lastCreate || order < firstDelete {
		t.Fatalf("order of mutations = %v", ms)
	}
	if _, ok := f.placements[old]; ok {
		t.Fatalf("old placement kept: %v", ms)
	}
}

// Test 5: retiring an asset deletes its placements first; a placement the
// tool cannot see yields ASSET_HAS_PLACEMENTS with a hint.
func TestArchiveDeletesPlacementsFirst(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	asset, pl := f.seed("a.png", "PREPARE_FOR_SUBMISSION", "loc-en", typeScreenshot, "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", 1320, 2868, 10)
	k.Elicit(true)
	args := fmt.Sprintf(`{"app":"app1","archive":[%q]}`, asset)
	tooltest.MustOK(t, call(t, k, "library_apply", withPlan(t, k, args)))
	ms := mutations(k)
	if !slices.Equal(ms, []string{"DELETE /v1/appAssetLibraryPlacements/" + pl, "DELETE /v1/appAssetLibraryImages/" + asset}) {
		t.Fatalf("mutations = %v", ms)
	}

	asset2, pl2 := f.seed("b.png", "PREPARE_FOR_SUBMISSION", "loc-other", typeScreenshot, "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", 1320, 2868, 10)
	f.hidden[pl2] = true
	args = fmt.Sprintf(`{"app":"app1","archive":[%q]}`, asset2)
	r := call(t, k, "library_apply", withPlan(t, k, args))
	if !strings.Contains(tooltest.JSON(t, r.Data), "ASSET_HAS_PLACEMENTS") {
		t.Fatalf("result = %s", tooltest.JSON(t, r.Data))
	}
	if !strings.Contains(tooltest.JSON(t, r.Data), "delete them, then run library_plan again") && !strings.Contains(tooltest.JSON(t, r.Data), "placements") {
		t.Fatalf("no hint: %s", tooltest.JSON(t, r.Data))
	}
}

// Test 6: an 11th screenshot in a group is a problem.
func TestEleventhScreenshotProblem(t *testing.T) {
	k, _ := newKit(t, "PREPARE_FOR_SUBMISSION")
	for i := range 11 {
		write(t, k, fmt.Sprintf("lib/en-US/iphone/%02d.png", i+1), pngImage(1320, 2868, byte('a'+i)))
	}
	p := plan(t, k, addArgs)
	if p.Status != "invalid" || !hasProblem(p, "limit") || p.PlanID != "" {
		t.Fatalf("plan = %+v", p)
	}
}

// Test 7: a non-editable parent is a problem.
func TestNonEditableParentProblem(t *testing.T) {
	k, _ := newKit(t, "READY_FOR_SALE")
	threeShots(t, k)
	if p := plan(t, k, addArgs); p.Status != "invalid" || !hasProblem(p, "parent_not_editable") {
		t.Fatalf("plan = %+v", p)
	}
}

func hasProblem(p planResult, kind string) bool {
	return slices.ContainsFunc(p.Problems, func(pr problem) bool { return pr.Kind == kind })
}

// Test 8: one 5244×2950 file is one asset with header and search placements.
func TestUniversalCreativeTwoPlacements(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	write(t, k, "lib/en-US/header-search.png", pngImage(5244, 2950, 'h'))
	p := plan(t, k, addArgs)
	if len(p.Uploads) != 1 || p.Uploads[0].Category != catCreative || len(p.Create) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	tooltest.MustOK(t, call(t, k, "library_apply", withPlan(t, k, addArgs)))
	types := []string{}
	for _, pl := range f.placements {
		types = append(types, pl.typ)
	}
	slices.Sort(types)
	if len(f.assets) != 1 || !slices.Equal(types, []string{typeSearch, typeHeader}) {
		t.Fatalf("assets = %d, placement types = %v", len(f.assets), types)
	}
}

// Test 9: an event target never sends an ordering request.
func TestEventNoOrdering(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	write(t, k, "lib/en-US/event-card.png", pngImage(1920, 1080, 'c'))
	write(t, k, "lib/en-US/event-details.png", pngImage(1080, 1920, 'd'))
	args := `{"app":"app1","target":"event","id":"ev1","dir":"lib"}`
	tooltest.MustOK(t, call(t, k, "library_apply", withPlan(t, k, args)))
	ms := mutations(k)
	if count(ms, "POST /v1/appAssetLibraryPlacementOrderingRequests") != 0 || len(f.placements) != 2 {
		t.Fatalf("mutations = %v", ms)
	}
}

// Test 10: part PUTs carry no Authorization header.
func TestPartsWithoutAuthorization(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	threeShots(t, k)
	tooltest.MustOK(t, call(t, k, "library_apply", withPlan(t, k, addArgs)))
	if len(f.headers) != 3 {
		t.Fatalf("part PUTs = %d", len(f.headers))
	}
	for _, h := range f.headers {
		if h.Get("Authorization") != "" {
			t.Fatalf("part PUT sent Authorization: %v", h)
		}
	}
}

// A 5xx on a placement POST is outcome_unknown and stops the run.
func TestServerErrorOutcomeUnknown(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	write(t, k, "lib/en-US/header.png", pngImage(5244, 2950, 'h'))
	f.failPlace, f.failStatus = 1, http.StatusInternalServerError
	r := call(t, k, "library_apply", withPlan(t, k, addArgs))
	if !strings.Contains(tooltest.JSON(t, r.Data), "outcome_unknown") {
		t.Fatalf("result = %s", tooltest.JSON(t, r.Data))
	}
	if n := count(mutations(k), "POST /v1/appAssetLibraryPlacements"); n != 1 {
		t.Fatalf("placement POSTs = %d (retried?)", n)
	}
}

func TestMeta(t *testing.T) {
	ap, ok := tool.Lookup("library_apply")
	if !ok {
		t.Fatal("library_apply not registered")
	}
	if ap.Meta().Class != tool.Destructive || ap.Meta().Confirm != tool.ConfirmByPlan || ap.Meta().Toolset != tool.Library {
		t.Fatalf("library_apply meta = %+v", ap.Meta())
	}
	pl, _ := tool.Lookup("library_plan")
	if pl.Meta().Class != tool.Read || !pl.Meta().Idempotent {
		t.Fatalf("library_plan meta = %+v", pl.Meta())
	}
	for _, tl := range []tool.Tool{ap, pl} {
		if len(tl.Meta().Description) > 280 {
			t.Errorf("%s description %d bytes", tl.Meta().Name, len(tl.Meta().Description))
		}
	}
}

// A Mac-sized screenshot on an iOS version fits no iOS group: a problem
// naming the file, size and platform, never a MAC_PROFILE placement.
func TestPlatformFiltersGroups(t *testing.T) {
	k, _ := newKit(t, "PREPARE_FOR_SUBMISSION")
	write(t, k, "lib/en-US/APP_DESKTOP/01-new.png", pngImage(2880, 1800, 'm'))
	p := plan(t, k, `{"app":"app1","platform":"IOS","dir":"lib"}`)
	if p.Status != "invalid" || len(p.Create) != 0 || len(p.Uploads) != 0 {
		t.Fatalf("plan = %+v", p)
	}
	i := slices.IndexFunc(p.Problems, func(pr problem) bool { return pr.Kind == "no_group" })
	if i < 0 {
		t.Fatalf("problems = %+v", p.Problems)
	}
	for _, want := range []string{"en-US/APP_DESKTOP/01-new.png", "2880x1800", "IOS"} {
		if !strings.Contains(p.Problems[i].Message, want) {
			t.Errorf("problem %q lacks %q", p.Problems[i].Message, want)
		}
	}
}

const (
	replaceArgs = `{"app":"app1","dir":"lib","mode":"replace"}`
	bigGroup    = "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"
)

func stepsOf(t *testing.T, r *tool.Result, step string) []stepOut {
	t.Helper()
	var out []stepOut
	for _, s := range tooltest.Decode[applyOut](t, r).Steps {
		if s.Step == step {
			out = append(out, s)
		}
	}
	return out
}

// Replace never deletes the old placement when the new one failed to land.
func TestReplaceKeepsOldWhenCreateFails(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	_, old := f.seed("old.png", "PREPARE_FOR_SUBMISSION", "loc-en", typeScreenshot, bigGroup, 1320, 2868, 10)
	write(t, k, "lib/en-US/iphone/01.png", pngImage(1320, 2868, 'x'))
	f.failPlace, f.failStatus = 1, http.StatusUnprocessableEntity
	k.Elicit(true)
	r := call(t, k, "library_apply", withPlan(t, k, replaceArgs))
	if n := count(mutations(k), "DELETE /v1/appAssetLibraryPlacements/"); n != 0 {
		t.Fatalf("deleted despite failed create: %v", mutations(k))
	}
	if _, ok := f.placements[old]; !ok {
		t.Fatal("old placement gone")
	}
	ds := stepsOf(t, r, "delete_placement")
	if len(ds) != 1 || ds[0].Status != "not_attempted" || !strings.Contains(ds[0].Hint, "library_plan") {
		t.Fatalf("delete steps = %+v", ds)
	}
}

// Replacing a full group frees one slot right before each create instead of
// emptying the group up front, and stops deleting after a failed create.
func TestReplaceFullGroupDeletesOnlyOverflow(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	for i := range 10 {
		f.seed(fmt.Sprintf("old%d.png", i), "PREPARE_FOR_SUBMISSION", "loc-en", typeScreenshot, bigGroup, 1320, 2868, 10+i)
		write(t, k, fmt.Sprintf("lib/en-US/iphone/%02d.png", i+1), pngImage(1320, 2868, byte('a'+i)))
	}
	f.failPlace, f.failStatus = 3, http.StatusUnprocessableEntity
	k.Elicit(true)
	call(t, k, "library_apply", withPlan(t, k, replaceArgs))
	var seq []string
	for _, m := range mutations(k) {
		switch {
		case strings.HasPrefix(m, "DELETE /v1/appAssetLibraryPlacements/"):
			seq = append(seq, "D")
		case m == "POST /v1/appAssetLibraryPlacements":
			seq = append(seq, "P")
		}
	}
	// Slots 1–3 each free one placement; create 3 fails, create 4 reuses its
	// slot, and nothing more is deleted.
	if got := strings.Join(seq, ""); got != "DPDPDPP" {
		t.Fatalf("placement mutations = %s (%v)", got, mutations(k))
	}
	if len(f.placements) != 10 {
		t.Fatalf("placements = %d, want 10", len(f.placements))
	}
}

// Overlapping applies keep each other's MD5 index entries.
func TestIndexKeepsConcurrentEntries(t *testing.T) {
	k, f := newKit(t, "PREPARE_FOR_SUBMISSION")
	write(t, k, "lib/en-US/iphone/01.png", pngImage(1320, 2868, 'x'))
	key := stateKey(k.Env, "app1")
	f.onAssetPost = func() {
		if err := k.Env.State.Save(stateKind, key, map[string]string{"other-md5": "asset-other"}); err != nil {
			t.Error(err)
		}
	}
	tooltest.MustOK(t, call(t, k, "library_apply", withPlan(t, k, addArgs)))
	var index map[string]string
	if _, err := k.Env.State.Load(stateKind, key, &index); err != nil {
		t.Fatal(err)
	}
	if index["other-md5"] != "asset-other" || len(index) != 2 {
		t.Fatalf("index = %v", index)
	}
}

// L2-4: a folder that names a display or group the pixels don't fit is a
// display_mismatch problem, never a silent placement in whatever group the
// size fits; a correctly sized file in that folder lands in its group.
func TestDisplayFolderMismatch(t *testing.T) {
	for _, dir := range []string{"APP_IPHONE_DUO", "IPHONE_DUO_PROFILE", "iphone_duo", "APP_IPHONE_65"} {
		t.Run(dir, func(t *testing.T) {
			k, _ := newKit(t, "PREPARE_FOR_SUBMISSION")
			write(t, k, "lib/en-US/"+dir+"/01-a.png", pngImage(1320, 2868, 'd'))
			p := plan(t, k, `{"app":"app1","platform":"IOS","dir":"lib"}`)
			if p.Status != "invalid" || len(p.Create) != 0 || p.PlanID != "" || !hasProblem(p, "display_mismatch") {
				t.Fatalf("plan = %+v", p)
			}
			i := slices.IndexFunc(p.Problems, func(pr problem) bool { return pr.Kind == "display_mismatch" })
			for _, want := range []string{dir, "1320x2868", bigGroup} {
				if !strings.Contains(p.Problems[i].Message, want) {
					t.Errorf("problem %q lacks %q", p.Problems[i].Message, want)
				}
			}
		})
	}
	k, _ := newKit(t, "PREPARE_FOR_SUBMISSION")
	write(t, k, "lib/en-US/APP_IPHONE_DUO/01-a.png", pngImage(1398, 2034, 'd'))
	write(t, k, "lib/en-US/APP_IPHONE_67/01-b.png", pngImage(1320, 2868, 'e'))
	p := plan(t, k, `{"app":"app1","platform":"IOS","dir":"lib"}`)
	if len(p.Problems) != 0 || len(p.Create) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	for _, c := range p.Create {
		want := map[string]string{"en-US/APP_IPHONE_DUO/01-a.png": "IPHONE_DUO_PROFILE", "en-US/APP_IPHONE_67/01-b.png": bigGroup}[c.File]
		if c.Group != want {
			t.Errorf("%s → %s, want %s", c.File, c.Group, want)
		}
	}
}

// RB2-6: concurrent rememberAsset calls in one process keep every entry.
func TestRememberAssetSerialized(t *testing.T) {
	k, _ := newKit(t, "PREPARE_FOR_SUBMISSION")
	key := stateKey(k.Env, "app1")
	const n = 32
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { rememberAsset(k.Env, key, fmt.Sprintf("md5-%d", i), fmt.Sprintf("asset-%d", i)) })
	}
	wg.Wait()
	var index map[string]string
	if _, err := k.Env.State.Load(stateKind, key, &index); err != nil {
		t.Fatal(err)
	}
	if len(index) != n {
		t.Fatalf("index has %d entries, want %d", len(index), n)
	}
}
