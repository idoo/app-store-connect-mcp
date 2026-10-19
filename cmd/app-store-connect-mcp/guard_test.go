package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/app"
	"github.com/idoo/app-store-connect-mcp/internal/asctest"
	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// guardAppID is the app every guard scenario serves.
const guardAppID = "1000000002"

// guardScenario is a minimal release-health fixture: the live version 1.1
// (build b2) against 1.0 (build b1), with hang weights and an optional
// phased release.
type guardScenario struct {
	curHangs, prevHangs float64
	phased              string
	// noSignatures serves empty diagnostic signature lists; noCurSignatures
	// only for the live build.
	noSignatures, noCurSignatures bool
	// crashes are TestFlight crash reports dated after the window start.
	crashes int
	// noLive serves no READY_FOR_DISTRIBUTION version.
	noLive bool
}

func guardDoc(t *testing.T, data any, included ...map[string]any) string {
	t.Helper()
	m := map[string]any{"data": data, "links": map[string]string{"self": "https://api.appstoreconnect.apple.com/v1/x"}}
	if included != nil {
		m["included"] = included
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func guardRel(typ, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": typ, "id": id}}
}

func installGuardScenario(t *testing.T, srv *asctest.Server, s guardScenario) {
	t.Helper()
	appDoc := map[string]any{"type": "apps", "id": guardAppID, "attributes": map[string]any{"name": "Sail", "bundleId": "com.example.sail", "sku": "sail"}}
	srv.Handle(http.MethodGet, "/v1/apps/{id}", http.StatusOK, guardDoc(t, appDoc))
	srv.Handle(http.MethodGet, "/v1/apps", http.StatusOK, guardDoc(t, []any{appDoc}))
	curRels := map[string]any{"build": guardRel("builds", "b2")}
	var inc []map[string]any
	if s.phased != "" {
		curRels["appStoreVersionPhasedRelease"] = guardRel("appStoreVersionPhasedReleases", "pr1")
		inc = append(inc, map[string]any{
			"type": "appStoreVersionPhasedReleases", "id": "pr1",
			"attributes": map[string]any{"phasedReleaseState": s.phased, "currentDayNumber": 3, "totalPauseDuration": 0},
		})
	}
	liveState := "READY_FOR_DISTRIBUTION"
	if s.noLive {
		liveState = "PENDING_DEVELOPER_RELEASE"
	}
	versions := []map[string]any{
		{
			"type": "appStoreVersions", "id": "v1", "attributes": map[string]any{"versionString": "1.0", "platform": "IOS", "appVersionState": "REPLACED_WITH_NEW_VERSION", "createdDate": "2026-09-01T00:00:00Z"},
			"relationships": map[string]any{"build": guardRel("builds", "b1")},
		},
		{
			"type": "appStoreVersions", "id": "v2", "attributes": map[string]any{"versionString": "1.1", "platform": "IOS", "appVersionState": liveState, "releaseType": "AFTER_APPROVAL", "createdDate": "2026-10-10T00:00:00Z"},
			"relationships": curRels,
		},
	}
	srv.Handle(http.MethodGet, "/v1/apps/{id}/appStoreVersions", http.StatusOK, guardDoc(t, versions, inc...))
	srv.Handle(http.MethodGet, "/v1/appStoreVersions/{id}", http.StatusOK, guardDoc(t, versions[1], inc...))
	sig := func(build string, w float64) string {
		if s.noSignatures || (s.noCurSignatures && build == "b2") {
			return guardDoc(t, []any{})
		}
		return guardDoc(t, []map[string]any{{
			"type": "diagnosticSignatures", "id": build + "h",
			"attributes": map[string]any{"diagnosticType": "HANGS", "signature": "main thread", "weight": w},
		}})
	}
	srv.Handle(http.MethodGet, "/v1/builds/b2/diagnosticSignatures", http.StatusOK, sig("b2", s.curHangs))
	srv.Handle(http.MethodGet, "/v1/builds/b1/diagnosticSignatures", http.StatusOK, sig("b1", s.prevHangs))
	srv.Handle(http.MethodGet, "/v1/apps/{id}/perfPowerMetrics", http.StatusOK, `{"productData":[],"insights":{"regressions":[]}}`)
	crashes := []map[string]any{}
	for i := range s.crashes {
		crashes = append(crashes, map[string]any{"type": "betaFeedbackCrashSubmissions", "id": fmt.Sprintf("c%d", i), "attributes": map[string]any{"createdDate": "2026-10-15T00:00:00Z"}})
	}
	srv.Handle(http.MethodGet, "/v1/apps/{id}/betaFeedbackCrashSubmissions", http.StatusOK, guardDoc(t, crashes))
	srv.Handle(http.MethodGet, "/v1/reviewSubmissions", http.StatusOK, guardDoc(t, []any{}))
	srv.Handle(http.MethodGet, "/v1/apps/{id}/customerReviews", http.StatusOK, guardDoc(t, []any{}))
	srv.Handle(http.MethodGet, "/v1/apps/{id}/analyticsReportRequests", http.StatusOK, guardDoc(t, []any{}))
	srv.Handle(http.MethodPatch, "/v1/appStoreVersionPhasedReleases/{id}", http.StatusOK, guardDoc(t, map[string]any{
		"type": "appStoreVersionPhasedReleases", "id": "pr1",
		"attributes": map[string]any{"phasedReleaseState": "PAUSED", "currentDayNumber": 3, "totalPauseDuration": 0},
	}))
}

// guardEnvFor builds a real environment against srv, as NewEnv does in
// production, with the profile's writes switch set to writes.
func guardEnvFor(t *testing.T, srv *asctest.Server, writes bool) *tool.Env {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "AuthKey_K1.p8")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "state"))
	t.Setenv("ASC_MCP_ROOT", "")
	if err := os.MkdirAll(filepath.Join(dir, "proj", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(dir, "proj"))
	r := &config.Resolved{BaseURL: srv.URL, Profile: config.ResolvedProfile{
		Name: "t", KeyID: "K1", IssuerID: "issuer-1", KeyKind: tool.KeyTeam, AllowWrites: writes,
		Key: config.KeyRef{Source: config.KeyFromFile, Path: keyPath},
	}}
	env, err := app.NewEnv(context.Background(), r, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, ok := env.Files.(io.Closer); ok {
			_ = c.Close()
		}
	})
	return env
}

