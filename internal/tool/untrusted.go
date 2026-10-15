package tool

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

// thirdPartyTypes are resource types authored entirely by customers or
// testers: every string attribute of theirs is untrusted.
var thirdPartyTypes = map[string]bool{
	"betaCrashLogs":                     true,
	"betaFeedbackCrashSubmissions":      true,
	"betaFeedbackScreenshotSubmissions": true,
	"betaTesters":                       true,
	"customerReviews":                   true,
}

// WrapUntrusted replaces, in place, third-party text in rs with [Untrusted]
// so it marshals as {"untrusted": text}. Every string attribute of a
// third-party-authored type (customer reviews, tester feedback, testers,
// crash logs) is wrapped. For other types, freeText returns the free-text
// attribute names (nil or a nil result means [UntrustedFallback]); their
// strings are wrapped wherever those keys appear, including inside nested
// objects and arrays. Already wrapped values are left as they are, so
// WrapUntrusted is idempotent.
func WrapUntrusted(rs []Resource, freeText func(resourceType string) []string) {
	for _, r := range rs {
		if thirdPartyTypes[r.Type] {
			for k, v := range r.Attributes {
				r.Attributes[k] = wrapAll(v)
			}
			continue
		}
		var keys []string
		if freeText != nil {
			keys = freeText(r.Type)
		}
		if keys == nil {
			keys = UntrustedFallback
		}
		set := make(map[string]bool, len(keys))
		for _, k := range keys {
			set[k] = true
		}
		for k, v := range r.Attributes {
			r.Attributes[k] = wrapKeys(k, v, set)
		}
	}
}

// wrapKeys wraps v entirely when key is free text, otherwise it descends
// into objects and arrays looking for free-text keys.
func wrapKeys(key string, v any, set map[string]bool) any {
	if set[key] {
		return wrapAll(v)
	}
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = wrapKeys(k, e, set)
		}
	case []any:
		for i, e := range x {
			x[i] = wrapKeys(key, e, set)
		}
	}
	return v
}

// wrapAll wraps every string in v, descending into objects and arrays.
func wrapAll(v any) any {
	switch x := v.(type) {
	case string:
		return Untrusted(x)
	case map[string]any:
		for k, e := range x {
			x[k] = wrapAll(e)
		}
	case []any:
		for i, e := range x {
			x[i] = wrapAll(e)
		}
	}
	return v
}

// Unwrap returns a copy of v with every [Untrusted] value, at any depth,
// replaced by its plain string. Use it before decoding or comparing
// attributes the client wrapped; never to echo text back to the agent.
func Unwrap(v any) any {
	switch x := v.(type) {
	case Untrusted:
		return string(x)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = Unwrap(e)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = Unwrap(e)
		}
		return s
	}
	return v
}
