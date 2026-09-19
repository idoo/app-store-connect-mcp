package signing

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "signing_profiles_list", Toolset: tool.Signing, Class: tool.Read, Idempotent: true, NeedsTeamKey: true,
		Title: "List provisioning profiles",
		Description: "Provisioning profiles with type, state, bundle identifier, expiration, device and certificate counts. " +
			"Filter by type, state, name or bundle identifier. download:true with one id returns profile_content (base64 .mobileprovision).",
		Enums: map[string][]any{"type": s13bProfileTypes, "state": s13bProfileStates},
	}, profilesList))
	tool.Register(tool.New(tool.Meta{
		Name: "signing_profile_create", Toolset: tool.Signing, Class: tool.Write, NeedsTeamKey: true,
		Title: "Create a provisioning profile",
		Description: "Create a profile for a bundle ID (identifier or id). certificates: ids or [\"all\"] (all unexpired of the matching type); " +
			"devices: ids or [\"all\"] (enabled devices), only for development/ad hoc types. An active profile with the same name, type and bundle is already:true.",
		Enums: map[string][]any{"profile_type": s13bProfileTypes},
	}, profileCreate))
}

// --- signing_profiles_list ---

type profilesListIn struct {
	ID       string `json:"id,omitempty" jsonschema:"read only this profile id"`
	Download bool   `json:"download,omitempty" jsonschema:"return profile_content (base64); requires id"`
	Type     string `json:"type,omitempty"`
	State    string `json:"state,omitempty"`
	Name     string `json:"name,omitempty" jsonschema:"profile name filter passed to Apple"`
	BundleID string `json:"bundle_id,omitempty" jsonschema:"keep only profiles of this bundle identifier"`
	MaxItems int    `json:"max_items,omitempty" jsonschema:"maximum profiles to read (default 200, max 1000)"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"next_cursor of a previous call"`
}

