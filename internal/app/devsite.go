package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	// devSiteSignInHost is Apple's sign-in host: a page or form that ends
	// there has no session.
	devSiteSignInHost = "idmsa.apple.com"
	// devSitePrimePath is the first legacy call that hands out a csrf pair.
	devSitePrimePath = "QH65B2/account/listTeams.action"
	// devSitePageMax caps one HTML page.
	devSitePageMax = 4 << 20
	// devSiteExpiredSniff bounds the download body searched for Apple's
	// "session has expired" text.
	devSiteExpiredSniff = 4 << 10
)

// devSiteClient is [tool.DevSite] over the human's web session. It shares
// the developer portal client's transport, cookies, csrf pair and bootstrap,
// and keeps only its own write switch.
type devSiteClient struct {
	d     *devPortalClient
	allow atomic.Bool // writes allowed (env SetWrites)
}

func newDevSiteClient(d *devPortalClient) *devSiteClient { return &devSiteClient{d: d} }

// SetAllowWrites turns writes (every call DevSiteIsRead does not call a
// read, every Submit) on or off.
func (c *devSiteClient) SetAllowWrites(on bool) { c.allow.Store(on) }

// devSiteJSONAPI reports whether path is on the JSON:API (v1, v2).
func devSiteJSONAPI(path string) bool {
	return strings.HasPrefix(path, "v1/") || strings.HasPrefix(path, "v2/")
}

// shapeDevSite turns call into its wire request and query string as
// [tool.DevSiteCall] documents; anything fitting no shape is refused.
func shapeDevSite(call tool.DevSiteCall) (devPortalRequest, string, error) {
	label := tool.SanitizeText(call.Method) + " " + tool.SanitizeText(call.Path)
	refuse := func(why string) (devPortalRequest, string, error) {
		return devPortalRequest{}, "", fmt.Errorf("app: developer site: %s: %s", label, why)
	}
	if err := devSiteAPIPath(call.Path); err != nil {
		return devPortalRequest{}, "", err
	}
	switch call.Method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
	default:
		return refuse("method must be GET, POST, PATCH, PUT or DELETE")
	}
	if call.JSON != nil && call.Form != nil {
		return refuse("a call takes JSON or Form, not both")
	}
	encode := func(v any) ([]byte, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("app: developer site: %s: encode body: %w", label, err)
		}
		return b, nil
	}
	if devSiteJSONAPI(call.Path) {
		if call.Form != nil {
			return refuse("a v1 call takes no Form")
		}
		switch call.Method {
		case http.MethodGet, http.MethodDelete:
			if call.JSON != nil {
				return refuse("a v1 " + call.Method + " takes Query, not a JSON body")
			}
			body := map[string]string{"urlEncodedQueryParams": call.Query.Encode()}
			if call.TeamID != "" {
				body["teamId"] = call.TeamID
			}
			b, err := encode(body)
			if err != nil {
				return devPortalRequest{}, "", err
			}
			return devPortalRequest{method: http.MethodPost, override: call.Method, contentType: "application/vnd.api+json", payload: b}, "", nil
		default:
			if call.JSON == nil {
				return refuse("a v1 " + call.Method + " needs a JSON body")
			}
			if len(call.Query) > 0 {
				return refuse("a v1 " + call.Method + " takes no Query")
			}
			b, err := encode(call.JSON)
			if err != nil {
				return devPortalRequest{}, "", err
			}
			return devPortalRequest{method: call.Method, contentType: "application/vnd.api+json", payload: b}, "", nil
		}
	}
	switch call.Method {
	case http.MethodGet:
		if call.JSON != nil || call.Form != nil {
			return refuse("a legacy GET takes no body")
		}
		q := url.Values{}
		for k, v := range call.Query {
			q[k] = v
		}
		if call.TeamID != "" {
			q.Set("teamId", call.TeamID)
		}
		return devPortalRequest{method: http.MethodGet, accept: "application/json, text/plain, */*"}, q.Encode(), nil
	case http.MethodPost:
		if len(call.Query) > 0 {
			return refuse("a legacy POST takes no Query")
		}
		if call.JSON != nil {
			b, err := encode(call.JSON)
			if err != nil {
				return devPortalRequest{}, "", err
			}
			return devPortalRequest{method: http.MethodPost, contentType: "application/json", payload: b}, "", nil
		}
		form := url.Values{}
		for k, v := range call.Form {
			form[k] = v
		}
		if call.TeamID != "" {
			form.Set("teamId", call.TeamID)
		}
		return devPortalRequest{method: http.MethodPost, contentType: "application/x-www-form-urlencoded", payload: []byte(form.Encode())}, "", nil
	default:
		return refuse("a legacy endpoint takes GET or POST")
	}
}

