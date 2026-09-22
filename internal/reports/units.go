package reports

import (
	"encoding/json"
	"math/big"
	"sort"
	"strings"
)

// Units holds exact unit counts per category. Apple types Units as
// DECIMAL(18,2): refunds are negative and partial refunds can be fractional.
type Units map[Category]*big.Rat

// Add adds v to category c.
func (u Units) Add(c Category, v *big.Rat) {
	if v == nil {
		return
	}
	cur, ok := u[c]
	if !ok {
		cur = new(big.Rat)
		u[c] = cur
	}
	cur.Add(cur, v)
}

// Get returns the count for c (zero when absent). The result must not be
// modified.
func (u Units) Get(c Category) *big.Rat {
	if v, ok := u[c]; ok {
		return v
	}
	return new(big.Rat)
}

// Total sums every category.
func (u Units) Total() *big.Rat {
	t := new(big.Rat)
	for _, v := range u {
		t.Add(t, v)
	}
	return t
}

// FormatUnits renders a count as a JSON number literal: integers without
// decimals, otherwise trimmed to at most two decimals as in Apple's reports.
func FormatUnits(v *big.Rat) string {
	if v == nil {
		return "0"
	}
	if v.IsInt() {
		return v.Num().String()
	}
	s := strings.TrimRight(v.FloatString(2), "0")
	return strings.TrimSuffix(s, ".")
}

// MarshalJSON encodes Units as {"download":12,"iap":1.5} with sorted keys.
func (u Units) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(u))
	for c := range u {
		keys = append(keys, string(c))
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.WriteString(FormatUnits(u[Category(k)]))
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}
