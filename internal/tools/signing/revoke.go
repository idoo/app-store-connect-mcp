package signing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "signing_revoke", Toolset: tool.Signing, Class: tool.Destructive, NeedsTeamKey: true,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Revoke a certificate or delete a profile",
		Description: "Revoke a signing certificate or delete a provisioning profile by id. Without confirm returns a preview " +
			"(name, type, expiry; for certificates the profiles it invalidates). confirm:true asks the human; irreversible.",
		Enums: map[string][]any{"kind": {"certificate", "profile"}},
	}, signingRevoke))
}

// maxAffectedNames caps the profile names listed in a revoke preview.
const maxAffectedNames = 20

type revokeIn struct {
	tool.ConfirmFlag
	Kind string `json:"kind"`
	ID   string `json:"id" jsonschema:"certificate or profile id"`
}

type revokeTarget struct {
	Kind           string   `json:"kind"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	ExpirationDate string   `json:"expiration_date,omitempty"`
	SerialNumber   string   `json:"serial_number,omitempty"`
	BundleID       string   `json:"bundle_id,omitempty"`
	AffectedCount  *int     `json:"affected_profiles,omitempty"`
	AffectedNames  []string `json:"affected_profile_names,omitempty"`
}

type revokeOut struct {
	Revoked revokeTarget `json:"revoked"`
}

func signingRevoke(ctx context.Context, env *tool.Env, in revokeIn) (*tool.Result, error) {
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		return nil, tool.InvalidArgs("id is required")
	}
	var (
		target revokeTarget
		path   string
		err    error
	)
	irreversible := []string{}
	switch in.Kind {
	case "certificate":
		target, err = certTarget(ctx, env, in.ID)
		path = "/v1/certificates/" + url.PathEscape(in.ID)
		irreversible = append(irreversible,
			"the certificate is revoked at once and cannot be restored",
			"profiles using it become INVALID; apps signed with it keep running until their profile expires")
	case "profile":
		var p profileOut
		p, err = readProfile(ctx, env, in.ID, false)
		target = revokeTarget{
			Kind: "profile", ID: p.ID, Name: p.Name, Type: p.Type, ExpirationDate: p.ExpirationDate, BundleID: p.BundleID,
		}
		path = "/v1/profiles/" + url.PathEscape(in.ID)
		irreversible = append(irreversible, "the profile is deleted; builds still need a new profile to be signed")
	default:
		return nil, tool.InvalidArgs("kind must be certificate or profile")
	}
	if err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("%s %s %q (%s, expires %s)", map[string]string{"certificate": "revoke certificate", "profile": "delete profile"}[in.Kind],
		target.ID, target.Name, target.Type, orDefault(target.ExpirationDate, "unknown"))
	affected := []string{in.Kind + " " + target.ID}
	if target.AffectedCount != nil {
		summary += fmt.Sprintf("; invalidates %d profile(s)", *target.AffectedCount)
		for _, n := range target.AffectedNames {
			affected = append(affected, "profile "+n)
		}
	}
	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{
		Summary: summary, Current: target, Affected: affected, Irreversible: irreversible,
	})
	if res != nil {
		return res, nil
	}
	if _, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodDelete, Path: path, Write: true}); err != nil {
		if !errors.Is(err, tool.ErrAlreadyGone) {
			return nil, fmt.Errorf("%s %s: %w", in.Kind, in.ID, err)
		}
		// A replayed DELETE found the object gone: the first attempt applied.
		next := tool.NextStep{Tool: "signing_profiles_list", Args: map[string]any{"state": "INVALID"}}
		return tool.Already(revokeOut{Revoked: target}).Then(next), nil
	}
	next := tool.NextStep{Tool: "signing_profiles_list", Args: map[string]any{"state": "INVALID"}}
	return tool.OK(revokeOut{Revoked: target}).Then(next), nil
}

// certTarget reads a certificate and the profiles that use it.
func certTarget(ctx context.Context, env *tool.Env, id string) (revokeTarget, error) {
	resp, err := env.ASC.Get(ctx, "/v1/certificates/"+url.PathEscape(id), url.Values{
		"fields[certificates]": {"name,certificateType,displayName,serialNumber,expirationDate"},
	})
	notFound := &tool.ErrNotFound{Kind: "certificate", Ref: id, Next: []tool.NextStep{{Tool: "signing_certificates_list"}}}
	if tool.APIStatus(err) == http.StatusNotFound {
		return revokeTarget{}, notFound
	}
	if err != nil {
		return revokeTarget{}, fmt.Errorf("read certificate %s: %w", id, err)
	}
	r, ok := resp.First()
	if !ok {
		return revokeTarget{}, notFound
	}
	t := revokeTarget{
		Kind: "certificate", ID: r.ID, Name: orDefault(r.Str("displayName"), r.Str("name")), Type: r.Str("certificateType"),
		ExpirationDate: r.Str("expirationDate"), SerialNumber: r.Str("serialNumber"),
	}
	// Profiles have no certificate filter: read them with certificate ids.
	q := url.Values{
		"fields[profiles]":     {"name,profileState,certificates"},
		"fields[certificates]": {"name"},
		"include":              {"certificates"},
		"limit[certificates]":  {"50"},
	}
	res, err := env.ASC.List(ctx, "/v1/profiles", tool.ListOptions{Query: q, MaxItems: 5000})
	if err != nil {
		return revokeTarget{}, fmt.Errorf("list profiles using certificate %s: %w", id, err)
	}
	n := 0
	for _, p := range res.Data {
		if !slices.Contains(p.RelIDs("certificates"), id) {
			continue
		}
		n++
		if len(t.AffectedNames) < maxAffectedNames {
			t.AffectedNames = append(t.AffectedNames, p.Str("name"))
		}
	}
	t.AffectedCount = &n
	return t, nil
}

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