// gate refuses a write unless writes are on and ctx is a write context.
func (c *devSiteClient) gate(ctx context.Context, label string) error {
	if !c.allow.Load() || !tool.IsWrite(ctx) {
		return fmt.Errorf("app: developer site: %s: %w", label, tool.ErrWritesDisabled)
	}
	return nil
}

// API implements [tool.DevSite].
func (c *devSiteClient) API(ctx context.Context, call tool.DevSiteCall) (tool.DevSiteAnswer, error) {
	r, rawQuery, err := shapeDevSite(call)
	if err != nil {
		return tool.DevSiteAnswer{}, err
	}
	label := call.Method + " " + tool.SanitizeText(call.Path)
	r.write = !tool.DevSiteIsRead(call.Method, call.Path)
	r.referer = c.d.origin + "/account/resources/"
	if r.write {
		if err := c.gate(ctx, label); err != nil {
			return tool.DevSiteAnswer{}, err
		}
		if !c.d.hasCSRF() {
			// Priming failing is not fatal: the write goes without a pair.
			prime := devPortalRequest{method: http.MethodPost, referer: r.referer}
			_, _, _, _ = c.d.send(ctx, prime, c.d.p.base+devSitePrimePath)
			if err := ctx.Err(); err != nil {
				return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: %w", label, err)
			}
		}
	}
	u := c.d.p.base + call.Path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	download := r.method == http.MethodGet
	stage := 0 // 0: first try, 1: after bootstrap, 2: after Expired
	retries := 0
	for {
		status, header, data, err := c.d.send(ctx, r, u)
		if err != nil {
			var sent sentError
			if r.write && errors.As(err, &sent) {
				return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: %w (%s)", label, tool.ErrOutcomeUnknown, tool.SanitizeText(err.Error()))
			}
			return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: %w", label, err)
		}
		if r.write && devSiteAmbiguousWrite(download, status, header, data) {
			// Apple may have applied it: the call is not sent again.
			return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: Apple answered %d with a non-JSON body and the call was not repeated: %w", label, status, tool.ErrOutcomeUnknown)
		}
		expired := devPortalExpired(status, header, data)
		if download {
			expired = devSiteDownloadExpired(status, header, data)
		}
		if expired {
			switch stage {
			case 0:
				if err := c.d.bootstrap(ctx); err != nil && ctx.Err() != nil {
					return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: %w", label, ctx.Err())
				}
			case 1:
				c.d.p.src.Expired()
			default:
				return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: session expired: %w", label, tool.ErrWebSessionRequired)
			}
			stage++
			continue
		}
		if status >= 200 && status < 300 {
			if pe := legacyResultError(status, data); pe != nil {
				return tool.DevSiteAnswer{}, pe
			}
			return tool.DevSiteAnswer{Status: status, ContentType: header.Get("Content-Type"), URL: u, Body: data}, nil
		}
		if r.write {
			// Writes are never replayed: a 5xx may have applied them.
			if status >= 500 {
				return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: developer site %d: %w", label, status, tool.ErrOutcomeUnknown)
			}
		} else if wait, ok := c.d.p.retryWait(status, header, retries); ok {
			retries++
			if err := c.d.p.sleep(ctx, wait); err != nil {
				return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: %w", label, err)
			}
			continue
		}
		return tool.DevSiteAnswer{}, portalErrorFrom(status, data)
	}
}

// devSiteAmbiguousWrite reports a write's 2xx answer that does not say
// whether Apple applied it: a non-empty non-JSON body, or for a download
// (whose non-JSON body is the file) an HTML page or "session has expired".
func devSiteAmbiguousWrite(download bool, status int, header http.Header, data []byte) bool {
	if status < 200 || status >= 300 || len(bytes.TrimSpace(data)) == 0 {
		return false
	}
	if download {
		return !json.Valid(data) && devSiteDownloadExpired(status, header, data)
	}
	return !isJSONAnswer(header.Get("Content-Type"), data)
}

// devSiteDownloadExpired is devPortalExpired for a legacy GET download: a
// non-JSON 2xx body (text, PEM, binary) is the download, unless it is an
// HTML page or a short body saying "session has expired".
func devSiteDownloadExpired(status int, header http.Header, data []byte) bool {
	if status < 200 || status >= 300 || json.Valid(data) {
		return devPortalExpired(status, header, data)
	}
	if devSiteHTML(header.Get("Content-Type"), data) {
		return true
	}
	return len(data) <= devSiteExpiredSniff && bytes.Contains(bytes.ToLower(data), []byte("session has expired"))
}

