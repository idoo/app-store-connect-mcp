package metadata

import (
	"context"
	"fmt"

	"github.com/idoo/hamn/internal/metadata"
	"github.com/idoo/hamn/internal/tool"
)

type applyIn struct {
	tool.ConfirmPlan
	sourceIn
}

type applyOut struct {
	Status   string                `json:"status"`
	App      appOut                `json:"app"`
	Platform tool.Platform         `json:"platform"`
	Version  versionOut            `json:"version"`
	Applied  int                   `json:"applied"`
	Already  int                   `json:"already"`
	Failed   int                   `json:"failed"`
	Items    []metadata.ItemResult `json:"items"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "metadata_apply", Toolset: tool.Metadata, Class: tool.Write, Confirm: tool.ConfirmByPlan,
		Title: "Apply store listing changes",
		Description: "Apply a metadata_plan: same arguments plus plan_id; refused if the listing changed since. Creates " +
			"missing locales; deletes only locales in remove.",
		Enums: map[string][]any{"platform": platformEnum},
	}, metadataApply))
}

func metadataApply(ctx context.Context, env *tool.Env, in applyIn) (*tool.Result, error) {
	p, err := computePlan(ctx, env, in.sourceIn)
	if err != nil {
		return nil, err
	}
	out := p.out()
	switch out.Status {
	case statusInvalid:
		return nil, tool.WithNext(fmt.Errorf("%w: listing has %d error(s), first: %s %s: %s", tool.ErrInvalidArgs,
			len(p.plan.Errors), p.plan.Errors[0].Locale, p.plan.Errors[0].Field, p.plan.Errors[0].Message),
			tool.NextStep{Tool: "metadata_plan", Args: p.nextArgs(in.sourceIn)})
	case statusNoChanges:
		return tool.Already(out), nil
	}
	wctx, res := tool.Preview(ctx, env, in.PlanID, p.key, p.plan.Fingerprint(), p.preview())
	if res != nil {
		return res, nil
	}
	items, err := metadata.Apply(wctx, env.ASC, p.plan, metadata.Targets{VersionID: p.t.Version.ID, AppInfoID: p.appInfoID},
		func(done, total int) { env.Progress(float64(done), float64(total), "localizations") })
	if err != nil {
		return nil, err
	}
	ao := applyOut{App: p.t.appOut(), Platform: p.t.Platform, Version: p.t.versionOut(), Items: items}
	for _, it := range items {
		switch it.Outcome {
		case metadata.OutcomeOK:
			ao.Applied++
		case metadata.OutcomeAlready:
			ao.Already++
		default:
			ao.Failed++
		}
	}
	ao.Status = "applied"
	r := tool.OK(ao)
	if ao.Failed > 0 {
		ao.Status = "partial"
		r = tool.OK(ao).Warn(fmt.Sprintf("%d change(s) failed; re-run metadata_plan to see what remains", ao.Failed))
	}
	return r.Then(tool.NextStep{Tool: "release_status", Args: map[string]any{
		"app": p.t.App.ID, "platform": string(p.t.Platform), "version": p.t.Version.String,
	}}), nil
}
