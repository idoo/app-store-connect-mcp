package tool

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

type nullArgs struct {
	Limit  int            `json:"limit,omitempty"`
	Name   string         `json:"name,omitempty"`
	Tags   []string       `json:"tags,omitempty"`
	Inner  *nullArgsInner `json:"inner,omitempty"`
	Needed string         `json:"needed"`
}

type nullArgsInner struct {
	On bool `json:"on,omitempty"`
}

// An optional argument sent as null behaves as omitted; a required one sent
// as null reports it missing, never Go reflection internals.
func TestDecodeArgsDropsNulls(t *testing.T) {
	s, err := jsonschema.For[nullArgs](nil)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	args, err := decodeArgs(json.RawMessage(`{"limit":null,"name":null,"tags":null,"inner":{"on":null},"needed":"x"}`), s, rs)
	if err != nil {
		t.Fatalf("null optionals rejected: %v", err)
	}
	if _, ok := args["limit"]; ok || len(args["inner"].(map[string]any)) != 0 {
		t.Fatalf("args = %v", args)
	}
	_, err = decodeArgs(json.RawMessage(`{"needed":null}`), s, rs)
	if err == nil || strings.Contains(err.Error(), "reflect.Value") || !strings.Contains(err.Error(), "needed") {
		t.Fatalf("required null err = %v", err)
	}
}

type openArgs struct {
	Age     map[string]any `json:"age,omitempty"`
	Ratings []int          `json:"ratings,omitempty"`
	Names   []string       `json:"names,omitempty"`
}

// A null inside a free-form map is data and reaches the handler (RB-1); a
// scalar sent for an array argument becomes a one-element array (L2-3).
func TestDecodeArgsOpenMapNullAndScalarArray(t *testing.T) {
	s, err := jsonschema.For[openArgs](nil)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	args, err := decodeArgs(json.RawMessage(`{"age":{"kidsAgeBand":null,"gambling":false},"ratings":1,"names":"a"}`), s, rs)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	age := args["age"].(map[string]any)
	if v, ok := age["kidsAgeBand"]; !ok || v != nil {
		t.Fatalf("age = %v, want kidsAgeBand:null kept", age)
	}
	if r, ok := args["ratings"].([]any); !ok || len(r) != 1 || r[0] != float64(1) {
		t.Fatalf("ratings = %#v", args["ratings"])
	}
	if n, ok := args["names"].([]any); !ok || len(n) != 1 || n[0] != "a" {
		t.Fatalf("names = %#v", args["names"])
	}
	args, err = decodeArgs(json.RawMessage(`{"ratings":"2"}`), s, rs)
	if err != nil {
		t.Fatalf("stringified scalar: %v", err)
	}
	if r := args["ratings"].([]any); len(r) != 1 || r[0] != float64(2) {
		t.Fatalf("ratings = %#v", r)
	}
	if _, err := decodeArgs(json.RawMessage(`{"ratings":1.5}`), s, rs); err == nil {
		t.Fatal("fractional rating accepted")
	}
}
