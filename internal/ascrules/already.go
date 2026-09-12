// Package ascrules holds App Store Connect behaviour rules shared by the
// client and the tool packages: the eventual-consistency read flag and the
// per-operation "already exists" answers.
package ascrules

import (
	"errors"
	"net/http"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// AlreadyRule recognizes the App Store Connect error that one create
// operation returns when its object already exists. A 409/422 code alone is
// ambiguous (the same codes mean "wrong state"), so rules are per operation.
type AlreadyRule struct {
	// Method and Path identify the operation; Path is a spec template whose
	// {param} segments match any single segment.
	Method string
	Path   string
	// Status is the HTTP status Apple returns.
	Status int
	// Code must equal the code of one errors[] entry.
	Code string
	// Pointer, when set, must equal that entry's source pointer.
	Pointer string
	// DetailContains, when set, must occur in that entry's detail
	// (case-insensitive).
	DetailContains string
	// ReadBack names the GET a tool must perform to confirm the object
	// exists before reporting already:true.
	ReadBack string
}

// AlreadyRules is the allow-list of "already exists" answers, verified live
// by the rudrank CLI (see challenge-appleapi #4). Re-adding a capability
// answers 201 and needs no rule.
var AlreadyRules = []AlreadyRule{
	{
		Method: http.MethodPost, Path: "/v1/appStoreVersions", Status: http.StatusConflict,
		Code:     "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE",
		ReadBack: "GET /v1/apps/{id}/appStoreVersions?filter[versionString]=…&filter[platform]=…",
	},
	{
		Method: http.MethodPost, Path: "/v1/appStoreReviewDetails", Status: http.StatusConflict,
		Code:     "STATE_ERROR.ALREADY_EXISTS",
		ReadBack: "GET /v1/appStoreVersions/{id}/appStoreReviewDetail",
	},
	{
		Method: http.MethodPost, Path: "/v1/appStoreVersionLocalizations", Status: http.StatusConflict,
		Code: "ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE", Pointer: "/data/attributes/locale",
		ReadBack: "GET /v1/appStoreVersions/{id}/appStoreVersionLocalizations?filter[locale]=…",
	},
	{
		Method: http.MethodPost, Path: "/v2/appAvailabilities", Status: http.StatusConflict,
		Code: "ENTITY_ERROR.RELATIONSHIP.INVALID", DetailContains: "already exists",
		ReadBack: "GET /v1/apps/{id}/appAvailabilityV2",
	},
	{
		Method: http.MethodPost, Path: "/v1/builds/{id}/relationships/betaGroups",
		Status: http.StatusUnprocessableEntity, Code: "STATE_ERROR.ENTITY_STATE_INVALID",
		ReadBack: "GET /v1/builds/{id}/relationships/betaGroups",
	},
}

// IsAlready reports whether err, returned for req, is an "already exists"
// answer listed in [AlreadyRules]. It is only a candidate: the calling tool
// must perform the rule's read-back GET and report already:true only when
// the object is found; otherwise it returns err unchanged.
func IsAlready(req tool.Request, err error) bool {
	_, ok := MatchAlready(req, err)
	return ok
}

// MatchAlready returns the [AlreadyRule] matching req and err, if any.
func MatchAlready(req tool.Request, err error) (AlreadyRule, bool) {
	var ae *tool.APIError
	if !errors.As(err, &ae) {
		return AlreadyRule{}, false
	}
	method := strings.ToUpper(req.Method)
	path, _, _ := strings.Cut(req.Path, "?")
	for _, r := range AlreadyRules {
		if r.Method != method || ae.Status != r.Status || !pathMatches(r.Path, path) {
			continue
		}
		if entryMatches(r, *ae) {
			return r, true
		}
		for _, m := range ae.More {
			if entryMatches(r, m) {
				return r, true
			}
		}
	}
	return AlreadyRule{}, false
}

// entryMatches checks one errors[] entry against r.
func entryMatches(r AlreadyRule, e tool.APIError) bool {
	if e.Code != r.Code || (r.Pointer != "" && e.Pointer != r.Pointer) {
		return false
	}
	return r.DetailContains == "" ||
		strings.Contains(strings.ToLower(e.Detail), strings.ToLower(r.DetailContains))
}

// pathMatches reports whether path matches template segment by segment.
func pathMatches(template, path string) bool {
	ts := strings.Split(strings.Trim(template, "/"), "/")
	ps := strings.Split(strings.Trim(path, "/"), "/")
	if len(ts) != len(ps) {
		return false
	}
	for i, t := range ts {
		if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
			if ps[i] == "" {
				return false
			}
			continue
		}
		if t != ps[i] {
			return false
		}
	}
	return true
}
