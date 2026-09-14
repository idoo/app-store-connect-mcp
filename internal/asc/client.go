package asc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/idoo/hamn/internal/ascrules"
	"github.com/idoo/hamn/internal/tool"
)

// Client is the App Store Connect HTTP client. It implements [tool.ASC]:
// JWT auth with one refresh on 401, the retry table of [retryPolicy], a
// read-only guard, rate-limit and clock-skew tracking, JSON:API decoding
// with untrusted-text wrapping, and redacted, hinted errors. It is safe for
// concurrent use.
type Client struct {
	base        *url.URL
	tokens      TokenSource
	http        *http.Client
	retry       retryPolicy
	sem         chan struct{}
	allowWrites atomic.Bool
	spec        SpecHints
	freeText    func(string) []string
	clock       tool.Clock
	userAgent   string

	rate      atomic.Pointer[tool.RateLimit]
	skew      atomic.Int64
	skewKnown atomic.Bool
}

var _ tool.ASC = (*Client)(nil)

// New returns a Client configured by opts.
func New(opts Options) (*Client, error) {
	if opts.Tokens == nil {
		return nil, errors.New("asc: new client: nil token source")
	}
	base := opts.BaseURL
	if base == nil {
		var err error
		if base, err = url.Parse(DefaultBaseURL); err != nil {
			return nil, fmt.Errorf("asc: new client: %w", err)
		}
	}
	if err := checkBaseURL(base); err != nil {
		return nil, fmt.Errorf("asc: new client: %w", err)
	}
	c := &Client{
		base:      base,
		tokens:    opts.Tokens,
		http:      opts.HTTP,
		spec:      opts.Spec,
		freeText:  opts.FreeText,
		clock:     opts.Clock,
		userAgent: opts.UserAgent,
		retry:     retryPolicy{max: opts.MaxRetries, jitter: opts.Jitter},
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: DefaultRequestTimeout}
	}
	if c.clock == nil {
		c.clock = tool.SystemClock{}
	}
	if c.userAgent == "" {
		c.userAgent = DefaultUserAgent
	}
	switch {
	case c.retry.max == 0:
		c.retry.max = DefaultMaxRetries
	case c.retry.max < 0:
		c.retry.max = 0
	}
	if c.retry.jitter == nil {
		c.retry.jitter = defaultJitter
	}
	n := opts.MaxConcurrent
	if n <= 0 {
		n = DefaultMaxConcurrent
	}
	c.sem = make(chan struct{}, n)
	c.allowWrites.Store(opts.AllowWrites)
	return c, nil
}

// SetAllowWrites switches the client's read-only guard at runtime (e.g. when
// the session enables writes). Non-GET requests still need Request.Write and
// a write context.
func (c *Client) SetAllowWrites(on bool) { c.allowWrites.Store(on) }

// checkBaseURL accepts https URLs, or http on a loopback host.
func checkBaseURL(u *url.URL) error {
	if u.Host == "" || u.User != nil {
		return fmt.Errorf("invalid base URL %q", u.Redacted())
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return fmt.Errorf("base URL %q must be https or loopback http", u.Redacted())
}

// rawResponse is one completed HTTP exchange.
type rawResponse struct {
	status int
	header http.Header
	body   []byte
}

// Do implements [tool.ASC]. Non-GET requests need Request.Write, a write
// context (tool.IsWrite) and writes enabled ([Options.AllowWrites], then
// [Client.SetAllowWrites]), otherwise they fail with
// tool.ErrWritesDisabled before anything is sent.
func (c *Client) Do(ctx context.Context, req tool.Request) (*tool.Response, error) {
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && (!req.Write || !c.allowWrites.Load() || !tool.IsWrite(ctx)) {
		return nil, fmt.Errorf("asc: %s %s: %w", method, req.Path, tool.ErrWritesDisabled)
	}
	u, err := c.resolve(req.Path, req.Query)
	if err != nil {
		return nil, err
	}
	body, ctype := req.RawBody, req.ContentType
	if body == nil && req.Body != nil {
		if body, err = json.Marshal(req.Body); err != nil {
			return nil, fmt.Errorf("asc: %s %s: encode body: %w", method, req.Path, err)
		}
		ctype = "application/json"
	}
	accept := req.Accept
	if accept == "" {
		accept = "application/json"
	}
	raw, err := c.send(ctx, method, u, body, ctype, accept)
	if err != nil {
		return nil, err
	}
	doc, err := tool.ParseDocument(raw.body)
	if err != nil {
		return nil, fmt.Errorf("asc: %s %s: %w", method, req.Path, err)
	}
	c.wrap(doc)
	return &tool.Response{
		Status:     raw.status,
		Header:     raw.header,
		Data:       doc.Data,
		Collection: doc.Collection,
		Included:   doc.Included,
		Meta:       doc.Meta,
		Links:      doc.Links,
		Body:       raw.body,
	}, nil
}

// Get implements [tool.ASC].
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*tool.Response, error) {
	return c.Do(ctx, tool.Request{Method: http.MethodGet, Path: path, Query: query})
}

