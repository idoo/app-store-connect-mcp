package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/idoo/app-store-connect-mcp/internal/asc"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	// keyPortalBaseURL is the endpoint Xcode itself uses with an API key:
	// the developer portal's v1 JSON:API, authenticated by the same bearer
	// JWT as the public API.
	keyPortalBaseURL = "https://appstoreconnect.apple.com/xcbuild/"
	// keyPortalBundleIDs is the collection the key may read and whose
	// members it may read and PATCH.
	keyPortalBundleIDs = "v1/bundleIds"
	// keyPortalCapabilities is the capability catalog the key may read.
	keyPortalCapabilities = "v1/capabilities"
	// keyPortalXcodeVersion is sent as X-Xcode-Version on catalog reads
	// only. Apple refuses the catalog without a version (403) and filters
	// it by the version given; one above every Xcode release lists every
	// capability.
	keyPortalXcodeVersion = "99.0"
	// keyPortalRedacted replaces the bearer token should Apple echo it.
	keyPortalRedacted = "[redacted]"
)

// errKeyRefused marks a 401 or 403 answer to the API key: Apple applied
// nothing, so the same call may go to the web session.
var errKeyRefused = errors.New("the API key was refused")

// keyRefusedError is Apple's 401/403 answer to the key. It matches
// errKeyRefused and unwraps to the *tool.PortalError.
type keyRefusedError struct{ pe *tool.PortalError }

func (e *keyRefusedError) Error() string        { return e.pe.Error() }
func (e *keyRefusedError) Unwrap() error        { return e.pe }
func (e *keyRefusedError) Is(target error) bool { return target == errKeyRefused }

// refusal says in words what Apple answered the key: the status and
// Apple's sanitized message.
func (e *keyRefusedError) refusal() string {
	if len(e.pe.Messages) == 0 {
		return fmt.Sprintf("Apple refused the API key for this call (HTTP %d)", e.pe.Status)
	}
	return fmt.Sprintf("Apple refused the API key for this call (HTTP %d: %s)", e.pe.Status, tool.SanitizeText(strings.Join(e.pe.Messages, "; ")))
}

// keyPortalServes reports whether the key transport sends method+path:
// GET v1/bundleIds, GET v1/bundleIds/{id}, GET v1/capabilities and
// PATCH v1/bundleIds/{id}. Everything else is the web session's.
func keyPortalServes(method, path string) bool {
	if !devPortalPathOK(path) {
		return false
	}
	id, member := strings.CutPrefix(path, keyPortalBundleIDs+"/")
	member = member && id != "" && !strings.Contains(id, "/")
	switch method {
	case http.MethodGet:
		return member || path == keyPortalBundleIDs || path == keyPortalCapabilities
	case http.MethodPatch:
		return member
	}
	return false
}

// keyPortalClient is [tool.DevPortal] over keyPortalBaseURL with the team
// API key's bearer token: no cookies, no csrf, no redirects followed. Reads
// go as POST + X-HTTP-Method-Override: GET like the web-session client's.
// A 401 or 403 answer matches errKeyRefused; the token never reaches an
// error.
type keyPortalClient struct {
	p      *portalClient // transport, semaphore, retry policy, user agent (no session source)
	tokens asc.TokenSource
	allow  atomic.Bool // writes allowed (env SetWrites)
}

// newKeyPortalClient serves base (keyPortalBaseURL outside tests) with
// tokens, the public API client's own token source.
func newKeyPortalClient(tokens asc.TokenSource, base, version string) *keyPortalClient {
	p := newPortalClient(nil, base, version)
	p.base = strings.TrimSuffix(base, "/") + "/"
	return &keyPortalClient{p: p, tokens: tokens}
}

// SetAllowWrites turns the PATCH on or off.
func (c *keyPortalClient) SetAllowWrites(on bool) { c.allow.Store(on) }

