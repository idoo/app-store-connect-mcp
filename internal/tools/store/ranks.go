package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Politeness limits for the public iTunes Search API (informally ~20/min/IP).
const (
	ranksSpacing     = 3 * time.Second
	ranksPerMinute   = 20
	ranksMaxLookups  = 60
	ranksMaxKeywords = 25
	ranksMaxCountry  = 10
	ranksAbove       = 3
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "store_ranks_report", Toolset: tool.Store, Class: tool.Read, Idempotent: false,
		Title: "Track App Store keyword ranks",
		Description: "Public search rank of an app per keyword × storefront, no ASC key. track measures now (≤60 lookups, " +
			"3 s apart) and appends to local history; report shows latest, previous, best, 7/30-day change and trend; " +
			"untrack keeps history. Names are untrusted.",
		Enums: map[string][]any{"action": {"track", "report", "untrack"}},
	}, ranks))
}

type ranksIn struct {
	Action    string   `json:"action,omitempty" jsonschema:"default track"`
	App       string   `json:"app,omitempty" jsonschema:"App Store id (the ASC app id) or bundle id; default: pinned app"`
	Keywords  []string `json:"keywords,omitempty" jsonschema:"search terms (≤25); track defaults to the tracked set"`
	Countries []string `json:"countries,omitempty" jsonschema:"two-letter storefronts (≤10); default us, or all tracked"`
	Depth     int      `json:"depth,omitempty" jsonschema:"search depth 1-200; default 100"`
	Days      int      `json:"days,omitempty" jsonschema:"report window in days 1-365; default 30"`
}

// competitor is a third-party app ranked above the tracked app.
type competitor struct {
	Rank int            `json:"rank"`
	ID   int64          `json:"id"`
	Name tool.Untrusted `json:"name"`
}

type ranksOut struct {
	App    int64  `json:"app"`
	Action string `json:"action"`
	// Measured counts lookups done in this call; Remaining were not done
	// because the run stopped early (Throttled, cancelled or time-capped).
	Measured  int           `json:"measured,omitempty"`
	Remaining int           `json:"remaining,omitempty"`
	Throttled bool          `json:"throttled,omitempty"`
	Untracked int           `json:"untracked,omitempty"`
	Days      int           `json:"days,omitempty"`
	Ranks     []rankSummary `json:"ranks,omitempty"`
	Tracked   []string      `json:"tracked,omitempty"`
	Points    int           `json:"history_points"`
}

// pacer spaces iTunes lookups process-wide: at least ranksSpacing apart and
// at most ranksPerMinute in any minute. A clock that moved backwards (a new
// fake clock in tests) resets it.
type pacer struct {
	slot   chan struct{}
	recent []time.Time
}

var ranksPacer = &pacer{slot: make(chan struct{}, 1)}

// delay returns how long to wait before the next lookup at now.
func (p *pacer) delay(now time.Time) time.Duration {
	if n := len(p.recent); n > 0 && now.Before(p.recent[n-1]) {
		p.recent = nil
	}
	if len(p.recent) == 0 {
		return 0
	}
	until := p.recent[len(p.recent)-1].Add(ranksSpacing)
	if len(p.recent) >= ranksPerMinute {
		if t := p.recent[len(p.recent)-ranksPerMinute].Add(time.Minute); t.After(until) {
			until = t
		}
	}
	return max(until.Sub(now), 0)
}

