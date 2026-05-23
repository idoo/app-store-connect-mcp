package reports

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// ParseAmount parses a decimal string exactly ("1,234.56", "-0.70", "(3.10)",
// ""). An empty string is zero. Exponents and fractions are rejected.
func ParseAmount(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return new(big.Rat), nil
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg, s = true, strings.TrimSpace(s[1:len(s)-1])
	}
	if strings.Contains(s, ".") {
		s = strings.ReplaceAll(s, ",", "")
	}
	if s == "" || strings.ContainsAny(s, "eE/,_ ") {
		return nil, fmt.Errorf("reports: invalid amount %q: %w", s, ErrCorrupt)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("reports: invalid amount %q: %w", s, ErrCorrupt)
	}
	if neg {
		r.Neg(r)
	}
	return r, nil
}

// minorUnits lists ISO 4217 currencies whose minor unit is not 2 digits.
var minorUnits = map[string]int{
	"BHD": 3, "CLP": 0, "HUF": 0, "IDR": 0, "ISK": 0, "JOD": 3, "JPY": 0,
	"KRW": 0, "KWD": 3, "OMR": 3, "TND": 3, "TWD": 0, "UGX": 0, "VND": 0,
}

// Precision returns the number of decimal digits shown for currency.
func Precision(currency string) int {
	if p, ok := minorUnits[strings.ToUpper(currency)]; ok {
		return p
	}
	return 2
}

// FormatAmount renders v with currency-appropriate precision (half away from
// zero rounding, display only; sums stay exact).
func FormatAmount(currency string, v *big.Rat) string {
	if v == nil {
		v = new(big.Rat)
	}
	return v.FloatString(Precision(currency))
}

// Money is an exact amount per ISO currency code.
type Money map[string]*big.Rat

// Add adds v (exactly) to the currency's total. Empty currency is "UNKNOWN".
func (m Money) Add(currency string, v *big.Rat) {
	if v == nil {
		return
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "UNKNOWN"
	}
	cur, ok := m[currency]
	if !ok {
		cur = new(big.Rat)
		m[currency] = cur
	}
	cur.Add(cur, v)
}

// AddMoney adds every currency of o into m.
func (m Money) AddMoney(o Money) {
	for c, v := range o {
		m.Add(c, v)
	}
}

// Currencies returns the currency codes in sorted order.
func (m Money) Currencies() []string {
	out := make([]string, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Strings returns each currency's formatted amount.
func (m Money) Strings() map[string]string {
	out := make(map[string]string, len(m))
	for c, v := range m {
		out[c] = FormatAmount(c, v)
	}
	return out
}

// String renders "EUR 1.40, USD 12.00" with currencies sorted.
func (m Money) String() string {
	parts := make([]string, 0, len(m))
	for _, c := range m.Currencies() {
		parts = append(parts, c+" "+FormatAmount(c, m[c]))
	}
	return strings.Join(parts, ", ")
}

// MarshalJSON encodes Money as {"USD":"12.00"} with decimal strings.
func (m Money) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(m.Strings())
	if err != nil {
		return nil, fmt.Errorf("reports: marshal money: %w", err)
	}
	return b, nil
}