// List implements [tool.ASC]: it builds the first page from opts (or resumes
// opts.Cursor) and follows links.next via [Paginate].
func (c *Client) List(ctx context.Context, path string, opts tool.ListOptions) (*tool.ListResult, error) {
	u, err := c.resolve(path, nil)
	if err != nil {
		return nil, err
	}
	var first string
	if opts.Cursor != "" {
		if first, err = DecodeCursor(opts.Cursor, c.base, u.Path); err != nil {
			return nil, fmt.Errorf("asc: list %s: %w", u.Path, err)
		}
	} else {
		q := u.Query()
		for k, vs := range opts.Query {
			q[k] = append([]string(nil), vs...)
		}
		for t, fs := range opts.Fields {
			q.Set("fields["+t+"]", strings.Join(fs, ","))
		}
		if len(opts.Include) > 0 {
			q.Set("include", strings.Join(opts.Include, ","))
		}
		limit := opts.Limit
		if limit <= 0 {
			limit = DefaultMaxItems
		}
		q.Set("limit", strconv.Itoa(limit))
		u.RawQuery = q.Encode()
		first = u.String()
	}
	res, err := Paginate(ctx, first, c.base, opts.MaxItems, c.fetchPage)
	if err != nil {
		return nil, fmt.Errorf("asc: list %s: %w", u.Path, err)
	}
	return res, nil
}

// fetchPage is the [PageFetcher] of List; rawURL was validated by Paginate.
func (c *Client) fetchPage(ctx context.Context, rawURL string) (*tool.Document, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("asc: page URL: %w", err)
	}
	raw, err := c.send(ctx, http.MethodGet, u, nil, "", "application/json")
	if err != nil {
		return nil, err
	}
	doc, err := tool.ParseDocument(raw.body)
	if err != nil {
		return nil, fmt.Errorf("asc: GET %s: %w", u.Path, err)
	}
	c.wrap(doc)
	return doc, nil
}

// Download implements [tool.ASC]: an authenticated GET returning the raw
// body (at most [MaxBodyBytes]); Accept defaults to application/a-gzip.
func (c *Client) Download(ctx context.Context, req tool.Request) ([]byte, http.Header, error) {
	u, err := c.resolve(req.Path, req.Query)
	if err != nil {
		return nil, nil, err
	}
	accept := req.Accept
	if accept == "" {
		accept = "application/a-gzip"
	}
	raw, err := c.send(ctx, http.MethodGet, u, nil, "", accept)
	if err != nil {
		return nil, nil, err
	}
	return raw.body, raw.header, nil
}

// wrap marks free-text attributes of doc as untrusted.
func (c *Client) wrap(doc *tool.Document) {
	tool.WrapUntrusted(doc.Data, c.freeText)
	tool.WrapUntrusted(doc.Included, c.freeText)
}

// resolve joins path (and query) onto the base URL, refusing anything that
// would leave the base host.
func (c *Client) resolve(path string, query url.Values) (*url.URL, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, fmt.Errorf("asc: path %q must be absolute, like /v1/apps: %w", path, tool.ErrInvalidArgs)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("asc: path %q: %w", path, err)
	}
	if query != nil {
		if ref.RawQuery != "" {
			return nil, fmt.Errorf("asc: path %q has a query string and Query is set: %w", path, tool.ErrInvalidArgs)
		}
		ref.RawQuery = query.Encode()
	}
	u := c.base.ResolveReference(ref)
	if u.Host != c.base.Host || u.Scheme != c.base.Scheme {
		return nil, fmt.Errorf("asc: path %q leaves the API host: %w", path, tool.ErrInvalidArgs)
	}
	return u, nil
}

