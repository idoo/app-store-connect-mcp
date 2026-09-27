package signing

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "signing_certificates_list", Toolset: tool.Signing, Class: tool.Read, Idempotent: true, NeedsTeamKey: true,
		Title: "List signing certificates",
		Description: "Signing certificates by type, sorted by expiration, with days_left. expiring_within_days keeps only certificates " +
			"expiring (or expired) within N days. Certificate content (base64 DER) is returned only with include_content.",
		Enums: map[string][]any{"type": certificateTypes},
	}, certificatesList))
}

type certificatesListIn struct {
	Type               string `json:"type,omitempty"`
	ExpiringWithinDays int    `json:"expiring_within_days,omitempty" jsonschema:"only certificates expiring within N days, including already expired ones"`
	IncludeContent     bool   `json:"include_content,omitempty" jsonschema:"also return certificateContent (base64 DER)"`
	Limit              int    `json:"limit,omitempty" jsonschema:"maximum certificates to read (default 200, max 1000)"`
}

type certificateOut struct {
	ID             string `json:"id"`
	Name           string `json:"name,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	Type           string `json:"type"`
	Platform       string `json:"platform,omitempty"`
	SerialNumber   string `json:"serial_number,omitempty"`
	ExpirationDate string `json:"expiration_date,omitempty"`
	DaysLeft       *int   `json:"days_left,omitempty"`
	Expired        bool   `json:"expired"`
	Activated      *bool  `json:"activated,omitempty"`
	Content        string `json:"content,omitempty"`

	exp time.Time
}

type certificatesListOut struct {
	Certificates []certificateOut `json:"certificates"`
	Returned     int              `json:"returned"`
	Expired      int              `json:"expired"`
	NextCursor   string           `json:"next_cursor,omitempty"`
}

// certFields is the sparse fieldset; certificateContent is added only on
// request (it is large and rarely needed).
var certFields = []string{"name", "certificateType", "displayName", "serialNumber", "platform", "expirationDate", "activated"}

func certificatesList(ctx context.Context, env *tool.Env, in certificatesListIn) (*tool.Result, error) {
	if in.ExpiringWithinDays < 0 {
		return nil, tool.InvalidArgs("expiring_within_days must not be negative")
	}
	if in.Limit < 0 || in.Limit > 1000 {
		return nil, tool.InvalidArgs("limit must be between 1 and 1000")
	}
	fields := slices.Clone(certFields)
	if in.IncludeContent {
		fields = append(fields, "certificateContent")
	}
	q := url.Values{"fields[certificates]": {strings.Join(fields, ",")}}
	if in.Type != "" {
		q.Set("filter[certificateType]", in.Type)
	}
	res, err := env.ASC.List(ctx, "/v1/certificates", tool.ListOptions{Query: q, MaxItems: in.Limit})
	if err != nil {
		return nil, fmt.Errorf("list certificates: %w", err)
	}
	now := env.Clock.Now()
	out := certificatesListOut{Certificates: []certificateOut{}, NextCursor: res.NextCursor}
	for _, r := range res.Data {
		c := certificateOut{
			ID: r.ID, Name: r.Str("name"), DisplayName: r.Str("displayName"), Type: r.Str("certificateType"),
			Platform: r.Str("platform"), SerialNumber: r.Str("serialNumber"), ExpirationDate: r.Str("expirationDate"),
		}
		if v, ok := r.Bool("activated"); ok {
			c.Activated = &v
		}
		if in.IncludeContent {
			c.Content = r.Str("certificateContent")
		}
		if exp, err := time.Parse(time.RFC3339, c.ExpirationDate); err == nil {
			days := int(math.Floor(exp.Sub(now).Hours() / 24))
			c.DaysLeft = &days
			c.exp = exp
			c.Expired = !exp.After(now)
		}
		if in.ExpiringWithinDays > 0 && (c.DaysLeft == nil || *c.DaysLeft >= in.ExpiringWithinDays) {
			continue
		}
		if c.Expired {
			out.Expired++
		}
		out.Certificates = append(out.Certificates, c)
	}
	// Soonest expiration first; certificates without a date last.
	slices.SortStableFunc(out.Certificates, func(a, b certificateOut) int {
		switch {
		case a.DaysLeft == nil && b.DaysLeft == nil:
			return strings.Compare(a.ID, b.ID)
		case a.DaysLeft == nil:
			return 1
		case b.DaysLeft == nil:
			return -1
		}
		if c := a.exp.Compare(b.exp); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	out.Returned = len(out.Certificates)
	result := tool.OK(out)
	if res.NextCursor != "" {
		result.Warn("more certificates exist than limit; the expiration order covers only the certificates read")
	}
	return result, nil
}
