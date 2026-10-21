package metadata

import (
	"context"
	"fmt"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "metadata_plan", Toolset: tool.Metadata, Class: tool.Read, Idempotent: true,
		Title: "Plan store listing changes", UI: "metadata",
		Description: "Full listing edits (release notes: release_prepare). Diffs listing, fastlane_dir or expo_config " +
			"against the version, checks limits, returns plan_id for metadata_apply; omitted fields stay. version:live: " +
			"promo text only. Nothing editable: preview.",
		Enums: map[string][]any{"platform": platformEnum},
	}, metadataPlan))
}

func metadataPlan(ctx context.Context, env *tool.Env, in sourceIn) (*tool.Result, error) {
	p, err := computePlan(ctx, env, in, true)
	if err != nil {
		return nil, err
	}
	out := p.out()
	res := tool.OK(out)
	for _, n := range p.notes {
		res.Warn(n)
	}
	if p.t.FellBack {
		applicable := false
		out.Applicable, out.Reason = &applicable, "no editable version; create one with release_prepare"
		res.Data = out
		return res.Then(tool.NextStep{Tool: "release_prepare", Args: map[string]any{"app": p.t.App.ID, "platform": string(p.t.Platform)}}), nil
	}
	switch out.Status {
	case statusInvalid:
		return res.Warn("fix the errors and plan again; no plan was issued"), nil
	case statusNoChanges:
		return res, nil
	}
	id, err := env.Plans.Propose(ctx, "metadata_apply", p.key, p.plan.Fingerprint(), p.preview())
	if err != nil {
		return nil, fmt.Errorf("propose metadata_apply: %w", err)
	}
	out.PlanID = id
	res.Data = out
	args := p.nextArgs(in)
	args["plan_id"] = id
	return res.Then(tool.NextStep{Tool: "metadata_apply", Args: args}), nil
}
