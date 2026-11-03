package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/idoo/app-store-connect-mcp/internal/asc"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	// devPortalBaseURL is the services-account root on the developer site.
	devPortalBaseURL = "https://developer.apple.com/services-account/"
	// devPortalLegacyPrefix holds the legacy App Group .action endpoints.
	devPortalLegacyPrefix = "QH65B2/account/ios/identifiers/"
	// devPortalMaxHops bounds the developer-site bootstrap redirects.
	devPortalMaxHops = 5
	// devPortalAppGroups is the one v1 collection a JSON POST may create in.
	devPortalAppGroups = "v1/appGroups"
)

// devPortalLegacy maps the allowed legacy .action endpoints to whether they
// write.
var devPortalLegacy = map[string]bool{
	"listApplicationGroups.action":         false,
	"addApplicationGroup.action":           true,
	"assignApplicationGroupToAppId.action": true,
}

// devPortalPatchPrefixes are the v1 collections whose members may be PATCHed.
var devPortalPatchPrefixes = []string{"v1/bundleIds/", "v1/bundleIdCapabilities/"}

// devPortalClient is [tool.DevPortal] over the human's web session (the
// same source as the iris and portal clients). It follows no redirects on
// API calls, sends web-session reads as POST + X-HTTP-Method-Override: GET,
// echoes the csrf/csrf_ts headers Apple hands out and never puts cookie
// values in errors.
type devPortalClient struct {
	p      *portalClient // transport, session source, retry policy
	origin string        // scheme://host of base, for the bootstrap hop
	allow  atomic.Bool   // writes allowed (env SetWrites)

	mu      sync.Mutex
	csrf    string
	csrfTS  string
	booting sync.Mutex // one bootstrap at a time
}

// newDevPortalClient serves base (devPortalBaseURL outside tests; it ends
// in "/" so paths are relative) over src.
func newDevPortalClient(src asc.SessionAuth, base, version string) *devPortalClient {
	p := newPortalClient(src, base, version)
	p.base = strings.TrimSuffix(base, "/") + "/"
	origin := ""
	if u, err := url.Parse(base); err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	return &devPortalClient{p: p, origin: origin}
}

// SetAllowWrites turns writes (PATCH, creating POSTs) on or off.
func (c *devPortalClient) SetAllowWrites(on bool) { c.allow.Store(on) }

// devPortalRequest is how one Do call goes on the wire.
type devPortalRequest struct {
	method      string // HTTP method sent
	override    string // X-HTTP-Method-Override, "" for none
	contentType string
	write       bool
	payload     []byte
}

// devPortalPathOK reports whether p is a clean relative path: no query,
// fragment, escapes, traversal, empty segments or leading slash.
func devPortalPathOK(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "?#%\\ ") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// devPortalPatchable reports whether p is v1/bundleIds/{id} or
// v1/bundleIdCapabilities/{id} (one member, nothing below it).
func devPortalPatchable(p string) bool {
	for _, prefix := range devPortalPatchPrefixes {
		if rest, ok := strings.CutPrefix(p, prefix); ok && rest != "" && !strings.Contains(rest, "/") {
			return true
		}
	}
	return false
}

