package uploads

import (
	"context"
	"net/url"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	packsMax    = 200
	versionsMax = 20
	packFanOut  = 4
)

type packsIn struct {
	App      tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Archived bool        `json:"archived,omitempty" jsonschema:"include archived packs"`
}

type packOut struct {
	ID         string       `json:"id"`
	Identifier string       `json:"asset_pack_identifier"`
	Archived   bool         `json:"archived,omitempty"`
	UsedBytes  int          `json:"used_bytes,omitempty"`
	Created    string       `json:"createdDate,omitempty"`
	Versions   []packVerOut `json:"versions"`
	Error      any          `json:"error,omitempty"`
}

type packVerOut struct {
	ID        string            `json:"id"`
	Version   string            `json:"version"`
	State     string            `json:"state"`
	Platforms []any             `json:"platforms,omitempty"`
	Locale    string            `json:"locale,omitempty"`
	Created   string            `json:"createdDate,omitempty"`
	Releases  map[string]string `json:"releases,omitempty"`
	Errors    []stateDetail     `json:"errors,omitempty"`
	Warnings  []stateDetail     `json:"warnings,omitempty"`
	Infos     []stateDetail     `json:"infos,omitempty"`
}

// stateDetail is Apple's processing message (e.g. ITMS-91139); its text
// echoes the uploaded asset pack, so it is untrusted.
type stateDetail struct {
	Code        string         `json:"code,omitempty"`
	Description tool.Untrusted `json:"description,omitempty"`
}

// releaseRels maps version relationships to the release states shown.
var releaseRels = map[string]string{
	"appStoreRelease": "app_store", "externalBetaRelease": "external_beta", "internalBetaRelease": "internal_beta",
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "asset_packs_list", Toolset: tool.Uploads, Class: tool.Read, Idempotent: true,
		Title: "List Background Assets asset packs",
		Description: "Apple-hosted asset packs (Background Assets) of an app: versions with state, platforms, " +
			"processing errors/warnings and App Store / TestFlight release state.",
	}, assetPacksList))
}

func assetPacksList(ctx context.Context, env *tool.Env, in packsIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	q := url.Values{"sort": {"assetPackIdentifier"}}
	if !in.Archived {
		q.Set("filter[archived]", "false")
	}
	res, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(app.ID)+"/backgroundAssets", tool.ListOptions{
		Query:    q,
		Fields:   map[string][]string{"backgroundAssets": {"archived", "assetPackIdentifier", "createdDate", "usedBytes"}},
		MaxItems: packsMax,
	})
	if err != nil {
		return tool.Fail(err), nil
	}
	packs := make([]packOut, len(res.Data))
	if err := tool.Parallel(ctx, packFanOut, res.Data, func(ctx context.Context, i int, r tool.Resource) error {
		p := packOut{ID: r.ID, Identifier: r.Str("assetPackIdentifier"), Created: r.Str("createdDate"), Versions: []packVerOut{}}
		p.Archived, _ = r.Bool("archived")
		p.UsedBytes, _ = r.Int("usedBytes")
		vs, err := packVersions(ctx, env, r.ID)
		if err != nil {
			p.Error = tool.BodyFor(err)
		} else {
			p.Versions = vs
		}
		packs[i] = p
		return nil
	}); err != nil {
		return nil, err
	}
	out := map[string]any{
		"app":   map[string]any{"id": app.ID, "name": tool.Untrusted(app.Name), "bundle_id": app.BundleID},
		"packs": packs, "returned": len(packs),
	}
	if res.NextCursor != "" {
		out["more"] = true
	}
	return tool.OK(out), nil
}

// packVersions reads a pack's newest versions with their release states.
func packVersions(ctx context.Context, env *tool.Env, packID string) ([]packVerOut, error) {
	res, err := env.ASC.List(ctx, "/v1/backgroundAssets/"+url.PathEscape(packID)+"/versions", tool.ListOptions{
		Query:   url.Values{"sort": {"-version"}},
		Include: []string{"appStoreRelease", "externalBetaRelease", "internalBetaRelease"},
		Fields: map[string][]string{
			"backgroundAssetVersions": {
				"createdDate", "platforms", "state", "stateDetails", "version", "locale",
				"appStoreRelease", "externalBetaRelease", "internalBetaRelease",
			},
			"backgroundAssetVersionAppStoreReleases":     {"state"},
			"backgroundAssetVersionExternalBetaReleases": {"state"},
			"backgroundAssetVersionInternalBetaReleases": {"state"},
		},
		Limit: versionsMax, MaxItems: versionsMax,
	})
	if err != nil {
		return nil, err
	}
	inc := tool.IndexIncluded(res.Included)
	out := make([]packVerOut, 0, len(res.Data))
	for _, v := range res.Data {
		o := packVerOut{ID: v.ID, Version: v.Str("version"), State: v.Str("state"), Locale: v.Str("locale"), Created: v.Str("createdDate")}
		o.Platforms, _ = v.Attributes["platforms"].([]any)
		if sd, ok := v.Attributes["stateDetails"].(map[string]any); ok {
			o.Errors, o.Warnings, o.Infos = details(sd["errors"]), details(sd["warnings"]), details(sd["infos"])
		}
		for rel, name := range releaseRels {
			id := v.RelID(rel)
			if id == "" {
				continue
			}
			state := ""
			for _, r := range inc {
				if r.ID == id && r.Type != "backgroundAssetVersions" {
					state = r.Str("state")
				}
			}
			if o.Releases == nil {
				o.Releases = map[string]string{}
			}
			o.Releases[name] = state
		}
		out = append(out, o)
	}
	return out, nil
}

func details(v any) []stateDetail {
	items, _ := v.([]any)
	var out []stateDetail
	for _, it := range items {
		m, _ := it.(map[string]any)
		code, _ := m["code"].(string)
		var desc tool.Untrusted
		switch d := m["description"].(type) {
		case string:
			desc = tool.Untrusted(d)
		case tool.Untrusted: // already wrapped by the client
			desc = d
		case map[string]any:
			s, _ := d["untrusted"].(string)
			desc = tool.Untrusted(s)
		}
		out = append(out, stateDetail{Code: code, Description: desc})
	}
	return out
}
