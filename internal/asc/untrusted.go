package asc

import "github.com/idoo/hamn/internal/tool"

// UntrustedFallback lists common free-text attribute names written by third
// parties (customers, testers, reviewers) or copied from them. [WrapUntrusted]
// uses it when no type-specific list is known.
var UntrustedFallback = []string{
	"body",
	"comment",
	"description",
	"feedback",
	"name",
	"notes",
	"promotionalText",
	"responseBody",
	"reviewerNickname",
	"subtitle",
	"title",
	"whatToTest",
	"whatsNew",
}

// WrapUntrusted replaces, in place, the string values of free-text attributes
// with [tool.Untrusted] so they marshal as {"untrusted": text}. freeText
// returns the attribute names for a resource type; when freeText is nil or
// returns nil, [UntrustedFallback] applies. Non-string values are untouched.
func WrapUntrusted(rs []tool.Resource, freeText func(resourceType string) []string) {
	for _, r := range rs {
		var keys []string
		if freeText != nil {
			keys = freeText(r.Type)
		}
		if keys == nil {
			keys = UntrustedFallback
		}
		for _, k := range keys {
			if s, ok := r.Attributes[k].(string); ok {
				r.Attributes[k] = tool.Untrusted(s)
			}
		}
	}
}
