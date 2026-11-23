package tool

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// DevSite reaches the Apple Developer account site (developer.apple.com)
// with the human's web session, beyond the signing calls of [DevPortal]:
// any services-account endpoint, the account and contact pages, and the
// forms on them. Every write needs a write context ([IsWrite]) and is never
// replayed. Without a usable session every call returns an error wrapping
// [ErrWebSessionRequired].
type DevSite interface {
	// API sends one call to https://developer.apple.com/services-account/.
	// Whether it is a read is decided by [DevSiteIsRead] alone.
	API(ctx context.Context, call DevSiteCall) (DevSiteAnswer, error)
	// Page GETs one HTML page under https://developer.apple.com/. path is
	// relative ("account/resources/identifiers/list") and its first
	// segment is one of [DevSitePageRoots]. Redirects are followed by hand
	// inside the developer site and Apple's sign-in host only; a page that
	// ends on the sign-in host is no session.
	Page(ctx context.Context, path string, query url.Values) (DevSiteAnswer, error)
	// Submit posts a form or a JSON body to a page's action under
	// [DevSitePageRoots]. Always a write. A redirect is not followed: the
	// answer carries it in Location.
	Submit(ctx context.Context, post DevSitePost) (DevSiteAnswer, error)
}

// DevSitePageRoots are the first path segments [DevSite.Page] and
// [DevSite.Submit] serve.
var DevSitePageRoots = []string{"account", "contact"}

// DevSiteCall is one services-account call. How it goes on the wire, as
// the site's own pages send it:
//   - v1 GET: POST with X-HTTP-Method-Override: GET and the body
//     {urlEncodedQueryParams, teamId};
//   - v1 DELETE: POST with X-HTTP-Method-Override: DELETE and the body
//     {urlEncodedQueryParams, teamId};
//   - v1 POST, PATCH, PUT: that method with JSON as
//     application/vnd.api+json (the team id is in the body's own
//     data.attributes.teamId, never added);
//   - a legacy path with Form: POST, form-encoded, teamId added;
//   - a legacy path with JSON: POST as application/json, sent as given;
//   - a legacy GET (a download): a real GET with Query and teamId in the
//     query string.
type DevSiteCall struct {
	// Method is GET, POST, PATCH, PUT or DELETE as the endpoint means it.
	Method string
	// Path is relative to services-account/: "v1/bundleIds",
	// "QH65B2/account/auth/key/list".
	Path string
	// Query is a v1 read's or delete's query, or a legacy GET's.
	Query url.Values
	// TeamID is the 10-character developer team id; see above where it
	// goes. Empty sends none.
	TeamID string
	// JSON is a JSON body; nil for a v1 GET or DELETE and for a form call.
	JSON any
	// Form is a legacy call's form body.
	Form url.Values
}

// DevSitePost is one form or JSON post to a page's action.
type DevSitePost struct {
	// Path is relative to https://developer.apple.com/, under
	// [DevSitePageRoots].
	Path  string
	Query url.Values
	// Referer is the relative path of the page the form stands on, without
	// its query; RefererQuery is that page's query.
	Referer      string
	RefererQuery url.Values
	// ContentType is the body's: application/x-www-form-urlencoded,
	// multipart/form-data with its boundary, or application/json.
	ContentType string
	Body        []byte
	// Headers are extra request headers, names from [DevSitePostHeaders]
	// only (a page's own anti-forgery header).
	Headers map[string]string
}

// DevSitePostHeaders are the header names a [DevSitePost] may add.
var DevSitePostHeaders = []string{"X-CSRF-Token", "X-Csrf-Token", "X-XSRF-TOKEN", "X-Requested-With", "Accept"}

// DevSiteAnswer is what the developer site answered. Body is bounded and
// may be JSON, HTML or text; it is Apple's and the account's data, to be
// shown as untrusted.
type DevSiteAnswer struct {
	Status      int
	ContentType string
	// URL is the absolute URL that answered (after a page's redirects).
	URL string
	// Location is a redirect's target on a [DevSite.Submit] answer.
	Location string
	Body     []byte
}

// devSiteReadVerbs start the name of a legacy endpoint that only reads.
var devSiteReadVerbs = []string{"list", "get", "search", "view", "check", "is", "has", "validate"}

// devSiteReadPosts are the v1 POSTs the site itself only reads with. POST
// v1/featureRequest answers the request history of one capability of an
// App ID and files nothing (run live on 2026-11-23: the capability's request
// count did not move).
var devSiteReadPosts = []string{"v1/featureRequest"}

// DevSiteIsRead reports whether a services-account call only reads: a v1
// GET, one of the v1 POSTs of devSiteReadPosts, or a legacy endpoint whose
// last path segment (".action" dropped) starts with a reading verb
// ("listKeys", "getTeams", "list"). Everything else, downloads of key
// material included, counts as a write and needs a human's approval.
func DevSiteIsRead(method, path string) bool {
	if strings.HasPrefix(path, "v1/") || strings.HasPrefix(path, "v2/") {
		return method == http.MethodGet || (method == http.MethodPost && slices.Contains(devSiteReadPosts, path))
	}
	if method != http.MethodPost && method != http.MethodGet {
		return false
	}
	name := path[strings.LastIndexByte(path, '/')+1:]
	name = strings.TrimSuffix(name, ".action")
	for _, verb := range devSiteReadVerbs {
		rest, ok := strings.CutPrefix(name, verb)
		if !ok {
			continue
		}
		// "list" itself, or the verb followed by a capital: "listKeys",
		// never "listen" or "issue".
		if rest == "" || (rest[0] >= 'A' && rest[0] <= 'Z') {
			return true
		}
	}
	return false
}

type noDevSite struct{}

func (noDevSite) API(context.Context, DevSiteCall) (DevSiteAnswer, error) {
	return DevSiteAnswer{}, ErrWebSessionRequired
}

func (noDevSite) Page(context.Context, string, url.Values) (DevSiteAnswer, error) {
	return DevSiteAnswer{}, ErrWebSessionRequired
}

func (noDevSite) Submit(context.Context, DevSitePost) (DevSiteAnswer, error) {
	return DevSiteAnswer{}, ErrWebSessionRequired
}
