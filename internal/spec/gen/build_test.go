package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/idoo/app-store-connect-mcp/internal/spec"
)

const tinySpec = `{
 "info": {"version": "9.9"},
 "paths": {
  "/v1/things": {
   "post": {
    "operationId": "things_createInstance", "tags": ["Things"],
    "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ThingCreateRequest"}}}},
    "responses": {"201": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/ThingResponse"}}}}}
   }
  },
  "/v1/things/{id}": {
   "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
   "get": {
    "operationId": "things_getInstance",
    "parameters": [{"name": "fields[things]", "in": "query", "schema": {"type": "array", "items": {"type": "string", "enum": ["note", "kind"]}}}],
    "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/ThingResponse"}}}}}
   }
  },
  "/v1/reports": {
   "get": {"operationId": "reports_getCollection", "responses": {"200": {"content": {"application/a-gzip": {}}}}}
  }
 },
 "components": {"schemas": {
  "Kind": {"type": "string", "enum": ["A", "B"]},
  "Thing": {"type": "object", "properties": {
   "type": {"type": "string", "enum": ["things"]}, "id": {"type": "string"},
   "attributes": {"type": "object", "properties": {"note": {"type": "string"}, "kind": {"$ref": "#/components/schemas/Kind"}, "supportUrl": {"type": "string", "format": "uri"}}}
  }},
  "ThingResponse": {"type": "object", "properties": {"data": {"$ref": "#/components/schemas/Thing"}}},
  "ThingCreateRequest": {"type": "object", "properties": {"data": {"type": "object", "required": ["type"], "properties": {
   "type": {"type": "string", "enum": ["things"]},
   "attributes": {"type": "object", "required": ["kind"], "properties": {"kind": {"$ref": "#/components/schemas/Kind"}, "note": {"type": "string"}}},
   "relationships": {"type": "object", "required": ["owner"], "properties": {"owner": {"type": "object", "properties": {"data": {"type": "object", "properties": {"type": {"type": "string", "enum": ["owners"]}, "id": {"type": "string"}}}}}}}
  }}}}
 }}
}`

func TestBuildResolvesRefs(t *testing.T) {
	f, err := Build([]byte(tinySpec))
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != "9.9" || f.Paths != 3 || len(f.Ops) != 3 {
		t.Fatalf("version=%q paths=%d ops=%d", f.Version, f.Paths, len(f.Ops))
	}
	byID := map[string]spec.Op{}
	for _, op := range f.Ops {
		byID[op.ID] = op
	}
	create := byID["things_createInstance"]
	wantBody := &spec.Body{
		Type: "things",
		Attrs: map[string]spec.Attr{
			"kind": {Type: "string", Required: true, Enum: []string{"A", "B"}},
			"note": {Type: "string"},
		},
		Rels: map[string]spec.Rel{"owner": {Type: "owners", Required: true}},
	}
	if diff := cmp.Diff(wantBody, create.Body); diff != "" {
		t.Errorf("body (-want +got):\n%s", diff)
	}
	if !create.BodyNeeded || create.RespType != "things" || create.Summary != "Create things" {
		t.Errorf("create op = %+v", create)
	}
	get := byID["things_getInstance"]
	if len(get.Params) != 2 || get.Params[0].Name != "id" || get.Params[1].Enum != nil {
		t.Errorf("get params = %+v (path param shared, fields enum moved)", get.Params)
	}
	if diff := cmp.Diff([]string{"kind", "note"}, f.Fields["things"]); diff != "" {
		t.Errorf("fields (-want +got):\n%s", diff)
	}
	if got := byID["reports_getCollection"].ContentType; got != "application/a-gzip" {
		t.Errorf("content type = %q", got)
	}
	if diff := cmp.Diff([]string{"note"}, f.FreeText["things"]); diff != "" {
		t.Errorf("free text (-want +got):\n%s", diff)
	}
}

func TestBuildRejectsInvalid(t *testing.T) {
	for _, in := range []string{`{`, `{"info":{},"paths":{}}`, `{"info":{"version":"1"},"paths":{"/x":{"get":{}}}}`} {
		if _, err := Build([]byte(in)); err == nil {
			t.Errorf("Build(%s) succeeded", in)
		}
	}
}

func TestRunFailsOnUnclassifiedMutation(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(in, []byte(tinySpec), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(in, dir, "", defaultSourceURL); err == nil {
		t.Fatal("run succeeded with unclassified POST /v1/things")
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json.gz")); !os.IsNotExist(err) {
		t.Errorf("index written despite failure: %v", err)
	}
}