// shapeDevPortal validates method+path against the allowlist and encodes
// body: JSON:API for v1, form for the legacy .action endpoints (body must be
// url.Values).
func shapeDevPortal(method, path string, body any) (devPortalRequest, error) {
	if !devPortalPathOK(path) {
		return devPortalRequest{}, fmt.Errorf("app: developer portal: path %s not allowed", tool.SanitizeText(path))
	}
	refuse := func() (devPortalRequest, error) {
		return devPortalRequest{}, fmt.Errorf("app: developer portal: %s %s not allowed", tool.SanitizeText(method), tool.SanitizeText(path))
	}
	v1 := strings.HasPrefix(path, "v1/")
	var r devPortalRequest
	switch method {
	case http.MethodGet:
		if !v1 || body == nil {
			return refuse()
		}
		r = devPortalRequest{method: http.MethodPost, override: http.MethodGet, contentType: "application/vnd.api+json"}
	case http.MethodPatch:
		if !devPortalPatchable(path) {
			return refuse()
		}
		r = devPortalRequest{method: http.MethodPatch, contentType: "application/vnd.api+json", write: true}
	case http.MethodPost:
		name, legacy := strings.CutPrefix(path, devPortalLegacyPrefix)
		write, known := devPortalLegacy[name]
		switch {
		case path == devPortalAppGroups:
			r = devPortalRequest{method: http.MethodPost, contentType: "application/vnd.api+json", write: true}
		case legacy && known:
			form, ok := body.(url.Values)
			if !ok {
				return devPortalRequest{}, fmt.Errorf("app: developer portal: %s needs a form body", tool.SanitizeText(path))
			}
			return devPortalRequest{
				method:      http.MethodPost,
				contentType: "application/x-www-form-urlencoded",
				write:       write,
				payload:     []byte(form.Encode()),
			}, nil
		default:
			return refuse()
		}
	default:
		return refuse()
	}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return devPortalRequest{}, fmt.Errorf("app: developer portal: %s %s: encode body: %w", method, tool.SanitizeText(path), err)
		}
		r.payload = b
	}
	return r, nil
}

// Do implements [tool.DevPortal].
func (c *devPortalClient) Do(ctx context.Context, method, path string, body, out any) error {
	r, err := shapeDevPortal(method, path, body)
	if err != nil {
		return err
	}
	if r.write && (!c.allow.Load() || !tool.IsWrite(ctx)) {
		return fmt.Errorf("app: developer portal: %s %s: %w", method, tool.SanitizeText(path), tool.ErrWritesDisabled)
	}
	label := method + " " + tool.SanitizeText(path)
	stage := 0 // 0: first try, 1: after bootstrap, 2: after Expired
	retries := 0
	for {
		status, header, data, err := c.send(ctx, r, c.p.base+path)
		if err != nil {
			return fmt.Errorf("app: developer portal: %s: %w", label, err)
		}
		if devPortalExpired(status, header, data) {
			switch stage {
			case 0:
				if err := c.bootstrap(ctx); err != nil && ctx.Err() != nil {
					return fmt.Errorf("app: developer portal: %s: %w", label, ctx.Err())
				}
			case 1:
				c.p.src.Expired()
			default:
				return fmt.Errorf("app: developer portal: %s: session expired: %w", label, tool.ErrWebSessionRequired)
			}
			stage++
			continue
		}
		if status >= 200 && status < 300 {
			if len(bytes.TrimSpace(data)) == 0 {
				return nil
			}
			if pe := legacyResultError(status, data); pe != nil {
				return pe
			}
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("app: developer portal: %s: decode: %w", label, err)
			}
			return nil
		}
		// Writes are never replayed: a 5xx may have applied them.
		if !r.write {
			if wait, ok := c.p.retryWait(status, header, retries); ok {
				retries++
				if err := c.p.sleep(ctx, wait); err != nil {
					return fmt.Errorf("app: developer portal: %s: %w", label, err)
				}
				continue
			}
		}
		return portalErrorFrom(status, data)
	}
}

// devPortalExpired reports an answer meaning "no valid session": 401, any
// errorCode 1100, a redirect (to the sign-in page), a non-JSON body saying
// "session has expired" (the legacy endpoints answer 200 text/plain "Your
// session has expired. Please log in."), or any other non-JSON 2xx (an HTML
// sign-in page).
func devPortalExpired(status int, header http.Header, data []byte) bool {
	switch {
	case status == http.StatusUnauthorized:
		return true
	case status >= 300 && status < 400:
		return true
	}
	if json.Valid(data) {
		var e struct {
			ErrorCode json.Number `json:"errorCode"`
		}
		return json.Unmarshal(data, &e) == nil && e.ErrorCode.String() == "1100"
	}
	if strings.Contains(strings.ToLower(string(data)), "session has expired") {
		return true
	}
	return status >= 200 && status < 300 && len(bytes.TrimSpace(data)) > 0 && !isJSONAnswer(header.Get("Content-Type"), data)
}

