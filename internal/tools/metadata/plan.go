package metadata

import (
	"context"
	"fmt"

	"github.com/idoo/hamn/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "metadata_plan", Toolset: tool.Metadata, Class: tool.Read, Idempotent: true,
		Title: "Plan store listing changes",
		Description: "Use for full listing edits (for release notes only, use release_prepare). Diffs one source (listing, " +
			"fastlane_dir or expo_config) against the editable version, checks limits, returns plan_id for metadata_apply. " +
			"Omitted locales/fields stay.",
		Enums: map[string][]any{"platform": platformEnum},
	}, metadataPlan))
}

func metadataPlan(ctx context.Context, env *tool.Env, in sourceIn) (*tool.Result, error) {
	p, err := computePlan(ctx, env, in)
	if err != nil {
		return nil, err
	}
	out := p.out()
	switch out.Status {
	case statusInvalid:
		return tool.OK(out).Warn("fix the errors and plan again; no plan was issued"), nil
	case statusNoChanges:
		return tool.OK(out), nil
	}
	id, err := env.Plans.Propose(ctx, "metadata_apply", p.key, p.plan.Fingerprint(), p.preview())
	if err != nil {
		return nil, fmt.Errorf("propose metadata_apply: %w", err)
	}
	out.PlanID = id
	args := p.nextArgs(in)
	args["plan_id"] = id
	return tool.OK(out).Then(tool.NextStep{Tool: "metadata_apply", Args: args}), nil
}
