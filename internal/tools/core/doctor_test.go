package core

import (
	"strings"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

const emptyApps = `{"data":[],"links":{"self":"https://api.appstoreconnect.apple.com/v1/apps"}}`

func checksOf(t *testing.T, res *tool.Result) map[string]check {
	t.Helper()
	out := tooltest.Decode[doctorOut](t, res)
	m := map[string]check{}
	for _, c := range out.Checks {
		m[c.Name] = c
	}
	return m
}

func TestDoctor(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(k *tooltest.Kit)
		status     int
		body       string
		wantStatus string
		want       map[string]string
	}{
		{
			name: "healthy team key",
			setup: func(k *tooltest.Kit) {
				k.ASC.Limit = tool.RateLimit{Limit: 3600, Remaining: 3500, Known: true}
				k.ASC.Skew = 2 * time.Second
			},
			wantStatus: statusOK,
			want: map[string]string{
				"profile": statusOK, "key_kind": statusOK, "jwt": statusOK, "apple_api": statusOK,
				"clock_skew": statusOK, "rate_limit": statusOK, "spec": statusOK, "writes": statusOK, "vendor_number": statusOK,
			},
		},
		{
			name: "individual key without vendor number",
			setup: func(k *tooltest.Kit) {
				k.Individual()
				k.Env.Profile.VendorNumber = ""
			},
			wantStatus: statusWarn,
			want:       map[string]string{"key_kind": statusWarn, "vendor_number": statusWarn, "jwt": statusOK},
		},
		{
			name:       "token rejected",
			status:     401,
			body:       `{"errors":[{"status":"401","code":"NOT_AUTHORIZED","title":"Authentication credentials are missing or invalid."}]}`,
			wantStatus: statusFail,
			want:       map[string]string{"jwt": statusFail, "apple_api": statusOK},
		},
		{
			name:       "forbidden",
			status:     403,
			body:       `{"errors":[{"status":"403","code":"FORBIDDEN_ERROR","title":"no"}]}`,
			wantStatus: statusFail,
			want:       map[string]string{"jwt": statusOK, "apple_api": statusFail},
		},
		{
			name: "skewed clock and low quota",
			setup: func(k *tooltest.Kit) {
				k.ASC.Skew = -90 * time.Second
				k.ASC.Limit = tool.RateLimit{Limit: 3600, Remaining: 10, Known: true}
			},
			wantStatus: statusFail,
			want:       map[string]string{"clock_skew": statusFail, "rate_limit": statusWarn},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := kit(t)
			status, body := tt.status, tt.body
			if status == 0 {
				status, body = 200, emptyApps
			}
			k.ASC.Handle("GET", "/v1/apps", status, body)
			if tt.setup != nil {
				tt.setup(k)
			}
			res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "doctor"), k.Env, ""))
			out := tooltest.Decode[doctorOut](t, res)
			if out.Status != tt.wantStatus {
				t.Errorf("status = %s, want %s (%+v)", out.Status, tt.wantStatus, out.Checks)
			}
			got := checksOf(t, res)
			for name, want := range tt.want {
				c, ok := got[name]
				if !ok || c.Status != want {
					t.Errorf("check %s = %+v, want %s", name, c, want)
				}
				if ok && c.Status != statusOK && c.Fix == "" && name != "jwt" {
					t.Errorf("check %s has no fix hint", name)
				}
			}
			if len(k.ASC.Requests()) != 1 {
				t.Errorf("requests = %d, want 1", len(k.ASC.Requests()))
			}
			if out.SpecVersion == "" || out.KeyKind == "" || len(out.Toolsets) == 0 {
				t.Errorf("summary incomplete: %+v", out)
			}
		})
	}
}

func TestDoctorReadOnlyMode(t *testing.T) {
	k := kit(t).ReadOnly()
	k.ASC.Handle("GET", "/v1/apps", 200, emptyApps)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "doctor"), k.Env, ""))
	c := checksOf(t, res)["writes"]
	if c.Status != statusOK || c.Fix == "" {
		t.Errorf("writes check = %+v, want ok with enable hint", c)
	}
	if out := tooltest.Decode[doctorOut](t, res); out.Writes {
		t.Error("writes reported enabled")
	}
}

func TestDoctorFileRoot(t *testing.T) {
	k := kit(t)
	k.ASC.Handle("GET", "/v1/apps", 200, emptyApps)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "doctor"), k.Env, ""))
	if out := tooltest.Decode[doctorOut](t, res); out.FileRoot != k.Files.Root || checksOf(t, res)["files"].Status != statusOK {
		t.Fatalf("file_root = %q, files = %+v, want %q ok", out.FileRoot, checksOf(t, res)["files"], k.Files.Root)
	}

	k.Env.Files = nil // no safe root: file tools refuse
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "doctor"), k.Env, ""))
	c := checksOf(t, res)["files"]
	if out := tooltest.Decode[doctorOut](t, res); out.FileRoot != "" || c.Status != statusWarn || !strings.Contains(c.Fix, "--root") {
		t.Fatalf("refused root: file_root = %q, files = %+v", out.FileRoot, c)
	}
}
