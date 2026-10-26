package asc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/netpolicy"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Fetcher defaults.
const (
	// DefaultFetchMaxBytes caps a fetched body when the caller passes 0.
	DefaultFetchMaxBytes = MaxBodyBytes
	// DefaultFetchTimeout bounds one fetch, including upload parts.
	DefaultFetchTimeout = 10 * time.Minute
	// maxFetchRedirects bounds the redirects followed by one fetch.
	maxFetchRedirects = 5
)

// ErrURLPolicy is returned when a URL, redirect target, dialed address or
// request header violates the unauthenticated fetch policy.
var ErrURLPolicy = errors.New("url policy")

// FetchOptions configures [NewFetcher].
type FetchOptions struct {
	// MaxBytes is the body cap used when Fetch is called with maxBytes 0;
	// it defaults to [DefaultFetchMaxBytes].
	MaxBytes int64
	// Timeout bounds one fetch; it defaults to [DefaultFetchTimeout].
	Timeout time.Duration
	// UserAgent defaults to [DefaultUserAgent].
	UserAgent string
}

// Fetcher performs unauthenticated requests to Apple-signed URLs (upload
// parts, analytics segments, report and screenshot downloads). It implements
// [tool.Fetcher] and enforces the URL policy on the request URL, every
// redirect target and the IP address actually dialed, which defeats DNS
// rebinding. It is safe for concurrent use.
type Fetcher struct {
	client    *http.Client
	transport *http.Transport
	maxBytes  int64
	userAgent string
	// allowLoopback admits loopback addresses on any port; tests only.
	allowLoopback bool
}

var _ tool.Fetcher = (*Fetcher)(nil)

// NewFetcher returns a Fetcher configured by opts.
func NewFetcher(opts FetchOptions) *Fetcher {
	f := &Fetcher{maxBytes: opts.MaxBytes, userAgent: opts.UserAgent}
	if f.maxBytes <= 0 {
		f.maxBytes = DefaultFetchMaxBytes
	}
	if f.userAgent == "" {
		f.userAgent = DefaultUserAgent
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultFetchTimeout
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: f.dialControl}
	f.transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	f.client = &http.Client{Transport: f.transport, Timeout: timeout, CheckRedirect: f.checkRedirect}
	return f
}

// Fetch implements [tool.Fetcher]. Non-2xx statuses are returned, not
// treated as errors; a body larger than maxBytes (0 means the configured
// default) is an error.
func (f *Fetcher) Fetch(ctx context.Context, method, rawURL string, header http.Header, body io.Reader, maxBytes int64) (*tool.FetchResponse, error) {
	method = strings.ToUpper(method)
	if method == "" {
		method = http.MethodGet
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w: unparsable URL", ErrURLPolicy)
	}
	shown := safeURL(u)
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPost:
	default:
		return nil, fmt.Errorf("fetch %s %s: %w: method not allowed", method, shown, ErrURLPolicy)
	}
	if err := f.checkURL(u); err != nil {
		return nil, fmt.Errorf("fetch %s %s: %w", method, shown, err)
	}
	if err := checkHeaders(header); err != nil {
		return nil, fmt.Errorf("fetch %s %s: %w", method, shown, err)
	}
	if maxBytes <= 0 {
		maxBytes = f.maxBytes
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("fetch %s %s: %w", method, shown, err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", f.userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s %s: %w", method, shown, unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch %s %s: read body: %w", method, shown, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("fetch %s %s: response exceeds %d bytes", method, shown, maxBytes)
	}
	return &tool.FetchResponse{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// checkRedirect re-applies the URL policy to every redirect target.
func (f *Fetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxFetchRedirects {
		return fmt.Errorf("%w: more than %d redirects", ErrURLPolicy, maxFetchRedirects)
	}
	if err := f.checkURL(req.URL); err != nil {
		return fmt.Errorf("redirect to %s: %w", safeURL(req.URL), err)
	}
	return nil
}

// checkURL enforces https, port 443, no userinfo and, for literal IP hosts,
// a public address.
func (f *Fetcher) checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not https", ErrURLPolicy, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("%w: userinfo is not allowed", ErrURLPolicy)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrURLPolicy)
	}
	addr, isIP := parseHostIP(host)
	loopbackOK := f.allowLoopback && (host == "localhost" || (isIP && addr.IsLoopback()))
	if port := u.Port(); port != "" && port != "443" && !loopbackOK {
		return fmt.Errorf("%w: port %s is not 443", ErrURLPolicy, port)
	}
	if isIP {
		return f.checkIP(addr)
	}
	return nil
}

// dialControl checks the address actually being dialed, after DNS
// resolution, so a hostname cannot rebind to a private address.
func (f *Fetcher) dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: dial address %q: %w", ErrURLPolicy, address, err)
	}
	addr, ok := parseHostIP(host)
	if !ok {
		return fmt.Errorf("%w: dial address %q is not an IP", ErrURLPolicy, address)
	}
	return f.checkIP(addr)
}

// checkIP refuses non-public addresses (loopback is allowed in test mode).
func (f *Fetcher) checkIP(addr netip.Addr) error {
	if f.allowLoopback && addr.IsLoopback() {
		return nil
	}
	if !netpolicy.PublicAddr(addr) {
		return fmt.Errorf("%w: address %s is not public", ErrURLPolicy, addr)
	}
	return nil
}

// parseHostIP parses a URL host or dial host as an IP, dropping any zone.
func parseHostIP(host string) (netip.Addr, bool) {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.WithZone("").Unmap(), true
}

// checkHeaders refuses credential and routing headers.
func checkHeaders(h http.Header) error {
	for k := range h {
		name := strings.ToLower(k)
		if name == "authorization" || name == "cookie" || name == "host" || strings.HasPrefix(name, "proxy-") {
			return fmt.Errorf("%w: header %q is not allowed", ErrURLPolicy, k)
		}
	}
	return nil
}

// safeURL renders u without userinfo, query or fragment, so signed URLs
// never reach errors or logs.
func safeURL(u *url.URL) string {
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// unwrapURLError drops the *url.Error wrapper, whose message repeats the
// full (signed) URL, while keeping the cause.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return fmt.Errorf("timeout: %w", ue.Err)
		}
		return ue.Err
	}
	return err
}
