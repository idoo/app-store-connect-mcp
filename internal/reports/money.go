package reports

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// ParseAmount parses a decimal string exactly ("1,234.56", "-0.70", "(3.10)",
// ""). An empty string is zero. Commas are accepted only as thousands
// separators of the integer part ("1,234.50", "1,234,567"); ambiguous commas
// ("1,23", "1,234" which may be a decimal comma) are rejected, as are
// exponents and fractions.
func ParseAmount(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return new(big.Rat), nil
	}
	raw := s
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg, s = true, strings.TrimSpace(s[1:len(s)-1])
	}
	if strings.Contains(s, ",") {
		ungrouped, ok := ungroupThousands(s)
		if !ok {
			return nil, fmt.Errorf("reports: invalid amount %q: %w", raw, ErrCorrupt)
		}
		s = ungrouped
	}
	if s == "" || strings.ContainsAny(s, "eE/,_ ") {
		return nil, fmt.Errorf("reports: invalid amount %q: %w", raw, ErrCorrupt)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("reports: invalid amount %q: %w", raw, ErrCorrupt)
	}
	if neg {
		r.Neg(r)
	}
	return r, nil
}

// ungroupThousands removes thousands separators from s when its integer part
// is grouped exactly as 1-3 digits followed by groups of 3 ("-1,234,567.8").
// A single group without a decimal point ("1,234") is rejected: it may be a
// decimal comma (1.234).
func ungroupThousands(s string) (string, bool) {
	sign := ""
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		sign, s = s[:1], s[1:]
	}
	intPart, frac, hasFrac := strings.Cut(s, ".")
	if strings.Contains(frac, ",") {
		return "", false
	}
	groups := strings.Split(intPart, ",")
	if len(groups) == 2 && !hasFrac {
		return "", false
	}
	for i, g := range groups {
		if (i == 0 && (len(g) < 1 || len(g) > 3)) || (i > 0 && len(g) != 3) || strings.Trim(g, "0123456789") != "" {
			return "", false
		}
	}
	out := sign + strings.Join(groups, "")
	if hasFrac {
		out += "." + frac
	}
	return out, true
}

// minorUnits lists ISO 4217 currencies whose minor unit is not 2 digits.
// HUF, IDR and TWD have 2 in ISO 4217 and appear with decimals in Apple's
// proceeds, so they use the default.
var minorUnits = map[string]int{
	"BHD": 3, "CLP": 0, "ISK": 0, "JOD": 3, "JPY": 0,
	"KRW": 0, "KWD": 3, "OMR": 3, "TND": 3, "UGX": 0, "VND": 0,
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
