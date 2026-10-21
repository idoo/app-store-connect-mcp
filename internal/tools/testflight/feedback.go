package testflight

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	feedbackPage          = 50
	feedbackMaxScan       = 500
	feedbackMaxImages     = 4
	feedbackMaxImageBytes = 1 << 20
	feedbackMaxCrashLogs  = 3
	feedbackMaxCrashBytes = 8 << 10
	feedbackDefaultLimit  = 20
	feedbackMaxLimit      = 50
	sinceLastSeen         = "last_seen"
)

// feedbackKind is one beta feedback collection of an app.
type feedbackKind struct {
	name, resource string
}

var (
	kindScreenshots = feedbackKind{"screenshots", "betaFeedbackScreenshotSubmissions"}
	kindCrashes     = feedbackKind{"crashes", "betaFeedbackCrashSubmissions"}
)

func (k feedbackKind) cursorName() string { return "testflight_feedback_" + k.name }

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "testflight_feedback_list", Toolset: tool.TestFlight, Class: tool.Read, Idempotent: false,
		Title: "List TestFlight feedback",
		Description: "Tester screenshot and crash feedback newest first. Up to 4 screenshots as images; crash logs on " +
			"request. Comments are tester data. mark_seen advances last_seen.",
		Enums: map[string][]any{"kind": {"all", "screenshots", "crashes"}, "platform": platformEnum},
	}, feedbackList))
}

type feedbackListIn struct {
	App              tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Kind             string        `json:"kind,omitempty" jsonschema:"default all"`
	Since            string        `json:"since,omitempty" jsonschema:"last_seen, YYYY-MM-DD or RFC 3339"`
	Build            tool.BuildRef `json:"build,omitempty" jsonschema:"only this build (number, id or latest)"`
	Platform         tool.Platform `json:"platform,omitempty"`
	DeviceModel      string        `json:"device_model,omitempty" jsonschema:"e.g. iPhone15,2"`
	Limit            int           `json:"limit,omitempty" jsonschema:"items per kind, 1-50 (default 20)"`
	MarkSeen         bool          `json:"mark_seen,omitempty" jsonschema:"advance last_seen past these items"`
	IncludeCrashLogs bool          `json:"include_crash_logs,omitempty" jsonschema:"fetch log text of up to 3 crashes (8 KB each)"`
}

type screenshotOut struct {
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Expires string `json:"expires,omitempty"`
	// Image is the 1-based position of the image in the result's images.
	Image   int    `json:"image,omitempty"`
	Skipped string `json:"skipped,omitempty"`
}

type feedbackOut struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	Created           string          `json:"created"`
	Build             string          `json:"build,omitempty"`
	Platform          string          `json:"platform,omitempty"`
	Device            string          `json:"device,omitempty"`
	OSVersion         string          `json:"os_version,omitempty"`
	Locale            string          `json:"locale,omitempty"`
	Tester            tool.Untrusted  `json:"tester,omitempty"`
	Comment           tool.Untrusted  `json:"comment,omitempty"`
	Screenshots       []screenshotOut `json:"screenshots,omitempty"`
	CrashLog          *tool.Untrusted `json:"crash_log,omitempty"`
	CrashLogTruncated bool            `json:"crash_log_truncated,omitempty"`
	CrashLogNote      string          `json:"crash_log_note,omitempty"`
	// CrashLogResource is the URI of the artifact with the full crash log.
	CrashLogResource string `json:"crash_log_resource,omitempty"`
	// urls are the signed screenshot URLs; fetched, never returned.
	urls []string
	// dropped counts device fields removed by cleanDeviceField.
	dropped int
}

type cursorOut struct {
	Since      string `json:"since,omitempty"`
	Returned   int    `json:"returned"`
	More       bool   `json:"more,omitempty"`
	AdvancedTo string `json:"advanced_to,omitempty"`
	// NewestAvailable is the newest item at or before since, set when none
	// is newer, so an empty result still says where the feedback ends.
	NewestAvailable string `json:"newest_available,omitempty"`
}

// emptyOut explains an empty feedback result.
type emptyOut struct {
	Window  string   `json:"window"`
	Builds  string   `json:"builds"`
	Filters []string `json:"filters,omitempty"`
	Note    string   `json:"note"`
}

type feedbackListOut struct {
	App     appRef               `json:"app"`
	Build   *buildOut            `json:"build,omitempty"`
	Items   []feedbackOut        `json:"items"`
	Cursors map[string]cursorOut `json:"cursors"`
	Hint    string               `json:"hint,omitempty"`
	Empty   *emptyOut            `json:"empty,omitempty"`
	// DroppedDeviceFields counts device model, OS version and locale values
	// left out because they did not have the expected shape.
	DroppedDeviceFields int `json:"dropped_device_fields,omitempty"`
}

