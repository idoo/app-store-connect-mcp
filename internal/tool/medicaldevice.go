package tool

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// MedicalDeviceCollected is Apple's requirement status once the regulated
// medical device declaration is saved.
const MedicalDeviceCollected = "COLLECTED"

// MedicalDeviceRequirementName is App Store Connect's name of the regulated
// medical device declaration (App Information → App Store Regulations &
// Permits).
const MedicalDeviceRequirementName = "MEDICAL_DEVICE"

// ComplianceFormsPath is the web-only compliance form service the
// declaration lives in; paths below it are keyed by the public provider id.
const ComplianceFormsPath = "/ppm/complianceform/v1/accounts/"

var (
	publicProviderRE = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	appIDRE          = regexp.MustCompile(`^[0-9]{1,20}$`)
	// requirementIDRE bounds Apple's requirement id to what may go into a
	// path segment.
	requirementIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// PublicProviderID returns the public id (a UUID) of the web session's
// team, read from /olympus/v1/session. The Xcode Cloud web API and the
// compliance forms are keyed by it. A session without one wraps
// [ErrWebSessionRequired].
func PublicProviderID(ctx context.Context, p Portal) (string, error) {
	var sess struct {
		Provider struct {
			PublicProviderID string `json:"publicProviderId"`
		} `json:"provider"`
	}
	if err := p.JSON(ctx, http.MethodGet, "/olympus/v1/session", nil, &sess); err != nil {
		return "", fmt.Errorf("read web session team: %w", err)
	}
	id := sess.Provider.PublicProviderID
	if !publicProviderRE.MatchString(id) {
		return "", fmt.Errorf("web session has no team public id; sign in again: %w", ErrWebSessionRequired)
	}
	return id, nil
}

// MedicalDeviceRequirement is the regulated medical device requirement App
// Store Connect tracks for one app.
type MedicalDeviceRequirement struct {
	// AccountID is the public provider id the compliance forms are keyed by.
	AccountID string
	// ID is Apple's requirement id; "" when Apple lists no such requirement
	// for the app.
	ID string
	// Status is Apple's status: [MedicalDeviceCollected] once answered.
	Status string
	// Required reports that Apple wants an answer for this app.
	Required bool
}

// Answered reports a saved declaration (yes or no).
func (r MedicalDeviceRequirement) Answered() bool { return r.Status == MedicalDeviceCollected }

// ReadMedicalDeviceRequirement reads whether App Store Connect has the
// app's regulated medical device declaration (two web requests: the
// session's team, then the app's requirements). appID is the numeric app
// id. No usable session wraps [ErrWebSessionRequired].
func ReadMedicalDeviceRequirement(ctx context.Context, p Portal, appID string) (MedicalDeviceRequirement, error) {
	if !appIDRE.MatchString(appID) {
		return MedicalDeviceRequirement{}, InvalidArgs("app id %q is not numeric", appID)
	}
	account, err := PublicProviderID(ctx, p)
	if err != nil {
		return MedicalDeviceRequirement{}, err
	}
	var doc struct {
		RequirementData []struct {
			ContentID    string `json:"contentId"`
			Requirements []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				IsRequired bool   `json:"isRequired"`
			} `json:"requirements"`
		} `json:"requirementData"`
	}
	path := ComplianceFormsPath + account + "/requirements?contentId=" + url.QueryEscape(appID)
	if err := p.JSON(ctx, http.MethodGet, path, nil, &doc); err != nil {
		return MedicalDeviceRequirement{}, fmt.Errorf("read the app's compliance requirements: %w", err)
	}
	out := MedicalDeviceRequirement{AccountID: account}
	// Apple answers per content id; an entry without one applies to the
	// requested app.
	for _, exact := range []bool{true, false} {
		for _, d := range doc.RequirementData {
			if (exact && d.ContentID != appID) || (!exact && d.ContentID != "") {
				continue
			}
			for _, r := range d.Requirements {
				if strings.TrimSpace(r.Name) != MedicalDeviceRequirementName {
					continue
				}
				id := strings.TrimSpace(r.ID)
				if !requirementIDRE.MatchString(id) {
					return MedicalDeviceRequirement{}, fmt.Errorf("%w: unexpected medical device requirement id from App Store Connect", ErrInternal)
				}
				out.ID, out.Status, out.Required = id, strings.TrimSpace(r.Status), r.IsRequired
				return out, nil
			}
		}
	}
	return out, nil
}
