package screenshots

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/upload"
)

// Processing wait bounds (seconds).
const (
	defaultWait = 45
	maxWait     = 300
)

// Sync item actions.
const (
	actionUploaded     = "uploaded"
	actionSkipped      = "skipped"
	actionDeleted      = "deleted"
	actionSetCreated   = "set_created"
	actionReordered    = "reordered"
	actionFailed       = "failed"
	actionNotAttempted = "not_attempted"
)

type syncIn struct {
	tool.ConfirmPlan
	sourceIn
	Wait int `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for Apple to process uploads (default 45, max 300)"`
}

// itemOut is the outcome of one sync step.
type itemOut struct {
	Locale  string                `json:"locale"`
	Display applemeta.DisplayType `json:"display"`
	Action  string                `json:"action"`
	File    string                `json:"file,omitempty"`
	ID      string                `json:"id,omitempty"`
	State   string                `json:"state,omitempty"`
	Error   string                `json:"error,omitempty"`
}

type syncOut struct {
	Status   string        `json:"status"`
	App      appOut        `json:"app"`
	Platform tool.Platform `json:"platform"`
	Version  versionOut    `json:"version"`
	Mode     string        `json:"mode"`
	Uploaded int           `json:"uploaded"`
	Skipped  int           `json:"skipped"`
	Deleted  int           `json:"deleted"`
	Failed   int           `json:"failed"`
	Pending  int           `json:"pending"`
	Items    []itemOut     `json:"items"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "screenshots_sync", Toolset: tool.Screenshots, Class: tool.Write, Confirm: tool.ConfirmByPlan,
		Title: "Sync screenshots",
		Description: "Applies a screenshots_plan: same arguments plus confirm=plan_id (required when anything is deleted). " +
			"Creates sets, uploads, deletes the listed screenshots, reorders. Refused if the remote sets changed since planning.",
		Enums: map[string][]any{"platform": platformEnum, "mode": modeEnum},
	}, screenshotsSync))
}

func screenshotsSync(ctx context.Context, env *tool.Env, in syncIn) (*tool.Result, error) {
	wait := in.Wait
	switch {
	case wait <= 0:
		wait = defaultWait
	case wait > maxWait:
		wait = maxWait
	}
	p, err := computePlan(ctx, env, in.sourceIn)
	if err != nil {
		return nil, err
	}
	po := p.out()
	switch po.Status {
	case statusInvalid:
		pr := p.problems[0]
		return nil, tool.WithNext(fmt.Errorf("%w: %d problem(s), first: %s %s: %s", tool.ErrInvalidArgs, len(p.problems), pr.File, pr.Kind, pr.Message),
			tool.NextStep{Tool: "screenshots_plan", Args: p.nextArgs()})
	case statusNoChanges:
		return tool.Already(po), nil
	}
	fp, err := p.fingerprint()
	if err != nil {
		return nil, err
	}
	var wctx context.Context
	var res *tool.Result
	if in.Confirm == "" && p.deletes() == 0 {
		wctx, res = tool.Unconfirmed(ctx, env)
	} else {
		wctx, res = tool.Preview(ctx, env, in.Confirm, p.key, fp, p.preview())
	}
	if res != nil {
		return res, nil
	}

	deps := upload.DepsFromEnv(env)
	deps.Progress = nil // file-level progress below
	out := syncOut{App: p.t.appOut(), Platform: p.t.Platform, Version: p.t.versionOut(), Mode: p.mode, Items: []itemOut{}}
	total := float64(po.Summary.Uploads + po.Summary.Deletes)
	done := 0.0
	step := func() {
		done++
		env.Progress(done, total, "screenshots")
	}
	var pending []int // indexes into out.Items of uploads still processing
	for _, sp := range p.sets {
		if !sp.changed() {
			for _, s := range sp.Skips {
				out.Items = append(out.Items, itemOut{Locale: sp.Locale, Display: sp.Display, Action: actionSkipped, File: s.File, ID: s.ID})
			}
			continue
		}
		pending = append(pending, syncSet(wctx, env, deps, sp, &out, step)...)
	}
	for _, it := range out.Items {
		switch it.Action {
		case actionUploaded:
			out.Uploaded++
		case actionSkipped:
			out.Skipped++
		case actionDeleted:
			out.Deleted++
		case actionFailed, actionNotAttempted:
			out.Failed++
		}
	}
	if len(pending) > 0 {
		pending, err = waitDelivery(ctx, env, &out, pending, time.Duration(wait)*time.Second)
		if err != nil {
			return nil, err
		}
	}
	out.Pending = len(pending)
	listNext := tool.NextStep{Tool: "screenshots_list", Args: map[string]any{"app": p.t.App.ID, "platform": string(p.t.Platform), "version": p.t.Version.String}}
	switch {
	case out.Failed > 0:
		out.Status = "partial"
		return tool.OK(out).Warn(fmt.Sprintf("%d step(s) failed; run screenshots_plan again to see what remains", out.Failed)).
			Then(tool.NextStep{Tool: "screenshots_plan", Args: p.nextArgs()}), nil
	case out.Pending > 0:
		out.Status = "uploaded"
		return tool.StillProcessing(out, "Apple is still processing the uploads; check delivery states with screenshots_list").Then(listNext), nil
	}
	out.Status = "synced"
	return tool.OK(out).Then(tool.NextStep{Tool: "release_status", Args: map[string]any{
		"app": p.t.App.ID, "platform": string(p.t.Platform), "version": p.t.Version.String,
	}}), nil
}

// syncSet applies one set plan, appending items to out. It returns the item
// indexes of uploads whose processing is still pending. A failure stops the
// set: its remaining steps are reported as not attempted.
func syncSet(ctx context.Context, env *tool.Env, deps upload.Deps, sp *setPlan, out *syncOut, step func()) []int {
	add := func(it itemOut) int {
		it.Locale, it.Display = sp.Locale, sp.Display
		out.Items = append(out.Items, it)
		return len(out.Items) - 1
	}
	for _, s := range sp.Skips {
		add(itemOut{Action: actionSkipped, File: s.File, ID: s.ID})
	}
	setID := sp.SetID
	abandon := func(fromUpload int, deletesLeft []deleteItem) {
		for _, d := range deletesLeft {
			add(itemOut{Action: actionNotAttempted, File: d.FileName, ID: d.ID})
		}
		for _, f := range sp.Uploads[fromUpload:] {
			add(itemOut{Action: actionNotAttempted, File: f.Rel})
		}
	}
	if sp.CreateSet {
		resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/appScreenshotSets", Write: true, Body: map[string]any{
			"data": map[string]any{
				"type":       "appScreenshotSets",
				"attributes": map[string]any{"screenshotDisplayType": string(sp.Display)},
				"relationships": map[string]any{"appStoreVersionLocalization": map[string]any{
					"data": map[string]string{"type": "appStoreVersionLocalizations", "id": sp.LocalizationID},
				}},
			},
		}})
		r, ok := resp.First()
		if err == nil && (!ok || r.ID == "") {
			err = errors.New("create set: response has no resource")
		}
		if err != nil {
			add(itemOut{Action: actionFailed, Error: tool.BodyFor(err).Message})
			abandon(0, sp.Deletes)
			return nil
		}
		setID = r.ID
		add(itemOut{Action: actionSetCreated, ID: setID})
	}

	deleteAll := func() bool {
		for i, d := range sp.Deletes {
			_, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodDelete, Path: "/v1/appScreenshots/" + d.ID, Write: true})
			var api *tool.APIError
			if err != nil && (!errors.As(err, &api) || api.Status != http.StatusNotFound) {
				add(itemOut{Action: actionFailed, File: d.FileName, ID: d.ID, Error: tool.BodyFor(err).Message})
				abandon(0, sp.Deletes[i+1:])
				return false
			}
			add(itemOut{Action: actionDeleted, File: d.FileName, ID: d.ID})
			step()
		}
		return true
	}
	// Delete first only when uploading first would exceed the set limit.
	deletesFirst := sp.remoteCount+len(sp.Uploads) > maxScreenshotsPerSet
	if deletesFirst && !deleteAll() {
		return nil
	}
	newIDs := make([]string, len(sp.Uploads))
	var pending []int
	for i, f := range sp.Uploads {
		r, err := uploadFile(ctx, env, deps, setID, f)
		if err != nil {
			idx := add(itemOut{Action: actionFailed, File: f.Rel, Error: tool.BodyFor(err).Message})
			if r != nil {
				out.Items[idx].ID, out.Items[idx].State = r.ID, r.State
			}
			left := sp.Deletes
			if deletesFirst {
				left = nil
			}
			abandon(i+1, left)
			return pending
		}
		newIDs[i] = r.ID
		idx := add(itemOut{Action: actionUploaded, File: f.Rel, ID: r.ID, State: r.State})
		if r.State != upload.StateComplete {
			pending = append(pending, idx)
		}
		step()
	}
	if !deletesFirst && !deleteAll() {
		return pending
	}
	if sp.Reorder {
		ids := make([]map[string]string, 0, len(sp.order))
		for _, s := range sp.order {
			id := s.id
			if s.upload >= 0 {
				id = newIDs[s.upload]
			}
			ids = append(ids, map[string]string{"type": "appScreenshots", "id": id})
		}
		_, err := env.ASC.Do(ctx, tool.Request{
			Method: http.MethodPatch, Path: "/v1/appScreenshotSets/" + setID + "/relationships/appScreenshots", Write: true,
			Body: map[string]any{"data": ids},
		})
		if err != nil {
			add(itemOut{Action: actionFailed, ID: setID, Error: "reorder: " + tool.BodyFor(err).Message})
			return pending
		}
		add(itemOut{Action: actionReordered, ID: setID})
	}
	return pending
}

// uploadFile runs the asset flow for one screenshot without waiting for
// processing (the caller waits for all uploads at once).
func uploadFile(ctx context.Context, env *tool.Env, deps upload.Deps, setID string, f localFile) (*upload.AssetResult, error) {
	fh, err := env.Files.Open(f.path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", f.Rel, err)
	}
	defer func() { _ = fh.Close() }()
	return upload.Asset(ctx, deps, upload.AssetSpec{
		CreatePath: "/v1/appScreenshots", Type: "appScreenshots",
		Parent:   upload.Relationship{Name: "appScreenshotSet", Type: "appScreenshotSets", ID: setID},
		FileName: path.Base(f.Rel), FileSize: f.Size, File: fh,
		Poll: upload.PollOptions{MaxWait: -1},
	})
}

// waitDelivery polls the pending uploads until they finish or the wait ends,
// updating their items, and returns the indexes still pending.
func waitDelivery(ctx context.Context, env *tool.Env, out *syncOut, pending []int, wait time.Duration) ([]int, error) {
	deadline := env.Clock.Now().Add(wait)
	for len(pending) > 0 {
		left := deadline.Sub(env.Clock.Now())
		if left <= 0 {
			break
		}
		if err := env.Clock.Sleep(ctx, min(upload.DefaultPollInterval, left)); err != nil {
			return nil, fmt.Errorf("wait for processing: %w", err)
		}
		still := pending[:0]
		for _, idx := range pending {
			it := &out.Items[idx]
			resp, err := env.ASC.Get(ctx, "/v1/appScreenshots/"+it.ID, url.Values{"fields[appScreenshots]": {"assetDeliveryState"}})
			if err != nil {
				return nil, fmt.Errorf("read screenshot %s: %w", it.ID, err)
			}
			r, _ := resp.First()
			it.State = deliveryState(r, "assetDeliveryState")
			switch it.State {
			case upload.StateComplete:
			case upload.StateFailed:
				it.Action, it.Error = actionFailed, "Apple processing FAILED"
				out.Uploaded--
				out.Failed++
			default:
				still = append(still, idx)
			}
		}
		pending = still
	}
	return pending, nil
}
