package monetization

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

// prcRateReserve is the hourly quota pricing_apply always leaves untouched.
const prcRateReserve = 200

var (
	prcKinds     = []any{prcKindApp, prcKindIAP, prcKindSub}
	prcPlanTypes = []any{"MONTHLY", "UPFRONT"}
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "pricing_plan", Toolset: tool.Monetization, Class: tool.Read, Idempotent: true,
		Title: "Plan a price or availability change",
		Description: "Preview a price change (base price matched to Apple's price points, Apple equalization elsewhere, " +
			"optional overrides) and app availability changes: old→new per territory, call count, rate-limit check. " +
			"Returns plan_id for pricing_apply.",
		Enums: map[string][]any{"kind": prcKinds, "plan_type": prcPlanTypes},
	}, pricingPlan))
	tool.Register(tool.New(tool.Meta{
		Name: "pricing_apply", Toolset: tool.Monetization, Class: tool.Destructive, Confirm: tool.ConfirmByPlan, RequireHuman: true,
		Title: "Apply a pricing plan",
		Description: "Apply a pricing_plan: same arguments plus plan_id; refused if prices changed since. App/IAP: one " +
			"schedule POST; subscriptions: one POST per territory, resumable by re-running pricing_plan.",
		Enums: map[string][]any{"kind": prcKinds, "plan_type": prcPlanTypes},
	}, pricingApply))
}

// ---- pricing_plan / pricing_apply ----

