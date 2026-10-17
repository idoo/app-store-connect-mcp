package metadata

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// bodyAttrs returns the data.attributes of a recorded request body.
func bodyAttrs(t *testing.T, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Data struct {
			Attributes    map[string]any `json:"attributes"`
			Relationships map[string]any `json:"relationships"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Data.Relationships != nil {
		doc.Data.Attributes["_rel"] = doc.Data.Relationships
	}
	return doc.Data.Attributes
}

const draftIPhone = `{"data":[
 {"type":"accessibilityDeclarations","id":"pub1","attributes":{"deviceFamily":"IPHONE","state":"PUBLISHED","supportsVoiceover":false,"supportsLargerText":true}},
 {"type":"accessibilityDeclarations","id":"dr1","attributes":{"deviceFamily":"IPHONE","state":"DRAFT","supportsVoiceover":false,"supportsVoiceControl":false,
  "supportsLargerText":true,"supportsDarkInterface":true,"supportsDifferentiateWithoutColorAlone":false,"supportsSufficientContrast":false,
  "supportsReducedMotion":false,"supportsCaptions":false,"supportsAudioDescriptions":false}}],"links":{}}`

func TestAppinfoAccessibilityCreate(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/accessibilityDeclarations", http.StatusOK, `{"data":[],"links":{}}`)
	k.ASC.Handle(http.MethodPost, "/v1/accessibilityDeclarations", http.StatusCreated,
		`{"data":{"type":"accessibilityDeclarations","id":"new1","attributes":{"deviceFamily":"IPHONE","state":"DRAFT"}}}`)
	args := `{"accessibility":{"device_family":"IPHONE","supports":["voiceover","larger_text"]}}`
	prev := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, args)))
	intended := prev["preview"].(map[string]any)["intended"].([]any)
	if prev["status"] != "needs_confirm" || len(intended) != 9 || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("preview = %v", prev)
	}
	first := intended[0].(map[string]any)
	if first["field"] != "voiceover" || first["current"] != nil || first["new"] != true {
		t.Errorf("voiceover change = %v", first)
	}
	out := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, strings.TrimSuffix(args, "}")+`,"confirm":true}`)))
	if out["status"] != "applied" {
		t.Fatalf("out = %v", out)
	}
	muts := k.ASC.Mutations()
	if len(muts) != 1 || muts[0].Method != http.MethodPost {
		t.Fatalf("mutations = %+v", muts)
	}
	a := bodyAttrs(t, muts[0].Body)
	if a["deviceFamily"] != "IPHONE" || a["supportsVoiceover"] != true || a["supportsLargerText"] != true || a["supportsCaptions"] != false || a["_rel"] == nil {
		t.Errorf("create attrs = %v", a)
	}
	if _, ok := a["publish"]; ok {
		t.Error("create published without being asked")
	}
}

// A DRAFT is PATCHed with only the changed booleans.
func TestAppinfoAccessibilityUpdateDraft(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/accessibilityDeclarations", http.StatusOK, draftIPhone)
	k.ASC.Handle(http.MethodPatch, "/v1/accessibilityDeclarations/{id}", http.StatusOK,
		`{"data":{"type":"accessibilityDeclarations","id":"dr1","attributes":{"state":"DRAFT"}}}`)
	args := `{"accessibility":{"device_family":"IPHONE","supports":["voiceover","larger_text","dark_interface"]},"confirm":true}`
	k.Elicit(true)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, strings.Replace(args, `,"confirm":true`, "", 1)))
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, args))
	muts := k.ASC.RequestsTo(http.MethodPatch, "/v1/accessibilityDeclarations/{id}")
	if len(muts) != 1 || !strings.HasSuffix(muts[0].Path, "/dr1") || len(k.ASC.RequestsTo(http.MethodPost, "/v1/accessibilityDeclarations")) != 0 {
		t.Fatalf("patches = %+v", muts)
	}
	if a := bodyAttrs(t, muts[0].Body); len(a) != 1 || a["supportsVoiceover"] != true {
		t.Errorf("patch attrs = %v (unchanged booleans must not be sent)", a)
	}
}

// Publishing is public: it needs a human, and the preview says so.
func TestAppinfoAccessibilityPublishNeedsHuman(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/accessibilityDeclarations", http.StatusOK, draftIPhone)
	k.ASC.Handle(http.MethodPatch, "/v1/accessibilityDeclarations/{id}", http.StatusOK,
		`{"data":{"type":"accessibilityDeclarations","id":"dr1","attributes":{"state":"PUBLISHED"}}}`)
	args := `{"accessibility":{"device_family":"IPHONE","publish":true}}`
	prev := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, args)))
	p := prev["preview"].(map[string]any)
	intended := p["intended"].([]any)
	if !strings.Contains(p["summary"].(string), "public product page") || len(intended) != 1 {
		t.Fatalf("preview = %v", p)
	}
	if c := intended[0].(map[string]any); c["field"] != "state" || c["current"] != "DRAFT" || c["new"] != "PUBLISHED" {
		t.Errorf("state change = %v", c)
	}
	confirmed := strings.TrimSuffix(args, "}") + `,"confirm":true}`
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, confirmed), "human_approval_required")
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("published without a human")
	}

	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, args))
	asked := k.Elicit(true)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, confirmed))
	muts := k.ASC.Mutations()
	if len(*asked) != 1 || len(muts) != 1 {
		t.Fatalf("asked = %v mutations = %+v", *asked, muts)
	}
	if a := bodyAttrs(t, muts[0].Body); len(a) != 1 || a["publish"] != true {
		t.Errorf("publish attrs = %v", a)
	}
}

// Creating and publishing in one call PATCHes publish onto the new id.
func TestAppinfoAccessibilityCreateThenPublish(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/accessibilityDeclarations", http.StatusOK, `{"data":[],"links":{}}`)
	k.ASC.Handle(http.MethodPost, "/v1/accessibilityDeclarations", http.StatusCreated,
		`{"data":{"type":"accessibilityDeclarations","id":"new1","attributes":{"deviceFamily":"MAC","state":"DRAFT"}}}`)
	k.ASC.Handle(http.MethodPatch, "/v1/accessibilityDeclarations/{id}", http.StatusOK,
		`{"data":{"type":"accessibilityDeclarations","id":"new1","attributes":{"state":"PUBLISHED"}}}`)
	args := `{"accessibility":{"device_family":"MAC","supports":["captions"],"publish":true}}`
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, args))
	k.Elicit(true)
	out := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, strings.TrimSuffix(args, "}")+`,"confirm":true}`)))
	muts := k.ASC.Mutations()
	if out["status"] != "applied" || len(muts) != 2 || muts[0].Method != http.MethodPost || !strings.HasSuffix(muts[1].Path, "/new1") {
		t.Fatalf("out = %v mutations = %+v", out, muts)
	}
}

func TestAppinfoAccessibilityInvalid(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/accessibilityDeclarations", http.StatusOK, `{"data":[],"links":{}}`)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"accessibility":{"device_family":"IPHONE","publish":true}}`), "invalid_arguments")
}
