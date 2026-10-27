package web

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Data protection tokens of appDataUsages.
const (
	protNotCollected = "DATA_NOT_COLLECTED"
	protLinked       = "DATA_LINKED_TO_YOU"
	protNotLinked    = "DATA_NOT_LINKED_TO_YOU"
	protTracking     = "DATA_USED_TO_TRACK_YOU"
)

// Step outcomes.
const (
	outcomeDone         = "done"
	outcomeUnknown      = "outcome_unknown"
	outcomeError        = "error"
	outcomeNotAttempted = "not_attempted"
	outcomeAlready      = "already"
)

// Plan step operations, in execution order.
const (
	opDelete  = "delete"
	opUpdate  = "update"
	opCreate  = "create"
	opPublish = "publish"
)

func outcomeOf(err error) string {
	if errors.Is(err, tool.ErrOutcomeUnknown) {
		return outcomeUnknown
	}
	return outcomeError
}

// tuple is one appDataUsages row: category + purpose + data protection.
// DATA_NOT_COLLECTED has neither; tracking rows have no purpose.
type tuple struct {
	Category   string `json:"category,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
	Protection string `json:"protection"`
}

func (t tuple) key() string { return t.Category + "|" + t.Purpose + "|" + t.Protection }

// String is the one-line human form used in previews and steps.
func (t tuple) String() string {
	switch t.Protection {
	case protNotCollected:
		return "data not collected"
	case protTracking:
		return t.Category + ": used for tracking"
	case protLinked:
		return t.Category + " / " + t.Purpose + ": linked to identity"
	case protNotLinked:
		return t.Category + " / " + t.Purpose + ": not linked to identity"
	}
	return strings.Trim(t.Category+" / "+t.Purpose, " /") + ": " + t.Protection
}

// usage is a remote appDataUsages resource.
type usage struct {
	ID string `json:"id"`
	tuple
}

// publishState is the app's appDataUsagesPublishState.
type publishState struct {
	ID            string `json:"id"`
	Published     *bool  `json:"published"`
	LastPublished string `json:"last_published,omitempty"`
}

func boolp(b bool) *bool { return &b }

// readUsages lists the app's data usages through the web session.
func readUsages(ctx context.Context, env *tool.Env, appID string) ([]usage, error) {
	res, err := env.Web.List(ctx, "/v1/apps/"+appID+"/dataUsages", tool.ListOptions{
		Include: []string{"category", "purpose", "dataProtection"}, Limit: 500, MaxItems: 5000,
	})
	if err != nil {
		return nil, fmt.Errorf("read App Privacy data usages: %w", err)
	}
	out := make([]usage, 0, len(res.Data))
	for _, r := range res.Data {
		u := usage{ID: r.ID, tuple: tuple{Category: r.RelID("category"), Purpose: r.RelID("purpose"), Protection: r.RelID("dataProtection")}}
		if u.Protection == "" {
			u.Protection = r.Str("appDataUsageDataProtection")
		}
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b usage) int { return strings.Compare(a.key()+a.ID, b.key()+b.ID) })
	return out, nil
}

// readPublishState reads the app's App Privacy publish state.
func readPublishState(ctx context.Context, env *tool.Env, appID string) (publishState, error) {
	resp, err := env.Web.Get(ctx, "/v1/apps/"+appID+"/dataUsagePublishState", nil)
	if err != nil {
		return publishState{}, fmt.Errorf("read App Privacy publish state: %w", err)
	}
	r, _ := resp.First()
	ps := publishState{ID: r.ID, LastPublished: r.Str("lastPublished")}
	if v, ok := r.Bool("published"); ok {
		ps.Published = boolp(v)
	}
	return ps, nil
}

// catalogEntry is one valid token of a catalog dimension.
type catalogEntry struct {
	ID       string `json:"id"`
	Grouping string `json:"grouping,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
}

// catalog holds the live App Privacy tokens.
type catalog struct {
	Categories  []catalogEntry `json:"categories"`
	Purposes    []catalogEntry `json:"purposes"`
	Protections []catalogEntry `json:"data_protections"`
}

