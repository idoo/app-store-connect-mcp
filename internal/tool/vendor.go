package tool

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Vendor number sources reported by [ResolveVendorNumber].
const (
	VendorFromProfile    = "profile"     // profile vendor_number or ASC_VENDOR_NUMBER
	VendorFromWebSession = "web_session" // discovered (now or earlier) from Payments via the web session
)

// VendorNumber is the account's Sales and Trends / Payments vendor number.
type VendorNumber struct {
	Number string `json:"vendor_number"`
	Source string `json:"vendor_number_source"`
}

// vendorStateKind is the [StateStore] kind caching discovered vendor numbers
// per profile.
const vendorStateKind = "vendor_number"

type vendorRecord struct {
	Number string `json:"number"`
}

// ResolveVendorNumber returns the vendor number for sales and finance
// reports: the profile's, else one discovered earlier and cached in the
// state store, else one discovered now through the web session (olympus
// session provider id → Payments sapVendorNumbers) and cached. Arcade
// vendors are ignored; several remaining vendors are ambiguous.
//
// Errors: *Error vendor_number_missing (no session, or none found; the hint
// names web_login and `auth set`), *Error vendor_number_ambiguous (lists
// the numbers), or the Portal error for anything else (context cancelled,
// Apple 5xx).
func ResolveVendorNumber(ctx context.Context, env *Env) (VendorNumber, error) {
	if n := strings.TrimSpace(env.Profile.VendorNumber); n != "" {
		return VendorNumber{Number: n, Source: VendorFromProfile}, nil
	}
	key := env.Profile.Name
	if key == "" {
		key = "default"
	}
	var rec vendorRecord
	if ok, err := env.State.Load(vendorStateKind, key, &rec); err == nil && ok && rec.Number != "" {
		return VendorNumber{Number: rec.Number, Source: VendorFromWebSession}, nil
	}
	n, err := discoverVendorNumber(ctx, env.Portal)
	if err != nil {
		if errors.Is(err, ErrWebSessionRequired) {
			return VendorNumber{}, VendorMissingError(env.Profile.Name, true)
		}
		return VendorNumber{}, err
	}
	// A failed save only costs a rediscovery next time.
	_ = env.State.Save(vendorStateKind, key, vendorRecord{Number: n})
	return VendorNumber{Number: n, Source: VendorFromWebSession}, nil
}

// VendorMissingError is the vendor_number_missing error for profile;
// noSession adds the web sign-in route that discovers the number.
func VendorMissingError(profile string, noSession bool) *Error {
	if profile == "" {
		profile = "default"
	}
	hint := fmt.Sprintf("run `app-store-connect-mcp auth set %s vendor_number=<number>` (or set ASC_VENDOR_NUMBER) and restart app-store-connect-mcp", profile)
	if noSession {
		hint = "call web_login (web toolset) so the vendor number is read from Payments and Financial Reports automatically, or " + hint
	}
	return &Error{
		Code: "vendor_number_missing",
		Message: "sales and finance reports need the vendor number of the account, which the API does not expose; " +
			"find it in App Store Connect → Payments and Financial Reports (top-left, under the account name)",
		Hint: hint,
	}
}

func discoverVendorNumber(ctx context.Context, p Portal) (string, error) {
	var sess struct {
		Provider struct {
			ProviderID int64 `json:"providerId"`
		} `json:"provider"`
	}
	if err := p.JSON(ctx, "GET", "/olympus/v1/session", nil, &sess); err != nil {
		return "", err
	}
	if sess.Provider.ProviderID <= 0 {
		return "", VendorMissingError("", false)
	}
	var vendors struct {
		Data []struct {
			SapVendorNumber int64 `json:"sapVendorNumber"`
			IsArcadeVendor  bool  `json:"isArcadeVendor"`
		} `json:"data"`
	}
	path := "/WebObjects/iTunesConnect.woa/ra/paymentConsolidation/providers/" + strconv.FormatInt(sess.Provider.ProviderID, 10) + "/sapVendorNumbers"
	if err := p.JSON(ctx, "GET", path, nil, &vendors); err != nil {
		return "", err
	}
	var found []string
	for _, v := range vendors.Data {
		if v.SapVendorNumber > 0 && !v.IsArcadeVendor {
			found = append(found, strconv.FormatInt(v.SapVendorNumber, 10))
		}
	}
	switch len(found) {
	case 0:
		return "", VendorMissingError("", false)
	case 1:
		return found[0], nil
	default:
		return "", &Error{
			Code:    "vendor_number_ambiguous",
			Message: "the account has several vendor numbers: " + strings.Join(found, ", "),
			Hint:    "pick one with `app-store-connect-mcp auth set <profile> vendor_number=<number>` (or ASC_VENDOR_NUMBER) and restart app-store-connect-mcp",
		}
	}
}
