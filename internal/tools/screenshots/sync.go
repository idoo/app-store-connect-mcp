package screenshots

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/upload"
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

// Item states set by sync itself (Apple's delivery states pass through).
const (
	stateUnknown             = "unknown"              // processing wait interrupted
	stateAwaitingReplacement = "awaiting_replacement" // delete held: replacement not COMPLETE
)

type syncIn struct {
	tool.ConfirmPlan
	sourceIn
	Wait int `json:"wait_seconds,omitempty" jsonschema:"processing wait, default 45, max 300"`
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
	Code    string                `json:"code,omitempty"`
	// err is the typed failure behind Error, joined into the error result
	// when nothing was applied.
	err error
}

// failedItem is a failed step whose typed error class is kept.
func failedItem(file, id, step string, err error) itemOut {
	if step != "" {
		err = fmt.Errorf("%s: %w", step, err)
	}
	b := tool.BodyFor(err)
	return itemOut{Action: actionFailed, File: file, ID: id, Error: b.Message, Code: b.Error, err: err}
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
	// Held counts deletes (and reorders) kept back because their set's
	// replacements have not reached COMPLETE; they are not failures.
	Held    int       `json:"held,omitempty"`
	Pending int       `json:"pending"`
	Items   []itemOut `json:"items"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "screenshots_sync", Toolset: tool.Screenshots, Class: tool.Destructive, Confirm: tool.ConfirmByPlan,
		Title: "Sync screenshots",
		Description: "Applies a screenshots_plan: same arguments plus its plan_id (required). " +
			"Creates sets, uploads, deletes the listed screenshots, reorders. Refused if the remote sets changed since planning.",
		Enums: map[string][]any{"platform": platformEnum, "mode": modeEnum},
	}, screenshotsSync))
}

func screenshotsSync(ctx context.Context, env *tool.Env, in syncIn) (*tool.Result, error) {
	if in.PlanID == "" {
		return nil, tool.WithNext(tool.InvalidArgs("plan_id is required: run screenshots_plan first"),
			tool.NextStep{Tool: "screenshots_plan"})
	}
	wait := in.Wait
	switch {
	case wait <= 0:
		wait = defaultWait
	case wait > maxWait:
		wait = maxWait
	}
	if !env.HasProgress() {
		wait = min(wait, int(tool.NoProgressWaitCap/time.Second))
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
	wctx, res := tool.Preview(ctx, env, in.PlanID, p.key, fp, p.preview())
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
	var deferred []*deferredSet
	for _, sp := range p.sets {
		if !sp.changed() {
			for _, s := range sp.Skips {
				out.Items = append(out.Items, itemOut{Locale: sp.Locale, Display: sp.Display, Action: actionSkipped, File: s.File, ID: s.ID})
			}
			continue
		}
		pend, d := syncSet(wctx, env, deps, sp, &out, step)
		pending = append(pending, pend...)
		if d != nil {
			deferred = append(deferred, d)
		}
	}
	replan := tool.NextStep{Tool: "screenshots_plan", Args: p.nextArgs()}
	if applied, unknown, failed := tally(&out); out.Failed > 0 && applied == 0 {
		// Nothing changed remotely, so no upload is pending. Outcome-unknown
		// items first, so the result reports outcome_unknown whenever any
		// step may have been applied.
		return tool.Batch(out, 0, 0, append(unknown, failed...), replan), nil
	}
	var waitWarn string
	if len(pending) > 0 {
		pending, waitWarn = waitDelivery(ctx, env, &out, pending, time.Duration(wait)*time.Second)
	}
	held := finishDeferred(wctx, env, deferred, &out, step)
	tally(&out)
	out.Pending = len(pending)
	listNext := tool.NextStep{Tool: "screenshots_list", Args: map[string]any{"app": p.t.App.ID, "platform": string(p.t.Platform), "version": p.t.Version.String}}
	switch {
	case out.Failed > 0:
		out.Status = "partial"
		res = tool.OK(out).Warn(fmt.Sprintf("%d step(s) failed or not attempted; run screenshots_plan again to see what remains", out.Failed))
		if out.Pending > 0 {
			res.Then(listNext)
		}
		res.Then(replan)
	case out.Pending > 0 || out.Held > 0:
		out.Status = "uploaded"
		res = tool.StillProcessing(out, "Apple is still processing the uploads; check delivery states with screenshots_list").Then(listNext)
	default:
		out.Status = "synced"
		return tool.OK(out).Then(tool.NextStep{Tool: "release_status", Args: map[string]any{
			"app": p.t.App.ID, "platform": string(p.t.Platform), "version": p.t.Version.String,
		}}), nil
	}
	if waitWarn != "" {
		res.Warn(waitWarn)
	}
	if held > 0 {
		res.Warn(fmt.Sprintf("%d old screenshot(s) kept: their replacements have not reached COMPLETE, so deleting them could leave the set short; "+
			"once screenshots_list shows the new uploads COMPLETE, run screenshots_plan again", held))
	}
	return res, nil
}

// tally recounts out's per-action totals and returns how many steps changed
// remote state plus the typed errors of failed steps (outcome-unknown apart).
func tally(out *syncOut) (applied int, unknown, failed []error) {
	out.Uploaded, out.Skipped, out.Deleted, out.Failed, out.Held = 0, 0, 0, 0, 0
	for _, it := range out.Items {
		switch {
		case it.Action == actionUploaded:
			out.Uploaded++
		case it.Action == actionSkipped:
			out.Skipped++
		case it.Action == actionDeleted:
			out.Deleted++
		case it.Action == actionNotAttempted && it.State == stateAwaitingReplacement:
			out.Held++
		case it.Action == actionFailed || it.Action == actionNotAttempted:
			out.Failed++
		}
		switch {
		case it.Action == actionUploaded || it.Action == actionDeleted || it.Action == actionSetCreated || it.Action == actionReordered:
			applied++
		case it.err == nil:
		case errors.Is(it.err, tool.ErrOutcomeUnknown):
			unknown = append(unknown, fmt.Errorf("%s %s: %w", it.Locale, it.Display, it.err))
		default:
			failed = append(failed, fmt.Errorf("%s %s: %w", it.Locale, it.Display, it.err))
		}
	}
	return applied, unknown, failed
}

// deferredSet holds the steps of one set that wait for its uploads to finish
// processing: old screenshots are deleted only once their replacements are
// COMPLETE, so a FAILED or slow upload never leaves the slot empty.
type deferredSet struct {
	sp      *setPlan
	setID   string
	uploads []int // item indexes of the set's uploads
	newIDs  []string
	deletes []deleteItem
}

// syncSet applies one set plan, appending items to out. It returns the item
// indexes of uploads whose processing is still pending and, when deletes must
// wait for those uploads, the deferred rest of the set. A failure stops the
// set: its remaining steps are reported as not attempted.
func syncSet(ctx context.Context, env *tool.Env, deps upload.Deps, sp *setPlan, out *syncOut, step func()) ([]int, *deferredSet) {
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
			add(failedItem("", "", "create set", err))
			abandon(0, sp.Deletes)
			return nil, nil
		}
		setID = r.ID
		add(itemOut{Action: actionSetCreated, ID: setID})
	}

	// Delete first only the overflow that uploading first would push past
	// the set limit; the other deletes wait for the new uploads.
	overflow := sp.overflowDeletes()
	if !deleteShots(ctx, env, sp.Deletes[:overflow], step, add, func(left []deleteItem) {
		abandon(0, slices.Concat(left, sp.Deletes[overflow:]))
	}) {
		return nil, nil
	}
	rest := sp.Deletes[overflow:]
	newIDs := make([]string, len(sp.Uploads))
	uploads := make([]int, 0, len(sp.Uploads))
	var pending []int
	for i, f := range sp.Uploads {
		r, err := uploadFile(ctx, env, deps, setID, f)
		if err != nil {
			idx := add(failedItem(f.Rel, "", "", err))
			if r != nil {
				out.Items[idx].ID, out.Items[idx].State = r.ID, r.State
			}
			abandon(i+1, rest)
			return pending, nil
		}
		newIDs[i] = r.ID
		idx := add(itemOut{Action: actionUploaded, File: f.Rel, ID: r.ID, State: r.State})
		uploads = append(uploads, idx)
		if r.State != upload.StateComplete {
			pending = append(pending, idx)
		}
		step()
	}
	d := &deferredSet{sp: sp, setID: setID, uploads: uploads, newIDs: newIDs, deletes: rest}
	if len(rest) > 0 && len(uploads) > 0 {
		return pending, d
	}
	finishSet(ctx, env, d, out, step)
	return pending, nil
}

// deleteShots deletes ds in order (404 counts as deleted). On a failure it
// reports the failed item, hands the deletes left to abandon and returns false.
func deleteShots(ctx context.Context, env *tool.Env, ds []deleteItem, step func(), add func(itemOut) int, abandon func([]deleteItem)) bool {
	for i, d := range ds {
		_, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodDelete, Path: "/v1/appScreenshots/" + d.ID, Write: true})
		var api *tool.APIError
		if err != nil && (!errors.As(err, &api) || api.Status != http.StatusNotFound) {
			add(failedItem(d.FileName, d.ID, "delete", err))
			abandon(ds[i+1:])
			return false
		}
		add(itemOut{Action: actionDeleted, File: d.FileName, ID: d.ID})
		step()
	}
	return true
}

// finishSet runs a set's deletes left after its uploads, then the reorder.
func finishSet(ctx context.Context, env *tool.Env, d *deferredSet, out *syncOut, step func()) {
	sp := d.sp
	add := func(it itemOut) int {
		it.Locale, it.Display = sp.Locale, sp.Display
		out.Items = append(out.Items, it)
		return len(out.Items) - 1
	}
	if !deleteShots(ctx, env, d.deletes, step, add, func(left []deleteItem) {
		for _, x := range left {
			add(itemOut{Action: actionNotAttempted, File: x.FileName, ID: x.ID})
		}
	}) {
		return
	}
	if !sp.Reorder {
		return
	}
	ids := make([]map[string]string, 0, len(sp.order))
	for _, s := range sp.order {
		id := s.id
		if s.upload >= 0 {
			id = d.newIDs[s.upload]
		}
		ids = append(ids, map[string]string{"type": "appScreenshots", "id": id})
	}
	_, err := env.ASC.Do(ctx, tool.Request{
		Method: http.MethodPatch, Path: "/v1/appScreenshotSets/" + d.setID + "/relationships/appScreenshots", Write: true,
		Body: map[string]any{"data": ids},
	})
	if err != nil {
		add(failedItem("", d.setID, "reorder", err))
		return
	}
	add(itemOut{Action: actionReordered, ID: d.setID})
}

// finishDeferred runs each deferred set whose uploads all reached COMPLETE;
// the deletes (and reorder) of any other set are reported as not attempted,
// so the old screenshots stay until a replan. It returns how many deletes
// were held back.
func finishDeferred(ctx context.Context, env *tool.Env, sets []*deferredSet, out *syncOut, step func()) int {
	held := 0
	for _, d := range sets {
		ready := true
		for _, idx := range d.uploads {
			if it := out.Items[idx]; it.Action != actionUploaded || it.State != upload.StateComplete {
				ready = false
				break
			}
		}
		if ready {
			finishSet(ctx, env, d, out, step)
			continue
		}
		for _, x := range d.deletes {
			out.Items = append(out.Items, itemOut{Locale: d.sp.Locale, Display: d.sp.Display, Action: actionNotAttempted, File: x.FileName, ID: x.ID, State: stateAwaitingReplacement})
		}
		if d.sp.Reorder {
			out.Items = append(out.Items, itemOut{Locale: d.sp.Locale, Display: d.sp.Display, Action: actionNotAttempted, ID: d.setID, State: stateAwaitingReplacement})
		}
		held += len(d.deletes)
	}
	return held
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
// updating their items, and returns the indexes still pending. A 404 on a
// just-created screenshot means it is still processing. Any other read error
// or a cancelled wait stops polling: the uploads still pending get state
// "unknown" and the returned warning explains why, so the batch result
// (what was already applied) is never lost.
func waitDelivery(ctx context.Context, env *tool.Env, out *syncOut, pending []int, wait time.Duration) ([]int, string) {
	unknown := func(ids []int, why error) ([]int, string) {
		for _, idx := range ids {
			out.Items[idx].State = stateUnknown
		}
		return ids, fmt.Sprintf("processing state of %d upload(s) unknown (%v); check them with screenshots_list", len(ids), why)
	}
	deadline := env.Clock.Now().Add(wait)
	for len(pending) > 0 {
		left := deadline.Sub(env.Clock.Now())
		if left <= 0 {
			break
		}
		if err := env.Clock.Sleep(ctx, min(upload.DefaultPollInterval, left)); err != nil {
			return unknown(pending, fmt.Errorf("wait for processing: %w", err))
		}
		var still []int
		for i, idx := range pending {
			it := &out.Items[idx]
			resp, err := env.ASC.Get(ctx, "/v1/appScreenshots/"+it.ID, url.Values{"fields[appScreenshots]": {"assetDeliveryState"}})
			var api *tool.APIError
			switch {
			case err != nil && errors.As(err, &api) && api.Status == http.StatusNotFound:
				still = append(still, idx) // not readable yet right after create
				continue
			case err != nil:
				return unknown(append(still, pending[i:]...), fmt.Errorf("read screenshot %s: %w", it.ID, err))
			}
			r, _ := resp.First()
			it.State = deliveryState(r, "assetDeliveryState")
			switch it.State {
			case upload.StateComplete:
			case upload.StateFailed:
				it.Action, it.Error = actionFailed, "Apple processing FAILED"
			default:
				still = append(still, idx)
			}
		}
		pending = still
	}
	return pending, ""
}