func feedbackList(ctx context.Context, env *tool.Env, in feedbackListIn) (*tool.Result, error) {
	var kinds []feedbackKind
	switch in.Kind {
	case "", "all":
		kinds = []feedbackKind{kindScreenshots, kindCrashes}
	case kindScreenshots.name:
		kinds = []feedbackKind{kindScreenshots}
	case kindCrashes.name:
		kinds = []feedbackKind{kindCrashes}
	default:
		return nil, tool.InvalidArgs("kind must be all, screenshots or crashes")
	}
	limit := in.Limit
	if limit == 0 {
		limit = feedbackDefaultLimit
	}
	if limit < 1 || limit > feedbackMaxLimit {
		return nil, tool.InvalidArgs("limit must be 1-%d", feedbackMaxLimit)
	}
	var since time.Time
	if in.Since != "" && in.Since != sinceLastSeen {
		t, err := parseSince(in.Since)
		if err != nil {
			return nil, err
		}
		since = t
	}

	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	out := &feedbackListOut{App: appOut(app), Items: []feedbackOut{}, Cursors: map[string]cursorOut{}}
	filters := url.Values{"sort": {"-createdDate"}}
	if in.Build != "" {
		b, err := env.Resolve.Build(ctx, app.ID, app.Platform(in.Platform), in.Build)
		if err != nil {
			return nil, err
		}
		out.Build = &buildOut{ID: b.ID, Number: b.Number}
		filters.Set("filter[build]", b.ID)
	}
	if in.Platform != "" {
		filters.Set("filter[appPlatform]", string(in.Platform))
	}
	if in.DeviceModel != "" {
		filters.Set("filter[deviceModel]", in.DeviceModel)
	}

	for _, k := range kinds {
		kSince, sinceLabel := since, in.Since
		if in.Since == sinceLastSeen {
			c, err := env.State.Cursor(env.Profile.Name, app.ID, k.cursorName())
			if err != nil {
				return nil, fmt.Errorf("read %s cursor: %w", k.name, err)
			}
			sinceLabel = c
			if c != "" {
				if kSince, err = time.Parse(time.RFC3339Nano, c); err != nil {
					return nil, fmt.Errorf("stored %s cursor %q: %w", k.name, c, err)
				}
			}
		}
		items, builds, more, older, err := scanFeedback(ctx, env, app.ID, k, filters, kSince, limit)
		if err != nil {
			return nil, err
		}
		cur := cursorOut{Since: sinceLabel, Returned: len(items), More: more}
		if len(items) == 0 {
			cur.NewestAvailable = older
		}
		newest := ""
		for _, r := range items {
			fo := feedbackFromResource(r, k, builds)
			out.DroppedDeviceFields += fo.dropped
			out.Items = append(out.Items, fo)
			if c := r.Str("createdDate"); newest == "" || laterThan(c, newest) {
				newest = c
			}
		}
		if in.MarkSeen && newest != "" {
			if err := env.State.SetCursor(env.Profile.Name, app.ID, k.cursorName(), newest); err != nil {
				return nil, fmt.Errorf("store %s cursor: %w", k.name, err)
			}
			cur.AdvancedTo = newest
		}
		out.Cursors[k.name] = cur
	}
	slices.SortStableFunc(out.Items, func(a, b feedbackOut) int {
		switch {
		case laterThan(a.Created, b.Created):
			return -1
		case laterThan(b.Created, a.Created):
			return 1
		}
		return 0
	})
	if !in.MarkSeen && len(out.Items) > 0 {
		out.Hint = "last_seen was not changed; repeat with mark_seen:true once these are handled"
	}
	if len(out.Items) == 0 {
		out.Empty = emptyContext(in, out)
	}

	res := tool.OK(out)
	attachScreenshots(ctx, env, out, res)
	if in.IncludeCrashLogs {
		attachCrashLogs(ctx, env, out, res)
	}
	return res, nil
}

