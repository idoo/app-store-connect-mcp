// Package portalsigning manages developer-portal-only signing setup: App
// Groups, their assignment to bundle IDs, and capabilities missing from the
// public API (Increased Memory Limit, Background GPU Access, ...). It goes
// through env.DevPortal, which uses the API key where Apple accepts it
// (reading a bundle ID, the capability catalog, saving a bundle ID) and the
// human's web session only where Apple refuses the key: listing every App
// Group of the team and creating one.
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
	"sync"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Portal paths (relative to the developer portal's API root).
const (
	legacyPrefix    = "QH65B2/account/ios/identifiers/"
	pathListGroups  = legacyPrefix + "listApplicationGroups.action"
	pathAddGroup    = legacyPrefix + "addApplicationGroup.action"
	pathBundlePrefx = "v1/bundleIds/"
	pathCatalog     = "v1/capabilities"
	capAppGroups    = "APP_GROUPS"
	groupsPageSize  = 500
	groupsMaxPages  = 20
	// discoveryMaxReads caps the bundle IDs read to learn App Groups without
	// a web session. Measured live: 24 reads, 4 at a time, took 3.2 s, so
	// 80 take about 11 s (20 s if Apple slows to 1 s a read), under a
	// client's 30 s call.
	discoveryMaxReads = 80
	discoveryParallel = 4
	// discoveryMaxBundles caps the public API listing of bundle IDs.
	discoveryMaxBundles = 2000
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

// groupList is the team's App Groups as far as they could be learned.
type groupList struct {
	groups []appGroup
	// partial: no web session, so the groups were learned from bundle IDs;
	// a group assigned to no bundle ID is missing.
	partial bool
	read    int // bundle IDs read to learn them (partial only)
	unread  int // bundle IDs with App Groups left unread at the cap
}

// reason says why a partial list is partial.
func (l groupList) reason() string {
	s := fmt.Sprintf("without an Apple ID sign-in App Groups are learned from bundle IDs (%d read)", l.read)
	if l.unread > 0 {
		s += fmt.Sprintf("; %d more bundle IDs with App Groups were not read (cap %d)", l.unread, discoveryMaxReads)
	}
	return s + "; a group assigned to no bundle ID is not listed"
}

// teamGroups lists the team's App Groups: the whole list through the web
// session, else (no session) the groups assigned to bundle IDs. near are
// the bundle IDs being changed (siblings are read first, these themselves
// not at all: known holds what their reads found); want stops the search
// once every identifier in it is found.
func teamGroups(ctx context.Context, env *tool.Env, team string, near []portalBundle, want []string) (groupList, error) {
	gs, err := listGroups(ctx, env, team)
	if err == nil {
		return groupList{groups: gs}, nil
	}
	if !errors.Is(err, tool.ErrWebSessionRequired) {
		return groupList{}, err
	}
	return groupsFromBundles(ctx, env, team, near, want)
}

// groupsFromBundles learns App Groups from the bundle IDs that carry them.
func groupsFromBundles(ctx context.Context, env *tool.Env, team string, near []portalBundle, want []string) (groupList, error) {
	l := groupList{partial: true}
	byID := map[string]appGroup{}
	skip := map[string]bool{}
	for _, b := range near {
		skip[b.ref.ID] = true
		for _, g := range b.groupInfo {
			byID[g.ID] = g
		}
	}
	done := func() bool {
		if len(want) == 0 {
			return false
		}
		found := map[string]bool{}
		for _, g := range byID {
			found[g.Identifier] = true
		}
		return !slices.ContainsFunc(want, func(w string) bool { return !found[w] })
	}
	finish := func() groupList {
		for _, g := range byID {
			if g.Identifier != "" {
				l.groups = append(l.groups, g)
			}
		}
		sort.Slice(l.groups, func(i, j int) bool { return l.groups[i].Identifier < l.groups[j].Identifier })
		return l
	}
	if done() {
		return finish(), nil
	}
	cands, more, err := groupBundles(ctx, env)
	if err != nil {
		return groupList{}, err
	}
	cands = slices.DeleteFunc(cands, func(b bundleRef) bool { return skip[b.ID] })
	score := func(b bundleRef) int {
		best := 0
		for _, n := range near {
			best = max(best, sharedLabels(b.Identifier, n.ref.Identifier))
		}
		return best
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if si, sj := score(cands[i]), score(cands[j]); si != sj {
			return si > sj
		}
		return cands[i].Identifier < cands[j].Identifier
	})
	var mu sync.Mutex
	for start := 0; start < len(cands) && !done(); start += discoveryParallel {
		if l.read >= discoveryMaxReads {
			l.unread = len(cands) - start
			break
		}
		wave := cands[start:min(start+discoveryParallel, len(cands), start+discoveryMaxReads-l.read)]
		err := tool.Parallel(ctx, discoveryParallel, wave, func(ctx context.Context, _ int, ref bundleRef) error {
			b, err := readBundle(ctx, env, team, ref)
			var pe *tool.PortalError
			if errors.As(err, &pe) && pe.Status == http.StatusNotFound {
				return nil // deleted since the listing
			}
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			for _, g := range b.groupInfo {
				byID[g.ID] = g
			}
			return nil
		})
		if err != nil {
			return groupList{}, fmt.Errorf("learn App Groups from bundle IDs: %w", err)
		}
		l.read += len(wave)
	}
	if more && !done() {
		l.unread++ // the listing itself was cut: at least one more may exist
	}
	return finish(), nil
}

// groupBundles lists the team's bundle IDs with the App Groups capability
// through the public API; more reports a listing cut at its cap.
func groupBundles(ctx context.Context, env *tool.Env) ([]bundleRef, bool, error) {
	q := url.Values{
		"fields[bundleIds]":            {"identifier,seedId,bundleIdCapabilities"},
		"fields[bundleIdCapabilities]": {"capabilityType"},
		"include":                      {"bundleIdCapabilities"},
		"limit[bundleIdCapabilities]":  {"50"},
	}
	res, err := env.ASC.List(ctx, "/v1/bundleIds", tool.ListOptions{Query: q, Limit: 200, MaxItems: discoveryMaxBundles})
	if err != nil {
		return nil, false, fmt.Errorf("list bundle IDs: %w", err)
	}
	inc := tool.IndexIncluded(res.Included)
	var out []bundleRef
	for _, r := range res.Data {
		if slices.ContainsFunc(r.RelIDs("bundleIdCapabilities"), func(id string) bool {
			return inc["bundleIdCapabilities/"+id].Str("capabilityType") == capAppGroups
		}) {
			out = append(out, bundleFromResource(r))
		}
	}
	return out, res.NextCursor != "", nil
}

// sharedLabels counts the leading dot-separated labels a and b share.
func sharedLabels(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return n
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
	ref       bundleRef
	caps      []enabledCap
	groups    []string   // assigned App Group ids, sorted
	groupInfo []appGroup // included appGroups resources, sorted by id
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
	out := portalBundle{ref: ref}
	for _, r := range doc.Included {
		switch r.Type {
		case "capabilities":
			names[r.ID] = r.str("name")
		case "appGroups":
			out.groupInfo = append(out.groupInfo, appGroup{ID: r.ID, Identifier: r.str("identifier"), Name: tool.Untrusted(r.str("name"))})
		}
	}
	sort.Slice(out.groupInfo, func(i, j int) bool { return out.groupInfo[i].ID < out.groupInfo[j].ID })
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
