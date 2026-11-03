// Package portalsigning manages developer-portal-only signing setup through
// the human's Apple ID web session (developer.apple.com/services-account):
// App Groups, their assignment to bundle IDs, and capabilities missing from
// the public API (Increased Memory Limit, Background GPU Access, ...).
package portalsigning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Portal paths (relative to developer.apple.com/services-account/).
const (
	legacyPrefix    = "QH65B2/account/ios/identifiers/"
	pathListGroups  = legacyPrefix + "listApplicationGroups.action"
	pathAddGroup    = legacyPrefix + "addApplicationGroup.action"
	pathBundlePrefx = "v1/bundleIds/"
	pathCatalog     = "v1/capabilities"
	capAppGroups    = "APP_GROUPS"
	groupsPageSize  = 500
	groupsMaxPages  = 20
)

var (
	teamRE  = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	groupRE = regexp.MustCompile(`^group\.[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*$`)
	nonAlnu = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// --- public API: bundle IDs and team ---

// bundleRef is a bundle ID resolved through the public API (its resource id
// is also the portal id).
type bundleRef struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	seedID     string
}

func bundleFromResource(r tool.Resource) bundleRef {
	return bundleRef{ID: r.ID, Identifier: r.Str("identifier"), seedID: r.Str("seedId")}
}

var bundleFields = url.Values{"fields[bundleIds]": {"identifier,seedId"}}

// resolveBundle resolves a bundle identifier (exact match) or resource id.
func resolveBundle(ctx context.Context, env *tool.Env, ref string) (bundleRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return bundleRef{}, tool.InvalidArgs("bundle ID must not be empty")
	}
	listNext := tool.NextStep{Tool: "signing_bundle_ids_list", Args: map[string]any{"filter": ref}}
	if !strings.Contains(ref, ".") {
		resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodGet, Path: "/v1/bundleIds/" + url.PathEscape(ref), Query: bundleFields})
		if err != nil {
			if tool.APIStatus(err) == http.StatusNotFound {
				return bundleRef{}, tool.WithNext(tool.InvalidArgs("no bundle ID with id %s", ref), listNext)
			}
			return bundleRef{}, err
		}
		if r, ok := resp.First(); ok {
			return bundleFromResource(r), nil
		}
		return bundleRef{}, tool.WithNext(tool.InvalidArgs("no bundle ID with id %s", ref), listNext)
	}
	q := url.Values{"filter[identifier]": {ref}, "fields[bundleIds]": {"identifier,seedId"}, "limit": {"200"}}
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodGet, Path: "/v1/bundleIds", Query: q})
	if err != nil {
		return bundleRef{}, err
	}
	for _, r := range resp.Data {
		if r.Str("identifier") == ref {
			return bundleFromResource(r), nil
		}
	}
	return bundleRef{}, tool.WithNext(tool.InvalidArgs("bundle identifier %s is not registered", ref), listNext)
}

// resolveTeam picks the portal team: the team_id argument, else the App ID
// prefix (seed id) shared by the bundle IDs. Without bundle IDs it reads
// one bundle ID of the API key's team.
func resolveTeam(ctx context.Context, env *tool.Env, arg string, bundles []bundleRef) (string, error) {
	if arg = strings.TrimSpace(arg); arg != "" {
		if !teamRE.MatchString(arg) {
			return "", tool.InvalidArgs("team_id %q is not a 10-character team id", tool.SanitizeText(arg))
		}
		return arg, nil
	}
	if len(bundles) == 0 {
		resp, err := env.ASC.Do(ctx, tool.Request{
			Method: http.MethodGet, Path: "/v1/bundleIds",
			Query: url.Values{"fields[bundleIds]": {"identifier,seedId"}, "limit": {"1"}},
		})
		if err != nil {
			return "", fmt.Errorf("find the team (pass team_id to skip): %w", err)
		}
		r, ok := resp.First()
		if !ok {
			return "", tool.InvalidArgs("the team has no bundle IDs to read its team id from; pass team_id")
		}
		bundles = []bundleRef{bundleFromResource(r)}
	}
	seeds := map[string]bool{}
	for _, b := range bundles {
		seeds[b.seedID] = true
	}
	if len(seeds) != 1 || !teamRE.MatchString(bundles[0].seedID) {
		return "", tool.InvalidArgs("cannot tell the team from the bundle IDs' App ID prefixes; pass team_id")
	}
	return bundles[0].seedID, nil
}

// --- portal JSON:API decoding ---

type jaRes struct {
	Type          string                     `json:"type"`
	ID            string                     `json:"id"`
	Attributes    map[string]any             `json:"attributes"`
	Relationships map[string]json.RawMessage `json:"relationships"`
}

type jaDoc struct {
	Data     json.RawMessage `json:"data"`
	Included []jaRes         `json:"included"`
}