// legacyResultError turns a legacy .action 2xx answer with a nonzero
// resultCode (Apple's in-band failure) into a *tool.PortalError carrying
// userString (or resultString). nil for success and for v1 answers.
func legacyResultError(status int, data []byte) *tool.PortalError {
	var r struct {
		ResultCode   *int   `json:"resultCode"`
		UserString   string `json:"userString"`
		ResultString string `json:"resultString"`
	}
	if json.Unmarshal(data, &r) != nil || r.ResultCode == nil || *r.ResultCode == 0 {
		return nil
	}
	pe := &tool.PortalError{Status: status}
	for _, m := range []string{r.UserString, r.ResultString} {
		if m = strings.TrimSpace(m); m != "" {
			pe.Messages = []string{m}
			break
		}
	}
	return pe
}

// send makes one API attempt: session cookies, csrf echo, override header.
func (c *devPortalClient) send(ctx context.Context, r devPortalRequest, u string) (int, http.Header, []byte, error) {
	select {
	case c.p.sem <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}
	defer func() { <-c.p.sem }()
	rd := io.Reader(http.NoBody)
	if r.payload != nil {
		rd = bytes.NewReader(r.payload)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	if err := c.p.src.Authorize(ctx, req); err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.p.ua)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if r.payload != nil {
		req.Header.Set("Content-Type", r.contentType)
	}
	if r.override != "" {
		req.Header.Set("X-HTTP-Method-Override", r.override)
	}
	c.mu.Lock()
	if c.csrf != "" {
		req.Header.Set("csrf", c.csrf)
		req.Header.Set("csrf_ts", c.csrfTS)
	}
	c.mu.Unlock()
	return c.exchange(ctx, req)
}

// exchange runs req without following redirects, keeps rotated cookies and
// csrf tokens, and returns the bounded body.
func (c *devPortalClient) exchange(ctx context.Context, req *http.Request) (int, http.Header, []byte, error) {
	rctx, cancel := context.WithTimeout(ctx, asc.DefaultRequestTimeout)
	defer cancel()
	resp, err := c.p.hc.Do(req.WithContext(rctx))
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	c.p.src.Observe(resp)
	if t := resp.Header.Get("csrf"); t != "" {
		c.mu.Lock()
		c.csrf, c.csrfTS = t, resp.Header.Get("csrf_ts")
		c.mu.Unlock()
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, portalMaxBody+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read body: %w", err)
	}
	if len(data) > portalMaxBody {
		return 0, nil, nil, fmt.Errorf("response exceeds %d MiB", portalMaxBody>>20)
	}
	return resp.StatusCode, resp.Header, data, nil
}

// bootstrap visits the developer site's /account/ page with the session so
// Apple can mint developer.apple.com cookies from the shared Apple ID
// sign-in. Redirects are followed by hand (each hop's Set-Cookie is kept)
// only within the developer site and idmsa.apple.com.
func (c *devPortalClient) bootstrap(ctx context.Context) error {
	c.booting.Lock()
	defer c.booting.Unlock()
	next := c.origin + "/account/"
	for range devPortalMaxHops {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, http.NoBody)
		if err != nil {
			return err
		}
		if err := c.p.src.Authorize(ctx, req); err != nil {
			return err
		}
		req.Header.Set("User-Agent", c.p.ua)
		req.Header.Set("Accept", "text/html")
		status, header, _, err := c.exchange(ctx, req)
		if err != nil {
			return err
		}
		if status < 300 || status >= 400 {
			return nil
		}
		loc, err := req.URL.Parse(header.Get("Location"))
		if err != nil || (loc.Scheme != "https" && loc.Scheme != req.URL.Scheme) {
			return errors.New("bootstrap: bad redirect")
		}
		if host := loc.Hostname(); host != req.URL.Hostname() && host != "developer.apple.com" && host != "idmsa.apple.com" {
			return errors.New("bootstrap: redirect off the developer site")
		}
		next = loc.String()
	}
	return errors.New("bootstrap: too many redirects")
}

var _ tool.DevPortal = (*devPortalClient)(nil)