type pricingPlanIn struct {
	App                  tool.AppRef       `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Kind                 string            `json:"kind,omitempty" jsonschema:"default app"`
	ProductID            string            `json:"product_id,omitempty" jsonschema:"product id of the IAP or subscription"`
	BaseTerritory        string            `json:"base_territory,omitempty" jsonschema:"territory of price; default current base or USA"`
	Price                string            `json:"price,omitempty" jsonschema:"customer price in the base territory currency, e.g. 4.99; nearest price point is used"`
	Overrides            map[string]string `json:"overrides,omitempty" jsonschema:"territory → customer price, set manually instead of equalized"`
	StartDate            string            `json:"start_date,omitempty" jsonschema:"YYYY-MM-DD; default today"`
	PlanType             string            `json:"plan_type,omitempty" jsonschema:"subscriptions only"`
	PreserveCurrentPrice *bool             `json:"preserve_current_price,omitempty" jsonschema:"subscriptions: existing subscribers keep their price on increases; default true"`
	Territories          []string          `json:"territories,omitempty" jsonschema:"subscriptions: limit the change to these territories"`
	ResetManualPrices    bool              `json:"reset_manual_prices,omitempty" jsonschema:"app/IAP: drop manual prices not in overrides so they follow equalization"`
	Availability         *prcAvailIn       `json:"availability,omitempty" jsonschema:"app availability change (kind app only)"`
}

type pricingApplyIn struct {
	tool.ConfirmPlan
	pricingPlanIn
}

// prcChange is one territory whose price changes.
type prcChange struct {
	Territory string `json:"territory"`
	Currency  string `json:"currency,omitempty"`
	Old       string `json:"old,omitempty"`
	New       string `json:"new"`
	Proceeds  string `json:"proceeds,omitempty"`
	PointID   string `json:"price_point_id"`
	Source    string `json:"source"`
	Direction string `json:"direction"`
}

// Directions of a prcChange.
const (
	prcIncrease = "increase"
	prcDecrease = "decrease"
	prcNew      = "new"
	prcSame     = "same"
)

// prcDirection classifies old→new customer prices of one territory.
func prcDirection(old, nw string) string {
	o, ok1 := prcRat(old)
	n, ok2 := prcRat(nw)
	switch {
	case old == "" || !ok1 || !ok2:
		return prcNew
	case n.Cmp(o) > 0:
		return prcIncrease
	case n.Cmp(o) < 0:
		return prcDecrease
	}
	return prcSame
}

// prcManual is one inline manual price of an app/IAP schedule POST.
type prcManual struct {
	Territory string `json:"territory"`
	PointID   string `json:"price_point_id"`
	StartDate string `json:"start_date,omitempty"`
	EndDate   string `json:"end_date,omitempty"`
}

// prcCall is one line of the call plan.
type prcCall struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Count  int    `json:"count"`
}

// prcPlan is the computed pricing plan shared by pricing_plan and
// pricing_apply.
type prcPlan struct {
	k            *prcKind
	T            prcTarget     `json:"target"`
	Date         string        `json:"effective_date"`
	Base         string        `json:"base_territory,omitempty"`
	BasePrice    *prcPoint     `json:"base_price,omitempty"`
	Exact        bool          `json:"exact_match,omitempty"`
	Changes      []prcChange   `json:"changes"`
	Increases    int           `json:"increases,omitempty"`
	Decreases    int           `json:"decreases,omitempty"`
	OldBase      string        `json:"old_base_territory,omitempty"`
	Kept         []string      `json:"kept_manual,omitempty"`
	Filtered     []string      `json:"territories,omitempty"`
	Manual       []prcManual   `json:"schedule,omitempty"`
	Avail        *prcAvailPlan `json:"availability,omitempty"`
	CallPlan     []prcCall     `json:"call_plan"`
	Calls        int           `json:"calls"`
	priceCalls   int
	key          map[string]any
	state        map[string]any
	irreversible []string
	warnings     []string
	manualSteps  []string
}

// prcValidate checks argument combinations before any request.
func prcValidate(env *tool.Env, k *prcKind, in *pricingPlanIn) error {
	if in.Price == "" && len(in.Overrides) > 0 {
		return tool.InvalidArgs("overrides need price (the base territory price)")
	}
	if in.Price == "" && in.Availability == nil {
		return tool.InvalidArgs("nothing to plan: pass price and/or availability")
	}
	if in.StartDate != "" {
		if _, err := time.Parse(time.DateOnly, in.StartDate); err != nil {
			return tool.InvalidArgs("start_date must be YYYY-MM-DD")
		}
		if in.StartDate < prcToday(env) {
			return tool.InvalidArgs("start_date %s is in the past", in.StartDate)
		}
	}
	if k != prcSub && (in.PlanType != "" || in.PreserveCurrentPrice != nil || len(in.Territories) > 0) {
		return tool.InvalidArgs("plan_type, preserve_current_price and territories apply to kind subscription only")
	}
	if k == prcSub && in.ResetManualPrices {
		return tool.InvalidArgs("reset_manual_prices applies to kind app or iap only")
	}
	if k != prcApp && in.Availability != nil {
		return tool.InvalidArgs("availability applies to kind app only")
	}
	if in.BaseTerritory != "" {
		b, err := prcTerritories("base_territory", []string{in.BaseTerritory})
		if err != nil {
			return err
		}
		in.BaseTerritory = b[0]
	}
	terrs, err := prcTerritories("territories", in.Territories)
	if err != nil {
		return err
	}
	in.Territories = terrs
	if len(in.Overrides) > 0 {
		norm := make(map[string]string, len(in.Overrides))
		for t, p := range in.Overrides {
			n, err := prcTerritories("overrides", []string{t})
			if err != nil {
				return err
			}
			norm[n[0]] = p
		}
		in.Overrides = norm
	}
	if in.Availability != nil {
		a := *in.Availability
		if a.Add, err = prcTerritories("availability.add", a.Add); err != nil {
			return err
		}
		if a.Remove, err = prcTerritories("availability.remove", a.Remove); err != nil {
			return err
		}
		in.Availability = &a
	}
	return nil
}

// prcCompute reads the current state and builds the plan.
func prcCompute(ctx context.Context, env *tool.Env, in pricingPlanIn) (*prcPlan, error) {
	k, err := prcKindFor(in.Kind)
	if err != nil {
		return nil, err
	}
	if err := prcValidate(env, k, &in); err != nil {
		return nil, err
	}
	t, err := prcResolve(ctx, env, in.App, k, in.ProductID)
	if err != nil {
		return nil, err
	}
	norm := in
	norm.App = ""
	p := &prcPlan{
		k: k, T: t, Date: in.StartDate, Changes: []prcChange{},
		key:   map[string]any{"target": t, "args": norm},
		state: map[string]any{},
	}
	if p.Date == "" {
		p.Date = prcToday(env)
	}
	if in.Price != "" {
		if err := p.planPrices(ctx, env, in); err != nil {
			return nil, err
		}
	}
	if in.Availability != nil {
		cur, err := prcReadAvail(ctx, env, t.AppID)
		if err != nil {
			return nil, err
		}
		if p.Avail, err = prcPlanAvail(ctx, env, cur, *in.Availability); err != nil {
			return nil, err
		}
		p.state["availability"] = cur
		p.manualSteps = append(p.manualSteps, p.Avail.Manual...)
		switch {
		case p.Avail.Create:
			p.CallPlan = append(p.CallPlan, prcCall{Method: "POST", Path: "/v2/appAvailabilities", Count: 1})
		case p.Avail.Calls > 0:
			p.CallPlan = append(p.CallPlan, prcCall{Method: "PATCH", Path: "/v1/territoryAvailabilities/{id}", Count: p.Avail.Calls})
		}
		p.Calls += p.Avail.Calls
	}
	if p.CallPlan == nil {
		p.CallPlan = []prcCall{}
	}
	return p, nil
}

// planPrices computes the price part: base point, equalized and override
// targets, and the diff against prices in effect on the start date.
func (p *prcPlan) planPrices(ctx context.Context, env *tool.Env, in pricingPlanIn) error {
	k := p.k
	var cur map[string]prcPrice
	oldBase := ""
	if k == prcSub {
		rows, err := prcSubPrices(ctx, env, p.T.ID, nil, in.PlanType)
		if err != nil {
			return err
		}
		cur = prcEffective(rows, p.Date)
	} else {
		s, err := prcReadSchedule(ctx, env, k, p.T.ID, nil)
		if err != nil {
			return err
		}
		oldBase = s.Base
		cur = prcEffective(append(append([]prcPrice{}, s.Manual...), s.Automatic...), p.Date)
		p.state["schedule"] = s
	}
	p.Base = in.BaseTerritory
	if p.Base == "" {
		p.Base = oldBase
	}
	if p.Base == "" {
		p.Base = prcDefaults
	}
	pts, err := prcPoints(ctx, env, k, p.T.ID, p.Base, in.PlanType)
	if err != nil {
		return err
	}
	bp, exact, err := prcMatch(pts, in.Price, p.Base)
	if err != nil {
		return err
	}
	p.BasePrice, p.Exact = &bp, exact
	if !exact {
		p.warnings = append(p.warnings, fmt.Sprintf("no price point equals %s in %s; using nearest %s", in.Price, p.Base, bp.CustomerPrice))
	}
	eq, err := prcEqualizations(ctx, env, k, bp.ID, in.PlanType)
	if err != nil {
		return err
	}
	type target struct {
		prcPoint
		source string
	}
	want := map[string]target{p.Base: {bp, "base"}}
	for terr, pt := range eq {
		if terr != p.Base {
			want[terr] = target{pt, "equalized"}
		}
	}
	for _, terr := range slices.Sorted(maps.Keys(in.Overrides)) {
		opts, err := prcPoints(ctx, env, k, p.T.ID, terr, in.PlanType)
		if err != nil {
			return err
		}
		op, ok, err := prcMatch(opts, in.Overrides[terr], terr)
		if err != nil {
			return err
		}
		if !ok {
			p.warnings = append(p.warnings, fmt.Sprintf("no price point equals %s in %s; using nearest %s", in.Overrides[terr], terr, op.CustomerPrice))
		}
		want[terr] = target{op, "override"}
	}
	if k != prcSub {
		if oldBase != "" && oldBase != p.Base {
			p.OldBase = oldBase
		}
		// A schedule POST replaces every manual price: carry over current
		// manual prices the caller did not change, except the old base
		// (it follows equalization from the new base) and all of them with
		// reset_manual_prices.
		for terr, c := range cur {
			if _, over := in.Overrides[terr]; c.Manual && terr != p.Base && terr != p.OldBase && !over && !in.ResetManualPrices {
				want[terr] = target{c.prcPoint, "kept"}
				p.Kept = append(p.Kept, terr)
			}
		}
		sort.Strings(p.Kept)
		if _, ok := want[p.OldBase]; p.OldBase != "" && !ok {
			p.warnings = append(p.warnings, fmt.Sprintf("Apple returned no equalized price for the old base %s; its manual price is dropped and Apple sets it", p.OldBase))
		}
	} else if len(in.Territories) > 0 {
		p.Filtered = in.Territories
		for terr := range want {
			if _, over := in.Overrides[terr]; !over && !slices.Contains(in.Territories, terr) {
				delete(want, terr)
			}
		}
	}
	order := make([]string, 0, len(want))
	for terr := range want {
		order = append(order, terr)
	}
	sort.Slice(order, func(i, j int) bool {
		if (order[i] == p.Base) != (order[j] == p.Base) {
			return order[i] == p.Base
		}
		return order[i] < order[j]
	})
	curIDs := map[string]string{}
	for _, terr := range order {
		w, c := want[terr], cur[terr]
		curIDs[terr] = c.ID
		if c.ID == w.ID {
			continue
		}
		cc := w.Currency
		if cc == "" {
			cc = c.Currency
		}
		d := prcDirection(c.CustomerPrice, w.CustomerPrice)
		switch d {
		case prcIncrease:
			p.Increases++
		case prcDecrease:
			p.Decreases++
		}
		p.Changes = append(p.Changes, prcChange{
			Territory: terr, Currency: cc, Old: c.CustomerPrice, New: w.CustomerPrice,
			Proceeds: w.Proceeds, PointID: w.ID, Source: w.source, Direction: d,
		})
	}
	p.state["current"] = curIDs
	if k == prcSub {
		p.priceCalls = len(p.Changes)
		if p.priceCalls > 0 {
			p.CallPlan = append(p.CallPlan, prcCall{Method: "POST", Path: k.createPath, Count: p.priceCalls})
			p.irreversible = append(p.irreversible, "subscription price rows cannot be edited once created")
			p.irreversible = append(p.irreversible, prcSubscriberImpact(p.Increases, p.Decreases, in.preserve())...)
		}
	} else if len(p.Changes) > 0 || p.OldBase != "" {
		for _, terr := range order {
			w, c := want[terr], cur[terr]
			if w.source == "equalized" {
				// A manual price that now follows equalization keeps
				// applying until the start date.
				if in.StartDate != "" && c.Manual && c.ID != "" {
					p.Manual = append(p.Manual, prcManual{Territory: terr, PointID: c.ID, EndDate: in.StartDate})
				}
				continue
			}
			if in.StartDate != "" && w.source != "kept" && c.Manual && c.ID != "" && c.ID != w.ID {
				p.Manual = append(p.Manual, prcManual{Territory: terr, PointID: c.ID, EndDate: in.StartDate})
			}
			m := prcManual{Territory: terr, PointID: w.ID}
			if w.source != "kept" {
				m.StartDate = in.StartDate
			}
			p.Manual = append(p.Manual, m)
		}
		p.priceCalls = 1
		p.CallPlan = append(p.CallPlan, prcCall{Method: "POST", Path: k.createPath, Count: 1})
		p.irreversible = append(p.irreversible, "replaces the whole price schedule; scheduled manual price changes not listed are dropped")
	}
	p.Calls += p.priceCalls
	return nil
}

// preserve is preserve_current_price with its default (true).
func (in pricingPlanIn) preserve() bool {
	return in.PreserveCurrentPrice == nil || *in.PreserveCurrentPrice
}

// prcSubscriberImpact states what a subscription price change does to
// existing subscribers.
func prcSubscriberImpact(increases, decreases int, preserve bool) []string {
	var out []string
	switch {
	case increases > 0 && preserve:
		out = append(out, fmt.Sprintf("%d territories increase: existing subscribers keep their current price, only new subscribers pay more", increases))
	case increases > 0:
		out = append(out, fmt.Sprintf("%d territories increase for existing subscribers too: Apple emails them, and where consent is required, "+
			"subscribers who don't agree lapse at renewal", increases))
	}
	if decreases > 0 {
		out = append(out, fmt.Sprintf("%d territories decrease: existing subscribers move to the lower price", decreases))
	}
	return out
}

// preview is the confirmation preview of the plan.
func (p *prcPlan) preview() tool.PlanPreview {
	sum := fmt.Sprintf("change %s %s", p.T.Kind, p.T.ID)
	if p.T.ProductID != "" {
		sum = fmt.Sprintf("change %s %s", p.T.Kind, p.T.ProductID)
	}
	intended := map[string]any{"calls": p.Calls}
	affected := []string{}
	if p.BasePrice != nil {
		baseChanged := slices.ContainsFunc(p.Changes, func(c prcChange) bool { return c.Territory == p.Base })
		inFilter := len(p.Filtered) == 0 || slices.Contains(p.Filtered, p.Base) || baseChanged
		if inFilter {
			sum += fmt.Sprintf(": price %s %s in %s, %d territories change", p.BasePrice.CustomerPrice, p.BasePrice.Currency, p.Base, len(p.Changes))
		} else {
			sum += fmt.Sprintf(": %d territories change to Apple's equivalents of %s %s; %s itself is not changed",
				len(p.Changes), p.BasePrice.CustomerPrice, p.BasePrice.Currency, p.Base)
		}
		if !baseChanged && inFilter {
			sum += fmt.Sprintf(" (%s already at that price)", p.Base)
		}
		if len(p.Filtered) > 0 {
			sum += "; only " + prcList(p.Filtered)
		}
		if p.Increases > 0 {
			sum += fmt.Sprintf("; %d increases", p.Increases)
		}
		if p.OldBase != "" {
			sum += fmt.Sprintf("; base moves from %s, which now follows equalization", p.OldBase)
		}
		if len(p.Kept) > 0 {
			sum += fmt.Sprintf("; %d territories keep manual prices: %s", len(p.Kept), prcList(p.Kept))
		}
		ch := p.Changes
		if len(ch) > 20 {
			ch = ch[:20]
		}
		intended["changes"] = ch
		for _, c := range p.Changes {
			affected = append(affected, c.Territory)
		}
	}
	if p.Avail != nil && p.Avail.Calls > 0 {
		sum += fmt.Sprintf("; availability: %d territories", len(p.Avail.Changes))
		intended["availability"] = p.Avail
	}
	return tool.PlanPreview{Summary: sum, Intended: intended, Affected: affected, Irreversible: p.irreversible}
}

// prcList joins territory codes, listing at most 20.
func prcList(ts []string) string {
	if len(ts) <= 20 {
		return strings.Join(ts, ", ")
	}
	return strings.Join(ts[:20], ", ") + fmt.Sprintf(" and %d more", len(ts)-20)
}

// prcRateCheck refuses plans that would leave less than the reserve.
func prcRateCheck(env *tool.Env, calls int) (tool.RateLimit, string) {
	rl := env.ASC.RateLimit()
	if rl.Known && rl.Remaining < calls+prcRateReserve {
		return rl, fmt.Sprintf("needs %d calls but only %d of the hourly quota remain (keeping %d in reserve); wait for the quota to recover",
			calls, rl.Remaining, prcRateReserve)
	}
	return rl, ""
}

type pricingPlanOut struct {
	Status string `json:"status"`
	*prcPlan
	RateLimit *tool.RateLimit `json:"rate_limit,omitempty"`
	Blockers  []string        `json:"blockers,omitempty"`
	Manual    []string        `json:"manual,omitempty"`
	PlanID    string          `json:"plan_id,omitempty"`
}

func pricingPlan(ctx context.Context, env *tool.Env, in pricingPlanIn) (*tool.Result, error) {
	p, err := prcCompute(ctx, env, in)
	if err != nil {
		return nil, err
	}
	out := pricingPlanOut{Status: "planned", prcPlan: p, Manual: p.manualSteps}
	rl, block := prcRateCheck(env, p.Calls)
	if rl.Known {
		out.RateLimit = &rl
	}
	switch {
	case p.Calls == 0:
		out.Status = "no_changes"
	case block != "":
		out.Status, out.Blockers = "blocked", []string{block}
	default:
		fp, err := tool.Fingerprint(p.state)
		if err != nil {
			return nil, err
		}
		if out.PlanID, err = env.Plans.Propose(ctx, "pricing_apply", p.key, fp, p.preview()); err != nil {
			return nil, fmt.Errorf("propose pricing_apply: %w", err)
		}
	}
	res := tool.OK(out)
	for _, w := range p.warnings {
		res.Warn(w)
	}
	if out.PlanID != "" {
		args := prcArgs(in)
		args["plan_id"] = out.PlanID
		res.Then(tool.NextStep{Tool: "pricing_apply", Args: args})
	}
	return res, nil
}

// prcArgs renders the input as a NextStep argument map.
func prcArgs(in pricingPlanIn) map[string]any {
	b, err := json.Marshal(in)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

type prcFailed struct {
	Territory string `json:"territory"`
	Error     string `json:"error"`
}

type pricingApplyOut struct {
	Status            string      `json:"status"`
	Target            prcTarget   `json:"target"`
	Schedule          string      `json:"schedule,omitempty"`
	Applied           []string    `json:"applied"`
	Failed            []prcFailed `json:"failed,omitempty"`
	Pending           []string    `json:"pending,omitempty"`
	AvailabilityCalls int         `json:"availability_writes"`
}

func pricingApply(ctx context.Context, env *tool.Env, in pricingApplyIn) (*tool.Result, error) {
	p, err := prcCompute(ctx, env, in.pricingPlanIn)
	if err != nil {
		return nil, err
	}
	out := pricingApplyOut{Status: "applied", Target: p.T, Applied: []string{}}
	if p.Calls == 0 {
		return tool.Already(out), nil
	}
	if _, block := prcRateCheck(env, p.Calls); block != "" {
		return nil, fmt.Errorf("%w: %s", tool.ErrRateLimited, block)
	}
	fp, err := tool.Fingerprint(p.state)
	if err != nil {
		return nil, err
	}
	wctx, res := tool.Preview(ctx, env, in.PlanID, p.key, fp, p.preview())
	if res != nil {
		return res, nil
	}
	replan := tool.NextStep{Tool: "pricing_plan", Args: prcArgs(in.pricingPlanIn)}
	if p.priceCalls > 0 && p.k != prcSub {
		if err := prcPostSchedule(wctx, env, p); err != nil {
			out.Pending = changedTerritories(p.Changes)
			return prcInterrupted(err, out, replan), nil
		}
		out.Schedule = "replaced"
		out.Applied = changedTerritories(p.Changes)
	}
	if p.k == prcSub {
		for i, c := range p.Changes {
			env.Progress(float64(i), float64(len(p.Changes)), "subscription prices")
			err := prcPostSubPrice(wctx, env, p, in.pricingPlanIn, c)
			switch {
			case err == nil:
				out.Applied = append(out.Applied, c.Territory)
			case prcUnknown(err):
				out.Pending = changedTerritories(p.Changes[i:])
				return prcInterrupted(err, out, replan), nil
			default:
				out.Failed = append(out.Failed, prcFailed{Territory: c.Territory, Error: err.Error()})
			}
		}
		env.Progress(float64(len(p.Changes)), float64(len(p.Changes)), "subscription prices")
	}
	n, err := prcApplyAvail(wctx, env, p.T.AppID, p.Avail)
	out.AvailabilityCalls = n
	if err != nil {
		return prcInterrupted(err, out, replan), nil
	}
	r := tool.OK(out)
	if len(out.Failed) > 0 {
		out.Status = "partial"
		r = tool.OK(out).Warn(fmt.Sprintf("%d territories failed; re-run pricing_plan to see what remains", len(out.Failed)))
		r.Then(replan)
	}
	for _, w := range p.manualSteps {
		r.Warn(w)
	}
	return r.Then(tool.NextStep{Tool: "pricing_get", Args: map[string]any{
		"app": p.T.AppID, "kind": p.T.Kind, "product_id": p.T.ProductID,
	}}), nil
}

func changedTerritories(cs []prcChange) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Territory)
	}
	return out
}

// prcInterrupted reports an apply that stopped part-way: the error (typed
// outcome_unknown when Apple may have applied the call) plus progress, and
// a pricing_plan re-diff as the next step.
func prcInterrupted(err error, out pricingApplyOut, replan tool.NextStep) *tool.Result {
	out.Status = "interrupted"
	if prcUnknown(err) {
		err = fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, err)
	}
	body := tool.BodyFor(err)
	body.Next = append(body.Next, replan)
	return &tool.Result{
		Data: struct {
			tool.ErrorBody
			Progress pricingApplyOut `json:"progress"`
		}{body, out},
		IsError: true,
		Next:    body.Next,
	}
}

// prcPostSchedule replaces an app/IAP price schedule in one POST with
// inline manual prices.
func prcPostSchedule(ctx context.Context, env *tool.Env, p *prcPlan) error {
	k := p.k
	data := make([]any, 0, len(p.Manual))
	included := make([]any, 0, len(p.Manual))
	for i, m := range p.Manual {
		lid := fmt.Sprintf("${price-%d}", i)
		data = append(data, map[string]any{"type": k.priceType, "id": lid})
		attrs := map[string]any{"startDate": nil, "endDate": nil}
		if m.StartDate != "" {
			attrs["startDate"] = m.StartDate
		}
		if m.EndDate != "" {
			attrs["endDate"] = m.EndDate
		}
		rels := map[string]any{k.priceRel: prcRel(k.pointType, m.PointID)}
		if k == prcIAP {
			rels["inAppPurchaseV2"] = prcRel("inAppPurchases", p.T.ID)
		}
		included = append(included, map[string]any{"type": k.priceType, "id": lid, "attributes": attrs, "relationships": rels})
	}
	_, err := env.ASC.Do(ctx, tool.Request{Method: "POST", Path: k.createPath, Write: true, Body: map[string]any{
		"data": map[string]any{
			"type": k.scheduleType,
			"relationships": map[string]any{
				k.targetRel:     prcRel(k.targetType, p.T.ID),
				"baseTerritory": prcRel("territories", p.Base),
				"manualPrices":  map[string]any{"data": data},
			},
		},
		"included": included,
	}})
	if err != nil {
		return fmt.Errorf("replace %s price schedule: %w", k.name, err)
	}
	return nil
}

// prcPostSubPrice creates one subscription price in one territory.
func prcPostSubPrice(ctx context.Context, env *tool.Env, p *prcPlan, in pricingPlanIn, c prcChange) error {
	attrs := map[string]any{"preserveCurrentPrice": in.preserve()}
	if in.StartDate != "" {
		attrs["startDate"] = in.StartDate
	}
	if in.PlanType != "" {
		attrs["planType"] = in.PlanType
	}
	_, err := env.ASC.Do(ctx, tool.Request{Method: "POST", Path: prcSub.createPath, Write: true, Body: map[string]any{
		"data": map[string]any{
			"type":       "subscriptionPrices",
			"attributes": attrs,
			"relationships": map[string]any{
				"subscription":           prcRel("subscriptions", p.T.ID),
				"territory":              prcRel("territories", c.Territory),
				"subscriptionPricePoint": prcRel("subscriptionPricePoints", c.PointID),
			},
		},
	}})
	if err != nil {
		return fmt.Errorf("set subscription price in %s: %w", c.Territory, err)
	}
	return nil
}
