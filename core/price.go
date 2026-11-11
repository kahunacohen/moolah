package core

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

const rateScale = 10

// PriceSource tells where an exchange rate came from.
type PriceSource string

// Price sources that are not downloaded. Downloaded rates use the name of their
// rate source (for example "ecb"), which comes from its TOML definition.
const (
	SourceManual PriceSource = "manual" // entered by the user
	SourceTx     PriceSource = "tx"     // implied by a transaction with two currencies
)

// Price is an exchange rate: 1 Base = Rate Quote on Date.
type Price struct {
	Base   Currency
	Date   Date
	Quote  Currency
	Rate   decimal.Decimal
	Source PriceSource
}

// MissingRateError is returned when no rate exists on or before a date.
type MissingRateError struct {
	Date     Date
	From, To Currency
}

func (e *MissingRateError) Error() string {
	return fmt.Sprintf("no %s→%s rate on or before %s. Add one with: moolah rates add %s %s %s <rate>",
		e.From, e.To, e.Date, e.Date, e.From, e.To)
}

type pairKey struct{ from, to Currency }

type quote struct {
	date     Date
	inverse  bool
	priority int
	rate     decimal.Decimal
}

// PriceIndex answers "what is the rate from X to Y on date D".
type PriceIndex struct {
	pairs  map[pairKey][]quote // sorted by date, then best last
	pivots []Currency          // tried in this order for cross rates
}

// IndexOptions controls how rates are chosen.
type IndexOptions struct {
	// Order lists downloaded sources from most to least trusted. On the same date,
	// a listed source wins over an unlisted one, any downloaded rate wins over a
	// manual one, and a manual one wins over a rate implied by a transaction.
	Order []PriceSource
	// Pivots are tried first for cross rates (usually the base currency); after
	// them, every other currency with rates is tried, in alphabetical order.
	Pivots []Currency
}

func priority(s PriceSource, order []PriceSource) int {
	for i, o := range order {
		if s == o {
			return i
		}
	}
	switch s {
	case SourceManual:
		return len(order) + 1
	case SourceTx:
		return len(order) + 2
	}
	return len(order)
}

// NewPriceIndex indexes prices in both directions.
func NewPriceIndex(prices []Price, opts IndexOptions) *PriceIndex {
	ix := &PriceIndex{pairs: map[pairKey][]quote{}}
	seen := map[Currency]bool{}
	for _, c := range opts.Pivots {
		if !seen[c] {
			seen[c] = true
			ix.pivots = append(ix.pivots, c)
		}
	}
	var others []Currency
	for _, p := range prices {
		if p.Rate.Sign() <= 0 || p.Base == p.Quote {
			continue
		}
		for _, c := range []Currency{p.Base, p.Quote} {
			if !seen[c] {
				seen[c] = true
				others = append(others, c)
			}
		}
		pr := priority(p.Source, opts.Order)
		ix.add(pairKey{p.Base, p.Quote}, quote{date: p.Date, priority: pr, rate: p.Rate})
		ix.add(pairKey{p.Quote, p.Base}, quote{date: p.Date, priority: pr, inverse: true,
			rate: decimal.NewFromInt(1).DivRound(p.Rate, rateScale)})
	}
	sort.Slice(others, func(i, j int) bool { return others[i] < others[j] })
	ix.pivots = append(ix.pivots, others...)
	for k, qs := range ix.pairs {
		sort.SliceStable(qs, func(i, j int) bool {
			if !qs[i].date.t.Equal(qs[j].date.t) {
				return qs[i].date.Before(qs[j].date)
			}
			if qs[i].priority != qs[j].priority {
				return qs[i].priority > qs[j].priority // best last, so it wins below
			}
			return qs[i].inverse && !qs[j].inverse
		})
		ix.pairs[k] = qs
	}
	return ix
}

func (ix *PriceIndex) add(k pairKey, q quote) { ix.pairs[k] = append(ix.pairs[k], q) }

// direct returns the latest rate on or before date for one pair, or false.
func (ix *PriceIndex) direct(from, to Currency, date Date) (decimal.Decimal, bool) {
	qs := ix.pairs[pairKey{from, to}]
	i := sort.Search(len(qs), func(i int) bool { return qs[i].date.After(date) })
	if i == 0 {
		return decimal.Decimal{}, false
	}
	return qs[i-1].rate, true
}

// Rate returns the rate to convert 1 unit of from into to, using the latest rate on
// or before date. It tries the direct pair, then cross rates through the pivots.
func (ix *PriceIndex) Rate(from, to Currency, date Date) (decimal.Decimal, error) {
	if from == to {
		return decimal.NewFromInt(1), nil
	}
	if r, ok := ix.direct(from, to, date); ok {
		return r, nil
	}
	for _, via := range ix.pivots {
		if via == from || via == to {
			continue
		}
		r1, ok1 := ix.direct(from, via, date)
		r2, ok2 := ix.direct(via, to, date)
		if ok1 && ok2 {
			return r1.Mul(r2).Round(rateScale), nil
		}
	}
	return decimal.Decimal{}, &MissingRateError{Date: date, From: from, To: to}
}

// Convert converts v from one currency to another on a date. The result is not rounded.
func (ix *PriceIndex) Convert(v decimal.Decimal, from, to Currency, date Date) (decimal.Decimal, error) {
	r, err := ix.Rate(from, to, date)
	if err != nil {
		return decimal.Decimal{}, err
	}
	return v.Mul(r), nil
}

// ConvertBalance converts every currency in b to one currency on a date and sums them.
func (ix *PriceIndex) ConvertBalance(b Balance, to Currency, date Date) (decimal.Decimal, error) {
	total := decimal.Zero
	for _, c := range b.Currencies() {
		v, err := ix.Convert(b[c], c, to, date)
		if err != nil {
			return decimal.Decimal{}, err
		}
		total = total.Add(v)
	}
	return total, nil
}
