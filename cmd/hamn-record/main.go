//go:build dev

// Command hamn-record captures sanitized App Store Connect responses as test
// fixtures. It is a developer tool, built only with the "dev" tag:
//
//	go run -tags dev ./cmd/hamn-record [-profile name] [-app id] [-out testdata/fixtures]
//
// It resolves credentials like hamn, forces read-only mode and wraps the
// HTTP transport with a guard that refuses every non-GET request. Each
// request is checked against the pinned spec before it is sent. Responses
// pass through asctest.Sanitizer (one mapping for the whole run) before they
// are written to <out>/<name>.json.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/idoo/hamn/internal/asc"
	"github.com/idoo/hamn/internal/asctest"
	"github.com/idoo/hamn/internal/auth"
	"github.com/idoo/hamn/internal/config"
	"github.com/idoo/hamn/internal/spec"
	"github.com/idoo/hamn/internal/tool"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hamn-record:", asc.Redact(err.Error()))
		os.Exit(1)
	}
}

// ids carries the identifiers discovered by earlier steps.
type ids struct {
	app, version, appInfo, versionLoc string
}

// step is one recorded GET. path builds the request path from ids and
// returns "" when a dependency is missing; learn stores ids from the reply.
type step struct {
	name  string
	path  func(ids) string
	query url.Values
	learn func(*ids, *tool.Response)
}

// steps is the fixed recording plan.
func steps() []step {
	lim := func(n string) url.Values { return url.Values{"limit": {n}} }
	appPath := func(suffix string) func(ids) string {
		return func(v ids) string {
			if v.app == "" {
				return ""
			}
			return "/v1/apps/" + v.app + suffix
		}
	}
	first := func(dst func(*ids) *string) func(*ids, *tool.Response) {
		return func(v *ids, r *tool.Response) {
			if res, ok := r.First(); ok && *dst(v) == "" {
				*dst(v) = res.ID
			}
		}
	}
	static := func(p string) func(ids) string { return func(ids) string { return p } }
	return []step{
		{name: "apps", path: static("/v1/apps"), query: lim("3"), learn: first(func(v *ids) *string { return &v.app })},
		{name: "app", path: appPath(""), query: url.Values{"include": {"appInfos,appStoreVersions"}}},
		{name: "appStoreVersions", path: appPath("/appStoreVersions"), query: lim("3"), learn: first(func(v *ids) *string { return &v.version })},
		{name: "builds", path: func(v ids) string { return ifSet(v.app, "/v1/builds") }, query: url.Values{"filter[app]": {""}, "limit": {"3"}}},
		{name: "betaGroups", path: appPath("/betaGroups"), query: lim("5")},
		{name: "betaTesters", path: func(v ids) string { return ifSet(v.app, "/v1/betaTesters") }, query: url.Values{"filter[apps]": {""}, "limit": {"3"}}},
		{name: "customerReviews", path: appPath("/customerReviews"), query: lim("5")},
		{name: "appInfos", path: appPath("/appInfos"), learn: first(func(v *ids) *string { return &v.appInfo })},
		{name: "appInfoLocalizations", path: func(v ids) string { return ifSet(v.appInfo, "/v1/appInfos/"+v.appInfo+"/appInfoLocalizations") }},
		{
			name: "appStoreVersionLocalizations", path: func(v ids) string {
				return ifSet(v.version, "/v1/appStoreVersions/"+v.version+"/appStoreVersionLocalizations")
			},
			learn: first(func(v *ids) *string { return &v.versionLoc }),
		},
		{name: "appScreenshotSets", path: func(v ids) string {
			return ifSet(v.versionLoc, "/v1/appStoreVersionLocalizations/"+v.versionLoc+"/appScreenshotSets")
		}},
		{name: "inAppPurchasesV2", path: appPath("/inAppPurchasesV2"), query: lim("5")},
		{name: "subscriptionGroups", path: appPath("/subscriptionGroups"), query: lim("5")},
		{name: "bundleIds", path: static("/v1/bundleIds"), query: lim("3")},
		{name: "certificates", path: static("/v1/certificates"), query: lim("2")},
		{name: "profiles", path: static("/v1/profiles"), query: lim("2")},
		{name: "ciProducts", path: static("/v1/ciProducts"), query: lim("5")},
		{name: "analyticsReportRequests", path: appPath("/analyticsReportRequests")},
		{name: "territories", path: static("/v1/territories"), query: lim("5")},
		{name: "appAvailabilityV2", path: appPath("/appAvailabilityV2")},
		{name: "appPricePoints", path: appPath("/appPricePoints"), query: lim("3")},
		{name: "reviewSubmissions", path: func(v ids) string { return ifSet(v.app, "/v1/reviewSubmissions") }, query: url.Values{"filter[app]": {""}, "limit": {"5"}}},
		{name: "betaFeedbackScreenshotSubmissions", path: appPath("/betaFeedbackScreenshotSubmissions"), query: lim("2")},
	}
}

