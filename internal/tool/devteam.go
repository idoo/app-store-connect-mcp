package tool

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var devTeamRE = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// ResolveDevTeam picks the developer team a developer-site call is for: arg
// (the tool's team_id) when given, else the App ID prefix (seedId) of one
// bundle ID read with the API key. Every failure names team_id, the way out.
func ResolveDevTeam(ctx context.Context, env *Env, arg string) (string, error) {
	if arg = strings.TrimSpace(arg); arg != "" {
		if !devTeamRE.MatchString(arg) {
			return "", InvalidArgs("team_id %q is not a 10-character team id", SanitizeText(arg))
		}
		return arg, nil
	}
	resp, err := env.ASC.Do(ctx, Request{
		Method: http.MethodGet, Path: "/v1/bundleIds",
		Query: url.Values{"fields[bundleIds]": {"identifier,seedId"}, "limit": {"1"}},
	})
	if err != nil {
		return "", fmt.Errorf("find the team (pass team_id to skip): %w", err)
	}
	r, ok := resp.First()
	if !ok {
		return "", InvalidArgs("the team has no bundle IDs to read its team id from; pass team_id")
	}
	seed := r.Str("seedId")
	if !devTeamRE.MatchString(seed) {
		return "", InvalidArgs("cannot tell the team from the bundle IDs' App ID prefixes; pass team_id")
	}
	return seed, nil
}
