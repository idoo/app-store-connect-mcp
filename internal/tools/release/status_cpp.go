package release

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// checkPendingDisplays reports announced screenshot slots the pinned spec
// may lack (iPhone Duo): warn before their required-from date; after it,
// block only when the spec can upload the slot, else warn with the manual
// route.
func checkPendingDisplays(s *statusSnapshot) check {
	c := check{ID: "iphone_duo_screenshots", Status: statusOK}
	uploadable := false
	for _, p := range applemeta.PendingDisplays(s.Platform) {
		if hasDisplay(s, p.Type) {
			continue
		}
		day := p.RequiredFrom.Format("2006-01-02")
		c.Items = append(c.Items, fmt.Sprintf("%s: no screenshots; required for submissions from %s", p.Type, day))
		switch {
		case s.Now.Before(p.RequiredFrom):
			if c.Status != statusBlock {
				c.Status, c.Detail = statusWarn, "required for submissions from "+day+"; add them on the web (not in the API yet)"
			}
		case s.Uploadable[p.Type]:
			c.Status, c.Detail, uploadable = statusBlock, "required since "+day, true
		case c.Status != statusBlock:
			c.Status = statusWarn
			c.Detail = "Apple requires iPhone Duo screenshots; this server's spec can't upload them yet — upload in App Store Connect"
		}
	}
	switch {
	case c.Status == statusOK:
		c.Detail = "pending display types covered"
	case uploadable:
		c.FixVia = fixTool("screenshots_plan")
	default:
		c.FixVia = fixURL(versionURL(s.App.ID, s.Platform))
	}
	return c
}

func hasDisplay(s *statusSnapshot, d applemeta.DisplayType) bool {
	for _, sets := range s.Screenshots {
		if slices.ContainsFunc(sets, func(set screenshotSet) bool { return set.Display == d && len(set.States) > 0 }) {
			return true
		}
	}
	return false
}

// pendingUploadable reports which pending slots of platform the pinned
// spec accepts as a ScreenshotDisplayType (so the API can upload them).
func pendingUploadable(spec tool.Spec, platform tool.Platform) map[applemeta.DisplayType]bool {
	m := map[applemeta.DisplayType]bool{}
	for _, p := range applemeta.PendingDisplays(platform) {
		m[p.Type] = specKnowsDisplay(spec, p.Type)
	}
	return m
}

func specKnowsDisplay(spec tool.Spec, d applemeta.DisplayType) bool {
	return spec.Validate(tool.Request{
		Method: "GET", Path: "/v1/appStoreVersionLocalizations/x/appScreenshotSets",
		Query: url.Values{"filter[screenshotDisplayType]": {string(d)}},
	}) == nil
}

// cppLocale is one custom product page localization and its keywords.
type cppLocale struct {
	Page, Locale string
	Keywords     []string
}

// cppVersionOrder prefers the editable page version over the live one.
var cppVersionOrder = []string{"PREPARE_FOR_SUBMISSION", "READY_FOR_REVIEW", "WAITING_FOR_REVIEW", "IN_REVIEW", "ACCEPTED", "APPROVED", "REJECTED"}

// fetchCPPKeywords reads the search keywords of the app's custom product
// pages into s. A failure is recorded, never fatal: the check is advisory.
func fetchCPPKeywords(ctx context.Context, env *tool.Env, s *statusSnapshot) {
	if s.Platform != tool.IOS || s.Version == nil {
		return
	}
	cpps, err := loadCPPKeywords(ctx, env, s.App.ID)
	if err != nil {
		s.CPPErr = err.Error()
		return
	}
	s.CPPs = cpps
}

func loadCPPKeywords(ctx context.Context, env *tool.Env, appID string) ([]cppLocale, error) {
	pages, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/appCustomProductPages", url.Values{
		"fields[appCustomProductPages]":        {"name,appCustomProductPageVersions"},
		"include":                              {"appCustomProductPageVersions"},
		"fields[appCustomProductPageVersions]": {"state"},
		"limit[appCustomProductPageVersions]":  {"10"},
		"limit":                                {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("list custom product pages: %w", err)
	}
	state := map[string]string{}
	for _, r := range pages.Included {
		if r.Type == "appCustomProductPageVersions" {
			state[r.ID] = r.Str("state")
		}
	}
	var out []cppLocale
	for _, p := range pages.Data {
		vid, rank := "", len(cppVersionOrder)
		for _, id := range p.RelIDs("appCustomProductPageVersions") {
			if i := slices.Index(cppVersionOrder, state[id]); i >= 0 && i < rank {
				vid, rank = id, i
			}
		}
		if vid == "" {
			continue
		}
		locs, err := env.ASC.Get(ctx, "/v1/appCustomProductPageVersions/"+vid+"/appCustomProductPageLocalizations", url.Values{
			"fields[appCustomProductPageLocalizations]": {"locale"},
			"limit": {"50"},
		})
		if err != nil {
			return nil, fmt.Errorf("list custom product page localizations: %w", err)
		}
		for _, l := range locs.Data {
			kws, err := env.ASC.Get(ctx, "/v1/appCustomProductPageLocalizations/"+l.ID+"/searchKeywords", url.Values{"limit": {"50"}})
			if err != nil {
				return nil, fmt.Errorf("list custom product page keywords: %w", err)
			}
			cl := cppLocale{Page: p.Str("name"), Locale: l.Str("locale")} // trusted: the developer's own page name
			for _, k := range kws.Data {
				cl.Keywords = append(cl.Keywords, k.ID) // appKeywords ids are the keywords
			}
			out = append(out, cl)
		}
	}
	return out, nil
}

// keywordSet splits a comma-separated keywords field, case-folded.
func keywordSet(v string) map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Split(v, ",") {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			m[k] = true
		}
	}
	return m
}

// checkCPPKeywords warns when a custom product page keyword is missing from
// the version's keywords for that locale (Apple only matches page keywords
// that are in the app's keywords). Never blocks.
func checkCPPKeywords(s *statusSnapshot) check {
	c := check{ID: "cpp_keywords", Status: statusOK}
	if s.CPPErr != "" {
		c.Status, c.Detail = statusWarn, "could not check custom product page keywords: "+s.CPPErr
		return c
	}
	byLocale := map[string]map[string]bool{}
	for _, l := range s.Version.localizations() {
		byLocale[l.Str("locale")] = keywordSet(l.Str("keywords")) // trusted: compared, never echoed
	}
	for _, p := range s.CPPs {
		have, ok := byLocale[p.Locale]
		if !ok {
			have = byLocale[s.AppAttrs.Str("primaryLocale")]
		}
		for _, k := range p.Keywords {
			if !have[strings.ToLower(strings.TrimSpace(k))] {
				c.Items = append(c.Items, fmt.Sprintf("%q (%s): %q not in version keywords", p.Page, p.Locale, k))
			}
		}
	}
	if len(c.Items) == 0 {
		c.Detail = fmt.Sprintf("%d custom product page localization(s) use version keywords only", len(s.CPPs))
		return c
	}
	slices.Sort(c.Items)
	c.Status, c.Detail = statusWarn, fmt.Sprintf("%d custom product page keyword(s) missing from the version's keywords", len(c.Items))
	c.FixVia = &fixVia{Tool: "metadata_plan", Args: map[string]any{"app": s.App.ID, "platform": string(s.Platform)}}
	return c
}
