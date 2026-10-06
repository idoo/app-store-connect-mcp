package testflight

// Shared helpers of testflight_distribute, testflight_feedback_list and
// builds_expire (distribute.go, feedback.go, expire.go).

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Per-item outcomes reported by the distribute and feedback tools, in addition to outcomeAdded,
// outcomeAlready and outcomeError (testers.go).
const (
	outcomeCreated   = "created"
	outcomeUpdated   = "updated"
	outcomeUnchanged = "unchanged"
	outcomeRejected  = "rejected"
)

// platformEnum lists the App Store Connect platforms accepted by `platform`.
var platformEnum = []any{string(tool.IOS), string(tool.MacOS), string(tool.TVOS), string(tool.VisionOS)}

// buildOut is the resolved build echoed in results.
type buildOut struct {
	ID              string `json:"id"`
	Number          string `json:"number"`
	Version         string `json:"version,omitempty"`
	ProcessingState string `json:"processing_state,omitempty"`
}

// localeOutcome is the outcome of one per-locale write.
type localeOutcome struct {
	Locale  string `json:"locale"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// localeMap splits a locale-keyed argument into its "*" entry and explicit
// entries keyed by canonical App Store Connect locale codes. Keys must be real
// locales: a typo, emoji or language name never creates a stray localization.
func localeMap[T any](arg string, m map[string]T, check func(T) error) (all *T, byLocale map[string]T, err error) {
	byLocale = make(map[string]T, len(m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := m[k]
		if err := check(v); err != nil {
			return nil, nil, tool.InvalidArgs("%s[%q]: %v", arg, k, err)
		}
		if strings.TrimSpace(k) == "*" {
			all = &v
			continue
		}
		loc, err := applemeta.NormalizeLocale(k)
		if err != nil {
			return nil, nil, tool.InvalidArgs("%s: %q is not an App Store Connect locale (use codes such as en-US or de-DE, or *)", arg, k)
		}
		if _, dup := byLocale[loc]; dup {
			return nil, nil, tool.InvalidArgs("%s: several keys mean %s", arg, loc)
		}
		byLocale[loc] = v
	}
	return all, byLocale, nil
}

// localeTargets resolves a localeMap against the existing localizations: the
// "*" entry applies to every existing locale (or the primary locale when there
// are none) and explicit entries override it. Keys are returned sorted.
func localeTargets[T any](all *T, byLocale map[string]T, existing []string, primary func() (string, error)) (map[string]T, []string, error) {
	out := make(map[string]T, len(byLocale)+len(existing))
	if all != nil {
		locs := existing
		if len(locs) == 0 {
			p, err := primary()
			if err != nil {
				return nil, nil, err
			}
			locs = []string{p}
		}
		for _, l := range locs {
			out[l] = *all
		}
	}
	for l, v := range byLocale {
		out[l] = v
	}
	keys := make([]string, 0, len(out))
	for l := range out {
		keys = append(keys, l)
	}
	slices.Sort(keys)
	return out, keys, nil
}

// primaryLocale returns a memoized lookup of the app's primary locale.
func primaryLocale(ctx context.Context, env *tool.Env, appID string) func() (string, error) {
	var cached string
	return func() (string, error) {
		if cached != "" {
			return cached, nil
		}
		res, err := env.ASC.Get(ctx, "/v1/apps/"+appID, map[string][]string{"fields[apps]": {"primaryLocale"}})
		if err != nil {
			return "", fmt.Errorf("read primary locale of app %s: %w", appID, err)
		}
		a, ok := res.First()
		if !ok || a.Str("primaryLocale") == "" {
			return "", fmt.Errorf("app %s has no primary locale: %w", appID, tool.ErrUnavailable)
		}
		cached = a.Str("primaryLocale")
		return cached, nil
	}
}

// ref is a JSON:API resource identifier.
func ref(typ, id string) map[string]string { return map[string]string{"type": typ, "id": id} }

// toOne is a to-one relationship body.
func toOne(typ, id string) map[string]any { return map[string]any{"data": ref(typ, id)} }

// write performs one mutation; the caller's context carries write access.
func write(ctx context.Context, env *tool.Env, method, path string, body any) (*tool.Response, error) {
	return env.ASC.Do(ctx, tool.Request{Method: method, Path: path, Body: body, Write: true})
}

// patchBody is a resource update document.
func patchBody(typ, id string, attrs map[string]any) map[string]any {
	return map[string]any{"data": map[string]any{"type": typ, "id": id, "attributes": attrs}}
}

// changedAttrs returns the entries of want whose value differs from cur.
func changedAttrs(cur tool.Resource, want map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range want {
		if fmt.Sprint(cur.Attributes[k]) != fmt.Sprint(v) {
			out[k] = v
		}
	}
	return out
}

// includedByID indexes included resources by type and id.
func includedByID(rs []tool.Resource) map[string]tool.Resource {
	m := make(map[string]tool.Resource, len(rs))
	for _, r := range rs {
		m[r.Type+"/"+r.ID] = r
	}
	return m
}

// truncateUTF8 cuts s to at most n bytes on a rune boundary.
func truncateUTF8(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}
