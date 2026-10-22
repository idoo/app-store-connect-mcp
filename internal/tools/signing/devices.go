package signing

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "signing_devices_register", Toolset: tool.Signing, Class: tool.Write, Idempotent: true, NeedsTeamKey: true,
		Title: "Register devices",
		Description: "Register up to 100 devices (name, udid, platform) in one call. Idempotent by UDID: already registered devices " +
			"are reported as already (with their status) and not re-posted. Per-device results; one failure does not stop the batch.",
		Enums: map[string][]any{"devices.platform": platforms},
	}, devicesRegister))
}

// maxDevices caps one batch.
const maxDevices = 100

type deviceIn struct {
	Name     string `json:"name" jsonschema:"device name shown in the developer portal"`
	UDID     string `json:"udid" jsonschema:"device UDID (iOS) or provisioning UDID (Mac)"`
	Platform string `json:"platform,omitempty" jsonschema:"default IOS"`
}

type devicesRegisterIn struct {
	Devices []deviceIn `json:"devices" jsonschema:"1 to 100 devices"`
}

// Per-device outcomes.
const (
	devRegistered = "registered"
	devAlready    = "already"
	devFailed     = "failed"
)

type deviceResult struct {
	UDID     string          `json:"udid"`
	Name     string          `json:"name"`
	Action   string          `json:"action"`
	ID       string          `json:"id,omitempty"`
	Platform string          `json:"platform,omitempty"`
	Status   string          `json:"status,omitempty"`
	Error    *tool.ErrorBody `json:"error,omitempty"`
}

type devicesRegisterOut struct {
	Items      []deviceResult `json:"items"`
	Registered int            `json:"registered"`
	Already    int            `json:"already"`
	Failed     int            `json:"failed"`
}

func devicesRegister(ctx context.Context, env *tool.Env, in devicesRegisterIn) (*tool.Result, error) {
	if len(in.Devices) == 0 || len(in.Devices) > maxDevices {
		return nil, invalidArgs("devices must hold 1 to %d entries", maxDevices)
	}
	seen := map[string]bool{}
	udids := make([]string, 0, len(in.Devices))
	for i := range in.Devices {
		d := &in.Devices[i]
		d.Name, d.UDID = strings.TrimSpace(d.Name), strings.TrimSpace(d.UDID)
		if d.Platform == "" {
			d.Platform = "IOS"
		}
		if d.Name == "" || d.UDID == "" {
			return nil, invalidArgs("devices[%d]: name and udid are required", i)
		}
		if strings.ContainsAny(d.UDID, ", ") {
			return nil, invalidArgs("devices[%d]: udid %q is malformed", i, d.UDID)
		}
		if err := checkEnum(fmt.Sprintf("devices[%d].platform", i), d.Platform, platforms); err != nil {
			return nil, err
		}
		key := strings.ToLower(d.UDID)
		if seen[key] {
			return nil, invalidArgs("udid %s is listed twice", d.UDID)
		}
		seen[key] = true
		udids = append(udids, d.UDID)
	}
	existing, err := devicesByUDID(ctx, env, udids)
	if err != nil {
		return nil, err
	}
	out := devicesRegisterOut{Items: make([]deviceResult, 0, len(in.Devices))}
	for _, d := range in.Devices {
		item := registerOne(ctx, env, d, existing)
		switch item.Action {
		case devRegistered:
			out.Registered++
		case devAlready:
			out.Already++
		default:
			out.Failed++
		}
		out.Items = append(out.Items, item)
	}
	res := tool.OK(out)
	if out.Registered == 0 && out.Failed == 0 {
		res = tool.Already(out)
	}
	for _, it := range out.Items {
		if it.Action == devAlready && it.Status == "DISABLED" {
			res.Warn(fmt.Sprintf("device %s is registered but DISABLED; enable it in the developer portal", it.UDID))
		}
	}
	if out.Registered > 0 {
		res.Then(tool.NextStep{Tool: "signing_profile_create"})
	}
	return res, nil
}

// registerOne registers one device unless its UDID is already known.
func registerOne(ctx context.Context, env *tool.Env, d deviceIn, existing map[string]tool.Resource) deviceResult {
	item := deviceResult{UDID: d.UDID, Name: d.Name, Platform: d.Platform}
	already := func(r tool.Resource) deviceResult {
		item.Action, item.ID, item.Status, item.Platform = devAlready, r.ID, r.Str("status"), r.Str("platform")
		if n := r.Str("name"); n != "" {
			item.Name = n
		}
		return item
	}
	if r, ok := existing[strings.ToLower(d.UDID)]; ok {
		return already(r)
	}
	body := map[string]any{"data": map[string]any{
		"type":       "devices",
		"attributes": map[string]any{"name": d.Name, "udid": d.UDID, "platform": d.Platform},
	}}
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/devices", Body: body, Write: true})
	if apiStatus(err) == http.StatusConflict {
		// Registered meanwhile (or the UDID differs only in case): read back.
		if found, rerr := devicesByUDID(ctx, env, []string{d.UDID}); rerr == nil {
			if r, ok := found[strings.ToLower(d.UDID)]; ok {
				return already(r)
			}
		}
	}
	if err == nil {
		r, ok := resp.First()
		if !ok {
			err = fmt.Errorf("%w: empty response", tool.ErrInternal)
		} else {
			item.Action, item.ID, item.Status = devRegistered, r.ID, r.Str("status")
			return item
		}
	}
	b := tool.BodyFor(fmt.Errorf("register device %s: %w", d.UDID, err))
	item.Action, item.Error = devFailed, &b
	return item
}

// devicesByUDID returns registered devices keyed by lower-case UDID.
func devicesByUDID(ctx context.Context, env *tool.Env, udids []string) (map[string]tool.Resource, error) {
	q := url.Values{
		"fields[devices]": {"name,platform,udid,status"},
		"filter[udid]":    {strings.Join(udids, ",")},
	}
	res, err := env.ASC.List(ctx, "/v1/devices", tool.ListOptions{Query: q, MaxItems: 1000})
	if err != nil {
		return nil, fmt.Errorf("look up devices: %w", err)
	}
	m := make(map[string]tool.Resource, len(res.Data))
	for _, r := range res.Data {
		m[strings.ToLower(r.Str("udid"))] = r
	}
	return m, nil
}
