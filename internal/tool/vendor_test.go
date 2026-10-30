package tool_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	olympusPath = "/olympus/v1/session"
	vendorsPath = "/WebObjects/iTunesConnect.woa/ra/paymentConsolidation/providers/4242424/sapVendorNumbers"
	sessionJSON = `{"provider":{"providerId":4242424,"name":"Ivan"}}`
)

func vendorsJSON(entries ...string) string {
	return `{"data":[` + strings.Join(entries, ",") + `],"statusCode":"SUCCESS"}`
}

func vendorEntry(n string, arcade bool) string {
	a := "false"
	if arcade {
		a = "true"
	}
	return `{"vendorName":"Ivan","sapVendorNumber":` + n + `,"isArcadeVendor":` + a + `}`
}

// webKit is a signed-in Kit whose profile has no vendor number.
func webKit(t *testing.T) *tooltest.Kit {
	k := tooltest.New(t).WebSession()
	k.Env.Profile.VendorNumber = ""
	return k
}

func wantCode(t *testing.T, err error, code string) *tool.Error {
	t.Helper()
	var te *tool.Error
	if !errors.As(err, &te) || te.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
	return te
}

func TestResolveVendorNumberProfileWins(t *testing.T) {
	k := tooltest.New(t).WebSession()
	k.Env.Profile.VendorNumber = " 777 "
	v, err := tool.ResolveVendorNumber(context.Background(), k.Env)
	if err != nil || v.Number != "777" || v.Source != tool.VendorFromProfile {
		t.Fatalf("v = %+v, err = %v", v, err)
	}
	if n := len(k.Portal.Calls()); n != 0 {
		t.Errorf("portal calls = %d, want 0", n)
	}
}

func TestResolveVendorNumberDiscoversAndCaches(t *testing.T) {
	k := webKit(t)
	k.Portal.HandleJSON("GET", olympusPath, sessionJSON)
	k.Portal.HandleJSON("GET", vendorsPath, vendorsJSON(vendorEntry("55500", true), vendorEntry("81234567", false)))
	v, err := tool.ResolveVendorNumber(context.Background(), k.Env)
	if err != nil || v.Number != "81234567" || v.Source != tool.VendorFromWebSession {
		t.Fatalf("v = %+v, err = %v", v, err)
	}
	if n := len(k.Portal.Calls()); n != 2 {
		t.Fatalf("portal calls = %d, want 2", n)
	}
	// The second call is answered from the cache, without the portal.
	v, err = tool.ResolveVendorNumber(context.Background(), k.Env)
	if err != nil || v.Number != "81234567" || v.Source != tool.VendorFromWebSession {
		t.Fatalf("cached v = %+v, err = %v", v, err)
	}
	if n := len(k.Portal.Calls()); n != 2 {
		t.Errorf("portal calls after cache = %d, want 2", n)
	}
}

func TestResolveVendorNumberUsesCachedRecord(t *testing.T) {
	k := tooltest.New(t) // no web session: the cache alone must answer
	k.Env.Profile.VendorNumber = ""
	if err := k.State.Save("vendor_number", k.Env.Profile.Name, map[string]string{"number": "4242"}); err != nil {
		t.Fatal(err)
	}
	k.Env.Portal = k.Portal
	v, err := tool.ResolveVendorNumber(context.Background(), k.Env)
	if err != nil || v.Number != "4242" || v.Source != tool.VendorFromWebSession {
		t.Fatalf("v = %+v, err = %v", v, err)
	}
	if n := len(k.Portal.Calls()); n != 0 {
		t.Errorf("portal calls = %d, want 0", n)
	}
}

func TestResolveVendorNumberAmbiguous(t *testing.T) {
	k := webKit(t)
	k.Portal.HandleJSON("GET", olympusPath, sessionJSON)
	k.Portal.HandleJSON("GET", vendorsPath, vendorsJSON(vendorEntry("111", false), vendorEntry("222", false)))
	_, err := tool.ResolveVendorNumber(context.Background(), k.Env)
	te := wantCode(t, err, "vendor_number_ambiguous")
	if !strings.Contains(te.Message, "111") || !strings.Contains(te.Message, "222") {
		t.Errorf("message = %q", te.Message)
	}
}

func TestResolveVendorNumberOnlyArcadeIsMissing(t *testing.T) {
	k := webKit(t)
	k.Portal.HandleJSON("GET", olympusPath, sessionJSON)
	k.Portal.HandleJSON("GET", vendorsPath, vendorsJSON(vendorEntry("55500", true)))
	_, err := tool.ResolveVendorNumber(context.Background(), k.Env)
	_ = wantCode(t, err, "vendor_number_missing")
}

func TestResolveVendorNumberNoSession(t *testing.T) {
	k := webKit(t)
	// What the real portal answers with no stored or a dead session.
	k.Portal.Handle("GET", olympusPath, func(tooltest.PortalCall) (string, error) {
		return "", tool.ErrWebSessionRequired
	})
	_, err := tool.ResolveVendorNumber(context.Background(), k.Env)
	te := wantCode(t, err, "vendor_number_missing")
	if !strings.Contains(te.Hint, "web_login") || !strings.Contains(te.Hint, "auth set test vendor_number=") {
		t.Errorf("hint = %q", te.Hint)
	}
	var rec map[string]string
	if ok, _ := k.State.Load("vendor_number", "test", &rec); ok {
		t.Errorf("cached a missing vendor number: %v", rec)
	}
}

func TestResolveVendorNumberNoProvider(t *testing.T) {
	k := webKit(t)
	k.Portal.HandleJSON("GET", olympusPath, `{"provider":{"providerId":0}}`)
	_, err := tool.ResolveVendorNumber(context.Background(), k.Env)
	_ = wantCode(t, err, "vendor_number_missing")
	if n := len(k.Portal.Calls()); n != 1 {
		t.Errorf("portal calls = %d, want 1", n)
	}
}

func TestResolveVendorNumberPortalErrorPropagates(t *testing.T) {
	k := webKit(t)
	k.Portal.HandleJSON("GET", olympusPath, sessionJSON)
	k.Portal.HandleError("GET", vendorsPath, 503, "Service Unavailable")
	_, err := tool.ResolveVendorNumber(context.Background(), k.Env)
	var pe *tool.PortalError
	if !errors.As(err, &pe) || pe.Status != 503 {
		t.Fatalf("err = %v, want the portal 503", err)
	}
	var te *tool.Error
	if errors.As(err, &te) && te.Code == "vendor_number_missing" {
		t.Errorf("5xx masked as missing: %v", err)
	}
}