// ifSet returns path when id is known, else "" (step skipped).
func ifSet(id, path string) string {
	if id == "" {
		return ""
	}
	return path
}

// queryFor returns the step's query; empty values (filter[app]/filter[apps])
// are filled with the app id.
func (st step) queryFor(v ids) url.Values {
	q := url.Values{}
	for k, vals := range st.query {
		for _, val := range vals {
			if val == "" {
				val = v.app
			}
			q.Add(k, val)
		}
	}
	return q
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("hamn-record", flag.ContinueOnError)
	profile := fs.String("profile", "", "config profile (default: hamn's usual resolution)")
	app := fs.String("app", "", "app id to record (default: the first app)")
	out := fs.String("out", filepath.Join("testdata", "fixtures"), "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	readOnly := false
	resolved, err := config.Resolve(config.Options{Flags: config.Flags{Profile: *profile, AllowWrites: &readOnly}})
	if err != nil {
		return fmt.Errorf("resolve config: %w", err)
	}
	client, idx, err := newClient(ctx, resolved)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", *out, err)
	}
	san := asctest.NewSanitizer()
	v := ids{app: *app}
	var failed []string
	for _, st := range steps() {
		if err := record(ctx, client, idx, san, *out, st, &v); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", st.name, asc.Redact(err.Error()))
			failed = append(failed, st.name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d steps failed: %v", len(failed), failed)
	}
	return nil
}

// record runs one step and writes its sanitized response.
func record(ctx context.Context, c *asc.Client, idx *spec.Index, san *asctest.Sanitizer, out string, st step, v *ids) error {
	path := st.path(*v)
	if path == "" {
		return errors.New("skipped: a previous step found no id to use")
	}
	q := st.queryFor(*v)
	if err := idx.Validate(tool.Request{Method: http.MethodGet, Path: path, Query: q}); err != nil {
		return fmt.Errorf("request does not match the spec: %w", err)
	}
	resp, err := c.Get(ctx, path, q)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	if st.learn != nil {
		st.learn(v, resp)
	}
	clean, err := san.Sanitize(resp.Body)
	if err != nil {
		return err
	}
	dst := filepath.Join(out, st.name+".json")
	if err := os.WriteFile(dst, clean, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	fmt.Fprintf(os.Stderr, "%s: wrote %s (%d bytes)\n", st.name, dst, len(clean))
	return nil
}

// newClient builds a read-only client whose transport refuses non-GETs.
func newClient(ctx context.Context, r *config.Resolved) (*asc.Client, *spec.Index, error) {
	p := r.Profile
	if p.KeyID == "" {
		return nil, nil, errors.New("no credentials: configure a profile or ASC_KEY_ID")
	}
	var src auth.KeySource
	switch p.Key.Source {
	case config.KeyFromKeychain:
		src = auth.KeychainSource{KeyID: p.KeyID}
	default:
		pem, err := p.Key.Read(os.Getenv)
		if err != nil {
			return nil, nil, fmt.Errorf("load key %s: %w", p.Key, err)
		}
		src = auth.InlineSource{Data: string(pem), Origin: p.Key.String()}
	}
	key, err := auth.ResolveKey(ctx, p.KeyID, src)
	if err != nil {
		return nil, nil, err
	}
	signer, err := auth.NewSigner(key, p.IssuerID)
	if err != nil {
		return nil, nil, err
	}
	idx, err := spec.Load()
	if err != nil {
		return nil, nil, err
	}
	opts := asc.Options{
		Tokens:      auth.NewTokenSource(signer),
		HTTP:        &http.Client{Timeout: asc.DefaultRequestTimeout, Transport: getOnly{next: http.DefaultTransport}},
		AllowWrites: false,
		Spec:        idx,
		UserAgent:   "hamn-record",
	}
	if r.BaseURL != "" {
		if opts.BaseURL, err = url.Parse(r.BaseURL); err != nil {
			return nil, nil, fmt.Errorf("base URL: %w", err)
		}
	}
	c, err := asc.New(opts)
	if err != nil {
		return nil, nil, err
	}
	return c, idx, nil
}

// getOnly is an http.RoundTripper that refuses every request but GET.
type getOnly struct{ next http.RoundTripper }

// RoundTrip implements http.RoundTripper.
func (g getOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("refusing %s %s: the recorder is GET-only", req.Method, req.URL.Path)
	}
	return g.next.RoundTrip(req)
}