func readCatalog(ctx context.Context, env *tool.Env) (catalog, error) {
	var c catalog
	for _, d := range []struct {
		path    string
		include []string
		dst     *[]catalogEntry
	}{
		{"/v1/appDataUsageCategories", []string{"grouping"}, &c.Categories},
		{"/v1/appDataUsagePurposes", nil, &c.Purposes},
		{"/v1/appDataUsageDataProtections", nil, &c.Protections},
	} {
		res, err := env.Web.List(ctx, d.path, tool.ListOptions{Include: d.include, Limit: 200, MaxItems: 1000})
		if err != nil {
			return catalog{}, fmt.Errorf("read App Privacy catalog %s: %w", d.path, err)
		}
		for _, r := range res.Data {
			del, _ := r.Bool("deleted")
			*d.dst = append(*d.dst, catalogEntry{ID: r.ID, Grouping: r.RelID("grouping"), Deleted: del})
		}
	}
	return c, nil
}

// active lists the non-deleted token ids.
func active(es []catalogEntry) []string {
	var out []string
	for _, e := range es {
		if !e.Deleted {
			out = append(out, e.ID)
		}
	}
	return out
}

// staleTokens reports desired tokens that are deleted in, or absent from, a
// non-empty catalog dimension. An empty dimension proves nothing.
func staleTokens(desired []tuple, c catalog) []string {
	var out []string
	check := func(kind, id string, es []catalogEntry) {
		if id == "" || len(es) == 0 {
			return
		}
		i := slices.IndexFunc(es, func(e catalogEntry) bool { return e.ID == id })
		var msg string
		switch {
		case i < 0:
			msg = fmt.Sprintf("%s %s is not in Apple's catalog", kind, id)
		case es[i].Deleted:
			msg = fmt.Sprintf("%s %s was retired by Apple", kind, id)
		default:
			return
		}
		if !slices.Contains(out, msg) {
			out = append(out, msg)
		}
	}
	for _, t := range desired {
		check("category", t.Category, c.Categories)
		check("purpose", t.Purpose, c.Purposes)
		check("data protection", t.Protection, c.Protections)
	}
	return out
}

// declEntry is one collected data category of a declaration, the input
// and output shape of the privacy tools.
type declEntry struct {
	Category string   `json:"category" jsonschema:"catalog token, e.g. EMAIL_ADDRESS"`
	Purposes []string `json:"purposes,omitempty" jsonschema:"catalog tokens, e.g. APP_FUNCTIONALITY; required unless tracking only"`
	Linked   bool     `json:"linked,omitempty" jsonschema:"the data is linked to the user's identity"`
	Tracking bool     `json:"tracking,omitempty" jsonschema:"the data is used to track the user"`
}

// declaration is a full App Privacy declaration.
type declaration struct {
	NotCollected bool        `json:"not_collected,omitempty"`
	Data         []declEntry `json:"data,omitempty"`
}

func token(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// desiredTuples validates d and expands it to tuples.
func desiredTuples(d declaration) ([]tuple, error) {
	if d.NotCollected && len(d.Data) > 0 {
		return nil, tool.InvalidArgs("not_collected:true cannot be combined with data")
	}
	if d.NotCollected {
		return []tuple{{Protection: protNotCollected}}, nil
	}
	if len(d.Data) == 0 {
		return nil, tool.InvalidArgs("pass not_collected:true or data with at least one category")
	}
	set := map[string]tuple{}
	for i, e := range d.Data {
		cat := token(e.Category)
		if cat == "" {
			return nil, tool.InvalidArgs("data[%d].category is required", i)
		}
		if len(e.Purposes) == 0 && !e.Tracking {
			return nil, tool.InvalidArgs("data[%d] (%s) needs purposes (or tracking:true)", i, cat)
		}
		if e.Tracking {
			t := tuple{Category: cat, Protection: protTracking}
			set[t.key()] = t
		}
		prot, other := protNotLinked, protLinked
		if e.Linked {
			prot, other = protLinked, protNotLinked
		}
		for _, p := range e.Purposes {
			p = token(p)
			if p == "" {
				return nil, tool.InvalidArgs("data[%d].purposes has an empty token", i)
			}
			if _, clash := set[tuple{Category: cat, Purpose: p, Protection: other}.key()]; clash {
				return nil, tool.InvalidArgs("%s / %s is declared both linked and not linked", cat, p)
			}
			t := tuple{Category: cat, Purpose: p, Protection: prot}
			set[t.key()] = t
		}
	}
	out := make([]tuple, 0, len(set))
	for _, t := range set {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b tuple) int { return strings.Compare(a.key(), b.key()) })
	return out, nil
}

