package monetization

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"

	"github.com/idoo/hamn/internal/tool"
)

// prcAvailIn is the requested change of an app's territory availability.
type prcAvailIn struct {
	Add                       []string `json:"add,omitempty" jsonschema:"territories to make the app available in"`
	Remove                    []string `json:"remove,omitempty" jsonschema:"territories to remove the app from sale in"`
	AvailableInNewTerritories *bool    `json:"available_in_new_territories,omitempty" jsonschema:"only settable when availability is first created"`
}

// prcTA is one territory availability record.
type prcTA struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
}

// prcAvail is an app's current availability (appAvailabilities v2).
type prcAvail struct {
	Exists         bool             `json:"exists"`
	ID             string           `json:"id,omitempty"`
	AvailableInNew bool             `json:"available_in_new_territories"`
	Territories    map[string]prcTA `json:"territories,omitempty"`
}

// prcAvailSummary is the compact availability shown by pricing_get.
type prcAvailSummary struct {
	Exists                    bool `json:"exists"`
	AvailableTerritories      int  `json:"available_territories"`
	UnavailableTerritories    int  `json:"unavailable_territories"`
	AvailableInNewTerritories bool `json:"available_in_new_territories"`
}

func (a prcAvail) summary() prcAvailSummary {
	s := prcAvailSummary{Exists: a.Exists, AvailableInNewTerritories: a.AvailableInNew}
	for _, t := range a.Territories {
		if t.Available {
			s.AvailableTerritories++
		} else {
			s.UnavailableTerritories++
		}
	}
	return s
}

// prcReadAvail reads the app's availability and every territory record.
func prcReadAvail(ctx context.Context, env *tool.Env, appID string) (prcAvail, error) {
	var a prcAvail
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+url.PathEscape(appID)+"/appAvailabilityV2", url.Values{
		"fields[appAvailabilities]": {"availableInNewTerritories"},
	})
	if prcNotFound(err) {
		return a, nil
	}
	if err != nil {
		return a, fmt.Errorf("read app availability: %w", err)
	}
	r, ok := resp.First()
	if !ok {
		return a, nil
	}
	a.Exists, a.ID = true, r.ID
	a.AvailableInNew, _ = r.Bool("availableInNewTerritories")
	res, err := env.ASC.List(ctx, "/v2/appAvailabilities/"+url.PathEscape(r.ID)+"/territoryAvailabilities", tool.ListOptions{
		Include:  []string{"territory"},
		Fields:   map[string][]string{"territoryAvailabilities": {"available", "territory"}, "territories": {"currency"}},
		Limit:    200,
		MaxItems: 1000,
	})
	if err != nil {
		return a, fmt.Errorf("list territory availabilities: %w", err)
	}
	a.Territories = make(map[string]prcTA, len(res.Data))
	for _, t := range res.Data {
		av, _ := t.Bool("available")
		if terr := t.RelID("territory"); terr != "" {
			a.Territories[terr] = prcTA{ID: t.ID, Available: av}
		}
	}
	return a, nil
}

// prcAvailChange is one territory availability write.
type prcAvailChange struct {
	Territory string `json:"territory"`
	ID        string `json:"territory_availability_id,omitempty"`
	Available bool   `json:"available"`
}

// prcAvailPlan is the availability part of a pricing plan.
type prcAvailPlan struct {
	// Create is true when no availability exists: one POST with every
	// territory inline. Otherwise Changes are PATCHed one by one.
	Create                    bool             `json:"create"`
	AvailableInNewTerritories *bool            `json:"available_in_new_territories,omitempty"`
	Changes                   []prcAvailChange `json:"changes"`
	Calls                     int              `json:"calls"`
	Manual                    []string         `json:"manual,omitempty"`
}

