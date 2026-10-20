package tool

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/idoo/app-store-connect-mcp/internal/secrets"
)

// LooksLikeSecret reports whether s contains what looks like a private key
// (PEM, base64-encoded PEM or a bare base64 DER key body) or a JWT, using the
// same patterns as the redactor. Certificates and provisioning profiles do
// not count. It is a heuristic for warning the human, not a redactor.
func LooksLikeSecret(s string) bool {
	return secrets.ContainsPrivateKeyOrToken(s)
}

// secretWarnings returns one server-authored note per approval-prompt field
// whose value looks like a private key or token, so the human sees a warning
// even when the value itself is long or scrolled away.
func secretWarnings(p PlanPreview) []string {
	var fields []string
	if LooksLikeSecret(p.Summary) {
		fields = append(fields, "summary")
	}
	for i, a := range p.Affected {
		if LooksLikeSecret(a) {
			fields = append(fields, fmt.Sprintf("targets[%d]", i))
		}
	}
	for i, u := range p.Untrusted {
		if LooksLikeSecret(u) {
			fields = append(fields, fmt.Sprintf("untrusted[%d]", i))
		}
	}
	if p.Intended != nil {
		var v any
		if b, err := json.Marshal(p.Intended); err == nil && json.Unmarshal(b, &v) == nil {
			secretPaths("change", v, &fields)
		}
	}
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f + " looks like a private key or token"
	}
	return out
}

// secretPaths appends the path of every string leaf of v that looks like a
// secret, visiting object keys in sorted order.
func secretPaths(path string, v any, out *[]string) {
	switch x := v.(type) {
	case string:
		if LooksLikeSecret(x) {
			*out = append(*out, path)
		}
	case []any:
		for i, e := range x {
			secretPaths(fmt.Sprintf("%s[%d]", path, i), e, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			secretPaths(path+"."+k, x[k], out)
		}
	}
}
