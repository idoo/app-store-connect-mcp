package tool_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func TestBatch(t *testing.T) {
	plain := fmt.Errorf("item b: %w", tool.ErrInvalidArgs)
	unknown := fmt.Errorf("item c: %w", tool.ErrOutcomeUnknown)
	next := tool.NextStep{Tool: "builds_list"}
	items := []string{"a", "b", "c"}
	tests := []struct {
		name             string
		applied, already int
		errs             []error
		wantErr          bool
		wantCode         string
		wantWarn         bool
	}{
		{"all ok", 3, 0, nil, false, "", false},
		{"all already", 0, 3, nil, false, "", false},
		{"partial", 1, 0, []error{plain}, false, "", true},
		{"already plus fail", 0, 1, []error{plain}, false, "", true},
		{"all fail", 0, 0, []error{plain, plain}, true, "invalid_arguments", false},
		{"all fail unknown wins", 0, 0, []error{plain, unknown}, true, "outcome_unknown", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := tool.Batch(items, tc.applied, tc.already, tc.errs, next)
			if r.IsError != tc.wantErr {
				t.Fatalf("IsError = %v", r.IsError)
			}
			if (len(r.Meta.Warnings) > 0) != tc.wantWarn {
				t.Errorf("warnings = %v", r.Meta.Warnings)
			}
			if len(r.Next) == 0 || r.Next[len(r.Next)-1].Tool != "builds_list" {
				t.Errorf("next = %+v", r.Next)
			}
			if !tc.wantErr {
				return
			}
			b, _ := json.Marshal(r.Data)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if body["error"] != tc.wantCode || body["items"] == nil {
				t.Errorf("data = %s", b)
			}
		})
	}
}

func TestItemError(t *testing.T) {
	if o, b := tool.ItemError(fmt.Errorf("x: %w", tool.ErrOutcomeUnknown)); o != "unknown" || b.Error != "outcome_unknown" {
		t.Errorf("unknown = %q %+v", o, b)
	}
	if o, b := tool.ItemError(errors.New("boom")); o != "error" || b.Message != "boom" {
		t.Errorf("plain = %q %+v", o, b)
	}
}

func TestBodyForSentinels(t *testing.T) {
	api := &tool.APIError{Status: 404, Code: "NOT_FOUND", Hint: "check the id"}
	tests := []struct {
		err  error
		code string
	}{
		{fmt.Errorf("%w: %w", tool.ErrAlreadyGone, api), "already_gone"},
		{fmt.Errorf("asc: %w", tool.ErrResponseTooLarge), "response_too_large"},
		{fmt.Errorf("asc: %w", tool.ErrUpstreamInvalid), "upstream_invalid"},
		// A broken 2xx on a POST is "may have been applied", whatever the cause.
		{fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, fmt.Errorf("parse: %w", tool.ErrUpstreamInvalid)), "outcome_unknown"},
		{fmt.Errorf("parse: %w: %w", tool.ErrUpstreamInvalid, tool.ErrOutcomeUnknown), "outcome_unknown"},
	}
	for _, tc := range tests {
		if b := tool.BodyFor(tc.err); b.Error != tc.code || b.Hint == "" {
			t.Errorf("%v: body = %+v, want %s with a hint", tc.err, b, tc.code)
		}
	}
}