type profilesListOut struct {
	Profiles   []s13bProfileOut `json:"profiles"`
	Returned   int              `json:"returned"`
	Total      int              `json:"total,omitempty"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func profilesList(ctx context.Context, env *tool.Env, in profilesListIn) (*tool.Result, error) {
	if in.MaxItems < 0 || in.MaxItems > 1000 {
		return nil, s13aInvalid("max_items must be between 1 and 1000")
	}
	in.ID = strings.TrimSpace(in.ID)
	if in.Download && in.ID == "" {
		return nil, s13aInvalid("download needs the id of one profile; list first to find it")
	}
	if in.ID != "" {
		return profileGet(ctx, env, in.ID, in.Download)
	}
	q := s13bProfileQuery(false)
	q.Set("sort", "name")
	if in.Type != "" {
		q.Set("filter[profileType]", in.Type)
	}
	if in.State != "" {
		q.Set("filter[profileState]", in.State)
	}
	if in.Name != "" {
		q.Set("filter[name]", in.Name)
	}
	res, err := env.ASC.List(ctx, "/v1/profiles", tool.ListOptions{Query: q, MaxItems: in.MaxItems, Cursor: in.Cursor})
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	inc := s13aIncluded(res.Included)
	now := env.Clock.Now()
	out := profilesListOut{Profiles: []s13bProfileOut{}, Total: res.Total, NextCursor: res.NextCursor}
	for _, r := range res.Data {
		p := s13bProfile(r, inc, now)
		if in.BundleID != "" && !strings.EqualFold(p.BundleID, in.BundleID) {
			continue
		}
		out.Profiles = append(out.Profiles, p)
	}
	out.Returned = len(out.Profiles)
	return tool.OK(out), nil
}

// profileGet reads one profile, with its content when download is set.
func profileGet(ctx context.Context, env *tool.Env, id string, download bool) (*tool.Result, error) {
	p, err := s13bReadProfile(ctx, env, id, download)
	if err != nil {
		return nil, err
	}
	res := tool.OK(profilesListOut{Profiles: []s13bProfileOut{p}, Returned: 1})
	if download && p.Content == "" {
		res.Warn("Apple returned no profile content; an INVALID profile must be regenerated")
	}
	return res, nil
}

// s13bReadProfile reads one profile by id.
func s13bReadProfile(ctx context.Context, env *tool.Env, id string, content bool) (s13bProfileOut, error) {
	resp, err := env.ASC.Get(ctx, "/v1/profiles/"+url.PathEscape(id), s13bProfileQuery(content))
	notFound := &tool.ErrNotFound{Kind: "profile", Ref: id, Next: []tool.NextStep{{Tool: "signing_profiles_list"}}}
	if s13aStatus(err) == http.StatusNotFound {
		return s13bProfileOut{}, notFound
	}
	if err != nil {
		return s13bProfileOut{}, fmt.Errorf("read profile %s: %w", id, err)
	}
	r, ok := resp.First()
	if !ok {
		return s13bProfileOut{}, notFound
	}
	return s13bProfile(r, s13aIncluded(resp.Included), env.Clock.Now()), nil
}

// --- signing_profile_create ---

type profileCreateIn struct {
	Name         string   `json:"name" jsonschema:"profile name, unique in the team"`
	ProfileType  string   `json:"profile_type"`
	BundleID     string   `json:"bundle_id" jsonschema:"bundle identifier (com.example.app) or bundle ID resource id"`
	Certificates []string `json:"certificates" jsonschema:"certificate ids, or [\"all\"] for every unexpired certificate of the matching type"`
	Devices      []string `json:"devices,omitempty" jsonschema:"device ids, or [\"all\"] for every enabled device; development and ad hoc types only"`
}

type profileCreateOut struct {
	Profile      s13bProfileOut `json:"profile"`
	Created      bool           `json:"created"`
	Certificates []string       `json:"certificate_ids,omitempty"`
	Devices      []string       `json:"device_ids,omitempty"`
}

func profileCreate(ctx context.Context, env *tool.Env, in profileCreateIn) (*tool.Result, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, s13aInvalid("name is required")
	}
	kind := s13bKind(in.ProfileType)
	if len(kind.CertTypes) == 0 {
		return nil, s13aInvalid("profile_type %q is not supported", in.ProfileType)
	}
	if len(in.Certificates) == 0 {
		return nil, s13aInvalid("certificates is required (ids or [\"all\"])")
	}
	switch {
	case kind.DevicePlatform == "" && len(in.Devices) > 0:
		return nil, s13aInvalid("%s profiles take no devices", in.ProfileType)
	case kind.DevicePlatform != "" && len(in.Devices) == 0:
		return nil, s13aInvalid("%s profiles need devices (ids or [\"all\"])", in.ProfileType)
	}
	bundle, err := s13aLookupBundle(ctx, env, in.BundleID)
	if err != nil {
		return nil, err
	}
	next := func(p s13bProfileOut) tool.NextStep {
		return tool.NextStep{Tool: "signing_profiles_list", Args: map[string]any{"id": p.ID, "download": true}}
	}
	if p, ok, err := s13bFindProfile(ctx, env, in.Name, in.ProfileType, bundle.ID); err != nil {
		return nil, err
	} else if ok {
		return tool.Already(profileCreateOut{Profile: p}).Then(next(p)), nil
	}
	certs, err := s13bCertIDs(ctx, env, in.Certificates, kind.CertTypes)
	if err != nil {
		return nil, err
	}
	var devices []string
	if kind.DevicePlatform != "" {
		if devices, err = s13bDeviceIDs(ctx, env, in.Devices, kind.DevicePlatform); err != nil {
			return nil, err
		}
	}
	rels := map[string]any{
		"bundleId":     s13bRelData("bundleIds", bundle.ID),
		"certificates": s13bRelList("certificates", certs),
	}
	if len(devices) > 0 {
		rels["devices"] = s13bRelList("devices", devices)
	}
	body := map[string]any{"data": map[string]any{
		"type":          "profiles",
		"attributes":    map[string]any{"name": in.Name, "profileType": in.ProfileType},
		"relationships": rels,
	}}
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/profiles", Body: body, Write: true})
	if s13aStatus(err) == http.StatusConflict {
		if p, ok, rerr := s13bFindProfile(ctx, env, in.Name, in.ProfileType, bundle.ID); rerr == nil && ok {
			return tool.Already(profileCreateOut{Profile: p}).Then(next(p)), nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("create profile %s: %w", in.Name, err)
	}
	r, ok := resp.First()
	if !ok {
		return nil, fmt.Errorf("create profile %s: %w: empty response", in.Name, tool.ErrInternal)
	}
	p := s13bProfile(r, nil, env.Clock.Now())
	p.Content = "" // large; fetched on demand via download
	p.BundleID, p.BundleIDRef = bundle.Identifier, bundle.ID
	p.DeviceCount, p.CertificateCount = len(devices), len(certs)
	p.CountsCapped = false
	return tool.OK(profileCreateOut{Profile: p, Created: true, Certificates: certs, Devices: devices}).Then(next(p)), nil
}

// s13bFindProfile returns the ACTIVE profile with exactly name, type and
// bundle ID, if any.
func s13bFindProfile(ctx context.Context, env *tool.Env, name, profileType, bundleRef string) (s13bProfileOut, bool, error) {
	q := s13bProfileQuery(false)
	q.Set("filter[name]", name)
	res, err := env.ASC.List(ctx, "/v1/profiles", tool.ListOptions{Query: q, MaxItems: 200})
	if err != nil {
		return s13bProfileOut{}, false, fmt.Errorf("find profile %s: %w", name, err)
	}
	inc := s13aIncluded(res.Included)
	now := env.Clock.Now()
	for _, r := range res.Data {
		p := s13bProfile(r, inc, now)
		if p.Name == name && p.Type == profileType && p.BundleIDRef == bundleRef && p.State == "ACTIVE" {
			return p, true, nil
		}
	}
	return s13bProfileOut{}, false, nil
}

// s13bCertIDs returns refs, or for ["all"] the ids of every unexpired
// certificate of types.
func s13bCertIDs(ctx context.Context, env *tool.Env, refs, types []string) ([]string, error) {
	if !s13bIsAll(refs) {
		return s13bCleanIDs("certificates", refs)
	}
	q := url.Values{
		"fields[certificates]":    {"certificateType,expirationDate"},
		"filter[certificateType]": {strings.Join(types, ",")},
	}
	res, err := env.ASC.List(ctx, "/v1/certificates", tool.ListOptions{Query: q, MaxItems: 1000})
	if err != nil {
		return nil, fmt.Errorf("list certificates: %w", err)
	}
	now := env.Clock.Now()
	var ids []string
	for _, r := range res.Data {
		if d := s13bDaysLeft(r.Str("expirationDate"), now); d != nil && *d < 0 {
			continue
		}
		if slices.Contains(types, r.Str("certificateType")) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil, tool.WithNext(&tool.ErrNotFound{
			Kind: "certificate", Ref: strings.Join(types, " or "),
			Hint: "no unexpired certificate of a matching type; create one in Xcode or the developer portal",
		}, tool.NextStep{Tool: "signing_certificates_list"})
	}
	return ids, nil
}

// s13bDeviceIDs returns refs, or for ["all"] the ids of every enabled device
// of platform.
func s13bDeviceIDs(ctx context.Context, env *tool.Env, refs []string, platform string) ([]string, error) {
	if !s13bIsAll(refs) {
		return s13bCleanIDs("devices", refs)
	}
	q := url.Values{
		"fields[devices]":  {"udid"},
		"filter[status]":   {"ENABLED"},
		"filter[platform]": {platform},
	}
	res, err := env.ASC.List(ctx, "/v1/devices", tool.ListOptions{Query: q, MaxItems: 1000})
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	ids := make([]string, 0, len(res.Data))
	for _, r := range res.Data {
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		return nil, tool.WithNext(&tool.ErrNotFound{
			Kind: "device", Ref: platform, Hint: "no enabled " + platform + " device is registered",
		}, tool.NextStep{Tool: "signing_devices_register"})
	}
	return ids, nil
}

// s13bCleanIDs trims, deduplicates and validates explicit ids.
func s13bCleanIDs(arg string, refs []string) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" || strings.EqualFold(r, "all") {
			return nil, s13aInvalid(`%s: pass ids, or exactly ["all"]`, arg)
		}
		if !slices.Contains(ids, r) {
			ids = append(ids, r)
		}
	}
	return ids, nil
}
