package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "medical_device_get", Toolset: tool.Web, Class: tool.Read, Idempotent: true,
		Title: "Read the regulated medical device status",
		Description: "The app's regulated medical device declaration (web session): not declared, not regulated or regulated " +
			"(with the regions), and whether Apple requires it before submission (Health & Fitness and Medical apps).",
	}, medicalDeviceGet))
	tool.Register(tool.New(tool.Meta{
		Name: "medical_device_set", Toolset: tool.Web, Class: tool.Write, Idempotent: true,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Declare the regulated medical device status",
		Description: "Declare that the app is not a regulated medical device (regulated:false) when nothing is declared yet. " +
			"Previews, then confirm:true; a human approves. Declaring yes or changing an answer is done in App Store Connect.",
	}, medicalDeviceSet))
}

// Declaration states of medical_device_get.
const (
	mdNotListed    = "not_listed"    // Apple tracks no such declaration for the app
	mdNotDeclared  = "not_declared"  // listed, nothing saved
	mdNotRegulated = "not_regulated" // saved: No
	mdRegulated    = "regulated"     // saved: Yes
)

// mdRegions are the regions App Store Connect's own page sends with a first
// answer that is not Yes with chosen regions (m-appstore main.js: the
// declaration form POSTs countriesOrRegions EEA, GBR, USA with "no").
var mdRegions = []string{"EEA", "GBR", "USA"}

type medicalDeviceGetIn struct {
	App tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
}

type medicalDeviceSetIn struct {
	App       tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Regulated bool        `json:"regulated" jsonschema:"false = not a regulated medical device; true is refused (declare it in App Store Connect)"`
	tool.ConfirmFlag
}

type medicalDeviceOut struct {
	App appRef `json:"app"`
	// State is not_listed, not_declared, not_regulated or regulated.
	State string `json:"state"`
	// Required reports that Apple wants an answer before the app can be
	// submitted.
	Required bool `json:"required"`
	// Complete is false while Apple still waits for the answer or for the
	// details of a regulated device.
	Complete bool `json:"complete"`
	// Regions the app is declared a regulated medical device in.
	Regions []string `json:"regulated_in,omitempty"`
	// Status is "declared" after medical_device_set saved the answer.
	Status string `json:"status,omitempty"`
	// Manual is the App Store Connect page of the declaration.
	Manual string `json:"manual"`
}

// medicalDevice is the declaration as read from App Store Connect.
type medicalDevice struct {
	out medicalDeviceOut
	req tool.MedicalDeviceRequirement
	// saved is the form's stored object (identity fields and the answer);
	// nil when the form holds nothing.
	saved map[string]any
}

func medicalDevicePage(appID string) string {
	return "https://appstoreconnect.apple.com/apps/" + appID + "/distribution/info"
}

// readMedicalDevice reads the requirement and, when Apple lists one, its
// form (three web requests).
func readMedicalDevice(ctx context.Context, env *tool.Env, app tool.App) (*medicalDevice, error) {
	req, err := tool.ReadMedicalDeviceRequirement(ctx, env.Portal, app.ID)
	if err != nil {
		return nil, err
	}
	md := &medicalDevice{req: req, out: medicalDeviceOut{
		App: newAppRef(app), State: mdNotListed, Required: req.Required, Manual: medicalDevicePage(app.ID),
	}}
	if req.ID == "" {
		return md, nil
	}
	var raw json.RawMessage
	path := tool.ComplianceFormsPath + req.AccountID + "/requirements/" + req.ID + "/forms?contentId=" + url.QueryEscape(app.ID)
	if err := env.Portal.JSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, fmt.Errorf("read the regulated medical device form: %w", err)
	}
	md.saved = savedForm(raw)
	data, _ := md.saved["medicalDeviceData"].(map[string]any)
	answer, _ := data["declaration"].(string)
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "no":
		md.out.State, md.out.Complete = mdNotRegulated, req.Answered()
	case "yes":
		md.out.State, md.out.Complete = mdRegulated, req.Answered()
		rows, _ := data["registrationInfo"].([]any)
		for _, r := range rows {
			row, _ := r.(map[string]any)
			if d, _ := row["declaration"].(string); !strings.EqualFold(strings.TrimSpace(d), "yes") {
				continue
			}
			regions, _ := row["countriesOrRegions"].([]any)
			for _, v := range regions {
				if s, _ := v.(string); s != "" && !slices.Contains(md.out.Regions, s) {
					md.out.Regions = append(md.out.Regions, s)
				}
			}
		}
		slices.Sort(md.out.Regions)
	default:
		md.out.State = mdNotDeclared
	}
	return md, nil
}

// savedForm returns the stored object of a form response. Apple puts it
// under data (an object, or a one-element array) or at the top level next
// to constraints; nil when nothing is stored.
func savedForm(raw json.RawMessage) map[string]any {
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal(doc["data"], &obj) == nil && obj != nil {
		return obj
	}
	var list []map[string]any
	if json.Unmarshal(doc["data"], &list) == nil && len(list) > 0 && list[0] != nil {
		return list[0]
	}
	delete(doc, "constraints")
	delete(doc, "data")
	if len(doc) == 0 {
		return nil
	}
	obj = make(map[string]any, len(doc))
	for k, v := range doc {
		var x any
		if json.Unmarshal(v, &x) != nil {
			return nil
		}
		obj[k] = x
	}
	return obj
}