type jaLink struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// rel returns the linkage of relationship name (one or many).
func (r jaRes) rel(name string) []jaLink {
	raw, ok := r.Relationships[name]
	if !ok {
		return nil
	}
	var wrap struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &wrap) != nil || len(wrap.Data) == 0 {
		return nil
	}
	var many []jaLink
	if json.Unmarshal(wrap.Data, &many) == nil {
		return many
	}
	var one jaLink
	if json.Unmarshal(wrap.Data, &one) == nil && one.ID != "" {
		return []jaLink{one}
	}
	return nil
}

func (r jaRes) str(key string) string {
	s, _ := r.Attributes[key].(string)
	return s
}

// --- portal reads ---

// appGroup is one App Group of the team.
type appGroup struct {
	ID         string         `json:"id"`
	Identifier string         `json:"identifier"`
	Name       tool.Untrusted `json:"name,omitempty"`
	Status     string         `json:"status,omitempty"`
}

// listGroups reads every App Group of the team (legacy endpoint, paged).
func listGroups(ctx context.Context, env *tool.Env, team string) ([]appGroup, error) {
	var out []appGroup
	for page := 1; page <= groupsMaxPages; page++ {
		form := url.Values{
			"teamId": {team}, "pageNumber": {strconv.Itoa(page)},
			"pageSize": {strconv.Itoa(groupsPageSize)}, "sort": {"name=asc"},
		}
		var resp struct {
			List []struct {
				ID         string `json:"applicationGroup"`
				Name       string `json:"name"`
				Identifier string `json:"identifier"`
				Status     string `json:"status"`
			} `json:"applicationGroupList"`
		}
		if err := env.DevPortal.Do(ctx, http.MethodPost, pathListGroups, form, &resp); err != nil {
			return nil, fmt.Errorf("list App Groups: %w", err)
		}
		for _, g := range resp.List {
			out = append(out, appGroup{ID: g.ID, Identifier: g.Identifier, Name: tool.Untrusted(g.Name), Status: g.Status})
		}
		if len(resp.List) < groupsPageSize {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identifier < out[j].Identifier })
	return out, nil
}

// enabledCap is one capability enabled on a bundle ID.
type enabledCap struct {
	ID       string         `json:"id"`
	Name     tool.Untrusted `json:"name,omitempty"`
	Settings any            `json:"settings,omitempty"`
	groups   []string       // App Group ids (APP_GROUPS only)
}

// catalogCap is one capability the portal offers for a bundle ID.
type catalogCap struct {
	ID          string         `json:"id"`
	Name        tool.Untrusted `json:"name,omitempty"`
	Description tool.Untrusted `json:"description,omitempty"`
}

// portalBundle is a bundle ID as the portal sees it.
type portalBundle struct {
	ref    bundleRef
	caps   []enabledCap
	groups []string // assigned App Group ids, sorted
}

func (b portalBundle) has(capID string) bool {
	return slices.ContainsFunc(b.caps, func(c enabledCap) bool { return c.ID == capID })
}

// readBundle reads a bundle ID's enabled capabilities and App Groups.
func readBundle(ctx context.Context, env *tool.Env, team string, ref bundleRef) (portalBundle, error) {
	q := url.Values{"include": {"bundleIdCapabilities,bundleIdCapabilities.capability,bundleIdCapabilities.appGroups"}}
	var doc jaDoc
	if err := tool.DevPortalGet(ctx, env.DevPortal, pathBundlePrefx+url.PathEscape(ref.ID), q, team, &doc); err != nil {
		return portalBundle{}, fmt.Errorf("read bundle ID %s: %w", ref.Identifier, err)
	}
	names := map[string]string{}
	for _, r := range doc.Included {
		if r.Type == "capabilities" {
			names[r.ID] = r.str("name")
		}
	}
	out := portalBundle{ref: ref}
	groups := map[string]bool{}
	for _, r := range doc.Included {
		if r.Type != "bundleIdCapabilities" {
			continue
		}
		if en, ok := r.Attributes["enabled"].(bool); ok && !en {
			continue
		}
		c := enabledCap{ID: capIDOf(r, ref.ID), Settings: r.Attributes["settings"]}
		c.Name = tool.Untrusted(names[c.ID])
		for _, l := range r.rel("appGroups") {
			c.groups = append(c.groups, l.ID)
			groups[l.ID] = true
		}
		if s, ok := c.Settings.([]any); ok && len(s) == 0 {
			c.Settings = nil
		}
		out.caps = append(out.caps, c)
	}
	sort.Slice(out.caps, func(i, j int) bool { return out.caps[i].ID < out.caps[j].ID })
	for g := range groups {
		out.groups = append(out.groups, g)
	}
	sort.Strings(out.groups)
	return out, nil
}

// capIDOf returns the capability id of a bundleIdCapabilities resource.
func capIDOf(r jaRes, bundleID string) string {
	if l := r.rel("capability"); len(l) == 1 {
		return l[0].ID
	}
	if t := r.str("capabilityType"); t != "" {
		return t
	}
	return strings.TrimPrefix(r.ID, bundleID+"_")
}

