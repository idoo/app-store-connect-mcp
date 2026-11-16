package uploads

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const packFile = "packs/supertonic-3.aar"

func packUploadTool(t *testing.T) tool.Tool {
	t.Helper()
	for _, tl := range tool.All() {
		if tl.Meta().Name == "asset_packs_upload" {
			return tl
		}
	}
	t.Fatal("asset_packs_upload not registered")
	return tool.Tool{}
}

func callPack(t *testing.T, k *tooltest.Kit, args string) *tool.Result {
	t.Helper()
	return tooltest.Call(t, packUploadTool(t), k.Env, args)
}

func doc(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"data": v})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func res(typ, id string, attrs map[string]any) map[string]any {
	return map[string]any{"type": typ, "id": id, "attributes": attrs}
}

// packBody is the archive the tests upload.
var packBody = bytes.Repeat([]byte("AA01 asset pack bytes "), 300)

// packKit writes the archive and registers the read routes: packs lists
// existing pack ids, versions the pack's versions (newest first), files
// the newest version's upload files.
func packKit(t *testing.T, packs []string, versions, files []map[string]any) (*tooltest.Kit, int64, *partFetcher) {
	t.Helper()
	k := newKit(t)
	if err := k.Files.WriteFile(packFile, packBody, 0o600); err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	for _, id := range packs {
		list = append(list, res("backgroundAssets", id, map[string]any{"assetPackIdentifier": "supertonic-3", "archived": false}))
	}
	k.ASC.Handle("GET", "/v1/apps/{id}/backgroundAssets", http.StatusOK, doc(t, append([]map[string]any{}, list...)))
	k.ASC.Handle("GET", "/v1/backgroundAssets/{id}/versions", http.StatusOK, doc(t, append([]map[string]any{}, versions...)))
	k.ASC.Handle("GET", "/v1/backgroundAssetVersions/{id}/backgroundAssetUploadFiles", http.StatusOK, doc(t, append([]map[string]any{}, files...)))
	return k, int64(len(packBody)), newFetcher(t, k)
}

func uploadFile(t *testing.T, id string, size int64, state string, withOps bool) map[string]any {
	t.Helper()
	attrs := map[string]any{
		"assetType": "ASSET", "fileName": "supertonic-3.aar", "fileSize": size,
		"assetDeliveryState": map[string]any{"state": state},
	}
	if withOps {
		attrs["uploadOperations"] = ops(size, 3)
	}
	return res("backgroundAssetUploadFiles", id, attrs)
}

func version(id, state string, details map[string]any) map[string]any {
	attrs := map[string]any{"version": "1", "state": state, "platforms": []string{"IOS"}}
	if details != nil {
		attrs["stateDetails"] = details
	}
	return res("backgroundAssetVersions", id, attrs)
}

// writeRoutes registers creates, reservation, commit and the version poll
// (states returned in order, the last repeats).
func writeRoutes(t *testing.T, k *tooltest.Kit, size int64, states ...map[string]any) {
	t.Helper()
	k.ASC.Handle("POST", "/v1/backgroundAssets", http.StatusCreated, doc(t, res("backgroundAssets", "pk-1", map[string]any{"assetPackIdentifier": "supertonic-3"})))
	k.ASC.Handle("POST", "/v1/backgroundAssetVersions", http.StatusCreated, doc(t, version("v-1", "AWAITING_UPLOAD", nil)))
	f := doc(t, uploadFile(t, "f-1", size, "AWAITING_UPLOAD", true))
	k.ASC.Handle("POST", "/v1/backgroundAssetUploadFiles", http.StatusCreated, f)
	k.ASC.Handle("GET", "/v1/backgroundAssetUploadFiles/{id}", http.StatusOK, f)
	k.ASC.Handle("PATCH", "/v1/backgroundAssetUploadFiles/{id}", http.StatusOK, doc(t, res("backgroundAssetUploadFiles", "f-1", nil)))
	for _, s := range states {
		k.ASC.Handle("GET", "/v1/backgroundAssetVersions/{id}", http.StatusOK, doc(t, s))
	}
}

