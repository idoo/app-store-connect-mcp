package signing

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func unknown500() tooltest.Fixture {
	return tooltest.Fixture{Err: fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, &tool.APIError{Status: http.StatusInternalServerError})}
}

// Every registration failing is an error result; an unknown outcome
// dominates and is reported per item.
func TestDevicesRegisterAllFail(t *testing.T) {
	k := newKit(t)
	k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/devices", http.StatusOK, `{"data":[]}`)
	calls := 0
	k.ASC.HandleFunc(http.MethodPost, "/v1/devices", func(tool.Request) tooltest.Fixture {
		calls++
		if calls == 1 {
			return unknown500()
		}
		return tooltest.Fixture{Err: &tool.APIError{Status: http.StatusUnprocessableEntity, Code: "ENTITY_ERROR"}}
	})
	res := tooltest.Call(t, mustTool(t, "signing_devices_register"), k.Env,
		`{"devices":[{"name":"A","udid":"U-1"},{"name":"B","udid":"U-2"}],"confirm":true}`)
	d := tooltest.Decode[struct {
		tool.ErrorBody
		Items devicesRegisterOut `json:"items"`
	}](t, res)
	if !res.IsError || d.Error != tool.ErrOutcomeUnknown.Code {
		t.Fatalf("result = %s", tooltest.JSON(t, res.Data))
	}
	items := d.Items.Items
	if len(items) != 2 || items[0].Action != devUnknown || items[1].Action != devFailed ||
		items[0].Error == nil || items[0].Error.Error != tool.ErrOutcomeUnknown.Code {
		t.Fatalf("items = %s", tooltest.JSON(t, items))
	}
}

// Every capability change failing is an error result.
func TestCapabilitiesAllFail(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, bundleDoc)
	k.ASC.HandleFixture(http.MethodPost, "/v1/bundleIdCapabilities", unknown500())
	res := tooltest.Call(t, mustTool(t, "signing_capabilities_set"), k.Env, `{"bundle_id":"se.jvcr.hjul","enable":[{"type":"MAPS"}]}`)
	d := tooltest.Decode[struct {
		tool.ErrorBody
		Items capsSetOut `json:"items"`
	}](t, res)
	if !res.IsError || d.Error != tool.ErrOutcomeUnknown.Code {
		t.Fatalf("result = %s", tooltest.JSON(t, res.Data))
	}
	items := d.Items.Items
	if len(items) != 1 || items[0].Action != capUnknown || items[0].Error == nil {
		t.Fatalf("items = %s", tooltest.JSON(t, items))
	}
}

// A replayed DELETE that finds the certificate gone is already revoked.
func TestRevokeReplayGoneIsAlready(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/certificates/{id}", http.StatusOK, certOne)
	k.ASC.Handle(http.MethodGet, "/v1/profiles", http.StatusOK, profilesDoc)
	k.Elicit(true)
	k.ASC.HandleFixture(http.MethodDelete, "/v1/certificates/{id}", tooltest.Fixture{
		Err: fmt.Errorf("%w: %w", tool.ErrAlreadyGone, &tool.APIError{Status: http.StatusNotFound, Code: "NOT_FOUND"}),
	})
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_revoke"), k.Env, `{"kind":"certificate","id":"C1","confirm":true}`))
	if o := tooltest.Decode[map[string]any](t, res); o["already"] != true {
		t.Fatalf("out = %s", tooltest.JSON(t, o))
	}
}
