package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/asc"
	"github.com/idoo/app-store-connect-mcp/internal/ascrules"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	// portalBaseURL hosts App Store Connect's web-only JSON endpoints.
	portalBaseURL = "https://appstoreconnect.apple.com"
	// portalMaxBody caps one portal response body.
	portalMaxBody = 32 << 20
	// portalMaxConcurrent bounds in-flight requests (dashboard fan-out).
	portalMaxConcurrent = 4
	// portalMaxRetries is the retry budget for 429 and 502/503/504.
	portalMaxRetries = 2
	// portalBackoff is the first retry wait; it doubles per retry.
	portalBackoff = 500 * time.Millisecond
	// portalSessionPath is the one allowed olympus endpoint.
	portalSessionPath = "/olympus/v1/session"
)

// portalPrefixes are the path prefixes a portal request may use.
var portalPrefixes = []string{
	"/analytics/api/v1/",
	"/WebObjects/iTunesConnect.woa/ra/paymentConsolidation/",
	portalCIPrefix,
	tool.ComplianceFormsPath,
}

// portalCIPrefix is the Xcode Cloud web API. Only GETs reach it, except the
// one PUT matched by portalCIPut.
const portalCIPrefix = "/ci/api/teams/"

// portalCIPut is a portal write: setting an Xcode Cloud product's next
// build number (team and product are UUIDs).
var portalCIPut = regexp.MustCompile(`^/ci/api/teams/[0-9A-Fa-f-]{36}/products/[0-9A-Fa-f-]{36}/next-build-number\?next_build_number=[1-9][0-9]{0,9}$`)

// portalFormWrite is the other portal write: saving one compliance form of
// an app (POST the first answer, PUT a changed one). Everything else under
// tool.ComplianceFormsPath is GET only.
var portalFormWrite = regexp.MustCompile(`^/ppm/complianceform/v1/accounts/[0-9A-Fa-f-]{36}/contents/[0-9]{1,20}/requirements/[A-Za-z0-9_-]{1,64}/forms$`)

// portalFormApp finds the app id in a compliance form path.
var portalFormApp = regexp.MustCompile(`(?:[?&]contentId=|/contents/)([0-9]{1,20})(?:[/&]|$)`)

// portalClient is [tool.Portal] over the human's web session. It shares the
// env's session source with the iris client, never follows redirects and
// never puts cookie values in errors.
type portalClient struct {
	base  string
	src   asc.SessionAuth
	hc    *http.Client
	ua    string
	sem   chan struct{}
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	allow atomic.Bool // writes allowed (env SetWrites)
}

func newPortalClient(src asc.SessionAuth, base, version string) *portalClient {
	return &portalClient{
		base: strings.TrimSuffix(base, "/"),
		src:  src,
		hc: &http.Client{
			Timeout: asc.DefaultRequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		ua:    "app-store-connect-mcp/" + version,
		sem:   make(chan struct{}, portalMaxConcurrent),
		now:   time.Now,
		sleep: sleepCtx,
	}
}

// SetAllowWrites turns the portal writes (portalCIPut, portalFormWrite) on
// or off.
func (c *portalClient) SetAllowWrites(on bool) { c.allow.Store(on) }

// portalPathAllowed reports whether p (query allowed) is an allowed portal
// endpoint: one of portalPrefixes or exactly portalSessionPath, with no
// traversal, escapes or fragment in the path part.
func portalPathAllowed(p string) bool {
	if strings.ContainsAny(p, "#\\") {
		return false
	}
	pathPart, _, _ := strings.Cut(p, "?")
	if strings.Contains(pathPart, "..") || strings.Contains(pathPart, "//") || strings.Contains(pathPart, "%") {
		return false
	}
	if pathPart == portalSessionPath {
		return true
	}
	for _, prefix := range portalPrefixes {
		if strings.HasPrefix(pathPart, prefix) && len(pathPart) > len(prefix) {
			return true
		}
	}
	return false
}

// JSON implements [tool.Portal]. GET and POST are reads (POST never on the
// Xcode Cloud API or the compliance forms); the writes are portalCIPut and
// portalFormWrite, which need writes allowed plus a write-enabled call. A
// form write is never replayed: a 5xx or a lost answer is
// [tool.ErrOutcomeUnknown].
func (c *portalClient) JSON(ctx context.Context, method, path string, body, out any) error {
	if !portalPathAllowed(path) {
		return fmt.Errorf("app: web portal: path %s not allowed", tool.SanitizeText(path))
	}
	ci := strings.HasPrefix(path, portalCIPrefix)
	form := strings.HasPrefix(path, tool.ComplianceFormsPath)
	formWrite := false
	switch {
	case method == http.MethodGet, method == http.MethodPost && !ci && !form:
	case method == http.MethodPut && body == nil && portalCIPut.MatchString(path),
		(method == http.MethodPost || method == http.MethodPut) && body != nil && portalFormWrite.MatchString(path):
		if !c.allow.Load() || !tool.IsWrite(ctx) {
			return fmt.Errorf("app: web portal: %s %s: %w", method, tool.SanitizeText(path), tool.ErrWritesDisabled)
		}
		formWrite = form
	default:
		return fmt.Errorf("app: web portal: %s %s not allowed", tool.SanitizeText(method), tool.SanitizeText(path))
	}
	label := method + " " + path
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("app: web portal: %s: encode body: %w", label, err)
		}
	}
	renewed := false
	retries := 0
	for {
		status, header, data, err := c.do(ctx, method, path, payload)
		if err != nil {
			var sent sentError
			if formWrite && errors.As(err, &sent) {
				return fmt.Errorf("app: web portal: %s: %w (%v)", label, tool.ErrOutcomeUnknown, err)
			}
			return fmt.Errorf("app: web portal: %s: %w", label, err)
		}
		switch {
		case status == http.StatusUnauthorized:
			if renewed {
				return fmt.Errorf("app: web portal: %s: session rejected after renewal: %w", label, tool.ErrWebSessionRequired)
			}
			renewed = true
			c.src.Expired()
			continue
		case status >= 300 && status < 400:
			return fmt.Errorf("app: web portal: %s: redirected (%d) to sign-in: %w", label, status, tool.ErrWebSessionRequired)
		case status >= 200 && status < 300:
			if len(bytes.TrimSpace(data)) == 0 {
				return nil
			}
			if !isJSONAnswer(header.Get("Content-Type"), data) {
				return fmt.Errorf("app: web portal: %s: answered with a sign-in page: %w", label, tool.ErrWebSessionRequired)
			}
			if out != nil {
				if err := json.Unmarshal(data, out); err != nil {
					return fmt.Errorf("app: web portal: %s: decode: %w", label, err)
				}
			}
			return nil
		case formWrite && status >= 500:
			return fmt.Errorf("app: web portal: %s: App Store Connect web %d: %w", label, status, tool.ErrOutcomeUnknown)
		}
		if wait, ok := c.retryWait(status, header, retries); ok {
			retries++
			if err := c.sleep(ctx, wait); err != nil {
				return fmt.Errorf("app: web portal: %s: %w", label, err)
			}
			continue
		}
		return portalErrorFrom(status, data)
	}
}

