package signing

// Shared helpers of the profile, device and revoke tools (profiles.go,
// devices.go, revoke.go).

import (
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// profileTypes is ProfileType of App Store Connect spec 4.5.1.
var profileTypes = []any{
	"IOS_APP_DEVELOPMENT", "IOS_APP_STORE", "IOS_APP_ADHOC", "IOS_APP_INHOUSE",
	"MAC_APP_DEVELOPMENT", "MAC_APP_STORE", "MAC_APP_DIRECT",
	"TVOS_APP_DEVELOPMENT", "TVOS_APP_STORE", "TVOS_APP_ADHOC", "TVOS_APP_INHOUSE",
	"MAC_CATALYST_APP_DEVELOPMENT", "MAC_CATALYST_APP_STORE", "MAC_CATALYST_APP_DIRECT",
}

// profileStates is the profileState filter enum.
var profileStates = []any{"ACTIVE", "INVALID"}

// includeLimit is the spec maximum of limit[devices] and
// limit[certificates] on profiles; counts at this value are lower bounds.
const includeLimit = 50

// profileFields is the sparse profile fieldset (profileContent is added
// only for a download).
var profileFields = []string{
	"name", "platform", "profileType", "profileState", "uuid", "createdDate", "expirationDate",
	"bundleId", "devices", "certificates",
}

// profileQuery reads profiles with their bundle ID and the ids of their
// devices and certificates (for counts).
func profileQuery(content bool) url.Values {
	fields := slices.Clone(profileFields)
	if content {
		fields = append(fields, "profileContent")
	}
	limit := []string{"50"}
	return url.Values{
		"fields[profiles]":     {strings.Join(fields, ",")},
		"fields[bundleIds]":    {"identifier,name,platform"},
		"fields[devices]":      {"udid"},
		"fields[certificates]": {"name"},
		"include":              {"bundleId,devices,certificates"},
		"limit[devices]":       limit,
		"limit[certificates]":  limit,
	}
}

// profileOut is one provisioning profile.
type profileOut struct {
	ID               string         `json:"id"`
	Name             tool.Untrusted `json:"name"`
	Type             string         `json:"type"`
	State            string         `json:"state"`
	Platform         string         `json:"platform,omitempty"`
	UUID             string         `json:"uuid,omitempty"`
	BundleID         string         `json:"bundle_id,omitempty"`
	BundleIDRef      string         `json:"bundle_id_ref,omitempty"`
	ExpirationDate   string         `json:"expiration_date,omitempty"`
	DaysLeft         *int           `json:"days_left,omitempty"`
	DeviceCount      int            `json:"device_count"`
	CertificateCount int            `json:"certificate_count"`
	// CountsCapped means a count reached Apple's include limit (50) and is a
	// lower bound.
	CountsCapped bool   `json:"counts_capped,omitempty"`
	Content      string `json:"profile_content,omitempty"`

	certIDs []string
}

// flattenProfile flattens a profiles resource with its included bundle ID.
func flattenProfile(r tool.Resource, inc map[string]tool.Resource, now time.Time) profileOut {
	p := profileOut{
		ID: r.ID, Name: r.Text("name"), Type: r.Str("profileType"), State: r.Str("profileState"),
		Platform: r.Str("platform"), UUID: r.Str("uuid"), ExpirationDate: r.Str("expirationDate"),
		BundleIDRef: r.RelID("bundleId"), Content: r.Str("profileContent"),
		DeviceCount: len(r.RelIDs("devices")), certIDs: r.RelIDs("certificates"),
	}
	p.CertificateCount = len(p.certIDs)
	p.CountsCapped = p.DeviceCount >= includeLimit || p.CertificateCount >= includeLimit
	if b, ok := inc["bundleIds/"+p.BundleIDRef]; ok {
		p.BundleID = b.Str("identifier")
	}
	p.DaysLeft = daysLeft(p.ExpirationDate, now)
	return p
}

// daysLeft returns whole days until an RFC 3339 date (negative when
// past), or nil when the date is missing or malformed.
func daysLeft(date string, now time.Time) *int {
	exp, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return nil
	}
	d := int(math.Floor(exp.Sub(now).Hours() / 24))
	return &d
}

// profileKind describes what a profile type needs.
type profileKind struct {
	// CertTypes are the certificate types a profile of this type accepts.
	CertTypes []string
	// DevicePlatform is the device platform for device-bound types, else "".
	DevicePlatform string
}

// profileKindOf maps a profile type to its certificate types and device platform.
func profileKindOf(profileType string) profileKind {
	dev, store := []string{"DEVELOPMENT", "IOS_DEVELOPMENT"}, []string{"DISTRIBUTION", "IOS_DISTRIBUTION"}
	macDev, macStore := []string{"DEVELOPMENT", "MAC_APP_DEVELOPMENT"}, []string{"DISTRIBUTION", "MAC_APP_DISTRIBUTION"}
	direct := []string{"DEVELOPER_ID_APPLICATION", "DEVELOPER_ID_APPLICATION_G2"}
	switch profileType {
	case "IOS_APP_DEVELOPMENT", "TVOS_APP_DEVELOPMENT":
		return profileKind{CertTypes: dev, DevicePlatform: "IOS"}
	case "IOS_APP_ADHOC", "TVOS_APP_ADHOC":
		return profileKind{CertTypes: store, DevicePlatform: "IOS"}
	case "IOS_APP_STORE", "IOS_APP_INHOUSE", "TVOS_APP_STORE", "TVOS_APP_INHOUSE":
		return profileKind{CertTypes: store}
	case "MAC_APP_DEVELOPMENT", "MAC_CATALYST_APP_DEVELOPMENT":
		return profileKind{CertTypes: macDev, DevicePlatform: "MAC_OS"}
	case "MAC_APP_STORE", "MAC_CATALYST_APP_STORE":
		return profileKind{CertTypes: macStore}
	case "MAC_APP_DIRECT", "MAC_CATALYST_APP_DIRECT":
		return profileKind{CertTypes: direct}
	}
	return profileKind{}
}

// isAll reports whether refs is the keyword list ["all"].
func isAll(refs []string) bool {
	return len(refs) == 1 && strings.EqualFold(strings.TrimSpace(refs[0]), "all")
}

// relList is a JSON:API to-many relationship body.
func relList(typ string, ids []string) map[string]any {
	data := make([]map[string]any, len(ids))
	for i, id := range ids {
		data[i] = map[string]any{"type": typ, "id": id}
	}
	return map[string]any{"data": data}
}