// readCatalog reads the capabilities the portal offers for a bundle ID.
func readCatalog(ctx context.Context, env *tool.Env, team, bundleID string) ([]catalogCap, error) {
	var doc jaDoc
	q := url.Values{"filter[bundleId]": {bundleID}}
	if err := tool.DevPortalGet(ctx, env.DevPortal, pathCatalog, q, team, &doc); err != nil {
		return nil, fmt.Errorf("read the capability catalog: %w", err)
	}
	var data []jaRes
	if err := json.Unmarshal(doc.Data, &data); err != nil {
		return nil, fmt.Errorf("read the capability catalog: %w: unexpected answer", tool.ErrInternal)
	}
	out := make([]catalogCap, 0, len(data))
	for _, r := range data {
		out = append(out, catalogCap{ID: r.ID, Name: tool.Untrusted(r.str("name")), Description: tool.Untrusted(r.str("description"))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// norm folds a capability id or name for matching.
func norm(s string) string {
	return strings.ToLower(nonAlnu.ReplaceAllString(s, ""))
}

// matchCap resolves a capability id or human name against the catalog:
// exact id, exact name (ignoring case, spaces and punctuation), else a
// unique name/id containing the query.
func matchCap(catalog []catalogCap, q string) (catalogCap, error) {
	n := norm(q)
	if n == "" {
		return catalogCap{}, tool.InvalidArgs("empty capability")
	}
	for _, c := range catalog {
		if c.ID == q || norm(c.ID) == n || norm(string(c.Name)) == n {
			return c, nil
		}
	}
	var hits []catalogCap
	for _, c := range catalog {
		if strings.Contains(norm(c.ID), n) || strings.Contains(norm(string(c.Name)), n) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return catalogCap{}, tool.InvalidArgs("capability %q is not in the portal catalog for this bundle ID (signing_portal_get lists it)", tool.SanitizeText(q))
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return catalogCap{}, tool.InvalidArgs("capability %q matches several: %s", tool.SanitizeText(q), strings.Join(ids, ", "))
}

// --- portal writes ---

// groupName derives the portal's App Group description (letters, digits
// and spaces only) from its identifier.
func groupName(identifier string) string {
	return strings.TrimSpace(nonAlnu.ReplaceAllString(strings.TrimPrefix(identifier, "group."), " "))
}

func createGroup(ctx context.Context, env *tool.Env, team, identifier string) (string, error) {
	form := url.Values{"name": {groupName(identifier)}, "identifier": {identifier}, "teamId": {team}}
	var resp struct {
		Group struct {
			ID string `json:"applicationGroup"`
		} `json:"applicationGroup"`
	}
	if err := env.DevPortal.Do(ctx, http.MethodPost, pathAddGroup, form, &resp); err != nil {
		return "", err
	}
	if resp.Group.ID == "" {
		return "", fmt.Errorf("%w: Apple did not return the new App Group id", tool.ErrOutcomeUnknown)
	}
	return resp.Group.ID, nil
}

// patchCapability sends the portal UI's bundle ID save: one capability
// enabled or disabled, with App Groups for APP_GROUPS.
func patchCapability(ctx context.Context, env *tool.Env, team, bundleID, capID string, enabled bool, groups []string) error {
	rels := map[string]any{"capability": map[string]any{"data": map[string]string{"type": "capabilities", "id": capID}}}
	if groups != nil {
		data := make([]map[string]string, len(groups))
		for i, g := range groups {
			data[i] = map[string]string{"type": "appGroups", "id": g}
		}
		rels["appGroups"] = map[string]any{"data": data}
	}
	body := map[string]any{"data": map[string]any{
		"type": "bundleIds", "id": bundleID,
		"attributes": map[string]any{"permissions": map[string]bool{"edit": true, "delete": true}, "seedId": team, "teamId": team},
		"relationships": map[string]any{"bundleIdCapabilities": map[string]any{"data": []any{map[string]any{
			"type":          "bundleIdCapabilities",
			"attributes":    map[string]any{"enabled": enabled, "settings": []any{}},
			"relationships": rels,
		}}}},
	}}
	return env.DevPortal.Do(ctx, http.MethodPatch, pathBundlePrefx+url.PathEscape(bundleID), body, nil)
}

// Step outcomes.
const (
	outcomeDone         = "done"
	outcomeUnknown      = "outcome_unknown"
	outcomeError        = "error"
	outcomeNotAttempted = "not_attempted"
)

// outcomeOf classifies a failed write: anything that may have reached Apple
// (5xx, timeout, transport failure) is outcome_unknown and never replayed.
func outcomeOf(err error) string {
	var pe *tool.PortalError
	switch {
	case errors.Is(err, tool.ErrOutcomeUnknown), errors.Is(err, context.DeadlineExceeded):
		return outcomeUnknown
	case errors.As(err, &pe):
		if pe.Status >= 500 {
			return outcomeUnknown
		}
		return outcomeError
	case errors.Is(err, tool.ErrWebSessionRequired), errors.Is(err, tool.ErrWritesDisabled), errors.Is(err, context.Canceled):
		return outcomeError
	}
	var te *tool.Error
	if errors.As(err, &te) {
		return outcomeError
	}
	return outcomeUnknown
}