// send performs one logical request with auth, retries, the 401 refresh and
// eventual-consistency handling, returning the 2xx response or a typed error.
func (c *Client) send(ctx context.Context, method string, u *url.URL, body []byte, ctype, accept string) (*rawResponse, error) {
	label := method + " " + u.Path
	refreshed := false
	consistency := 0
	for attempt := 0; ; {
		raw, err := c.exchange(ctx, method, u, body, ctype, accept)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, c.unknownIf(method, fmt.Errorf("asc: %s: %w", label, ctxErr))
			}
			if errors.Is(err, errToken) {
				return nil, fmt.Errorf("asc: %s: %w", label, err)
			}
			retry, wait := c.retry.shouldRetry(method, 0, err, attempt)
			if !retry {
				err = &redactedError{msg: "asc: " + label + ": " + Redact(err.Error()), cause: err}
				if isConnRefused(err) {
					return nil, err
				}
				return nil, c.unknownIf(method, err)
			}
			if err := c.sleep(ctx, wait); err != nil {
				return nil, fmt.Errorf("asc: %s: %w", label, err)
			}
			attempt++
			continue
		}
		c.observe(raw.header)
		status := raw.status
		if status >= 200 && status < 300 {
			return raw, nil
		}
		if status == http.StatusUnauthorized && !refreshed {
			refreshed = true
			c.tokens.Invalidate()
			continue
		}
		if status == http.StatusNotFound && method == http.MethodGet &&
			consistency < ascrules.ConsistencyRetries && ascrules.IsEventualConsistency(ctx) {
			consistency++
			if err := c.sleep(ctx, ascrules.ConsistencyWait); err != nil {
				return nil, fmt.Errorf("asc: %s: %w", label, err)
			}
			continue
		}
		apiErr := c.apiError(status, raw.body, method, u.Path)
		retry, wait := c.retry.shouldRetry(method, status, nil, attempt)
		if status == http.StatusTooManyRequests {
			if ra, ok := parseRetryAfter(raw.header.Get("Retry-After"), c.clock.Now()); ok {
				if ra > maxRetryAfter {
					return nil, fmt.Errorf("asc: %s: Retry-After %s exceeds %s: %w: %w",
						label, ra, maxRetryAfter, tool.ErrRateLimited, apiErr)
				}
				wait = ra
			}
			if !retry || !c.fits(ctx, wait) {
				return nil, fmt.Errorf("asc: %s: %w: %w", label, tool.ErrRateLimited, apiErr)
			}
		}
		if retry && c.fits(ctx, wait) {
			if err := c.sleep(ctx, wait); err != nil {
				return nil, fmt.Errorf("asc: %s: %w", label, err)
			}
			attempt++
			continue
		}
		if status == http.StatusRequestTimeout || status >= http.StatusInternalServerError {
			return nil, c.unknownIf(method, fmt.Errorf("asc: %s: %w", label, apiErr))
		}
		return nil, fmt.Errorf("asc: %s: %w", label, apiErr)
	}
}

// errToken marks token-source failures, which are never retried.
var errToken = errors.New("sign token")

// exchange performs one HTTP round trip under the concurrency semaphore.
func (c *Client) exchange(ctx context.Context, method string, u *url.URL, body []byte, ctype, accept string) (*rawResponse, error) {
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errToken, Redact(err.Error()))
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil && ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for a request slot: %w", ctx.Err())
	}
	defer func() { <-c.sem }()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body; close errors carry no information
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(b) > MaxBodyBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes: %w", MaxBodyBytes, tool.ErrInvalidArgs)
	}
	return &rawResponse{status: resp.StatusCode, header: resp.Header, body: b}, nil
}

// unknownIf wraps err with tool.ErrOutcomeUnknown for methods that are not
// safe to replay, since Apple may have applied them.
func (c *Client) unknownIf(method string, err error) error {
	if idempotent(method) {
		return err
	}
	return fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, err)
}

// fits reports whether waiting d leaves ctx's deadline unexpired.
func (c *Client) fits(ctx context.Context, d time.Duration) bool {
	dl, ok := ctx.Deadline()
	return !ok || c.clock.Now().Add(d).Before(dl)
}

// redactedError carries a redacted message while keeping the cause chain for
// errors.Is/As.
type redactedError struct {
	msg   string
	cause error
}

// Error implements error.
func (e *redactedError) Error() string { return e.msg }

// Unwrap returns the cause.
func (e *redactedError) Unwrap() error { return e.cause }

// sleep waits d via the client clock.
func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if err := c.clock.Sleep(ctx, d); err != nil {
		return fmt.Errorf("wait before retry: %w", err)
	}
	return nil
}