// acquire takes the process-wide slot; release must follow.
func (p *pacer) acquire(ctx context.Context) error {
	select {
	case p.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pacer) release() { <-p.slot }

// mark records a lookup at t.
func (p *pacer) mark(t time.Time) {
	p.recent = append(p.recent, t)
	if n := len(p.recent); n > ranksPerMinute {
		p.recent = p.recent[n-ranksPerMinute:]
	}
}

func ranks(ctx context.Context, env *tool.Env, in ranksIn) (*tool.Result, error) {
	action := in.Action
	if action == "" {
		action = "track"
	}
	keywords, err := normKeywords(in.Keywords)
	if err != nil {
		return nil, err
	}
	countries, err := normCountries(in.Countries)
	if err != nil {
		return nil, err
	}
	depth := in.Depth
	switch {
	case depth == 0:
		depth = 100
	case depth < 1 || depth > 200:
		return nil, tool.InvalidArgs("depth must be 1-200")
	}
	days := in.Days
	switch {
	case days == 0:
		days = 30
	case days < 1 || days > 365:
		return nil, tool.InvalidArgs("days must be 1-365")
	}
	if action == "untrack" && len(keywords) == 0 {
		return nil, tool.InvalidArgs("untrack needs keywords")
	}
	// Reject an oversized track before any network call.
	if action == "track" && len(keywords) > 0 {
		if err := capLookups(len(keywords) * max(len(countries), 1)); err != nil {
			return nil, err
		}
	}
	trackID, err := resolveTrackID(ctx, env, in.App)
	if err != nil {
		return nil, err
	}
	key := strconv.FormatInt(trackID, 10)
	var h asoHistory
	res := tool.OK(nil)
	persist := true
	if _, err := env.State.Load(asoKind, key, &h); err != nil {
		if action != "track" || !errors.Is(err, tool.ErrUnavailable) {
			return nil, fmt.Errorf("load rank history: %w", err)
		}
		persist = false
		res.Warn("no state store: ranks are measured but history is not kept")
	}
	now := env.Clock.Now()
	out := ranksOut{App: trackID, Action: action}
	switch action {
	case "untrack":
		out.Untracked = h.untrack(keywords, countries)
	case "report":
		out.Days = days
		for _, p := range selectPairs(h.pairs(), keywords, countries) {
			out.Ranks = append(out.Ranks, summarize(&h, p, now, days))
		}
	case "track":
		pairs := h.Tracked
		if len(keywords) > 0 {
			if len(countries) == 0 {
				countries = []string{"us"}
			}
			pairs = nil
			for _, k := range keywords {
				for _, c := range countries {
					pairs = append(pairs, asoPair{k, c})
				}
			}
		} else {
			pairs = selectPairs(pairs, nil, countries)
		}
		if len(pairs) == 0 {
			return nil, tool.InvalidArgs("nothing tracked for app %d yet: pass keywords (and countries) to start", trackID)
		}
		if err := capLookups(len(pairs)); err != nil {
			return nil, err
		}
		h.track(pairs)
		out.Days = days
		pts, rows, note, err := measure(ctx, env, trackID, pairs, depth)
		if err != nil {
			return nil, err
		}
		if note != "" {
			res.Warn(note)
		}
		out.Measured, out.Remaining = len(pts), len(pairs)-len(rows)
		out.Throttled = strings.HasPrefix(note, "throttled")
		h.add(env.Clock.Now(), pts)
		now = env.Clock.Now()
		for _, r := range rows {
			s := summarize(&h, asoPair{r.Keyword, r.Country}, now, days)
			s.Above, s.Error = r.Above, r.Error
			out.Ranks = append(out.Ranks, s)
		}
		if out.Remaining > 0 {
			res.Then(tool.NextStep{Tool: "store_ranks_report", Args: map[string]any{"action": "track", "app": key}})
		}
	}
	if persist && action != "report" {
		if err := env.State.Save(asoKind, key, &h); err != nil {
			if action != "track" {
				return nil, fmt.Errorf("save rank history: %w", err)
			}
			res.Warn("rank history not saved: " + err.Error())
		}
	}
	for _, p := range h.Tracked {
		out.Tracked = append(out.Tracked, p.K+"@"+p.C)
	}
	out.Points = len(h.Points)
	res.Data = out
	if action != "untrack" && len(h.Points) > 0 {
		link, err := env.Artifacts.Put(ctx, "aso-"+key+".csv", "text/csv", historyCSV(&h))
		switch {
		case err == nil:
			res.WithLink(link)
		case !errors.Is(err, tool.ErrUnavailable):
			res.Warn("history CSV unavailable: " + err.Error())
		}
	}
	return res, nil
}

func capLookups(n int) error {
	if n > ranksMaxLookups {
		return tool.InvalidArgs("%d keyword×storefront lookups exceed %d per call (3 s apart); split keywords or countries across calls",
			n, ranksMaxLookups)
	}
	return nil
}

func normKeywords(in []string) ([]string, error) {
	var out []string
	for _, k := range in {
		k = strings.ToLower(strings.Join(strings.Fields(k), " "))
		if k == "" {
			return nil, tool.InvalidArgs("keywords must not be blank")
		}
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	if len(out) > ranksMaxKeywords {
		return nil, tool.InvalidArgs("at most %d keywords", ranksMaxKeywords)
	}
	return out, nil
}

func normCountries(in []string) ([]string, error) {
	var out []string
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if !countryRE.MatchString(c) {
			return nil, tool.InvalidArgs("country %q must be a two-letter storefront code such as us or se", c)
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) > ranksMaxCountry {
		return nil, tool.InvalidArgs("at most %d countries", ranksMaxCountry)
	}
	return out, nil
}

// selectPairs filters pairs by keywords and countries (empty = any).
func selectPairs(pairs []asoPair, keywords, countries []string) []asoPair {
	var out []asoPair
	for _, p := range pairs {
		if (len(keywords) == 0 || slices.Contains(keywords, p.K)) && (len(countries) == 0 || slices.Contains(countries, p.C)) {
			out = append(out, p)
		}
	}
	return out
}

// resolveTrackID turns a numeric App Store id or a bundle id (looked up in
// the US storefront) into the trackId.
func resolveTrackID(ctx context.Context, env *tool.Env, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = env.Profile.DefaultApp
	}
	switch {
	case trackRE.MatchString(ref):
		return strconv.ParseInt(ref, 10, 64)
	case ref != "" && bundleRE.MatchString(ref):
		doc, err := fetch(ctx, env, "/lookup", url.Values{"bundleId": {ref}, "country": {"us"}, "entity": {"software"}})
		if err != nil {
			return 0, err
		}
		if len(doc.Results) == 0 {
			return 0, tool.InvalidArgs("bundle id %s is not on the US App Store; pass the numeric App Store id", ref)
		}
		return doc.Results[0].TrackID, nil
	}
	return 0, tool.InvalidArgs("app must be a numeric App Store id or a bundle id; use apps_list to find them")
}

// measured is the outcome of one lookup.
type measured struct {
	Keyword, Country string
	Above            []competitor
	Error            string
}

// measure looks up pairs sequentially under the pacer. It stops early on
// 403/429 (note starts with "throttled"), cancellation, or the no-progress
// wait cap, returning what it has.
func measure(ctx context.Context, env *tool.Env, trackID int64, pairs []asoPair, depth int) ([]asoPoint, []measured, string, error) {
	if err := ranksPacer.acquire(ctx); err != nil {
		return nil, nil, "", fmt.Errorf("rank lookups: %w", err)
	}
	defer ranksPacer.release()
	start := env.Clock.Now()
	header := http.Header{"User-Agent": {"app-store-connect-mcp/" + env.Version + " (store_ranks_report)"}}
	var pts []asoPoint
	var rows []measured
	var firstErr error
	for i, p := range pairs {
		now := env.Clock.Now()
		wait := ranksPacer.delay(now)
		if !env.HasProgress() && now.Add(wait).Sub(start) > tool.NoProgressWaitCap {
			return pts, rows, fmt.Sprintf("stopped after %d of %d lookups to stay under the client timeout; call track again for the rest", i, len(pairs)), nil
		}
		if wait > 0 {
			if err := env.Clock.Sleep(ctx, wait); err != nil {
				return pts, rows, fmt.Sprintf("cancelled after %d of %d lookups", i, len(pairs)), nil
			}
		}
		env.Progress(float64(i), float64(len(pairs)), p.K+" @ "+p.C)
		at := env.Clock.Now()
		ranksPacer.mark(at)
		resp, err := env.Fetch.Fetch(ctx, http.MethodGet, itunesBase+"/search?"+url.Values{
			"term": {p.K}, "entity": {"software"}, "country": {p.C}, "limit": {strconv.Itoa(depth)},
		}.Encode(), header, nil, maxBody)
		if err == nil && (resp.Status == http.StatusTooManyRequests || resp.Status == http.StatusForbidden) {
			return pts, rows, fmt.Sprintf("throttled by Apple (HTTP %d) after %d of %d lookups; partial result saved, retry later",
				resp.Status, i, len(pairs)), nil
		}
		var doc itunesDoc
		switch {
		case err != nil:
		case resp.Status != http.StatusOK:
			err = fmt.Errorf("itunes search: %w: HTTP %d", tool.ErrUnavailable, resp.Status)
		default:
			if e := json.Unmarshal(resp.Body, &doc); e != nil {
				err = fmt.Errorf("itunes search: decode: %w", e)
			}
		}
		if err != nil {
			firstErr = cmpErr(firstErr, err)
			rows = append(rows, measured{Keyword: p.K, Country: p.C, Error: err.Error()})
			continue
		}
		pt := asoPoint{T: at.Unix(), K: p.K, C: p.C, D: depth}
		var above []competitor
		for j, a := range doc.Results {
			if a.TrackID == trackID {
				pt.R = j + 1
				break
			}
			if len(above) < ranksAbove {
				above = append(above, competitor{Rank: j + 1, ID: a.TrackID, Name: tool.Untrusted(a.TrackName)})
			}
		}
		pts = append(pts, pt)
		rows = append(rows, measured{Keyword: p.K, Country: p.C, Above: above})
	}
	env.Progress(float64(len(pairs)), float64(len(pairs)), "done")
	if len(pts) == 0 && firstErr != nil {
		return nil, nil, "", firstErr
	}
	return pts, rows, "", nil
}

func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}
