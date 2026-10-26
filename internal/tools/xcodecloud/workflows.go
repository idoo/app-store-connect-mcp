package xcodecloud

import (
	"context"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "xcc_workflows_list", Toolset: tool.XcodeCloud, Class: tool.Read, Idempotent: true,
		Title: "List Xcode Cloud workflows",
		Description: "Xcode Cloud products of the app (all apps when none is given or pinned) with their workflows: id, name, enabled, " +
			"repository, start conditions summary and actions. Use the workflow name or id with xcc_build_start and xcc_build_status.",
	}, workflowsList))
}

type workflowsListIn struct {
	App tool.AppRef `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app, else every app"`
}

type workflowsListOut struct {
	AppID    string       `json:"app_id,omitempty"`
	Products []productOut `json:"products"`
}

// maxProducts caps the products whose workflows are read when no app is
// given (one call per product).
const maxProducts = 20

func workflowsList(ctx context.Context, env *tool.Env, in workflowsListIn) (*tool.Result, error) {
	products, appID, err := listProducts(ctx, env, in.App)
	if err != nil {
		return nil, err
	}
	out := workflowsListOut{AppID: appID, Products: []productOut{}}
	shown := products[:min(len(products), maxProducts)]
	lists, err := productWorkflows(ctx, env, shown)
	if err != nil {
		return nil, err
	}
	for i, p := range shown {
		out.Products = append(out.Products, productOut{
			ID: p.ID, Name: p.Str("name"), Type: p.Str("productType"), AppID: p.RelID("app"), Workflows: lists[i],
		})
	}
	res := tool.OK(out)
	if len(products) > maxProducts {
		res.Warn("more than 20 Xcode Cloud products; pass app to see the others")
	}
	if len(products) == 0 {
		res.Warn("no Xcode Cloud product found; set up Xcode Cloud for the app in Xcode first")
	}
	return res, nil
}