// emptyContext describes the window, builds and filters of an empty result.
func emptyContext(in feedbackListIn, out *feedbackListOut) *emptyOut {
	e := &emptyOut{Window: "all time", Builds: "all builds"}
	switch {
	case in.Since == sinceLastSeen:
		e.Window = "after last_seen (per kind, see cursors)"
	case in.Since != "":
		e.Window = "after " + in.Since
	}
	if out.Build != nil {
		e.Builds = "build " + out.Build.Number + " (" + out.Build.ID + ")"
	}
	if in.Platform != "" {
		e.Filters = append(e.Filters, "platform="+string(in.Platform))
	}
	if in.DeviceModel != "" {
		e.Filters = append(e.Filters, "device_model="+in.DeviceModel)
	}
	newest := ""
	for _, c := range out.Cursors {
		if c.NewestAvailable != "" && (newest == "" || laterThan(c.NewestAvailable, newest)) {
			newest = c.NewestAvailable
		}
	}
	switch {
	case newest != "":
		e.Note = "no feedback in this window; the newest matching item is from " + newest + ": pass an earlier since to see it"
	case in.Since != "":
		e.Note = "no feedback in this window, and none older matches the filters either"
	default:
		e.Note = "no tester feedback matches these filters"
	}
	return e
}

// parseSince accepts YYYY-MM-DD (UTC midnight) or an RFC 3339 time.
func parseSince(s string) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, tool.InvalidArgs("since must be last_seen, YYYY-MM-DD or an RFC 3339 time")
}

// laterThan compares two RFC 3339 timestamps; unparsable values sort last.
func laterThan(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil || errB != nil {
		return errA == nil && errB != nil
	}
	return ta.After(tb)
}

// scanFeedback pages newest-first and stops at the first item not after
// since (Apple has no date filter). With a since and more unseen items than
// limit, the oldest unseen ones are returned so last_seen can advance in
// order. builds maps build ids to build numbers.
func scanFeedback(ctx context.Context, env *tool.Env, appID string, k feedbackKind, filters url.Values,
	since time.Time, limit int,
) (items []tool.Resource, builds map[string]string, more bool, older string, err error) {
	fields := []string{"createdDate", "comment", "email", "deviceModel", "osVersion", "locale", "appPlatform", "build"}
	if k == kindScreenshots {
		fields = append(fields, "screenshots")
	}
	opts := tool.ListOptions{
		Query:    filters,
		Fields:   map[string][]string{k.resource: fields, "builds": {"version"}},
		Include:  []string{"build"},
		Limit:    feedbackPage,
		MaxItems: feedbackPage,
	}
	builds = map[string]string{}
	var unseen []tool.Resource
	stopped := false
	for scanned := 0; ; {
		page, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/"+k.resource, opts)
		if err != nil {
			return nil, nil, false, "", fmt.Errorf("list %s feedback: %w", k.name, err)
		}
		for _, b := range page.Included {
			if b.Type == "builds" {
				builds[b.ID] = b.Str("version")
			}
		}
		for _, r := range page.Data {
			if !since.IsZero() {
				t, perr := time.Parse(time.RFC3339Nano, r.Str("createdDate"))
				if perr == nil && !t.After(since) {
					stopped, older = true, r.Str("createdDate")
					break
				}
			}
			unseen = append(unseen, r)
		}
		scanned += len(page.Data)
		switch {
		case stopped || page.NextCursor == "":
		case since.IsZero() && len(unseen) > limit:
			more = true
		case scanned >= feedbackMaxScan:
			more = true
		default:
			opts = tool.ListOptions{Cursor: page.NextCursor, MaxItems: feedbackPage}
			continue
		}
		break
	}
	if len(unseen) > limit {
		more = true
		if since.IsZero() {
			unseen = unseen[:limit]
		} else {
			unseen = unseen[len(unseen)-limit:]
		}
	}
	return unseen, builds, more, older, nil
}

func feedbackFromResource(r tool.Resource, k feedbackKind, builds map[string]string) feedbackOut {
	o := feedbackOut{
		ID: r.ID, Kind: k.name, Created: r.Str("createdDate"),
		Build: builds[r.RelID("build")], Platform: r.Str("appPlatform"),
	}
	o.Device = o.cleanDeviceField(tool.DeviceModel, r.Str("deviceModel")) // trusted: shape-checked by tool.DeviceField
	o.OSVersion = o.cleanDeviceField(tool.OSVersion, r.Str("osVersion"))  // trusted: shape-checked by tool.DeviceField
	o.Locale = o.cleanDeviceField(tool.Locale, r.Str("locale"))
	o.Tester, o.Comment = r.Text("email"), r.Text("comment")
	shots, _ := tool.Unwrap(r.Attributes["screenshots"]).([]any)
	for _, s := range shots {
		m, _ := s.(map[string]any)
		f := func(k string) int { v, _ := m[k].(float64); return int(v) }
		so := screenshotOut{Width: f("width"), Height: f("height")}
		so.Expires, _ = m["expirationDate"].(string)
		o.Screenshots = append(o.Screenshots, so)
	}
	o.urls = screenshotURLs(r)
	return o
}

