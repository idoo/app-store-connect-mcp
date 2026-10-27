package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/websession"
)

// webSandbox isolates config/state, forces the file store and routes Apple
// calls to a fake olympus that accepts myacinfo=GOODSECRET.
func webSandbox(t *testing.T) (teamPosts func() []string) {
	t.Helper()
	home, _ := sandbox(t)
	stateDir := filepath.Join(home, "state")
	t.Setenv("ASC_MCP_WEB_STORE", "file")
	t.Setenv("XDG_STATE_HOME", stateDir)
	var mu sync.Mutex
	posts := []string{}
	team := int64(100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/olympus/v1/session" {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(r.Header.Get("Cookie"), "myacinfo=GOODSECRET") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			var b struct {
				Provider struct {
					ProviderID int64 `json:"providerId"`
				} `json:"provider"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			team = b.Provider.ProviderID
			posts = append(posts, r.Header.Get("X-Requested-With"))
			http.SetCookie(w, &http.Cookie{Name: "dqsid", Value: "ROTATEDSECRET", Path: "/"})
			return
		}
		names := map[int64]string{100: "Main Team", 200: "Side Team"}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user":     map[string]any{"emailAddress": "dev@example.com"},
			"provider": map[string]any{"providerId": team, "name": names[team]},
			"availableProviders": []map[string]any{
				{"providerId": 100, "name": "Main Team"},
				{"providerId": 200, "name": "Side Team"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	old := webHTTPClient
	webHTTPClient = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host = u.Scheme, u.Host
		resp, err := http.DefaultTransport.RoundTrip(r2)
		if resp != nil {
			resp.Request = r
		}
		return resp, err
	})}
	t.Cleanup(func() { webHTTPClient = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), posts...)
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func assertNoSecrets(t *testing.T, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if strings.Contains(s, "SECRET") {
			t.Fatalf("cookie value leaked: %q", s)
		}
	}
}

func TestWebImportStatusTeamLogout(t *testing.T) {
	posts := webSandbox(t)

	out, errOut, err := runCLI(t, "", "web", "status")
	assertNoSecrets(t, out, errOut)
	var ge *guardExit
	if !errors.As(err, &ge) || ge.code != 1 || !strings.Contains(out, "none") {
		t.Fatalf("status before import: %v %q", err, out)
	}

	curl := `curl 'https://appstoreconnect.apple.com/iris/v1/apps' -H 'accept: */*' -H 'cookie: myacinfo=GOODSECRET; dqsid=DQSECRET' --compressed`
	out, errOut, err = runCLI(t, curl, "web", "import", "-")
	assertNoSecrets(t, out, errOut)
	if err != nil {
		t.Fatalf("import: %v\n%s", err, errOut)
	}
	for _, want := range []string{"profile default", "format curl", "2 cookies", "dev@example.com", "Main Team", `toolsets {"action":"enable","toolsets":["web"]}`} {
		if !strings.Contains(out, want) {
			t.Errorf("import output missing %q: %s", want, out)
		}
	}

	out, errOut, err = runCLI(t, "", "web", "status")
	assertNoSecrets(t, out, errOut)
	if err != nil || !strings.Contains(out, "status: active as dev@example.com") || !strings.Contains(out, "source import:curl") {
		t.Fatalf("status: %v %q", err, out)
	}

	out, errOut, err = runCLI(t, "", "web", "team")
	assertNoSecrets(t, out, errOut)
	if err != nil || !strings.Contains(out, "* 100  Main Team") || !strings.Contains(out, "  200  Side Team") {
		t.Fatalf("team list: %v %q", err, out)
	}
	out, errOut, err = runCLI(t, "", "web", "team", "200")
	assertNoSecrets(t, out, errOut)
	if err != nil || !strings.Contains(out, "Side Team") || len(posts()) != 1 || posts()[0] != "olympus-ui" {
		t.Fatalf("team switch: %v %q %v", err, out, posts())
	}

	out, errOut, err = runCLI(t, "", "web", "logout")
	assertNoSecrets(t, out, errOut)
	if err != nil || !strings.Contains(out, "Signed out") {
		t.Fatalf("logout: %v %q", err, out)
	}
	if _, _, err := runCLI(t, "", "web", "status"); !errors.As(err, &ge) {
		t.Fatalf("status after logout: %v", err)
	}
}

func TestWebImportFromFileWithProfileAndTeam(t *testing.T) {
	webSandbox(t)
	f := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(f, []byte("Cookie: myacinfo=GOODSECRET; itctx=ITSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runCLI(t, "", "web", "import", "--profile", "work", "--team", "200", f)
	assertNoSecrets(t, out, errOut)
	if err != nil || !strings.Contains(out, "profile work") || !strings.Contains(out, "Side Team") || !strings.Contains(out, "format header") {
		t.Fatalf("import: %v %q %q", err, out, errOut)
	}
	dir, err := webStateDir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "web", "work.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "ROTATEDSECRET") {
		t.Error("cookie rotated by the team switch was not stored")
	}
}

func TestWebImportRejectsWithoutLeaking(t *testing.T) {
	webSandbox(t)
	cases := map[string]string{
		"no myacinfo": "dqsid=DQSECRET",
		"expired":     "myacinfo=BADSECRET",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, errOut, err := runCLI(t, in, "web", "import")
			if err == nil {
				t.Fatal("want error")
			}
			assertNoSecrets(t, out, errOut, err.Error())
		})
	}
}

func TestWebLoginNeedsTerminal(t *testing.T) {
	webSandbox(t)
	_, _, err := runCLI(t, "", "web", "login", "--password", "--apple-id", "a@b.c")
	if err == nil || !strings.Contains(err.Error(), "--password-stdin") {
		t.Fatalf("got %v", err)
	}
}

func TestWebUsage(t *testing.T) {
	out, _, err := runCLI(t, "", "web", "--help")
	if err != nil || !strings.Contains(out, "web import") {
		t.Fatalf("%v %q", err, out)
	}
	if _, _, err := runCLI(t, "", "web", "bogus"); err == nil {
		t.Fatal("unknown subcommand accepted")
	}
}

func TestWebLoginPasswordStdinAndTwoFactor(t *testing.T) {
	webSandbox(t)
	old := webLogin
	t.Cleanup(func() { webLogin = old })
	var replies []string
	webLogin = func(ctx context.Context, o websession.LoginOptions) (*websession.Session, error) {
		if o.AppleID != "dev@example.com" || o.Password != "PWSECRET" {
			return nil, errors.New("wrong credentials passed")
		}
		r, err := o.Code(ctx, websession.TwoFactor{Kind: "trusted_device", Phones: []string{"•••• 11", "•••• 22"}, CodeLength: 6})
		if err != nil {
			return nil, err
		}
		replies = append(replies, r)
		r, err = o.Code(ctx, websession.TwoFactor{Kind: "sms", Phones: []string{"•••• 11", "•••• 22"}, CodeLength: 6})
		if err != nil {
			return nil, err
		}
		replies = append(replies, r)
		return &websession.Session{Cookies: []websession.Cookie{{Name: "myacinfo", Value: "GOODSECRET", Domain: ".apple.com", Path: "/"}}}, nil
	}
	// password, empty (list phones), phone 2, a bad code, the code.
	stdin := "PWSECRET\n\n2\n12\n123 456\n"
	out, errOut, err := runCLI(t, stdin, "web", "login", "--password", "--apple-id", "dev@example.com", "--password-stdin", "--team", "200")
	assertNoSecrets(t, out, errOut)
	if err != nil {
		t.Fatalf("login: %v\n%s", err, errOut)
	}
	if len(replies) != 2 || replies[0] != "sms:2" || replies[1] != "123456" {
		t.Fatalf("2FA replies %v", replies)
	}
	if !strings.Contains(errOut, "•••• 22") || !strings.Contains(errOut, "6 digits") {
		t.Errorf("prompts: %q", errOut)
	}
	if !strings.Contains(out, "Signed in as dev@example.com (team Side Team, id 200)") || !strings.Contains(out, "--toolsets +web") {
		t.Errorf("output: %q", out)
	}
	out, _, err = runCLI(t, "", "web", "status")
	if err != nil || !strings.Contains(out, "source login") {
		t.Fatalf("status: %v %q", err, out)
	}
}
