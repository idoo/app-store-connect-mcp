package uploads

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
	"github.com/idoo/app-store-connect-mcp/internal/upload"
)

const infoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>se.jvcr.hjul</string>
<key>CFBundleShortVersionString</key><string>2.3.1</string>
<key>CFBundleVersion</key><string>417</string>
</dict></plist>`

// newKit returns a Kit with one app; its FakeASC checks every request
// against the spec.
func newKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Resolve.Apps = []tool.App{{ID: "app-1", BundleID: "se.jvcr.hjul", Name: "Hjul", Platforms: []tool.Platform{tool.IOS}}}
	return k
}

func buildsUploadTool(t *testing.T) tool.Tool {
	t.Helper()
	for _, tl := range tool.All() {
		if tl.Meta().Name == "builds_upload" {
			return tl
		}
	}
	t.Fatal("builds_upload not registered")
	return tool.Tool{}
}

func call(t *testing.T, k *tooltest.Kit, args string) *tool.Result {
	t.Helper()
	return tooltest.Call(t, buildsUploadTool(t), k.Env, args)
}

// writeIPA stores a zip with Payload/Hjul.app/Info.plist and returns its size.
func writeIPA(t *testing.T, k *tooltest.Kit, name string) int64 {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for file, body := range map[string]string{
		"Payload/Hjul.app/Info.plist": infoPlist,
		"Payload/Hjul.app/Hjul":       strings.Repeat("\xCF\xFA\xED\xFE machine code ", 64),
	} {
		w, err := zw.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := k.Files.WriteFile(name, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return int64(buf.Len())
}

func partURL(i int) string { return fmt.Sprintf("https://upload.example.com/part/%d?sig=x", i) }

// ops splits size bytes into n presigned parts; each asks for a forbidden
// Authorization header that must never be sent.
func ops(size int64, n int) []upload.Operation {
	out := make([]upload.Operation, n)
	per := size / int64(n)
	for i := range out {
		length := per
		if i == n-1 {
			length = size - per*int64(n-1)
		}
		out[i] = upload.Operation{
			Method: "PUT", URL: partURL(i), Offset: per * int64(i), Length: length,
			RequestHeaders: []upload.Header{{Name: "Content-Type", Value: "application/octet-stream"}, {Name: "Authorization", Value: "Bearer leak"}},
		}
	}
	return out
}

func resource(t *testing.T, typ, id string, attrs map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"data": map[string]any{"type": typ, "id": id, "attributes": attrs}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fixture registers the build-upload routes; states are the successive
// buildUpload states returned by GET (the last repeats).
func fixture(t *testing.T, k *tooltest.Kit, size int64, uti string, states ...string) []upload.Operation {
	t.Helper()
	parts := ops(size, 3)
	k.ASC.Handle("GET", "/v1/apps/{id}/buildUploads", http.StatusOK, `{"data":[]}`)
	k.ASC.Handle("POST", "/v1/buildUploads", http.StatusCreated, resource(t, "buildUploads", "bu-1", map[string]any{
		"state": map[string]any{"state": "AWAITING_UPLOAD"},
	}))
	file := resource(t, "buildUploadFiles", "buf-1", map[string]any{
		"fileName": "Hjul.ipa", "fileSize": size, "uploadOperations": parts, "assetType": "ASSET", "uti": uti,
	})
	k.ASC.Handle("POST", "/v1/buildUploadFiles", http.StatusCreated, file)
	k.ASC.Handle("GET", "/v1/buildUploadFiles/{id}", http.StatusOK, file)
	k.ASC.Handle("PATCH", "/v1/buildUploadFiles/{id}", http.StatusOK, resource(t, "buildUploadFiles", "buf-1", nil))
	for _, s := range states {
		st := map[string]any{"state": s}
		if s == "COMPLETE" {
			st["infos"] = []map[string]string{{"code": "I1", "description": "Build accepted."}}
		}
		k.ASC.Handle("GET", "/v1/buildUploads/{id}", http.StatusOK, resource(t, "buildUploads", "bu-1", map[string]any{"state": st}))
	}
	return parts
}

// partFetcher answers presigned PUTs, records them, fails the first
// failures[url] calls with 403 and rejects any Authorization header.
type partFetcher struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string
	bytes    int64
	failures map[string]int
}

func (f *partFetcher) Fetch(_ context.Context, method, rawURL string, header http.Header, body io.Reader, _ int64) (*tool.FetchResponse, error) {
	if header.Get("Authorization") != "" || !strings.HasPrefix(rawURL, "https://") {
		f.t.Errorf("%s %s sent with Authorization or without https", method, rawURL)
	}
	n, err := io.Copy(io.Discard, body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method+" "+rawURL)
	if f.failures[rawURL] > 0 {
		f.failures[rawURL]--
		return &tool.FetchResponse{Status: http.StatusForbidden, Header: http.Header{}}, nil
	}
	f.bytes += n
	return &tool.FetchResponse{Status: http.StatusOK, Header: http.Header{}}, nil
}

func newFetcher(t *testing.T, k *tooltest.Kit) *partFetcher {
	f := &partFetcher{t: t, failures: map[string]int{}}
	k.Env.Fetch = f
	return f
}

func paths(k *tooltest.Kit) []string {
	var out []string
	for _, r := range k.ASC.Requests() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func TestBuildsUploadIPASequence(t *testing.T) {
	k := newKit(t)
	size := writeIPA(t, k, "build/Hjul.ipa")
	fixture(t, k, size, "com.apple.ipa", "PROCESSING", "COMPLETE")
	f := newFetcher(t, k)
	var progress []float64
	k.Env.Progress = func(done, _ float64, _ string) { progress = append(progress, done) }

	res := tooltest.MustOK(t, call(t, k, `{"app":"Hjul","path":"build/Hjul.ipa"}`))
	out := tooltest.Decode[map[string]any](t, res)
	if out["build_upload_id"] != "bu-1" || out["state"] != "COMPLETE" || out["version"] != "2.3.1" || out["build_number"] != "417" ||
		out["platform"] != "IOS" || out["uploaded_bytes"] != float64(size) || out["size_bytes"] != float64(size) {
		t.Fatalf("out = %v", out)
	}
	if infos, _ := out["infos"].([]any); len(infos) != 1 {
		t.Errorf("infos = %v", out["infos"])
	}
	if app, _ := out["app"].(map[string]any); app["id"] != "app-1" {
		t.Errorf("app = %v", out["app"])
	}
	want := []string{
		"GET /v1/apps/app-1/buildUploads", "POST /v1/buildUploads", "POST /v1/buildUploadFiles",
		"PATCH /v1/buildUploadFiles/buf-1", "GET /v1/buildUploads/bu-1", "GET /v1/buildUploads/bu-1",
	}
	if got := paths(k); !slices.Equal(got, want) {
		t.Errorf("requests:\n got %q\nwant %q", got, want)
	}
	create := tooltest.JSON(t, k.ASC.RequestsTo("POST", "/v1/buildUploads")[0].Body)
	for _, s := range []string{`"cfBundleShortVersionString":"2.3.1"`, `"cfBundleVersion":"417"`, `"platform":"IOS"`, `"id":"app-1"`} {
		if !strings.Contains(create, s) {
			t.Errorf("create body %s lacks %s", create, s)
		}
	}
	fileBody := tooltest.JSON(t, k.ASC.RequestsTo("POST", "/v1/buildUploadFiles")[0].Body)
	if !strings.Contains(fileBody, `"uti":"com.apple.ipa"`) || !strings.Contains(fileBody, fmt.Sprintf(`"fileSize":%d`, size)) {
		t.Errorf("file body = %s", fileBody)
	}
	commit := tooltest.JSON(t, k.ASC.RequestsTo("PATCH", "/v1/buildUploadFiles/{id}")[0].Body)
	if !strings.Contains(commit, `"uploaded":true`) || !strings.Contains(commit, `"algorithm":"MD5"`) {
		t.Errorf("commit = %s", commit)
	}
	if len(f.calls) != 3 || f.bytes != size {
		t.Errorf("parts = %v bytes=%d", f.calls, f.bytes)
	}
	if len(progress) == 0 || progress[len(progress)-1] != float64(size) {
		t.Errorf("progress = %v", progress)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "builds_wait" || res.Next[0].Args["build"] != "417" {
		t.Errorf("next = %+v", res.Next)
	}
}

func TestBuildsUploadPKGNeedsVersion(t *testing.T) {
	k := newKit(t)
	if err := k.Files.WriteFile("Hjul.pkg", []byte("xar!pkg-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := tooltest.MustFail(t, call(t, k, `{"path":"Hjul.pkg","version":"1.0"}`), "invalid_arguments")
	if !strings.Contains(b.Message, "needs version and build number") {
		t.Errorf("message = %s", b.Message)
	}
	if n := len(k.ASC.Requests()); n != 0 {
		t.Errorf("requests = %d", n)
	}
}

func TestBuildsUploadMismatchedVersion(t *testing.T) {
	k := newKit(t)
	writeIPA(t, k, "Hjul.ipa")
	b := tooltest.MustFail(t, call(t, k, `{"path":"Hjul.ipa","build_number":"999"}`), "invalid_arguments")
	if !strings.Contains(b.Message, "contains 2.3.1 (417)") {
		t.Errorf("message = %s", b.Message)
	}
}

func TestBuildsUploadOutsideRoot(t *testing.T) {
	k := newKit(t)
	tooltest.MustFail(t, call(t, k, `{"path":"../../etc/Hjul.ipa"}`), "invalid_arguments")
}

func TestBuildsUploadDuplicate(t *testing.T) {
	k := newKit(t)
	writeIPA(t, k, "Hjul.ipa")
	k.ASC.Handle("GET", "/v1/apps/{id}/buildUploads", http.StatusOK,
		`{"data":[{"type":"buildUploads","id":"bu-9","attributes":{"state":{"state":"COMPLETE"}}}]}`)
	b := tooltest.MustFail(t, call(t, k, `{"path":"Hjul.ipa"}`), "duplicate_build")
	if len(b.Next) != 1 || b.Next[0].Tool != "builds_list" || b.Next[0].Args["app"] != "app-1" {
		t.Errorf("next = %+v", b.Next)
	}
	if !strings.Contains(b.Message, "bu-9") {
		t.Errorf("message = %s", b.Message)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Error("mutated on duplicate")
	}
}

func TestBuildsUploadResume(t *testing.T) {
	k := newKit(t)
	size := writeIPA(t, k, "Hjul.ipa")
	parts := fixture(t, k, size, "com.apple.ipa", "COMPLETE")
	f := newFetcher(t, k)
	f.failures[partURL(2)] = 1

	b := tooltest.MustFail(t, call(t, k, `{"path":"Hjul.ipa"}`), "error")
	if !strings.Contains(b.Message, "call again with the same file to resume") {
		t.Fatalf("message = %s", b.Message)
	}
	if n := len(k.ASC.RequestsTo("PATCH", "/v1/buildUploadFiles/{id}")); n != 0 {
		t.Fatalf("committed after interruption: %d", n)
	}
	first := len(f.calls)

	res := tooltest.MustOK(t, call(t, k, `{"path":"Hjul.ipa"}`))
	out := tooltest.Decode[map[string]any](t, res)
	if out["resumed"] != true || out["parts_skipped"] != float64(len(parts)-1) || out["state"] != "COMPLETE" {
		t.Fatalf("out = %v", out)
	}
	if again := f.calls[first:]; len(again) != 1 || again[0] != "PUT "+partURL(2) {
		t.Errorf("resumed PUTs = %v", again)
	}
	if n := len(k.ASC.RequestsTo("POST", "/v1/buildUploads")); n != 1 {
		t.Errorf("buildUploads created %d times", n)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/buildUploadFiles/{id}")); n != 1 {
		t.Errorf("resume reads = %d", n)
	}
}

func TestBuildsUploadStillProcessing(t *testing.T) {
	k := newKit(t)
	size := writeIPA(t, k, "Hjul.ipa")
	fixture(t, k, size, "com.apple.ipa", "PROCESSING")
	newFetcher(t, k)

	res := tooltest.MustOK(t, call(t, k, `{"path":"Hjul.ipa","wait_seconds":10}`))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "still_processing" {
		t.Fatalf("out = %v", got)
	}
	inner, _ := got["result"].(map[string]any)
	if inner["state"] != "PROCESSING" || inner["still_processing"] != true || inner["build_upload_id"] != "bu-1" {
		t.Errorf("result = %v", inner)
	}
	if k.Clock.Slept != 10*time.Second {
		t.Errorf("waited %v", k.Clock.Slept)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "builds_wait" ||
		res.Next[0].Args["app"] != "app-1" || res.Next[0].Args["build"] != "417" {
		t.Errorf("next = %+v", res.Next)
	}

	// Calling again resumes polling without re-uploading.
	puts := len(k.ASC.Mutations())
	tooltest.MustOK(t, call(t, k, `{"path":"Hjul.ipa","wait_seconds":1}`))
	if len(k.ASC.Mutations()) != puts {
		t.Error("re-uploaded while Apple was processing")
	}
}

func TestBuildsUploadFailed(t *testing.T) {
	k := newKit(t)
	size := writeIPA(t, k, "Hjul.ipa")
	fixture(t, k, size, "com.apple.ipa")
	k.ASC.Handle("GET", "/v1/buildUploads/{id}", http.StatusOK, resource(t, "buildUploads", "bu-1", map[string]any{
		"state": map[string]any{"state": "FAILED", "errors": []map[string]string{{"code": "90062", "description": "Invalid bundle version."}}},
	}))
	newFetcher(t, k)
	b := tooltest.MustFail(t, call(t, k, `{"path":"Hjul.ipa"}`), "upload_failed")
	if !strings.Contains(b.Message, "90062: Invalid bundle version.") || len(b.Next) != 1 || b.Next[0].Tool != "builds_list" {
		t.Errorf("body = %+v", b)
	}
}

func TestBuildsUploadReadOnly(t *testing.T) {
	k := newKit(t).ReadOnly()
	writeIPA(t, k, "Hjul.ipa")
	tooltest.MustFail(t, call(t, k, `{"path":"Hjul.ipa"}`), "writes_disabled")
}
