package tool

import (
	"context"
	"net/url"
)

// DevPortal reaches the Apple Developer portal's v1 API and its legacy
// App Group endpoints. With an API key, bundle ID reads, the capability
// catalog and bundle ID PATCHes go with the key (appstoreconnect.apple.com/
// xcbuild/); everything else, and anything Apple refuses the key, goes
// through developer.apple.com/services-account/ with the human's web
// session. path is relative, e.g. "v1/bundleIds/{id}" or
// "QH65B2/account/ios/identifiers/listApplicationGroups.action".
type DevPortal interface {
	// Do sends method to path with body encoded as JSON when non-nil and
	// decodes a 2xx JSON answer into out (out may be nil). A call that
	// needs a web session when none is active returns an error wrapping
	// [ErrWebSessionRequired]; other non-2xx answers return *PortalError.
	Do(ctx context.Context, method, path string, body, out any) error
}

// devPortalRead is the body of a services-account v1 read: web sessions
// can't send GETs, so reads are POSTs overridden to GET.
type devPortalRead struct {
	URLEncodedQueryParams string `json:"urlEncodedQueryParams"`
	TeamID                string `json:"teamId"`
}

// DevPortalGet reads a services-account v1 resource. It calls Do with
// method "GET" and body {urlEncodedQueryParams, teamId}; implementations
// send that as POST with X-HTTP-Method-Override: GET (web sessions can't
// send real GETs to services-account).
func DevPortalGet(ctx context.Context, p DevPortal, path string, query url.Values, teamID string, out any) error {
	return p.Do(ctx, "GET", path, devPortalRead{URLEncodedQueryParams: query.Encode(), TeamID: teamID}, out)
}

type noDevPortal struct{}

func (noDevPortal) Do(context.Context, string, string, any, any) error {
	return ErrWebSessionRequired
}