// runGuardWith runs the guard against the kit with writes on or off.
func runGuardWith(t *testing.T, srv *asctest.Server, writes bool, args ...string) (int, map[string]any, string) {
	t.Helper()
	env := guardEnvFor(t, srv, writes)
	old := guardEnv
	guardEnv = func(context.Context, string, bool) (*tool.Env, bool, error) { return env, writes, nil }
	t.Cleanup(func() { guardEnv = old })
	var out, errb bytes.Buffer
	err := run(context.Background(), append([]string{"guard"}, args...), strings.NewReader(""), &out, &errb)
	code := 0
	var ge *guardExit
	switch {
	case errors.As(err, &ge):
		code = ge.code
	case err != nil:
		t.Fatalf("guard: %v (stderr %s)", err, errb.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, out.String())
	}
	return code, got, errb.String()
}

// assertPauseOnly fails if any mutation is not a PATCH setting PAUSED on
// the phased release, and returns the number of mutations.
func assertPauseOnly(t *testing.T, srv *asctest.Server) int {
	t.Helper()
	n := 0
	for _, m := range srv.Requests() {
		if m.Method == http.MethodGet {
			continue
		}
		n++
		b := m.Body
		var body struct {
			Data struct {
				Attributes map[string]any `json:"attributes"`
			} `json:"data"`
		}
		_ = json.Unmarshal(b, &body)
		if m.Method != http.MethodPatch || m.Path != "/v1/appStoreVersionPhasedReleases/pr1" ||
			len(body.Data.Attributes) != 1 || body.Data.Attributes["phasedReleaseState"] != "PAUSED" {
			t.Errorf("guard sent a non-pause mutation: %s %s %s", m.Method, m.Path, b)
		}
	}
	return n
}

