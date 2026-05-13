package asc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/idoo/hamn/internal/tool"
)

// DefaultMaxResponseBytes is the default result byte cap
// (HAMN_MAX_RESPONSE_BYTES).
const DefaultMaxResponseBytes = 60 << 10

// CapResult bounds the compact JSON encoding of v to maxBytes (≤ 0 disables
// the cap). Values that fit are returned unchanged with a nil Truncation.
// Otherwise it drops included resources first, then tail items of the
// primary list: Data of a *tool.ListResult, the largest top-level array of an
// object, or the tail of a top-level array. Other values are converted to
// their generic JSON form (map/slice) before trimming. If nothing more can be
// dropped the trimmed value is returned even when it still exceeds maxBytes.
func CapResult(v any, maxBytes int) (any, *tool.Truncation, error) {
	if maxBytes <= 0 {
		return v, nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("asc: cap result: %w", err)
	}
	if len(b) <= maxBytes {
		return v, nil, nil
	}
	switch lr := v.(type) {
	case *tool.ListResult:
		if lr != nil {
			return capList(*lr, maxBytes)
		}
	case tool.ListResult:
		return capList(lr, maxBytes)
	}
	var g any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&g); err != nil {
		return nil, nil, fmt.Errorf("asc: cap result: %w", err)
	}
	switch g := g.(type) {
	case map[string]any:
		return capObject(g, maxBytes)
	case []any:
		keep, err := fitPrefix(len(g), maxBytes, func(n int) any { return g[:n] })
		if err != nil {
			return nil, nil, err
		}
		return g[:keep], truncation(0, len(g)-keep, maxBytes), nil
	}
	return v, truncation(0, 0, maxBytes), nil
}

// capList trims a copy of a list result.
func capList(lr tool.ListResult, maxBytes int) (any, *tool.Truncation, error) {
	incl := len(lr.Included)
	lr.Included = nil
	data := lr.Data
	keep, err := fitPrefix(len(data), maxBytes, func(n int) any {
		lr.Data, lr.Returned = data[:n], n
		return &lr
	})
	if err != nil {
		return nil, nil, err
	}
	lr.Data, lr.Returned = data[:keep], keep
	return &lr, truncation(incl, len(data)-keep, maxBytes), nil
}

// capObject trims a generic JSON object in place.
func capObject(m map[string]any, maxBytes int) (any, *tool.Truncation, error) {
	incl := 0
	if arr, ok := m["included"].([]any); ok {
		incl = len(arr)
		delete(m, "included")
	} else if _, ok := m["included"]; ok {
		incl = 1
		delete(m, "included")
	}
	key, err := largestArray(m)
	if err != nil {
		return nil, nil, err
	}
	if key == "" {
		return m, truncation(incl, 0, maxBytes), nil
	}
	arr := m[key].([]any)
	keep, err := fitPrefix(len(arr), maxBytes, func(n int) any {
		m[key] = arr[:n]
		return m
	})
	if err != nil {
		return nil, nil, err
	}
	m[key] = arr[:keep]
	return m, truncation(incl, len(arr)-keep, maxBytes), nil
}

// largestArray returns the key of the top-level array with the largest JSON
// encoding (ties broken by key order), or "" when there is none.
func largestArray(m map[string]any) (string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best, bestSize := "", -1
	for _, k := range keys {
		arr, ok := m[k].([]any)
		if !ok || len(arr) == 0 {
			continue
		}
		b, err := json.Marshal(arr)
		if err != nil {
			return "", fmt.Errorf("asc: cap result: %w", err)
		}
		if len(b) > bestSize {
			best, bestSize = k, len(b)
		}
	}
	return best, nil
}

// fitPrefix binary-searches the largest n in [0,total] whose candidate(n)
// encodes within maxBytes; it returns 0 when even the empty prefix is too big.
func fitPrefix(total, maxBytes int, candidate func(n int) any) (int, error) {
	lo, hi := 0, total
	for lo < hi {
		mid := (lo + hi + 1) / 2
		b, err := json.Marshal(candidate(mid))
		if err != nil {
			return 0, fmt.Errorf("asc: cap result: %w", err)
		}
		if len(b) <= maxBytes {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo, nil
}

func truncation(included, items, maxBytes int) *tool.Truncation {
	return &tool.Truncation{
		Dropped: included + items,
		Hint: fmt.Sprintf("response exceeded %d bytes: dropped %d included and %d trailing items; narrow with filters, fields or a smaller max_items",
			maxBytes, included, items),
	}
}