func mutations(k *tooltest.Kit) []string {
	var out []string
	for _, r := range k.ASC.Mutations() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func TestAssetPacksUploadPreviewChangesNothing(t *testing.T) {
	k, size, f := packKit(t, []string{"pk-1"}, nil, nil)
	writeRoutes(t, k, size, version("v-1", "COMPLETE", nil))
	out := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, callPack(t, k, `{"pack":"supertonic-3","file":"`+packFile+`"}`)))
	if out["status"] != "needs_confirm" {
		t.Fatalf("out = %v", out)
	}
	p, _ := out["preview"].(map[string]any)
	in, _ := p["intended"].(map[string]any)
	if in["pack"] != "existing" || in["version"] != "new" || in["size_bytes"] != float64(size) {
		t.Errorf("intended = %v", in)
	}
	if len(k.ASC.Mutations()) != 0 || len(f.calls) != 0 {
		t.Errorf("preview wrote: %v %v", mutations(k), f.calls)
	}
}

func TestAssetPacksUploadNewPackNeedsHuman(t *testing.T) {
	k, size, _ := packKit(t, nil, nil, nil)
	writeRoutes(t, k, size, version("v-1", "COMPLETE", nil))
	args := `{"app":"Hjul","pack":"supertonic-3","file":"` + packFile + `","confirm":true}`
	tooltest.MustOK(t, callPack(t, k, `{"app":"Hjul","pack":"supertonic-3","file":"`+packFile+`"}`))
	if res := callPack(t, k, args); !res.IsError {
		t.Fatal("created a pack without a human")
	}
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("mutations without approval: %v", mutations(k))
	}

	asked := k.Elicit(true)
	out := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, callPack(t, k, args)))
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "supertonic-3") || !strings.Contains((*asked)[0], "app-1") {
		t.Errorf("prompts = %q", *asked)
	}
	want := []string{
		"POST /v1/backgroundAssets", "POST /v1/backgroundAssetVersions", "POST /v1/backgroundAssetUploadFiles",
		"PATCH /v1/backgroundAssetUploadFiles/f-1",
	}
	if got := mutations(k); !slices.Equal(got, want) {
		t.Errorf("mutations:\n got %q\nwant %q", got, want)
	}
	pack, _ := out["pack"].(map[string]any)
	ver, _ := out["version"].(map[string]any)
	if pack["id"] != "pk-1" || pack["created"] != true || ver["state"] != "COMPLETE" || ver["id"] != "v-1" ||
		out["uploaded_bytes"] != float64(size) || out["upload_file_id"] != "f-1" {
		t.Errorf("out = %v", out)
	}
	create := tooltest.JSON(t, k.ASC.RequestsTo("POST", "/v1/backgroundAssets")[0].Body)
	if !strings.Contains(create, `"assetPackIdentifier":"supertonic-3"`) || !strings.Contains(create, `"id":"app-1"`) {
		t.Errorf("create = %s", create)
	}
	commit := tooltest.JSON(t, k.ASC.RequestsTo("PATCH", "/v1/backgroundAssetUploadFiles/{id}")[0].Body)
	if !strings.Contains(commit, `"uploaded":true`) || !strings.Contains(commit, `"algorithm":"MD5"`) {
		t.Errorf("commit = %s", commit)
	}
}

func TestAssetPacksUploadExistingPackNoHuman(t *testing.T) {
	k, size, f := packKit(t, []string{"pk-1"}, []map[string]any{version("v-0", "COMPLETE", nil)}, nil)
	writeRoutes(t, k, size, version("v-1", "PROCESSING", nil), version("v-1", "COMPLETE", nil))
	args := `{"pack":"supertonic-3","file":"` + packFile + `"`
	tooltest.MustOK(t, callPack(t, k, args+`}`))
	out := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, callPack(t, k, args+`,"confirm":true}`)))
	want := []string{"POST /v1/backgroundAssetVersions", "POST /v1/backgroundAssetUploadFiles", "PATCH /v1/backgroundAssetUploadFiles/f-1"}
	if got := mutations(k); !slices.Equal(got, want) {
		t.Errorf("mutations = %q", got)
	}
	if ver, _ := out["version"].(map[string]any); ver["state"] != "COMPLETE" || len(f.calls) != 3 || f.bytes != size {
		t.Errorf("out = %v parts = %v", out, f.calls)
	}
}

