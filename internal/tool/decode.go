package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"
)

// decodeArgs parses raw tool arguments into a JSON object, coerces
// stringified values where the schema expects an object, array, integer,
// number or boolean (a common client bug), and validates the result against
// the resolved schema.
func decodeArgs(raw json.RawMessage, s *jsonschema.Schema, rs *jsonschema.Resolved) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("arguments are not JSON: %w", err)
	}
	if str, ok := v.(string); ok { // whole argument object sent as a string
		if err := json.Unmarshal([]byte(str), &v); err != nil {
			return nil, fmt.Errorf("arguments are not a JSON object: %w", err)
		}
	}
	args, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}
	coerce(s, args)
	if err := rs.Validate(args); err != nil {
		return nil, err
	}
	return args, nil
}

// coerce replaces string values with their parsed JSON where the schema
// expects an object, array, integer, number or boolean, recursively. Only
// exact spellings convert ("3", "2.5", "true"); anything else stays a string
// and fails validation. String-typed properties are never touched. A null
// property the schema does not accept is dropped, so it behaves as omitted
// (clients in strict mode send null for unset optionals).
func coerce(s *jsonschema.Schema, v any) any {
	if s == nil {
		return v
	}
	if str, ok := v.(string); ok {
		if parsed, ok := coerceString(s, str); ok {
			v = parsed
		}
	}
	switch x := v.(type) {
	case map[string]any:
		for k, el := range x {
			ps := s.Properties[k]
			if ps == nil {
				ps = s.AdditionalProperties
			}
			if ps == nil {
				continue
			}
			if el == nil && !acceptsNull(ps) {
				delete(x, k)
				continue
			}
			x[k] = coerce(ps, el)
		}
	case []any:
		for i, el := range x {
			x[i] = coerce(s.Items, el)
		}
	}
	return v
}

// acceptsNull reports whether s explicitly allows JSON null.
func acceptsNull(s *jsonschema.Schema) bool {
	return s.Type == "null" || slices.Contains(s.Types, "null")
}

// maxExactInt is the largest integer a float64 (how JSON numbers decode)
// holds exactly.
const maxExactInt = 1 << 53

// coerceString parses str as the type the schema expects. ok is false when
// the schema accepts strings, has no non-string type, or str is not an exact
// spelling of that type.
func coerceString(s *jsonschema.Schema, str string) (any, bool) {
	types := s.Types
	if s.Type != "" {
		types = append(slices.Clip(types), s.Type)
	}
	if slices.Contains(types, "string") {
		return nil, false
	}
	for _, t := range types {
		switch t {
		case "object", "array":
			var parsed any
			if json.Unmarshal([]byte(str), &parsed) == nil {
				switch parsed.(type) {
				case map[string]any, []any:
					return parsed, true
				}
			}
		case "boolean":
			if str == "true" || str == "false" {
				return str == "true", true
			}
		case "integer":
			// Canonical decimal only: no sign prefix, leading zeros, spaces,
			// fractions or exponents.
			n, err := strconv.ParseInt(str, 10, 64)
			if err == nil && strconv.FormatInt(n, 10) == str && n >= -maxExactInt && n <= maxExactInt {
				return float64(n), true
			}
		case "number":
			if f, ok := jsonNumber(str); ok {
				return f, true
			}
		}
	}
	return nil, false
}

// jsonNumber parses str when it is exactly one JSON number token.
func jsonNumber(str string) (float64, bool) {
	if str == "" || (str[0] != '-' && (str[0] < '0' || str[0] > '9')) {
		return 0, false
	}
	if c := str[len(str)-1]; c < '0' || c > '9' {
		return 0, false
	}
	var f float64
	if !json.Valid([]byte(str)) || json.Unmarshal([]byte(str), &f) != nil {
		return 0, false
	}
	return f, true
}
