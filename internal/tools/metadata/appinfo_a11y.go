package metadata

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// a11yIn declares the Accessibility Nutrition Labels of one device family.
type a11yIn struct {
	DeviceFamily string   `json:"device_family"`
	Supports     []string `json:"supports,omitempty" jsonschema:"all supported features; unlisted become false"`
	Publish      bool     `json:"publish,omitempty" jsonschema:"make public (human approval)"`
}

// a11yFeatures maps supports values to accessibilityDeclaration attributes.
var a11yFeatures = map[string]string{
	"voiceover":                   "supportsVoiceover",
	"voice_control":               "supportsVoiceControl",
	"larger_text":                 "supportsLargerText",
	"dark_interface":              "supportsDarkInterface",
	"differentiate_without_color": "supportsDifferentiateWithoutColorAlone",
	"sufficient_contrast":         "supportsSufficientContrast",
	"reduced_motion":              "supportsReducedMotion",
	"captions":                    "supportsCaptions",
	"audio_descriptions":          "supportsAudioDescriptions",
}

// a11yFeatureEnum is the supports enum, in display order.
var a11yFeatureEnum = []any{
	"voiceover", "voice_control", "larger_text", "dark_interface", "differentiate_without_color",
	"sufficient_contrast", "reduced_motion", "captions", "audio_descriptions",
}

// desired returns every supports* attribute (true when listed), or nil when
// supports was omitted.
func (a *a11yIn) desired() (map[string]any, error) {
	if a.Supports == nil {
		return nil, nil
	}
	attrs := make(map[string]any, len(a11yFeatures))
	for _, attr := range a11yFeatures {
		attrs[attr] = false
	}
	for _, f := range a.Supports {
		attr, ok := a11yFeatures[f]
		if !ok {
			return nil, fmt.Errorf("%w: unknown accessibility feature %q", tool.ErrInvalidArgs, f)
		}
		attrs[attr] = true
	}
	return attrs, nil
}

// planAccessibility diffs the declaration of one device family: a DRAFT is
// PATCHed, otherwise a new one is POSTed (a published one is never edited
// in place); publish sets publish:true. Nothing is ever deleted. It
// reports whether a publish is planned.
func planAccessibility(ctx context.Context, env *tool.Env, appID string, in *a11yIn) ([]infoWrite, []infoChange, bool, error) {
	want, err := in.desired()
	if err != nil {
		return nil, nil, false, err
	}
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/accessibilityDeclarations", url.Values{
		"filter[deviceFamily]": {in.DeviceFamily}, "limit": {"50"},
	})
	if err != nil {
		return nil, nil, false, fmt.Errorf("list accessibility declarations: %w", err)
	}
	var draft, published *tool.Resource
	for i, d := range resp.Data {
		switch d.Str("state") {
		case "DRAFT":
			draft = &resp.Data[i]
		case "PUBLISHED":
			published = &resp.Data[i]
		}
	}
	const target = "accessibilityDeclaration"
	base, baseID, baseState := published, "", any(nil)
	if draft != nil {
		base = draft
	}
	if base != nil {
		baseID, baseState = base.ID, base.Str("state")
	}
	var (
		changes []infoChange
		attrs   = map[string]any{}
	)
	for _, f := range a11yFeatureEnum {
		attr := a11yFeatures[f.(string)]
		v, ok := want[attr]
		if !ok {
			continue
		}
		var cur any
		if base != nil {
			cur = base.Attributes[attr]
		}
		if cur == v {
			continue
		}
		attrs[attr] = v
		changes = append(changes, infoChange{Target: target, ID: baseID, Field: f.(string), Current: cur, New: v})
	}
	publish := in.Publish && (draft != nil || len(attrs) > 0)
	if publish {
		changes = append(changes, infoChange{Target: target, ID: baseID, Field: "state", Current: baseState, New: "PUBLISHED"})
	}
	publishReq := func(id string) tool.Request {
		return patchReq("/v1/accessibilityDeclarations/"+id, "accessibilityDeclarations", id, map[string]any{"publish": true}, nil)
	}
	label := target + " " + in.DeviceFamily
	switch {
	case draft != nil:
		if publish {
			attrs["publish"] = true
		}
		if len(attrs) == 0 {
			return nil, nil, false, nil
		}
		return []infoWrite{{label: label, req: patchReq("/v1/accessibilityDeclarations/"+draft.ID, "accessibilityDeclarations", draft.ID, attrs, nil)}}, changes, publish, nil
	case len(attrs) > 0:
		create := map[string]any{"deviceFamily": in.DeviceFamily}
		for k, v := range want {
			create[k] = v
		}
		w := infoWrite{label: label, req: tool.Request{Method: http.MethodPost, Path: "/v1/accessibilityDeclarations", Write: true, Body: map[string]any{"data": map[string]any{
			"type": "accessibilityDeclarations", "attributes": create,
			"relationships": map[string]any{"app": map[string]any{"data": map[string]any{"type": "apps", "id": appID}}},
		}}}}
		if publish {
			w.then = publishReq
		}
		return []infoWrite{w}, changes, publish, nil
	case want == nil && published == nil:
		return nil, nil, false, fmt.Errorf("%w: no %s accessibility declaration yet; pass supports to create one", tool.ErrInvalidArgs, in.DeviceFamily)
	}
	return nil, nil, false, nil
}
