package release

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// fetchAccessibility reads the app's Accessibility Nutrition Label
// declarations into s. A failure is recorded, never fatal: the check is
// advisory.
func fetchAccessibility(ctx context.Context, env *tool.Env, s *statusSnapshot) {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+s.App.ID+"/accessibilityDeclarations", url.Values{"limit": {"50"}})
	if err != nil {
		s.A11yErr = err.Error()
		return
	}
	s.A11y = resp.Data
}

// a11yFamilies are the device families whose labels the inspected platform
// needs; iPad only when the version has iPad screenshots.
func a11yFamilies(s *statusSnapshot) []string {
	switch s.Platform {
	case tool.IOS:
		fams := []string{"IPHONE"}
		for _, sets := range s.Screenshots {
			if slices.ContainsFunc(sets, func(set screenshotSet) bool { return strings.HasPrefix(string(set.Display), "APP_IPAD") }) {
				return append(fams, "IPAD")
			}
		}
		return fams
	case tool.MacOS:
		return []string{"MAC"}
	case tool.TVOS:
		return []string{"APPLE_TV"}
	case tool.VisionOS:
		return []string{"VISION"}
	}
	return nil
}

// checkAccessibility warns (never blocks) when a device family has no
// published Accessibility Nutrition Label.
func checkAccessibility(s *statusSnapshot) check {
	c := check{ID: "accessibility_labels", Status: statusOK, FixVia: &fixVia{
		Tool: "appinfo_update", Manual: ascURL(s.App.ID, "distribution/accessibility"),
	}}
	if s.A11yErr != "" {
		c.Status, c.Detail = statusWarn, "could not read accessibility declarations: "+s.A11yErr
		return c
	}
	for _, fam := range a11yFamilies(s) {
		state := ""
		for _, d := range s.A11y {
			if d.Str("deviceFamily") != fam {
				continue
			}
			if st := d.Str("state"); st == "PUBLISHED" || state == "" {
				state = st
			}
		}
		switch state {
		case "PUBLISHED":
		case "":
			c.Items = append(c.Items, fam+": no accessibility declaration")
		default:
			c.Items = append(c.Items, fmt.Sprintf("%s: declaration is %s; publish it", fam, state))
		}
	}
	if len(c.Items) > 0 {
		c.Status, c.Detail = statusWarn, "Accessibility Nutrition Labels missing or unpublished (not required for submission yet)"
		return c
	}
	c.Detail, c.FixVia = "Accessibility Nutrition Labels published", nil
	return c
}