// declarationOf groups tuples back into the declaration shape (category →
// linked purposes, not-linked purposes, tracking). Tuples with protections
// the declaration cannot express are returned as unrecognized.
func declarationOf(ts []tuple) (declaration, []string) {
	var d declaration
	var unrecognized []string
	type group struct{ linked, notLinked []string }
	groups := map[string]*declEntry{}
	parts := map[string]*group{}
	var order []string
	get := func(cat string) (*declEntry, *group) {
		if _, ok := groups[cat]; !ok {
			groups[cat], parts[cat] = &declEntry{Category: cat}, &group{}
			order = append(order, cat)
		}
		return groups[cat], parts[cat]
	}
	for _, t := range ts {
		switch {
		case t.Protection == protNotCollected:
			d.NotCollected = true
		case t.Protection == protTracking && t.Category != "":
			e, _ := get(t.Category)
			e.Tracking = true
		case t.Protection == protLinked && t.Category != "" && t.Purpose != "":
			_, g := get(t.Category)
			g.linked = append(g.linked, t.Purpose)
		case t.Protection == protNotLinked && t.Category != "" && t.Purpose != "":
			_, g := get(t.Category)
			g.notLinked = append(g.notLinked, t.Purpose)
		default:
			unrecognized = append(unrecognized, t.String())
		}
	}
	slices.Sort(order)
	for _, cat := range order {
		e, g := groups[cat], parts[cat]
		switch {
		case len(g.linked) == 0 && len(g.notLinked) == 0:
			d.Data = append(d.Data, *e)
		default:
			if len(g.linked) > 0 {
				d.Data = append(d.Data, declEntry{Category: cat, Purposes: sorted(g.linked), Linked: true, Tracking: e.Tracking})
			}
			if len(g.notLinked) > 0 {
				d.Data = append(d.Data, declEntry{Category: cat, Purposes: sorted(g.notLinked), Tracking: e.Tracking && len(g.linked) == 0})
			}
		}
	}
	return d, unrecognized
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return slices.Compact(s)
}

// step is one planned privacy mutation and, after apply, its outcome.
type step struct {
	Op      string `json:"op"`
	Usage   string `json:"usage,omitempty"`
	UsageID string `json:"usage_id,omitempty"`
	// From is the protection an update replaces.
	From    string          `json:"from,omitempty"`
	Outcome string          `json:"outcome,omitempty"`
	Error   *tool.ErrorBody `json:"error,omitempty"`
	t       tuple
}

// diffSteps orders the mutations that turn remote into desired so an
// interruption leaves a superset of the desired declaration: prerequisite
// deletes (DATA_NOT_COLLECTED cannot coexist with collected rows), then
// in-place linked/not-linked flips (PATCH), creates, remaining deletes.
// Tracking, DATA_NOT_COLLECTED and scope changes are delete+create; only a
// same category+purpose identity flip is a PATCH (live-verified by
// rudrankriyam/App-Store-Connect-CLI).
func diffSteps(remote []usage, desired []tuple) []step {
	want := map[string]bool{}
	for _, t := range desired {
		want[t.key()] = true
	}
	have := map[string]bool{}
	var deletes []usage
	for _, u := range remote {
		if want[u.key()] && !have[u.key()] {
			have[u.key()] = true
			continue
		}
		deletes = append(deletes, u) // not desired, or a duplicate row
	}
	var creates []tuple
	for _, t := range desired {
		if !have[t.key()] {
			creates = append(creates, t)
		}
	}
	identity := func(p string) bool { return p == protLinked || p == protNotLinked }
	var updates []step
	creates = slices.DeleteFunc(creates, func(c tuple) bool {
		if !identity(c.Protection) {
			return false
		}
		i := slices.IndexFunc(deletes, func(d usage) bool {
			return identity(d.Protection) && d.Protection != c.Protection && d.Category == c.Category && d.Purpose == c.Purpose
		})
		if i < 0 {
			return false
		}
		updates = append(updates, step{Op: opUpdate, Usage: c.String(), UsageID: deletes[i].ID, From: deletes[i].Protection, t: c})
		deletes = slices.Delete(deletes, i, i+1)
		return true
	})
	wantNotCollected := want[tuple{Protection: protNotCollected}.key()]
	var steps, trailing []step
	for _, d := range deletes {
		s := step{Op: opDelete, Usage: d.String(), UsageID: d.ID, t: d.tuple}
		if wantNotCollected || d.Protection == protNotCollected {
			steps = append(steps, s)
		} else {
			trailing = append(trailing, s)
		}
	}
	steps = append(steps, updates...)
	for _, c := range creates {
		steps = append(steps, step{Op: opCreate, Usage: c.String(), t: c})
	}
	return append(steps, trailing...)
}