// devSiteHTML reports an HTML answer: its media type says so, or its body
// (byte-order mark and leading space dropped) opens with an HTML doctype or
// <html.
func devSiteHTML(contentType string, data []byte) bool {
	if mt, _, err := mime.ParseMediaType(contentType); err == nil && (mt == "text/html" || mt == "application/xhtml+xml") {
		return true
	}
	head := bytes.TrimPrefix(data[:min(len(data), 512)], []byte("\xef\xbb\xbf"))
	head = bytes.ToLower(bytes.TrimSpace(head))
	return bytes.HasPrefix(head, []byte("<!doctype html")) || bytes.HasPrefix(head, []byte("<html"))
}

// devSiteSegmentsOK reports whether every segment of p is made of
// unreserved URL characters only (letters, digits, ".", "_", "~", "-") and
// is neither empty, "." nor "..".
func devSiteSegmentsOK(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for i := range len(seg) {
			switch b := seg[i]; {
			case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
			case b == '.', b == '_', b == '~', b == '-':
			default:
				return false
			}
		}
	}
	return true
}

// devSiteAPIPath checks a services-account path: clean, relative, every
// segment allowed by devSiteSegmentsOK.
func devSiteAPIPath(p string) error {
	if !devPortalPathOK(p) || !devSiteSegmentsOK(p) {
		return fmt.Errorf("app: developer site: path %s not allowed", tool.SanitizeText(p))
	}
	return nil
}

// devSitePagePath checks a page or form path: clean, relative, every
// segment allowed by devSiteSegmentsOK, first segment in
// tool.DevSitePageRoots.
func devSitePagePath(p string) error {
	root, _, _ := strings.Cut(p, "/")
	if !devPortalPathOK(p) || !devSiteSegmentsOK(p) || !slices.Contains(tool.DevSitePageRoots, root) {
		return fmt.Errorf("app: developer site: page %s not allowed (it must start with %s)", tool.SanitizeText(p), strings.Join(tool.DevSitePageRoots, " or "))
	}
	return nil
}

// do runs one request through the shared transport, bounded by the
// shared concurrency limit.
func (c *devSiteClient) do(ctx context.Context, req *http.Request) (int, http.Header, []byte, error) {
	select {
	case c.d.p.sem <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}
	defer func() { <-c.d.p.sem }()
	if err := c.d.p.src.Authorize(ctx, req); err != nil {
		return 0, nil, nil, err
	}
	return c.d.exchange(ctx, req)
}

// Page implements [tool.DevSite].
func (c *devSiteClient) Page(ctx context.Context, path string, query url.Values) (tool.DevSiteAnswer, error) {
	if err := devSitePagePath(path); err != nil {
		return tool.DevSiteAnswer{}, err
	}
	u := c.d.origin + "/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	label := "page " + tool.SanitizeText(path)
	for try := 0; ; try++ {
		ans, signIn, err := c.fetchPage(ctx, u)
		if err != nil {
			return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: %w", label, err)
		}
		if !signIn {
			return ans, nil
		}
		if try > 0 {
			return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: ended on the sign-in page: %w", label, tool.ErrWebSessionRequired)
		}
		c.d.p.src.Expired()
	}
}

// fetchPage GETs u, following at most devPortalMaxHops redirects by hand,
// and reports whether the answer means "no session": it ended on the
// sign-in host or was a 401. A hop that stays on the developer site must
// be a page path (devSitePagePath, one trailing slash allowed); any other
// ends the read with an error. A 403 is a permission error, never "no
// session".
func (c *devSiteClient) fetchPage(ctx context.Context, u string) (tool.DevSiteAnswer, bool, error) {
	next := u
	for hop := 0; ; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, http.NoBody)
		if err != nil {
			return tool.DevSiteAnswer{}, false, err
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("User-Agent", c.d.p.ua)
		status, header, data, err := c.do(ctx, req)
		if err != nil {
			return tool.DevSiteAnswer{}, false, err
		}
		if status >= 300 && status < 400 && header.Get("Location") != "" {
			if hop >= devPortalMaxHops {
				return tool.DevSiteAnswer{}, false, errors.New("too many redirects")
			}
			loc, err := devPortalHop(req.URL, header.Get("Location"))
			if err != nil {
				return tool.DevSiteAnswer{}, false, err
			}
			if loc.Hostname() != devSiteSignInHost {
				p := strings.TrimPrefix(loc.EscapedPath(), "/")
				if devSitePagePath(strings.TrimSuffix(p, "/")) != nil {
					return tool.DevSiteAnswer{}, false, fmt.Errorf("redirect to %s refused: pages are followed only under %s", tool.SanitizeText(loc.Path), strings.Join(tool.DevSitePageRoots, "/ and ")+"/")
				}
			}
			next = loc.String()
			continue
		}
		if req.URL.Hostname() == devSiteSignInHost || status == http.StatusUnauthorized {
			return tool.DevSiteAnswer{}, true, nil
		}
		if status == http.StatusForbidden {
			return tool.DevSiteAnswer{}, false, fmt.Errorf("the page was refused: %w", portalErrorFrom(status, data))
		}
		if len(data) > devSitePageMax {
			return tool.DevSiteAnswer{}, false, fmt.Errorf("page exceeds %d MiB", devSitePageMax>>20)
		}
		return tool.DevSiteAnswer{Status: status, ContentType: header.Get("Content-Type"), URL: req.URL.String(), Body: data}, false, nil
	}
}

