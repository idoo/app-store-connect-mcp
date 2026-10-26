package reports

import (
	"context"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tools/insights"
)

// portfolioApp is one app's in-flight versions in an every-app release
// section; the app name is third-party text.
type portfolioApp struct {
	ID       string           `json:"id"`
	Name     tool.Untrusted   `json:"name,omitempty"`
	InFlight []versionInsight `json:"in_flight"`
}

// portfolioRelease is the release section without an app (PO2): in-flight
// versions per app from ONE apps call with versions included (state only,
// no per-app preflight), ranked as digest_summary app:"all" ranks them.
func portfolioRelease(ctx context.Context, env *tool.Env) releaseInsight {
	ranked, err := insights.Portfolio(ctx, env)
	if err != nil {
		return releaseInsight{section: unavailable(reasonOf(err))}
	}
	out := releaseInsight{section: sectionOK, Apps: []portfolioApp{}}
	for _, p := range ranked {
		if len(p.InFlight) == 0 {
			continue
		}
		pa := portfolioApp{ID: p.App.ID, Name: tool.Untrusted(p.App.Name)}
		for _, v := range p.InFlight {
			pa.InFlight = append(pa.InFlight, versionInsight{ID: v.ID, Version: v.Version, Platform: v.Platform, State: v.State})
		}
		out.Apps = append(out.Apps, pa)
	}
	return out
}