// prcPlanAvail diffs the wanted availability against cur. Creating needs
// the full territory list (GET /v1/territories).
func prcPlanAvail(ctx context.Context, env *tool.Env, cur prcAvail, in prcAvailIn) (*prcAvailPlan, error) {
	add, err := prcTerritories("availability.add", in.Add)
	if err != nil {
		return nil, err
	}
	remove, err := prcTerritories("availability.remove", in.Remove)
	if err != nil {
		return nil, err
	}
	for _, t := range add {
		for _, r := range remove {
			if t == r {
				return nil, s12aInvalid("availability: %s is in both add and remove", t)
			}
		}
	}
	p := &prcAvailPlan{Changes: []prcAvailChange{}}
	if !cur.Exists {
		all, err := prcAllTerritories(ctx, env)
		if err != nil {
			return nil, err
		}
		onlyAdd := len(add) > 0
		for _, t := range append(append([]string{}, add...), remove...) {
			if !all[t] {
				return nil, s12aInvalid("availability: unknown territory %s", t)
			}
		}
		for t := range all {
			avail := !onlyAdd || slices.Contains(add, t)
			if slices.Contains(remove, t) {
				avail = false
			}
			p.Changes = append(p.Changes, prcAvailChange{Territory: t, Available: avail})
		}
		sort.Slice(p.Changes, func(i, j int) bool { return p.Changes[i].Territory < p.Changes[j].Territory })
		newT := !onlyAdd
		if in.AvailableInNewTerritories != nil {
			newT = *in.AvailableInNewTerritories
		}
		p.Create, p.AvailableInNewTerritories, p.Calls = true, &newT, 1
		return p, nil
	}
	if in.AvailableInNewTerritories != nil && *in.AvailableInNewTerritories != cur.AvailableInNew {
		p.Manual = append(p.Manual, "available_in_new_territories can only be set when availability is created; change it in App Store Connect > Pricing and Availability")
	}
	want := map[string]bool{}
	for _, t := range add {
		want[t] = true
	}
	for _, t := range remove {
		want[t] = false
	}
	for t, avail := range want {
		ta, ok := cur.Territories[t]
		if !ok {
			return nil, s12aInvalid("availability: territory %s has no availability record for this app", t)
		}
		if ta.Available != avail {
			p.Changes = append(p.Changes, prcAvailChange{Territory: t, ID: ta.ID, Available: avail})
		}
	}
	sort.Slice(p.Changes, func(i, j int) bool { return p.Changes[i].Territory < p.Changes[j].Territory })
	p.Calls = len(p.Changes)
	return p, nil
}

// prcAllTerritories lists every App Store territory code.
func prcAllTerritories(ctx context.Context, env *tool.Env) (map[string]bool, error) {
	res, err := env.ASC.List(ctx, "/v1/territories", tool.ListOptions{
		Fields: map[string][]string{"territories": {"currency"}}, Limit: 200, MaxItems: 1000,
	})
	if err != nil {
		return nil, fmt.Errorf("list territories: %w", err)
	}
	out := make(map[string]bool, len(res.Data))
	for _, t := range res.Data {
		out[t.ID] = true
	}
	return out, nil
}

// prcApplyAvail executes an availability plan. It returns the number of
// writes that succeeded; PATCHes are idempotent, so a re-run is safe.
func prcApplyAvail(ctx context.Context, env *tool.Env, appID string, p *prcAvailPlan) (int, error) {
	if p == nil || p.Calls == 0 {
		return 0, nil
	}
	if p.Create {
		data := make([]any, 0, len(p.Changes))
		included := make([]any, 0, len(p.Changes))
		for _, c := range p.Changes {
			lid := "${ta-" + c.Territory + "}"
			data = append(data, map[string]any{"type": "territoryAvailabilities", "id": lid})
			included = append(included, map[string]any{
				"type": "territoryAvailabilities", "id": lid,
				"attributes":    map[string]any{"available": c.Available},
				"relationships": map[string]any{"territory": prcRel("territories", c.Territory)},
			})
		}
		_, err := env.ASC.Do(ctx, tool.Request{Method: "POST", Path: "/v2/appAvailabilities", Write: true, Body: map[string]any{
			"data": map[string]any{
				"type":       "appAvailabilities",
				"attributes": map[string]any{"availableInNewTerritories": *p.AvailableInNewTerritories},
				"relationships": map[string]any{
					"app":                     prcRel("apps", appID),
					"territoryAvailabilities": map[string]any{"data": data},
				},
			},
			"included": included,
		}})
		if err != nil {
			return 0, fmt.Errorf("create app availability: %w", err)
		}
		return 1, nil
	}
	done := 0
	for _, c := range p.Changes {
		_, err := env.ASC.Do(ctx, tool.Request{Method: "PATCH", Path: "/v1/territoryAvailabilities/" + url.PathEscape(c.ID), Write: true, Body: map[string]any{
			"data": map[string]any{"type": "territoryAvailabilities", "id": c.ID, "attributes": map[string]any{"available": c.Available}},
		}})
		if err != nil {
			return done, fmt.Errorf("set availability in %s: %w", c.Territory, err)
		}
		done++
	}
	return done, nil
}