func TestGuardExitCodes(t *testing.T) {
	for _, c := range []struct {
		name    string
		s       guardScenario
		code    int
		verdict string
	}{
		{"healthy", guardScenario{curHangs: 1, prevHangs: 1}, 0, "healthy"},
		{"watch", guardScenario{curHangs: 1.5, prevHangs: 1}, exitWatch, "watch"},
		{"breach", guardScenario{curHangs: 3, prevHangs: 1, phased: "ACTIVE"}, exitBreach, "breach"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := asctest.New(t)
			installGuardScenario(t, srv, c.s)
			code, out, _ := runGuardWith(t, srv, true, "--app", "Sail")
			if code != c.code || out["verdict"] != c.verdict {
				t.Errorf("code %d verdict %v, want %d %s", code, out["verdict"], c.code, c.verdict)
			}
			if out["pause"] != nil || assertPauseOnly(t, srv) != 0 {
				t.Errorf("mutated without --pause-on-breach: %v", out["pause"])
			}
		})
	}
}

func TestGuardPausesActiveOnBreachOnly(t *testing.T) {
	srv := asctest.New(t)
	installGuardScenario(t, srv, guardScenario{curHangs: 3, prevHangs: 1, phased: "ACTIVE"})
	code, out, logs := runGuardWith(t, srv, true, "--app", "Sail", "--pause-on-breach")
	if code != exitBreach {
		t.Errorf("code %d", code)
	}
	if p, _ := out["pause"].(map[string]any); p["action"] != "paused" {
		t.Errorf("pause = %v (logs %s)", out["pause"], logs)
	}
	if n := assertPauseOnly(t, srv); n != 1 {
		t.Errorf("%d mutations, want 1", n)
	}
	if !strings.Contains(logs, "guard: pause paused") {
		t.Errorf("not logged: %q", logs)
	}
}

func TestGuardPauseOnlyInvariant(t *testing.T) {
	for _, c := range []struct {
		name string
		s    guardScenario
	}{
		// A paused release with a breach must never be resumed or completed.
		{"paused breach", guardScenario{curHangs: 3, prevHangs: 1, phased: "PAUSED"}},
		{"complete breach", guardScenario{curHangs: 3, prevHangs: 1, phased: "COMPLETE"}},
		{"inactive breach", guardScenario{curHangs: 3, prevHangs: 1, phased: "INACTIVE"}},
		{"no phased breach", guardScenario{curHangs: 3, prevHangs: 1}},
		{"healthy active", guardScenario{curHangs: 1, prevHangs: 1, phased: "ACTIVE"}},
		{"watch active", guardScenario{curHangs: 1.5, prevHangs: 1, phased: "ACTIVE"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := asctest.New(t)
			installGuardScenario(t, srv, c.s)
			runGuardWith(t, srv, true, "--app", "Sail", "--pause-on-breach")
			if n := assertPauseOnly(t, srv); n != 0 {
				t.Errorf("%d mutations, want 0", n)
			}
		})
	}
	if guardPauseAction != "pause" {
		t.Fatalf("guard action %q", guardPauseAction)
	}
}

func TestGuardUnknownExits5AndNeverPauses(t *testing.T) {
	srv := asctest.New(t)
	installGuardScenario(t, srv, guardScenario{phased: "ACTIVE", noSignatures: true})
	code, out, _ := runGuardWith(t, srv, true, "--app", "Sail", "--pause-on-breach")
	if code != exitUnknown || out["verdict"] != "unknown" {
		t.Errorf("code %d verdict %v", code, out["verdict"])
	}
	if out["pause"] != nil {
		t.Errorf("pause = %v", out["pause"])
	}
	if n := assertPauseOnly(t, srv); n != 0 {
		t.Errorf("%d mutations, want 0", n)
	}
}

