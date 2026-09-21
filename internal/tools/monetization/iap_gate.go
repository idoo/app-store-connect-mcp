package monetization

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// gateLevel is how much approval a planned upsert needs; the strongest
// change decides.
type gateLevel int

const (
	gateNone      gateLevel = iota // nothing to write
	gateAdditive                   // new objects or empty fields only
	gateOverwrite                  // replaces existing values: confirm:true
	gateHuman                      // irreversible or money-affecting: human approval
)

// upsertGate collects the writes an upsert would make before any of them run.
type upsertGate struct {
	level gateLevel
	p     tool.PlanPreview
	cur   map[string]any
	want  map[string]any
}

// add records one planned write and raises the gate to level.
func (g *upsertGate) add(level gateLevel, affected string) {
	g.level = max(g.level, level)
	g.p.Affected = append(g.p.Affected, affected)
}

// irreversible records a one-way consequence and forces human approval.
func (g *upsertGate) irreversible(note string) {
	g.level = gateHuman
	g.p.Irreversible = append(g.p.Irreversible, note)
}

// attrs records a planned attribute change for the preview.
func (g *upsertGate) attrs(label string, cur tool.Resource, changed map[string]any) {
	if g.cur == nil {
		g.cur, g.want = map[string]any{}, map[string]any{}
	}
	c, w := map[string]any{}, map[string]any{}
	for k, v := range changed {
		c[k], w[k] = tool.Unwrap(cur.Attributes[k]), v
	}
	g.cur[label], g.want[label] = c, w
}

// open runs the gate for the planned level: additive writes proceed,
// overwrites need confirm:true, irreversible ones need a human.
func (g *upsertGate) open(ctx context.Context, env *tool.Env, confirm bool, summary string) (context.Context, *tool.Result) {
	g.p.Summary = summary
	if g.cur != nil {
		g.p.Current, g.p.Intended = g.cur, g.want
	}
	switch g.level {
	case gateNone:
		return ctx, nil
	case gateAdditive:
		return tool.Unconfirmed(ctx, env)
	case gateOverwrite:
		return tool.RequireConfirm(ctx, env, confirm, g.p)
	default:
		return tool.RequireHumanConfirm(ctx, env, confirm, g.p)
	}
}

// planAttrs classifies an attribute diff on an existing product: turning
// family sharing on and (with levelHuman) the group level need a human,
// replacing a non-empty value needs confirm, filling an empty one is additive.
func (g *upsertGate) planAttrs(label, path string, cur tool.Resource, changed map[string]any, humanAttrs ...string) {
	if len(changed) == 0 {
		return
	}
	g.attrs(label, cur, changed)
	level := gateAdditive
	for k := range changed {
		v := tool.Unwrap(cur.Attributes[k])
		if v != nil && v != "" && v != false {
			level = max(level, gateOverwrite)
		}
		if slices.Contains(humanAttrs, k) {
			level = gateHuman
		}
	}
	if on, _ := changed["familySharable"].(bool); on {
		g.irreversible("family sharing on " + label + " cannot be turned off again; family members get it from one purchase")
	}
	g.add(level, "PATCH "+path+" "+strings.Join(sortedKeys(changed), ","))
}

// planLocs classifies localization writes: new locales are additive, changed
// existing ones are overwrites.
func (g *upsertGate) planLocs(k versionKind, existing []tool.Resource, want map[string]map[string]any) error {
	byLocale := make(map[string]tool.Resource, len(existing))
	for _, r := range existing {
		byLocale[r.Str("locale")] = r
	}
	for _, loc := range sortedKeys(want) {
		cur, ok := byLocale[loc]
		if !ok {
			g.add(gateAdditive, "POST "+k.LocPath+" "+loc)
			continue
		}
		changed, names, err := attrDiff(cur, want[loc])
		if err != nil {
			return err
		}
		if len(changed) > 0 {
			g.attrs(k.LocType+" "+loc, cur, changed)
			g.add(gateOverwrite, "PATCH "+k.LocPath+"/"+cur.ID+" "+strings.Join(names, ","))
		}
	}
	return nil
}

// findOpenVersion reads the parent's editable version and its localizations
// without creating one.
func findOpenVersion(ctx context.Context, env *tool.Env, k versionKind, parentID string) (tool.Resource, []tool.Resource, bool, error) {
	resp, err := env.ASC.Get(ctx, fmt.Sprintf(k.ListPath, url.PathEscape(parentID)), url.Values{
		"filter[state]":             {openVersionStates},
		"include":                   {"localizations"},
		"fields[" + k.Type + "]":    {"version,state,localizations"},
		"fields[" + k.LocType + "]": {strings.Join(k.LocFields, ",")},
		"limit[localizations]":      {"50"},
	})
	if err != nil {
		return tool.Resource{}, nil, false, fmt.Errorf("read %s of %s: %w", k.Type, parentID, err)
	}
	v, ok := latestVersion(resp.Data)
	if !ok {
		return v, nil, false, nil
	}
	return v, related(v, "localizations", k.LocType, indexIncluded(resp.Included)), true, nil
}

// writeLocs writes localizations on the version found while planning, or
// creates the version when there was none.
func writeLocs(ctx context.Context, env *tool.Env, k versionKind, parentID string, found bool, v tool.Resource, existing []tool.Resource, want map[string]map[string]any) (versionOut, []upsertItem, error) {
	out := versionOut{ID: v.ID, State: v.Str("state")}
	if !found {
		var err error
		if out, existing, err = openVersion(ctx, env, k, parentID); err != nil {
			return out, nil, err
		}
	}
	return out, upsertLocs(ctx, env, k, out.ID, existing, want), nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
