package core

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// listLimits turns asc_api_get's limits into list options. The spec index
// decides whether the operation takes a limit parameter: without one no
// limit is sent (Apple 400s on it, e.g. /v1/appAssetLibraryRefData). A
// query.limit is honoured as the page size and, unless limit is given, as
// the maximum number of items; note says so.
func listLimits(env *tool.Env, opID string, q url.Values, maxItems int) (tool.ListOptions, string, error) {
	opts := tool.ListOptions{Query: q, MaxItems: maxItems}
	if opts.MaxItems <= 0 {
		opts.MaxItems = getDefaultMax
	}
	if !opHasLimit(env, opID) {
		opts.Limit = -1
		return opts, "", nil
	}
	raw := q.Get("limit")
	if raw == "" {
		return opts, "", nil
	}
	page, err := strconv.Atoi(raw)
	if err != nil || page < 1 || page > appleMaxLimit {
		return opts, "", tool.InvalidArgs("query.limit %q must be 1-%d", raw, appleMaxLimit)
	}
	opts.Limit = page
	if maxItems <= 0 {
		opts.MaxItems = page
		return opts, fmt.Sprintf("query.limit %d used as page size and max items; set limit to read more pages", page), nil
	}
	return opts, fmt.Sprintf("query.limit %d used as page size; limit %d caps items", page, maxItems), nil
}

// appleMaxLimit is Apple's maximum page size; the pinned spec index carries
// no per-parameter maximum, and Apple 400s above it.
const appleMaxLimit = 200

// opHasLimit reports whether the spec lists a limit query parameter for the
// operation; an unknown description is treated as having one.
func opHasLimit(env *tool.Env, opID string) bool {
	d, err := env.Spec.Describe(opID)
	if err != nil || d == nil {
		return true
	}
	return slices.ContainsFunc(d.Params, func(p tool.Param) bool { return p.In == "query" && p.Name == "limit" })
}

// appleEnum matches Apple enum values (IN_REVIEW, USA, IOS).
var appleEnum = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// trustAppleValues unwraps attributes the client marked untrusted that look
// Apple-generated (RFC 3339 timestamps, enum values) rather than third-party
// text. Resources authored by customers or testers ([tool.IsThirdPartyType])
// stay wrapped entirely: the spec index has no response attribute types, so
// a hostile all-caps nickname there cannot be told from an enum. Free-text
// keys always stay wrapped.
func trustAppleValues(rs []tool.Resource) {
	for _, r := range rs {
		if tool.IsThirdPartyType(r.Type) {
			continue
		}
		for k, v := range r.Attributes {
			u, ok := v.(tool.Untrusted)
			if !ok || slices.Contains(tool.FreeTextKeys, k) {
				continue
			}
			s := string(u)
			if appleEnum.MatchString(s) || isTimestamp(s) {
				r.Attributes[k] = s
			}
		}
	}
}

func isTimestamp(s string) bool {
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}
