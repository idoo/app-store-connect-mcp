package ascrules

import (
	"net/http"
	"testing"

	"github.com/idoo/hamn/internal/tool"
)

func TestMatchAlready(t *testing.T) {
	post := func(path string) tool.Request { return tool.Request{Method: http.MethodPost, Path: path} }
	tests := []struct {
		name string
		req  tool.Request
		err  error
		want bool
	}{
		{
			"duplicate version in second entry", post("/v1/appStoreVersions"),
			&tool.APIError{Status: 409, Code: "ENTITY_ERROR.RELATIONSHIP.INVALID", More: []tool.APIError{
				{Status: 409, Code: "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE"},
			}}, true,
		},
		{
			"version wrong state only", post("/v1/appStoreVersions"),
			&tool.APIError{Status: 409, Code: "ENTITY_ERROR.RELATIONSHIP.INVALID"}, false,
		},
		{
			"review detail exists", post("/v1/appStoreReviewDetails"),
			&tool.APIError{Status: 409, Code: "STATE_ERROR.ALREADY_EXISTS"}, true,
		},
		{
			"locale exists", post("/v1/appStoreVersionLocalizations"),
			&tool.APIError{Status: 409, Code: "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", Pointer: "/data/attributes/locale"}, true,
		},
		{
			"locale duplicate on other pointer", post("/v1/appStoreVersionLocalizations"),
			&tool.APIError{Status: 409, Code: "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", Pointer: "/data/attributes/name"}, false,
		},
		{
			"availability exists", post("/v2/appAvailabilities"),
			&tool.APIError{Status: 409, Code: "ENTITY_ERROR.RELATIONSHIP.INVALID", Detail: "An 'appAvailabilities' resource Already Exists"}, true,
		},
		{
			"availability other relationship error", post("/v2/appAvailabilities"),
			&tool.APIError{Status: 409, Code: "ENTITY_ERROR.RELATIONSHIP.INVALID", Detail: "territory missing"}, false,
		},
		{
			"beta group add", post("/v1/builds/123/relationships/betaGroups?x=1"),
			&tool.APIError{Status: 422, Code: "STATE_ERROR.ENTITY_STATE_INVALID"}, true,
		},
		{
			"wrong method",
			tool.Request{Method: http.MethodPatch, Path: "/v1/appStoreReviewDetails"},
			&tool.APIError{Status: 409, Code: "STATE_ERROR.ALREADY_EXISTS"}, false,
		},
		{
			"wrong status", post("/v1/appStoreReviewDetails"),
			&tool.APIError{Status: 422, Code: "STATE_ERROR.ALREADY_EXISTS"}, false,
		},
		{"not an API error", post("/v1/appStoreReviewDetails"), tool.ErrOutcomeUnknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAlready(tt.req, tt.err); got != tt.want {
				t.Fatalf("IsAlready = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAlreadyRulesHaveReadBack(t *testing.T) {
	for _, r := range AlreadyRules {
		if r.ReadBack == "" || r.Code == "" || r.Status == 0 {
			t.Errorf("rule %s %s incomplete: %+v", r.Method, r.Path, r)
		}
	}
}
