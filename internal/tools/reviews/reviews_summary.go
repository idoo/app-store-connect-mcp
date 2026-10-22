package reviews

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// summaryOut is one Apple-generated review summary for a platform.
type summaryOut struct {
	Platform  string         `json:"platform"`
	Locale    string         `json:"locale,omitempty"`
	Territory string         `json:"territory,omitempty"`
	Created   string         `json:"createdDate,omitempty"`
	Text      tool.Untrusted `json:"text"`
}

// reviewSummary reads Apple's customer review summarizations for every
// platform of app (one call each; filter[platform] is required). Summaries
// are Apple-generated from third-party reviews, so the text is untrusted.
func reviewSummary(ctx context.Context, env *tool.Env, app tool.App, territories []string) (map[string]any, error) {
	platforms := app.Platforms
	if len(platforms) == 0 {
		platforms = []tool.Platform{"IOS"}
	}
	var out []summaryOut
	for _, p := range platforms {
		q := url.Values{"filter[platform]": {string(p)}, "include": {"territory"}}
		if len(territories) > 0 {
			ts := make([]string, 0, len(territories))
			for _, t := range territories {
				ts = append(ts, strings.ToUpper(strings.TrimSpace(t)))
			}
			q.Set("filter[territory]", strings.Join(ts, ","))
		}
		res, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(app.ID)+"/customerReviewSummarizations",
			tool.ListOptions{Query: q, MaxItems: 50})
		if err != nil {
			return nil, fmt.Errorf("review summary %s: %w", p, err)
		}
		for _, r := range res.Data {
			platform := r.Str("platform")
			if platform == "" {
				platform = string(p)
			}
			out = append(out, summaryOut{
				Platform: platform, Locale: r.Str("locale"), Territory: r.RelID("territory"),
				Created: r.Str("createdDate"), Text: r.Text("text"),
			})
		}
	}
	if len(out) == 0 {
		return map[string]any{"status": "none_yet", "note": "Apple summarizes only apps with enough reviews"}, nil
	}
	return map[string]any{"status": "ok", "summaries": out}, nil
}