// do sends one attempt and returns the status, headers and bounded body.
func (c *portalClient) do(ctx context.Context, method, path string, payload []byte) (int, http.Header, []byte, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}
	defer func() { <-c.sem }()
	rd := io.Reader(http.NoBody)
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	// Authorize may renew the session (a headless browser run longer than
	// one request), so only the exchange itself gets the request timeout.
	if err := c.src.Authorize(ctx, req); err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("X-Requested-By", "appstoreconnect.apple.com")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.HasPrefix(path, tool.ComplianceFormsPath) {
		c.formHeaders(req, path)
	}
	rctx, cancel := context.WithTimeout(ctx, asc.DefaultRequestTimeout)
	defer cancel()
	resp, err := c.hc.Do(req.WithContext(rctx))
	if err != nil {
		return 0, nil, nil, sentError{err}
	}
	defer func() { _ = resp.Body.Close() }()
	c.src.Observe(resp)
	data, err := io.ReadAll(io.LimitReader(resp.Body, portalMaxBody+1))
	if err != nil {
		return 0, nil, nil, sentError{fmt.Errorf("read body: %w", err)}
	}
	if len(data) > portalMaxBody {
		return 0, nil, nil, sentError{fmt.Errorf("response exceeds %d MiB", portalMaxBody>>20)}
	}
	return resp.StatusCode, resp.Header, data, nil
}

// sentError marks a failure after the request may have reached Apple, so a
// write's outcome is unknown.
type sentError struct{ error }

func (e sentError) Unwrap() error { return e.error }

// formHeaders adds what App Store Connect's own page sends to the
// compliance form service: without the CSRF header a write is answered
// with an empty 403.
func (c *portalClient) formHeaders(req *http.Request, path string) {
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Csrf-Itc", "itc")
	req.Header.Set("Origin", c.base)
	referer := c.base + "/apps"
	if m := portalFormApp.FindStringSubmatch(path); m != nil {
		referer += "/" + m[1] + "/distribution/info"
	}
	req.Header.Set("Referer", referer)
}

// retryWait reports whether status is retried after retries earlier
// retries, and how long to wait: Retry-After when usable, else exponential
// backoff. A Retry-After beyond ascrules.MaxRetryAfter is not waited out.
func (c *portalClient) retryWait(status int, header http.Header, retries int) (time.Duration, bool) {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return 0, false
	}
	if retries >= portalMaxRetries {
		return 0, false
	}
	wait := portalBackoff << retries
	if ra, ok := ascrules.ParseRetryAfter(header.Get("Retry-After"), c.now()); ok {
		if ra > ascrules.MaxRetryAfter {
			return 0, false
		}
		wait = max(wait, ra)
	}
	return wait, true
}

// isJSONAnswer reports whether a 2xx body is JSON rather than an HTML
// sign-in page.
func isJSONAnswer(contentType string, data []byte) bool {
	if contentType != "" {
		mt, _, err := mime.ParseMediaType(contentType)
		if err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json")) {
			return true
		}
		if err == nil && mt != "text/plain" && mt != "application/octet-stream" {
			return false
		}
	}
	return json.Valid(data)
}

// portalErrorFrom builds the error of a non-2xx answer from Apple's
// errors[].message (detail or title as fallback), else top-level message.
func portalErrorFrom(status int, data []byte) *tool.PortalError {
	var body struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
			Title   string `json:"title"`
		} `json:"errors"`
	}
	pe := &tool.PortalError{Status: status}
	if json.Unmarshal(data, &body) != nil {
		return pe
	}
	for _, e := range body.Errors {
		for _, m := range []string{e.Message, e.Detail, e.Title} {
			if m = strings.TrimSpace(m); m != "" {
				pe.Messages = append(pe.Messages, m)
				break
			}
		}
	}
	if len(pe.Messages) == 0 {
		if m := strings.TrimSpace(body.Message); m != "" {
			pe.Messages = []string{m}
		}
	}
	return pe
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ tool.Portal = (*portalClient)(nil)