// devSiteContentTypes are the body kinds a Submit may send.
var devSiteContentTypes = []string{"application/x-www-form-urlencoded", "multipart/form-data", "application/json"}

// Submit implements [tool.DevSite].
func (c *devSiteClient) Submit(ctx context.Context, post tool.DevSitePost) (tool.DevSiteAnswer, error) {
	if err := devSitePagePath(post.Path); err != nil {
		return tool.DevSiteAnswer{}, err
	}
	referer := post.Referer
	if referer == "" {
		referer = post.Path
	}
	if err := devSitePagePath(referer); err != nil {
		return tool.DevSiteAnswer{}, fmt.Errorf("referer: %w", err)
	}
	label := "submit " + tool.SanitizeText(post.Path)
	mt, params, err := mime.ParseMediaType(post.ContentType)
	if err != nil || !slices.Contains(devSiteContentTypes, mt) {
		return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: content type must be %s", label, strings.Join(devSiteContentTypes, ", "))
	}
	if mt == "multipart/form-data" && params["boundary"] == "" {
		return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: multipart/form-data needs a boundary", label)
	}
	allowed := make(map[string]bool, len(tool.DevSitePostHeaders))
	for _, h := range tool.DevSitePostHeaders {
		allowed[http.CanonicalHeaderKey(h)] = true
	}
	for name, value := range post.Headers {
		if !allowed[http.CanonicalHeaderKey(name)] {
			return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: header %s not allowed (allowed: %s)", label, tool.SanitizeText(name), strings.Join(tool.DevSitePostHeaders, ", "))
		}
		if strings.ContainsAny(value, "\r\n") {
			return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: header %s has a line break", label, tool.SanitizeText(name))
		}
	}
	if err := c.gate(ctx, label); err != nil {
		return tool.DevSiteAnswer{}, err
	}
	u := c.d.origin + "/" + post.Path
	if len(post.Query) > 0 {
		u += "?" + post.Query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(post.Body))
	if err != nil {
		return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: %w", label, err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json")
	req.Header.Set("User-Agent", c.d.p.ua)
	req.Header.Set("Content-Type", post.ContentType)
	req.Header.Set("Origin", c.d.origin)
	ref := c.d.origin + "/" + referer
	if len(post.RefererQuery) > 0 {
		ref += "?" + post.RefererQuery.Encode()
	}
	req.Header.Set("Referer", ref)
	c.d.mu.Lock()
	if c.d.csrf != "" {
		req.Header.Set("csrf", c.d.csrf)
		req.Header.Set("csrf_ts", c.d.csrfTS)
	}
	c.d.mu.Unlock()
	for name, value := range post.Headers {
		req.Header.Set(name, value)
	}
	status, header, data, err := c.do(ctx, req)
	if err != nil {
		var sent sentError
		if errors.As(err, &sent) {
			return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: %w (%s)", label, tool.ErrOutcomeUnknown, tool.SanitizeText(err.Error()))
		}
		return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: %w", label, err)
	}
	ans := tool.DevSiteAnswer{Status: status, ContentType: header.Get("Content-Type"), URL: u, Body: data}
	switch {
	case status >= 300 && status < 400:
		if raw := header.Get("Location"); raw != "" {
			loc, err := req.URL.Parse(raw)
			if err != nil {
				return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: redirect with a bad Location", label)
			}
			if loc.Hostname() == devSiteSignInHost {
				c.d.p.src.Expired()
				return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: redirected to sign-in, the form was not accepted: %w", label, tool.ErrWebSessionRequired)
			}
			ans.Location = loc.String()
		}
	case status == http.StatusUnauthorized:
		c.d.p.src.Expired()
		return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: session rejected, the form was not accepted: %w", label, tool.ErrWebSessionRequired)
	case status >= 500:
		return tool.DevSiteAnswer{}, fmt.Errorf("app: developer site: %s: developer site %d: %w", label, status, tool.ErrOutcomeUnknown)
	}
	return ans, nil
}

var _ tool.DevSite = (*devSiteClient)(nil)
