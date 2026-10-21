package metadata

import (
	"context"
	"errors"
	"fmt"

	"github.com/idoo/app-store-connect-mcp/internal/metadata"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
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
		Name: "metadata_apply", Toolset: tool.Metadata, Class: tool.Destructive, Confirm: tool.ConfirmByPlan,
		Title: "Apply store listing changes",
		Description: "Apply a metadata_plan: same arguments plus plan_id; refused if the listing changed since. Creates " +
			"missing locales; deletes only locales in remove.",
		Enums: map[string][]any{"platform": platformEnum},
	}, metadataApply))
}

func metadataApply(ctx context.Context, env *tool.Env, in applyIn) (*tool.Result, error) {
	p, err := computePlan(ctx, env, in.sourceIn, false)
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
	gate, pv := tool.Preview, p.preview()
	if p.t.Version.State == versionStateLive {
		// Promotional text on the live version goes public at once, with no
		// App Review in between: a human must approve.
		gate = tool.PreviewHuman
		pv.Irreversible = append(pv.Irreversible, "promotional text is public on the live product page immediately (no review)")
	}
	wctx, res := gate(ctx, env, in.PlanID, p.key, p.plan.Fingerprint(), pv)
	if res != nil {
		return res, nil
	}
	items, err := metadata.Apply(wctx, env.ASC, p.plan, metadata.Targets{VersionID: p.t.Version.ID, AppInfoID: p.appInfoID},
		func(done, total int) { env.Progress(float64(done), float64(total), "localizations") })
	if err != nil {
		return nil, err
	}
	ao := applyOut{App: p.t.appOut(), Platform: p.t.Platform, Version: p.t.versionOut(), Items: items}
	var unknown, failed []error
	for _, it := range items {
		switch it.Outcome {
		case metadata.OutcomeOK:
			ao.Applied++
		case metadata.OutcomeAlready:
			ao.Already++
		default:
			ao.Failed++
			e := fmt.Errorf("%s %s: %w", it.Level, it.Locale, it.Err)
			if errors.Is(it.Err, tool.ErrOutcomeUnknown) {
				unknown = append(unknown, e)
			} else {
				failed = append(failed, e)
			}
		}
	}
	replan := tool.NextStep{Tool: "metadata_plan", Args: p.nextArgs(in.sourceIn)}
	if ao.Failed > 0 && ao.Applied+ao.Already == 0 {
		// Outcome-unknown items first, so the result reports outcome_unknown
		// whenever any change may have been applied.
		return tool.Batch(ao, 0, 0, append(unknown, failed...), replan), nil
	}
	if ao.Failed > 0 {
		ao.Status = "partial"
		return tool.OK(ao).Warn(fmt.Sprintf("%d change(s) failed; re-run metadata_plan to see what remains", ao.Failed)).Then(replan), nil
	}
	ao.Status = "applied"
	return tool.OK(ao).Then(tool.NextStep{Tool: "release_status", Args: map[string]any{
		"app": p.t.App.ID, "platform": string(p.t.Platform), "version": p.t.Version.String,
	}}), nil
}