func TestGuardWithoutWritesRefusesToPause(t *testing.T) {
	srv := asctest.New(t)
	installGuardScenario(t, srv, guardScenario{curHangs: 3, prevHangs: 1, phased: "ACTIVE"})
	code, out, logs := runGuardWith(t, srv, false, "--app", "Sail", "--pause-on-breach")
	if code != exitBreach || out["verdict"] != "breach" {
		t.Errorf("code %d verdict %v", code, out["verdict"])
	}
	p, _ := out["pause"].(map[string]any)
	if p["action"] != "skipped" || !strings.Contains(fmt.Sprint(p["reason"]), "ASC_MCP_ALLOW_WRITES") {
		t.Errorf("pause = %v", p)
	}
	if n := assertPauseOnly(t, srv); n != 0 {
		t.Errorf("%d mutations", n)
	}
	if !strings.Contains(logs, "writes disabled") {
		t.Errorf("logs %q", logs)
	}
}

func TestGuardRequiresApp(t *testing.T) {
	var out, errb bytes.Buffer
	err := run(context.Background(), []string{"guard"}, strings.NewReader(""), &out, &errb)
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Errorf("err = %v", err)
	}
}

// IQ-8: a TestFlight-only breach exits 4 but never pauses.
func TestGuardTestFlightBreachDoesNotPause(t *testing.T) {
	srv := asctest.New(t)
	installGuardScenario(t, srv, guardScenario{curHangs: 1, prevHangs: 1, phased: "ACTIVE", crashes: 25})
	code, out, logs := runGuardWith(t, srv, true, "--app", "Sail", "--pause-on-breach")
	if code != exitBreach || out["verdict"] != "breach" {
		t.Fatalf("code %d verdict %v", code, out["verdict"])
	}
	reasons, _ := out["reasons"].([]any)
	for _, r := range reasons {
		if m, _ := r.(map[string]any); m["pausing"] != false {
			t.Errorf("pausing reason %v", m)
		}
	}
	if p, _ := out["pause"].(map[string]any); p["action"] != "skipped" {
		t.Errorf("pause = %v (logs %s)", out["pause"], logs)
	}
	if n := assertPauseOnly(t, srv); n != 0 {
		t.Errorf("%d mutations, want 0", n)
	}
}

// IQ-2: no signatures for the live build (previous HANGS 5) is unknown.
func TestGuardNoCurrentSignaturesExits5(t *testing.T) {
	srv := asctest.New(t)
	installGuardScenario(t, srv, guardScenario{prevHangs: 5, phased: "ACTIVE", noCurSignatures: true})
	code, out, _ := runGuardWith(t, srv, true, "--app", "Sail", "--pause-on-breach")
	if code != exitUnknown || out["verdict"] != "unknown" || out["pause"] != nil {
		t.Errorf("code %d verdict %v pause %v", code, out["verdict"], out["pause"])
	}
	if n := assertPauseOnly(t, srv); n != 0 {
		t.Errorf("%d mutations, want 0", n)
	}
}

// IQ-10: no live version exits 5, not 1.
func TestGuardNoLiveVersionExits5(t *testing.T) {
	srv := asctest.New(t)
	installGuardScenario(t, srv, guardScenario{noLive: true})
	code, out, _ := runGuardWith(t, srv, true, "--app", "Sail", "--pause-on-breach")
	if code != exitUnknown || out["verdict"] != "unknown" {
		t.Fatalf("code %d verdict %v", code, out["verdict"])
	}
	reasons, _ := out["reasons"].([]any)
	if len(reasons) != 1 || reasons[0].(map[string]any)["message"] != "no live version on IOS" {
		t.Errorf("reasons %v", reasons)
	}
}
