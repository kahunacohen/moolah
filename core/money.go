package core

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
	"golang.org/x/text/currency"
)

// Currency is an ISO 4217 currency code, for example "ILS".
type Currency string

// ParseCurrency validates and normalizes an ISO 4217 currency code.
func ParseCurrency(s string) (Currency, error) {
	code := strings.ToUpper(strings.TrimSpace(s))
	if _, err := currency.ParseISO(code); err != nil || len(code) != 3 {
		return "", fmt.Errorf("unknown currency %q: use an ISO code such as USD, EUR or JPY", s)
	}
	return Currency(code), nil
}

// Scale is the number of decimal places of the currency's minor unit (2 for ILS, 0 for JPY).
func (c Currency) Scale() int32 {
	u, err := currency.ParseISO(string(c))
	if err != nil {
		return 2
	}
	scale, _ := currency.Standard.Rounding(u)
	return int32(scale)
}

// ParseDecimal parses a number such as "1,530.50" or "-42.17".
func ParseDecimal(s string) (decimal.Decimal, error) {
	clean := strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	clean = strings.ReplaceAll(clean, "−", "-") // the Unicode minus sign, also used by the CLI for negative arguments
	d, err := decimal.NewFromString(clean)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("invalid number %q", s)
	}
	return d, nil
}

// CheckScale returns an error when v has more decimal places than the currency allows.
func CheckScale(v decimal.Decimal, c Currency) error {
	if !v.Equal(v.Truncate(c.Scale())) {
		return fmt.Errorf("%s has more decimal places than %s allows (%d)", v.String(), c, c.Scale())
	}
	return nil
}

// ToMinor converts v to integer minor units of c (agorot, cents).
func ToMinor(v decimal.Decimal, c Currency) (int64, error) {
	if err := CheckScale(v, c); err != nil {
		return 0, err
	}
	return v.Shift(c.Scale()).IntPart(), nil
}

// FromMinor converts integer minor units of c back to a decimal value.
func FromMinor(minor int64, c Currency) decimal.Decimal {
	return decimal.New(minor, -c.Scale())
}

// Amount is a value in one currency.
type Amount struct {
	Currency Currency
	Value    decimal.Decimal
}

// String formats the amount, for example "-1,530.00 ILS".
func (a Amount) String() string {
	return FormatNumber(a.Value, a.Currency.Scale()) + " " + string(a.Currency)
}

// NumberStyle is how a language writes numbers.
type NumberStyle struct {
	Decimal string // "." in English, "," in German
	Group   string // "," in English, "." in German, a thin space in French
}

var numberStyle = NumberStyle{Decimal: ".", Group: ","}

// SetNumberStyle sets how FormatNumber writes numbers in this process (from the
// book's locale). The default is English: 1,234.56.
func SetNumberStyle(s NumberStyle) { numberStyle = s }

// FormatNumber formats v with thousands separators and a fixed number of decimals,
// in the current number style.
func FormatNumber(v decimal.Decimal, scale int32) string {
	s := v.StringFixed(scale)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteString(numberStyle.Group)
		}
		b.WriteRune(r)
	}
	out := b.String()
	if hasFrac {
		out += numberStyle.Decimal + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// Balance is a sum of amounts in several currencies.
type Balance map[Currency]decimal.Decimal

// Add adds v in currency c.
func (b Balance) Add(c Currency, v decimal.Decimal) {
	b[c] = b[c].Add(v)
	if b[c].IsZero() {
		delete(b, c)
	}
}

// AddBalance adds all amounts of o.
func (b Balance) AddBalance(o Balance) {
	for c, v := range o {
		b.Add(c, v)
	}
}

// IsZero reports whether every currency sums to zero.
func (b Balance) IsZero() bool { return len(b) == 0 }

// Currencies returns the currencies in b, sorted.
func (b Balance) Currencies() []Currency {
	out := make([]Currency, 0, len(b))
	for c := range b {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Amounts returns b as amounts sorted by currency.
func (b Balance) Amounts() []Amount {
	out := make([]Amount, 0, len(b))
	for _, c := range b.Currencies() {
		out = append(out, Amount{Currency: c, Value: b[c]})
	}
	return out
}

// String formats b as "120.00 ILS, 42.17 USD", or "0" when empty.
func (b Balance) String() string {
	if b.IsZero() {
		return "0"
	}
	parts := make([]string, 0, len(b))
	for _, a := range b.Amounts() {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}