// The live case: version 1 AWAITING_UPLOAD with a reserved, unsent file.
func TestAssetPacksUploadResumesReservation(t *testing.T) {
	files := []map[string]any{
		uploadFile(t, "f-0", int64(len(packBody))+1, "AWAITING_UPLOAD", false), // same name, other size: not this file
		uploadFile(t, "f-1", int64(len(packBody)), "AWAITING_UPLOAD", false),
	}
	k, size, f := packKit(t, []string{"pk-1"}, []map[string]any{version("v-1", "AWAITING_UPLOAD", nil)}, files)
	writeRoutes(t, k, size, version("v-1", "COMPLETE", nil))
	args := `{"pack":"supertonic-3","file":"` + packFile + `"`
	pv := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, callPack(t, k, args+`}`)))
	if in := pv["preview"].(map[string]any)["intended"].(map[string]any); in["upload_file"] != "reuse reservation f-1" {
		t.Errorf("intended = %v", in)
	}
	out := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, callPack(t, k, args+`,"confirm":true}`)))
	if got := mutations(k); !slices.Equal(got, []string{"PATCH /v1/backgroundAssetUploadFiles/f-1"}) {
		t.Errorf("mutations = %q", got)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/backgroundAssetUploadFiles/{id}")); n != 1 {
		t.Errorf("fresh reads of the reservation = %d", n)
	}
	if out["resumed"] != true || len(f.calls) != 3 || out["uploaded_bytes"] != float64(size) {
		t.Errorf("out = %v parts = %v", out, f.calls)
	}
}

func TestAssetPacksUploadAlreadyComplete(t *testing.T) {
	k, _, f := packKit(t, []string{"pk-1"}, []map[string]any{version("v-1", "COMPLETE", nil)},
		[]map[string]any{uploadFile(t, "f-1", int64(len(packBody)), "COMPLETE", false)})
	k.ASC.Handle("GET", "/v1/backgroundAssetVersions/{id}", http.StatusOK, doc(t, version("v-1", "COMPLETE", nil)))
	out := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, callPack(t, k, `{"pack":"supertonic-3","file":"`+packFile+`","confirm":true}`)))
	if out["already"] != true || out["upload_file_id"] != "f-1" || out["uploaded_bytes"] != float64(0) {
		t.Errorf("out = %v", out)
	}
	if len(k.ASC.Mutations()) != 0 || len(f.calls) != 0 {
		t.Errorf("wrote: %v %v", mutations(k), f.calls)
	}
}

func TestAssetPacksUploadFailedReportsErrors(t *testing.T) {
	k, size, _ := packKit(t, []string{"pk-1"}, nil, nil)
	writeRoutes(t, k, size, version("v-1", "FAILED", map[string]any{
		"errors": []map[string]string{{"code": "ITMS-91139", "description": "Manifest assetPackID does not match."}},
	}))
	args := `{"pack":"supertonic-3","file":"` + packFile + `"`
	tooltest.MustOK(t, callPack(t, k, args+`}`))
	out := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, callPack(t, k, args+`,"confirm":true}`)))
	ver, _ := out["version"].(map[string]any)
	errs, _ := ver["errors"].([]any)
	if ver["state"] != "FAILED" || len(errs) != 1 || !strings.Contains(tooltest.JSON(t, errs), "ITMS-91139") || out["hint"] == nil {
		t.Errorf("out = %v", out)
	}
}

func TestAssetPacksUploadStillProcessing(t *testing.T) {
	k, size, _ := packKit(t, []string{"pk-1"}, nil, nil)
	writeRoutes(t, k, size, version("v-1", "PROCESSING", nil))
	args := `{"pack":"supertonic-3","file":"` + packFile + `","wait_seconds":10`
	tooltest.MustOK(t, callPack(t, k, args+`}`))
	res := tooltest.MustOK(t, callPack(t, k, args+`,"confirm":true}`))
	out := tooltest.Decode[map[string]any](t, res)
	if out["status"] != "still_processing" || k.Clock.Slept != 10*time.Second {
		t.Fatalf("out = %v slept %v", out, k.Clock.Slept)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "asset_packs_list" {
		t.Errorf("next = %+v", res.Next)
	}
}

func TestAssetPacksUploadRefusesBeforeRequests(t *testing.T) {
	for name, args := range map[string]string{
		"outside root": `{"pack":"supertonic-3","file":"../../etc/supertonic-3.aar","confirm":true}`,
		"not aar":      `{"pack":"supertonic-3","file":"packs/supertonic-3.zip","confirm":true}`,
		"no pack":      `{"file":"` + packFile + `","confirm":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			k, _, _ := packKit(t, nil, nil, nil)
			tooltest.MustFail(t, callPack(t, k, args), "invalid_arguments")
			if n := len(k.ASC.Requests()); n != 0 {
				t.Errorf("requests = %d", n)
			}
		})
	}
}

func TestAssetPacksUploadReadOnly(t *testing.T) {
	k, _, _ := packKit(t, []string{"pk-1"}, nil, nil)
	k.ReadOnly()
	tooltest.MustFail(t, callPack(t, k, `{"pack":"supertonic-3","file":"`+packFile+`","confirm":true}`), "writes_disabled")
}