// Do implements [tool.DevPortal] for the calls keyPortalServes allows.
func (c *keyPortalClient) Do(ctx context.Context, method, path string, body, out any) error {
	if !keyPortalServes(method, path) || body == nil {
		return fmt.Errorf("app: developer portal (API key): %s %s not allowed", tool.SanitizeText(method), tool.SanitizeText(path))
	}
	label := method + " " + tool.SanitizeText(path)
	write := method != http.MethodGet
	if write && (!c.allow.Load() || !tool.IsWrite(ctx)) {
		return fmt.Errorf("app: developer portal (API key): %s: %w", label, tool.ErrWritesDisabled)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("app: developer portal (API key): %s: encode body: %w", label, err)
	}
	retries := 0
	for {
		status, header, data, err := c.send(ctx, method, path, payload)
		if err != nil {
			return fmt.Errorf("app: developer portal (API key): %s: %w", label, err)
		}
		if status >= 200 && status < 300 {
			if out == nil || len(bytes.TrimSpace(data)) == 0 {
				return nil
			}
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("app: developer portal (API key): %s: decode: %w", label, err)
			}
			return nil
		}
		// Writes are never replayed: a 5xx may have applied them.
		if !write {
			if wait, ok := c.p.retryWait(status, header, retries); ok {
				retries++
				if err := c.p.sleep(ctx, wait); err != nil {
					return fmt.Errorf("app: developer portal (API key): %s: %w", label, err)
				}
				continue
			}
		}
		pe := portalErrorFrom(status, data)
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return &keyRefusedError{pe: pe}
		}
		return pe
	}
}

// send makes one attempt and returns the status, headers and bounded body
// with the bearer token blanked out of it.
func (c *keyPortalClient) send(ctx context.Context, method, path string, payload []byte) (int, http.Header, []byte, error) {
	select {
	case c.p.sem <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}
	defer func() { <-c.p.sem }()
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("sign token: %s", asc.Redact(err.Error()))
	}
	rctx, cancel := context.WithTimeout(ctx, asc.DefaultRequestTimeout)
	defer cancel()
	wire := method
	if method == http.MethodGet {
		wire = http.MethodPost
	}
	req, err := http.NewRequestWithContext(rctx, wire, c.p.base+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/vnd.api+json")
	req.Header.Set("User-Agent", c.p.ua)
	if method == http.MethodGet {
		req.Header.Set("X-HTTP-Method-Override", http.MethodGet)
	}
	if path == keyPortalCapabilities {
		req.Header.Set("X-Xcode-Version", keyPortalXcodeVersion)
	}
	resp, err := c.p.hc.Do(req)
	if err != nil {
		return 0, nil, nil, errors.New(strings.ReplaceAll(err.Error(), tok, keyPortalRedacted))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, portalMaxBody+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read body: %s", strings.ReplaceAll(err.Error(), tok, keyPortalRedacted))
	}
	if len(data) > portalMaxBody {
		return 0, nil, nil, fmt.Errorf("response exceeds %d MiB", portalMaxBody>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data = bytes.ReplaceAll(data, []byte(tok), []byte(keyPortalRedacted))
	}
	return resp.StatusCode, resp.Header, data, nil
}

// devPortalRouter is the env's [tool.DevPortal]: the API key first, the
// human's web session where Apple refuses the key (401/403, nothing
// applied) or the key transport does not serve the call. Any other key
// answer is final, so a write is never sent twice.
type devPortalRouter struct {
	key *keyPortalClient
	web tool.DevPortal // nil without a web session source
}

// SetAllowWrites turns writes on or off on both clients.
func (r *devPortalRouter) SetAllowWrites(on bool) {
	r.key.SetAllowWrites(on)
	if w, ok := r.web.(interface{ SetAllowWrites(bool) }); ok {
		w.SetAllowWrites(on)
	}
}

// Do implements [tool.DevPortal].
func (r *devPortalRouter) Do(ctx context.Context, method, path string, body, out any) error {
	label := tool.SanitizeText(method) + " " + tool.SanitizeText(path)
	var refused *keyRefusedError
	if keyPortalServes(method, path) && body != nil {
		err := r.key.Do(ctx, method, path, body, out)
		if !errors.As(err, &refused) {
			return err
		}
	}
	if r.web == nil {
		if refused != nil {
			return fmt.Errorf("app: developer portal: %s: %s and no web session is available: %w", label, refused.refusal(), tool.ErrWebSessionRequired)
		}
		if _, err := shapeDevPortal(method, path, body); err != nil {
			return err
		}
		return fmt.Errorf("app: developer portal: %s: Apple does not serve this call to an API key: %w", label, tool.ErrWebSessionRequired)
	}
	err := r.web.Do(ctx, method, path, body, out)
	if refused != nil && errors.Is(err, tool.ErrWebSessionRequired) {
		return fmt.Errorf("app: developer portal: %s: %s: %w", label, refused.refusal(), err)
	}
	return err
}

var (
	_ tool.DevPortal = (*keyPortalClient)(nil)
	_ tool.DevPortal = (*devPortalRouter)(nil)
)
