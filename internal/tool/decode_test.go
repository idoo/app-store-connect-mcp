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
