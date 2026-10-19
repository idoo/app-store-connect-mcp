package webhooks

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// kit returns a kit with one app; its FakeASC checks every request against
// the spec.
func kit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Resolve.Apps = []tool.App{{ID: "A1", BundleID: "se.jvcr.hjul", Name: "Hjul"}}
	return k
}

func lookupTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

func wantLog(t *testing.T, k *tooltest.Kit, want ...string) {
	t.Helper()
	got := make([]string, 0)
	for _, r := range k.ASC.Requests() {
		got = append(got, r.Method+" "+r.Path)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func bodyOf(t *testing.T, r tool.Request) map[string]any {
	t.Helper()
	b, err := json.Marshal(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m["data"].(map[string]any)
}

const hooksDoc = `{"data":[
 {"type":"webhooks","id":"W1","attributes":{"name":"ci","url":"https://hooks.example.com/asc?token=abc","enabled":true,
  "eventTypes":["BUILD_UPLOAD_STATE_UPDATED","APP_STORE_VERSION_APP_VERSION_STATE_UPDATED"]}}]}`

const emptyDoc = `{"data":[]}`

const createdDoc = `{"data":{"type":"webhooks","id":"W9","attributes":{"name":"slack","url":"https://hooks.example.org/x","enabled":true,
 "eventTypes":["BUILD_UPLOAD_STATE_UPDATED"]}}}`

const createArgs = `{"name":"slack","url":"https://hooks.example.org/x","event_types":["BUILD_UPLOAD_STATE_UPDATED"],"confirm":true}`

func TestWebhooksListHostOnlyAndDeliveries(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	k.ASC.Handle(http.MethodGet, "/v1/webhooks/W1/deliveries", http.StatusOK, `{"data":[
	 {"type":"webhookDeliveries","id":"D1","attributes":{"deliveryState":"FAILED","createdDate":"2026-10-24T10:00:00Z",
	  "errorMessage":"connection refused","response":{"httpStatusCode":502}}},
	 {"type":"webhookDeliveries","id":"D2","attributes":{"deliveryState":"SUCCEEDED","response":{"httpStatusCode":200}}}]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "webhooks_list"), k.Env, `{"app":"Hjul"}`))
	wantLog(t, k, "GET /v1/apps/A1/webhooks", "GET /v1/webhooks/W1/deliveries")
	if q := k.ASC.Requests()[1].Query; q.Get("limit") != "10" {
		t.Fatalf("deliveries query = %s", q.Encode())
	}
	js := tooltest.JSON(t, res.Data)
	if strings.Contains(js, "token=abc") || !strings.Contains(js, `"url_host":"hooks.example.com"`) {
		t.Fatalf("full url leaked or host missing: %s", js)
	}
	out := tooltest.Decode[map[string]any](t, res)
	h := out["webhooks"].([]any)[0].(map[string]any)
	ds := h["recent_deliveries"].([]any)
	d0 := ds[0].(map[string]any)
	if len(ds) != 2 || d0["http_status"] != float64(502) || d0["state"] != "FAILED" ||
		h["event_types"].([]any)[0] != "APP_STORE_VERSION_APP_VERSION_STATE_UPDATED" {
		t.Fatalf("out = %v", out)
	}
	if !strings.Contains(js, `"error":{"untrusted":"connection refused"}`) {
		t.Fatalf("error message not wrapped: %s", js)
	}
}

func TestWebhooksCreateGeneratesSecretOnce(t *testing.T) {
	k := kit(t)
	asked := k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	k.ASC.Handle(http.MethodPost, "/v1/webhooks", http.StatusCreated, createdDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env, createArgs))
	wantLog(t, k, "GET /v1/apps/A1/webhooks", "POST /v1/webhooks")
	data := bodyOf(t, k.ASC.Requests()[1])
	attrs := data["attributes"].(map[string]any)
	secret, _ := attrs["secret"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(secret) || attrs["enabled"] != true {
		t.Fatalf("attrs = %v", attrs)
	}
	if data["relationships"].(map[string]any)["app"].(map[string]any)["data"].(map[string]any)["id"] != "A1" {
		t.Fatalf("relationships = %v", data["relationships"])
	}
	out, _ := res.Data.(upsertOut) // name is tool.Untrusted: compare the Go value
	if out.Action != "created" || !strings.Contains(out.SecretNote, "sensitive") || out.Webhook.ID != "W9" {
		t.Fatalf("out = %+v", out)
	}
	if js := tooltest.JSON(t, res.Data); regexp.MustCompile(`[0-9a-f]{64}`).MatchString(js) {
		t.Fatalf("generated secret in result: %s", js)
	}
	saved, err := os.ReadFile(filepath.Join(k.Files.Root, "webhook-secrets", "W9.txt"))
	if err != nil || strings.TrimSpace(string(saved)) != secret {
		t.Fatalf("secret file = %q, %v", saved, err)
	}
	if fi, err := os.Stat(filepath.Join(k.Files.Root, "webhook-secrets", "W9.txt")); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) || out.SecretFile == "" { // Windows reports 0666.
		t.Fatalf("secret file mode = %v, %v; secret_file = %q", fi, err, out.SecretFile)
	}
	if len(*asked) != 1 || strings.Contains((*asked)[0], secret) || !strings.Contains((*asked)[0], "https://hooks.example.org/x") {
		t.Fatalf("prompt = %v", *asked)
	}

	// A provided secret is sent but never echoed.
	k2 := kit(t)
	k2.Elicit(true)
	k2.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, emptyDoc)
	k2.ASC.Handle(http.MethodPost, "/v1/webhooks", http.StatusCreated, createdDoc)
	res = tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k2.Env,
		`{"name":"slack","url":"https://hooks.example.org/x","event_types":["BUILD_UPLOAD_STATE_UPDATED"],"secret":"s3cr3t-value","confirm":true}`))
	if strings.Contains(tooltest.JSON(t, res.Data), "s3cr3t-value") {
		t.Fatal("provided secret echoed")
	}
}

// A failed create keeps the generated secret in its 0600 pending file, written
// before the POST, and names that file in the hint.
func TestWebhooksCreateFailureKeepsPendingSecret(t *testing.T) {
	k := kit(t)
	k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	k.ASC.Handle(http.MethodPost, "/v1/webhooks", http.StatusInternalServerError, `{"errors":[{"status":"500","code":"UNEXPECTED_ERROR","title":"boom"}]}`)
	res := tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env, createArgs)
	if !res.IsError {
		t.Fatalf("want error, got %+v", res)
	}
	pending, err := filepath.Glob(filepath.Join(k.Files.Root, "webhook-secrets", "pending-*.txt"))
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending files = %v, %v", pending, err)
	}
	secret := bodyOf(t, k.ASC.Requests()[1])["attributes"].(map[string]any)["secret"].(string)
	saved, err := os.ReadFile(pending[0])
	if err != nil || strings.TrimSpace(string(saved)) != secret {
		t.Fatalf("pending file = %q, %v", saved, err)
	}
	if fi, err := os.Stat(pending[0]); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("pending mode = %v, %v", fi, err)
	}
	b := tooltest.Decode[tool.ErrorBody](t, res)
	if !strings.Contains(b.Hint, pending[0]) || strings.Contains(tooltest.JSON(t, res.Data), secret) {
		t.Fatalf("hint = %q", b.Hint)
	}
}

// An existing webhook-secrets/.gitignore that is not exactly "*\n" is refused
// before any approval or POST.
func TestWebhooksCreateRefusesWeakGitignore(t *testing.T) {
	k := kit(t)
	asked := k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	k.ASC.Handle(http.MethodPost, "/v1/webhooks", http.StatusCreated, createdDoc)
	if err := k.Files.WriteFile("webhook-secrets/.gitignore", []byte("!*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env, createArgs)
	if b := tooltest.Decode[tool.ErrorBody](t, res); !res.IsError || b.Error != "invalid_arguments" {
		t.Fatalf("res = %+v", res)
	}
	if got := k.ASC.RequestsTo(http.MethodPost, "/v1/webhooks"); len(got) != 0 || len(*asked) != 0 {
		t.Fatalf("posted %d, asked %d", len(got), len(*asked))
	}
}

// Without a writable file root a generated secret could not be saved, so the
// create is refused before anything reaches Apple.
func TestWebhooksCreateWithoutFileRootRefused(t *testing.T) {
	k := kit(t)
	k.Elicit(true)
	notDir := filepath.Join(k.Files.Root, "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	k.Env.Files = &tooltest.FakeFiles{Root: notDir}
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, emptyDoc)
	k.ASC.Handle(http.MethodPost, "/v1/webhooks", http.StatusCreated, createdDoc)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env, createArgs), tool.ErrInvalidArgs.Code)
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("mutations = %d", n)
	}
}

func TestWebhooksUpsertRequiresHTTPS(t *testing.T) {
	for _, u := range []string{"http://hooks.example.org/x", "https://user:pw@hooks.example.org/x", "hooks.example.org"} {
		k := kit(t)
		tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env,
			`{"name":"slack","url":"`+u+`","event_types":["BUILD_UPLOAD_STATE_UPDATED"],"confirm":true}`), tool.ErrInvalidArgs.Code)
		if len(k.ASC.Requests()) != 0 {
			t.Fatalf("%s: requests made", u)
		}
	}
	k := kit(t)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env,
		`{"name":"slack","url":"https://h.example.org","event_types":["NOPE"]}`), tool.ErrInvalidArgs.Code)
}

func TestWebhooksUpsertNeedsHuman(t *testing.T) {
	// Preview without confirm: nothing written.
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, emptyDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env,
		`{"name":"slack","url":"https://hooks.example.org/x","event_types":["BUILD_UPLOAD_STATE_UPDATED"]}`))
	if tooltest.Decode[map[string]any](t, res)["status"] != tool.ErrNeedsConfirm.Code || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("preview = %s", tooltest.JSON(t, res.Data))
	}

	// Declined.
	k = kit(t)
	k.Elicit(false)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, emptyDoc)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env, createArgs), tool.ErrDeclined.Code)
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("declined call wrote")
	}

	// No elicitation: refused.
	k = kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, emptyDoc)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env, createArgs), tool.ErrHumanRequired.Code)
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("unapproved call wrote")
	}

	// No elicitation, AllowNoElicit: proceeds.
	k = kit(t)
	k.Env.Profile.AllowNoElicit = true
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, emptyDoc)
	k.ASC.Handle(http.MethodPost, "/v1/webhooks", http.StatusCreated, createdDoc)
	tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env,
		`{"name":"slack","url":"https://hooks.example.org/x","event_types":["BUILD_UPLOAD_STATE_UPDATED"]}`))
	tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env, createArgs))
	if len(k.ASC.Mutations()) != 1 {
		t.Fatal("AllowNoElicit call did not write")
	}

	// Read-only profile: refused.
	k = kit(t).ReadOnly()
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env, createArgs), tool.ErrWritesDisabled.Code)
}

func TestWebhooksUpdateByNameAndPing(t *testing.T) {
	k := kit(t)
	k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	k.ASC.Handle(http.MethodPatch, "/v1/webhooks/W1", http.StatusOK,
		`{"data":{"type":"webhooks","id":"W1","attributes":{"name":"ci","url":"https://hooks.example.com/asc","enabled":false,"eventTypes":["BUILD_UPLOAD_STATE_UPDATED"]}}}`)
	k.ASC.Handle(http.MethodPost, "/v1/webhookPings", http.StatusCreated, `{"data":{"type":"webhookPings","id":"P1"}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env,
		`{"name":"CI","enabled":false,"ping":true,"confirm":true}`))
	wantLog(t, k, "GET /v1/apps/A1/webhooks", "PATCH /v1/webhooks/W1", "POST /v1/webhookPings")
	attrs := bodyOf(t, k.ASC.Requests()[1])["attributes"].(map[string]any)
	if len(attrs) != 2 || attrs["enabled"] != false || attrs["name"] != "CI" {
		t.Fatalf("patch attrs = %v", attrs)
	}
	ping := bodyOf(t, k.ASC.Requests()[2])
	if ping["relationships"].(map[string]any)["webhook"].(map[string]any)["data"].(map[string]any)["id"] != "W1" {
		t.Fatalf("ping = %v", ping)
	}
	out, _ := res.Data.(upsertOut) // name is tool.Untrusted: compare the Go value
	if out.Action != "updated" || out.PingID != "P1" || out.SecretFile != "" {
		t.Fatalf("out = %+v", out)
	}
}

// TestWebhooksRedirectNamesOldAndNewHost (CD-1): a URL change shows both hosts
// in the approval form, and a look-alike non-ASCII host is escaped with a note.
func TestWebhooksRedirectNamesOldAndNewHost(t *testing.T) {
	k := kit(t)
	asked := k.Elicit(false)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env,
		`{"webhook":"W1","url":"https://h\u043e\u043eks.example.com/asc","confirm":true}`), tool.ErrDeclined.Code)
	if len(*asked) != 1 || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("asked %d, mutations %d", len(*asked), len(k.ASC.Mutations()))
	}
	form := (*asked)[0]
	summary, _, _ := strings.Cut(form, "\n")
	if !strings.Contains(summary, `from host hooks.example.com to new host "h\u043e\u043eks.example.com"`) {
		t.Errorf("summary = %s", summary)
	}
	if strings.Contains(form, "h\u043e\u043eks") {
		t.Errorf("form shows the look-alike host unescaped:\n%s", form)
	}
	if !strings.Contains(form, `Notes: non-ASCII host "h\u043e\u043eks.example.com"`) {
		t.Errorf("form lacks the non-ASCII note:\n%s", form)
	}

	// An ASCII redirect still names both hosts and adds no note.
	k = kit(t)
	asked = k.Elicit(false)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env, `{"webhook":"W1","url":"https://evil.example.net/x","confirm":true}`)
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "from host hooks.example.com to new host evil.example.net") || strings.Contains((*asked)[0], "non-ASCII") {
		t.Errorf("ascii redirect form = %v", *asked)
	}
}