func medicalDeviceGet(ctx context.Context, env *tool.Env, in medicalDeviceGetIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	md, err := readMedicalDevice(ctx, env, app)
	if err != nil {
		return nil, err
	}
	res := tool.OK(md.out)
	switch {
	case md.out.State == mdNotDeclared && md.out.Required:
		res.Warn("Apple refuses to review the app until this is declared; medical_device_set saves regulated:false, a regulated device is declared in App Store Connect").
			Then(tool.NextStep{Tool: "medical_device_set", Args: map[string]any{"app": app.ID}})
	case md.out.State != mdNotListed && md.out.State != mdNotDeclared && !md.out.Complete:
		res.Warn("Apple does not report the declaration as complete; finish it in App Store Connect")
	}
	return res, nil
}

func medicalDeviceSet(ctx context.Context, env *tool.Env, in medicalDeviceSetIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	page := medicalDevicePage(app.ID)
	read := tool.NextStep{Tool: "medical_device_get", Args: map[string]any{"app": app.ID}}
	if in.Regulated {
		return nil, tool.InvalidArgs("a regulated medical device is declared with its registration number, instructions for use and safety information per region in App Store Connect (%s); this tool only saves regulated:false", page)
	}
	md, err := readMedicalDevice(ctx, env, app)
	if err != nil {
		return nil, err
	}
	switch md.out.State {
	case mdNotListed:
		return nil, tool.InvalidArgs("App Store Connect tracks no regulated medical device declaration for app %s: nothing to declare", app.ID)
	case mdRegulated:
		return nil, tool.WithNext(tool.InvalidArgs("the app is declared a regulated medical device; change that in App Store Connect (%s)", page), read)
	case mdNotRegulated:
		if !md.out.Complete {
			return nil, tool.WithNext(tool.InvalidArgs("the answer No is saved but Apple does not report it as complete; finish it in App Store Connect (%s)", page), read)
		}
		md.out.Status = "declared"
		return tool.Already(md.out), nil
	}
	// The first answer, as App Store Connect's own page sends it: the
	// form's stored object with the answer and the page's region list.
	body := maps.Clone(md.saved)
	if body == nil {
		body = map[string]any{}
	}
	data, _ := body["medicalDeviceData"].(map[string]any)
	data = maps.Clone(data)
	if data == nil {
		data = map[string]any{}
	}
	data["declaration"] = "no"
	body["medicalDeviceData"] = data
	body["countriesOrRegions"] = mdRegions
	for k, v := range map[string]string{
		"accountId": md.req.AccountID, "contentId": app.ID, "requirementId": md.req.ID, "requirementName": tool.MedicalDeviceRequirementName,
	} {
		if _, ok := body[k]; !ok {
			body[k] = v
		}
	}
	p := tool.PlanPreview{
		Summary:  fmt.Sprintf("declare that %s is not a regulated medical device", app.Name),
		Current:  "not declared",
		Intended: body,
		Affected: []string{"app " + app.ID + " regulated medical device declaration (web session)"},
		Warnings: []string{
			"a regulatory statement in the account holder's name: No is right only for an app with no FDA clearance or registration, no CE or UKCA mark and no self-certification under the EU or UK medical device rules",
		},
	}
	// Bound to what was read: an answer saved since the preview needs a new one.
	wctx, res := tool.RequireConfirmTarget(ctx, env, in.Confirm, map[string]any{"app": app.ID, "state": md.out.State}, p)
	if res != nil {
		return res, nil
	}
	path := tool.ComplianceFormsPath + md.req.AccountID + "/contents/" + app.ID + "/requirements/" + md.req.ID + "/forms"
	werr := env.Portal.JSON(wctx, http.MethodPost, path, body, nil)
	if werr != nil && !errors.Is(werr, tool.ErrOutcomeUnknown) {
		return nil, fmt.Errorf("save the regulated medical device declaration: %w", werr)
	}
	// Read back what Apple now has; an unanswered write is settled by it.
	after, rerr := readMedicalDevice(ctx, env, app)
	switch {
	case rerr != nil && werr != nil:
		return nil, tool.WithNext(fmt.Errorf("save the regulated medical device declaration: %w", werr), read)
	case rerr != nil:
		return nil, tool.WithNext(fmt.Errorf("the declaration was sent, but reading it back failed: %w", rerr), read)
	case after.out.State != mdNotRegulated:
		if werr != nil {
			return nil, tool.WithNext(fmt.Errorf("save the regulated medical device declaration: %w", werr), read)
		}
		return nil, tool.WithNext(fmt.Errorf("%w: Apple accepted the declaration but still reports %s", tool.ErrUpstreamInvalid, after.out.State), read)
	}
	after.out.Status = "declared"
	r := tool.OK(after.out)
	if !after.out.Complete {
		r.Warn("Apple saved the answer but does not report the declaration as complete yet; check medical_device_get")
	}
	return r.Then(tool.NextStep{Tool: "release_status", Args: map[string]any{"app": app.ID}}), nil
}
