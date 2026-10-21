package release

// release_status check_urls: probe the listing's support, marketing and
// privacy URLs without ever connecting to a private network address.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// URL probe bounds.
const (
	urlCheckTimeout      = 5 * time.Second
	urlCheckMaxRedirects = 3
	urlCheckMaxURLs      = 12
	urlCheckParallel     = 4
)

var (
	errPrivateAddr = errors.New("private, loopback or link-local address")
	errNotHTTPS    = errors.New("not https")
)

// urlNet resolves and dials for the URL probes; tests replace urlNetFor.
type urlNet struct {
	lookup func(ctx context.Context, host string) ([]net.IP, error)
	dial   func(ctx context.Context, network, addr string) (net.Conn, error)
	tls    *tls.Config
}

var urlNetFor = func() urlNet {
	d := &net.Dialer{Timeout: urlCheckTimeout}
	return urlNet{
		lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		dial: d.DialContext,
	}
}

// blockedNets are non-public ranges the net.IP predicates miss.
var blockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// publicIP reports whether ip may be connected to.
func publicIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	return !slices.ContainsFunc(blockedNets, func(n *net.IPNet) bool { return n.Contains(ip) })
}

// dialPublic resolves addr, refuses it when any address is not public, and
// dials the checked address (never re-resolving). It runs for every
// connection, so redirects are checked before connecting too.
func (n urlNet) dialPublic(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		if ips, err = n.lookup(ctx, host); err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errPrivateAddr
		}
	}
	return n.dial(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

func (n urlNet) client() *http.Client {
	return &http.Client{
		Timeout: urlCheckTimeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           n.dialPublic,
			TLSClientConfig:       n.tls,
			TLSHandshakeTimeout:   urlCheckTimeout,
			ResponseHeaderTimeout: urlCheckTimeout,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > urlCheckMaxRedirects {
				return fmt.Errorf("more than %d redirects", urlCheckMaxRedirects)
			}
			if req.URL.Scheme != "https" || req.URL.User != nil {
				return fmt.Errorf("redirect: %w", errNotHTTPS)
			}
			return nil
		},
	}
}

// probe HEADs raw (GET when HEAD fails) and returns the final status.
func (n urlNet) probe(ctx context.Context, raw string) (int, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return 0, errNotHTTPS
	}
	c := n.client()
	var status int
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		status, err = n.do(ctx, c, method, u.String())
		if errors.Is(err, errPrivateAddr) || errors.Is(err, errNotHTTPS) || (err == nil && status < 400) {
			break
		}
	}
	return status, err
}

func (n urlNet) do(ctx context.Context, c *http.Client, method, u string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, urlCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, http.NoBody)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "app-store-connect-mcp/url-check")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// urlReason is a short failure reason without the URL or server text.
func urlReason(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, errPrivateAddr):
		return "refused: private address"
	case errors.Is(err, errNotHTTPS):
		return "refused: not https"
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(err.Error(), "redirects"):
		return "too many redirects"
	default:
		return "unreachable"
	}
}

// listingURLs returns the distinct listing URLs as "label\x00url", sorted.
func listingURLs(s *statusSnapshot) []string {
	seen := map[string]bool{}
	add := func(label, u string) {
		if u = strings.TrimSpace(u); u != "" {
			seen[label+"\x00"+u] = true
		}
	}
	if s.Version != nil {
		for _, l := range s.Version.localizations() {
			add("support", l.Str("supportUrl"))
			add("marketing", l.Str("marketingUrl"))
		}
	}
	for _, l := range s.AppInfoLocs {
		add("privacy", l.Str("privacyPolicyUrl"))
	}
	return slices.Sorted(maps.Keys(seen))
}

// checkURLs probes the listing URLs; unreachable ones warn, never block.
// Items carry the host and status only.
func checkURLs(ctx context.Context, s *statusSnapshot) check {
	c := check{ID: "urls", Status: statusOK}
	targets := listingURLs(s)
	if len(targets) > urlCheckMaxURLs {
		c.Items = append(c.Items, fmt.Sprintf("checked the first %d of %d URLs", urlCheckMaxURLs, len(targets)))
		targets = targets[:urlCheckMaxURLs]
	}
	n := urlNetFor()
	items := make([]string, len(targets))
	bad := make([]bool, len(targets))
	sem := make(chan struct{}, urlCheckParallel)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			label, raw, _ := strings.Cut(t, "\x00")
			host := raw
			if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
				host = u.Hostname()
			}
			status, err := n.probe(ctx, raw)
			switch {
			case err != nil:
				items[i], bad[i] = fmt.Sprintf("%s %s: %s", label, host, urlReason(err)), true
			default:
				items[i], bad[i] = fmt.Sprintf("%s %s: %d", label, host, status), status >= 400
			}
		})
	}
	wg.Wait()
	c.Items = append(c.Items, items...)
	if slices.Contains(bad, true) {
		c.Status, c.Detail = statusWarn, "some listing URLs are unreachable or refused"
		c.FixVia = &fixVia{Tool: "metadata_plan", Args: map[string]any{"app": s.App.ID, "platform": string(s.Platform)}}
		return c
	}
	c.Detail = fmt.Sprintf("%d listing URL(s) reachable", len(targets))
	return c
}