func TestWebhooksUpdateUnchangedIsAlready(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "webhooks_upsert"), k.Env,
		`{"webhook":"W1","enabled":true,"event_types":["APP_STORE_VERSION_APP_VERSION_STATE_UPDATED","BUILD_UPLOAD_STATE_UPDATED"],"confirm":true}`))
	if tooltest.Decode[map[string]any](t, res)["already"] != true || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("out = %s", tooltest.JSON(t, res.Data))
	}
}

func TestWebhooksDelete(t *testing.T) {
	k := kit(t)
	asked := k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	k.ASC.Handle(http.MethodDelete, "/v1/webhooks/W1", http.StatusNoContent, ``)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "webhooks_delete"), k.Env, `{"webhook":"ci","confirm":true}`))
	wantLog(t, k, "GET /v1/apps/A1/webhooks", "DELETE /v1/webhooks/W1")
	if tooltest.Decode[map[string]any](t, res)["deleted"] != "W1" || !strings.Contains((*asked)[0], "Irreversible") {
		t.Fatalf("out = %s prompt = %v", tooltest.JSON(t, res.Data), *asked)
	}

	k = kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/A1/webhooks", http.StatusOK, hooksDoc)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "webhooks_delete"), k.Env, `{"webhook":"nope","confirm":true}`), "not_found")
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "webhooks_delete"), k.Env, `{"webhook":"W1","confirm":true}`), tool.ErrHumanRequired.Code)
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("unapproved delete wrote")
	}
}