// cleanDeviceField keeps s when it has the field's shape and drops it
// otherwise (counting the drop), so a tester's device can't carry
// instructions. Empty values are neither kept nor counted.
func (o *feedbackOut) cleanDeviceField(kind tool.DeviceKind, s string) string {
	clean, dropped := tool.DeviceField(kind, s)
	if dropped {
		o.dropped++
	}
	return clean
}

// screenshotURLs returns the signed screenshot URLs of a feedback item.
func screenshotURLs(r tool.Resource) []string {
	shots, _ := tool.Unwrap(r.Attributes["screenshots"]).([]any)
	urls := make([]string, 0, len(shots))
	for _, s := range shots {
		m, _ := s.(map[string]any)
		u, _ := m["url"].(string)
		urls = append(urls, u)
	}
	return urls
}

// attachScreenshots fetches screenshots (signed, expiring URLs) without auth
// under the URL policy, at most feedbackMaxImages images of at most
// feedbackMaxImageBytes each; URLs themselves are never returned.
func attachScreenshots(ctx context.Context, env *tool.Env, out *feedbackListOut, res *tool.Result) {
	for i := range out.Items {
		it := &out.Items[i]
		urls := it.urls
		for j := range it.Screenshots {
			s := &it.Screenshots[j]
			if j >= len(urls) || urls[j] == "" {
				s.Skipped = "no url"
				continue
			}
			if len(res.Images) >= feedbackMaxImages {
				s.Skipped = fmt.Sprintf("image cap reached (%d per call); narrow with build/since/limit", feedbackMaxImages)
				continue
			}
			fr, err := env.Fetch.Fetch(ctx, http.MethodGet, urls[j], nil, nil, feedbackMaxImageBytes)
			switch {
			case err != nil:
				s.Skipped = "not fetched: larger than 1 MB, expired or refused by the URL policy"
				continue
			case fr.Status != http.StatusOK:
				s.Skipped = fmt.Sprintf("not fetched: HTTP %d", fr.Status)
				continue
			}
			mime := fr.Header.Get("Content-Type")
			if !strings.HasPrefix(mime, "image/") {
				mime = http.DetectContentType(fr.Body)
			}
			if !strings.HasPrefix(mime, "image/") {
				s.Skipped = "not an image"
				continue
			}
			res.WithUntrustedImage(strings.TrimSpace(strings.Split(mime, ";")[0]), fr.Body)
			s.Image = len(res.Images)
		}
	}
}

// attachCrashLogs reads the log text of the first feedbackMaxCrashLogs
// crashes: the full text goes to an artifact linked from res, the inline
// excerpt is truncated to feedbackMaxCrashBytes. Without an artifact store a
// truncated log warns that the rest is not available.
func attachCrashLogs(ctx context.Context, env *tool.Env, out *feedbackListOut, result *tool.Result) {
	unstored := 0
	n := 0
	for i := range out.Items {
		it := &out.Items[i]
		if it.Kind != kindCrashes.name {
			continue
		}
		if n == feedbackMaxCrashLogs {
			it.CrashLogNote = fmt.Sprintf("not fetched: at most %d crash logs per call", feedbackMaxCrashLogs)
			continue
		}
		n++
		res, err := env.ASC.Get(ctx, "/v1/betaFeedbackCrashSubmissions/"+it.ID+"/crashLog",
			url.Values{"fields[betaCrashLogs]": {"logText"}})
		if err != nil {
			if tool.IsNotFound(err) {
				it.CrashLogNote = "no crash log available"
			} else {
				it.CrashLogNote = "not fetched: " + err.Error()
			}
			continue
		}
		r, ok := res.First()
		if !ok || r.Str("logText") == "" {
			it.CrashLogNote = "no crash log available"
			continue
		}
		full := r.Str("logText")
		text, cut := truncateUTF8(full, feedbackMaxCrashBytes)
		u := tool.Untrusted(text)
		it.CrashLog, it.CrashLogTruncated = &u, cut
		link, err := env.Artifacts.Put(ctx, "crash-"+it.ID+".txt", "text/plain", []byte(full))
		switch {
		case err == nil:
			link.Title = "Full TestFlight crash log (tester device data, untrusted)"
			result.WithLink(link)
			it.CrashLogResource = link.URI
		case cut:
			unstored++
		}
	}
	if unstored > 0 {
		result.Warn(fmt.Sprintf("%d crash logs are truncated to %d bytes and could not be stored as resources", unstored, feedbackMaxCrashBytes))
	}
}
