package core

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// TxKind tells how a transaction was created.
type TxKind string

// Transaction kinds.
const (
	KindNormal    TxKind = "normal"
	KindOpening   TxKind = "opening"   // opening balance of an account
	KindValuation TxKind = "valuation" // value change of an investment account (moolah value)
)

// Valid reports whether k is a known kind.
func (k TxKind) Valid() bool {
	return k == KindNormal || k == KindOpening || k == KindValuation
}

// Posting is one line of a transaction: an amount added to one account.
type Posting struct {
	AccountID int64
	Amount    Amount
	Cost      *Amount // total cost in another currency (like "@@" in ledger); same sign as Amount
}

// Weight is the amount used for the balance rule: the cost when there is one.
func (p Posting) Weight() Amount {
	if p.Cost != nil {
		return *p.Cost
	}
	return p.Amount
}

// Transaction moves money between accounts. Its postings must balance.
type Transaction struct {
	ChargeDate Date // card charge date, when known
	Date       Date
	ID         int64
	Kind       TxKind
	Meta       map[string]string
	Note       string
	Payee      string
	Postings   []Posting
	Tags       []string
	Version    int64 // increases on every change; used to detect edits from two places
}

// ValidateTag checks a tag name.
func ValidateTag(tag string) error {
	if tag == "" || strings.ContainsAny(tag, " \t\n,#") {
		return fmt.Errorf("invalid tag %q: tags cannot be empty or contain spaces, commas or #", tag)
	}
	return nil
}

// Validate checks the transaction against the account tree: at least two postings,
// postable and open accounts, the right currency on real accounts, amounts that fit
// the currency, and postings that balance to zero in each currency.
func (t Transaction) Validate(tree *Tree) error {
	if t.Date.IsZero() {
		return fmt.Errorf("transaction has no date")
	}
	if !t.Kind.Valid() {
		return fmt.Errorf("unknown transaction kind %q", t.Kind)
	}
	if len(t.Postings) < 2 {
		return fmt.Errorf("a transaction needs at least two lines")
	}
	for _, tag := range t.Tags {
		if err := ValidateTag(tag); err != nil {
			return err
		}
	}
	weights := Balance{}
	for _, p := range t.Postings {
		a, ok := tree.Get(p.AccountID)
		if !ok {
			return fmt.Errorf("posting refers to account ID %d, which does not exist", p.AccountID)
		}
		path := tree.Path(a.ID)
		if a.IsGroup() {
			return fmt.Errorf("%s is a group without a currency and cannot receive transactions; use one of its sub-accounts", path)
		}
		if a.IsClosed() && t.Date.After(a.ClosedOn) {
			return fmt.Errorf("account %s is closed since %s", path, a.ClosedOn)
		}
		if a.Type.IsReal() && p.Amount.Currency != a.Currency {
			return fmt.Errorf("%s is a %s account; this line is in %s", path, a.Currency, p.Amount.Currency)
		}
		if p.Amount.Value.IsZero() {
			return fmt.Errorf("the line for %s has a zero amount", path)
		}
		if err := CheckScale(p.Amount.Value, p.Amount.Currency); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if p.Cost != nil {
			if p.Cost.Currency == p.Amount.Currency {
				return fmt.Errorf("%s: the cost is in the same currency as the amount", path)
			}
			if p.Cost.Value.Sign() != p.Amount.Value.Sign() {
				return fmt.Errorf("%s: the cost must have the same sign as the amount", path)
			}
			if err := CheckScale(p.Cost.Value, p.Cost.Currency); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		}
		w := p.Weight()
		weights.Add(w.Currency, w.Value)
	}
	if !weights.IsZero() {
		return fmt.Errorf("transaction does not balance: %s", weights)
	}
	return nil
}

// ImpliedPrices returns the exchange rates that the transaction's costs imply,
// for example 420 USD costing 1,530 ILS implies USD→ILS 3.642857….
func (t Transaction) ImpliedPrices() []Price {
	var out []Price
	for _, p := range t.Postings {
		if p.Cost == nil {
			continue
		}
		rate := p.Cost.Value.Abs().DivRound(p.Amount.Value.Abs(), rateScale)
		out = append(out, Price{
			Base:   p.Amount.Currency,
			Date:   t.Date,
			Quote:  p.Cost.Currency,
			Rate:   rate,
			Source: SourceTx,
		})
	}
	return out
}

// Sum returns the sum of the postings to the given accounts, in their own currencies.
func (t Transaction) Sum(accounts map[int64]bool) Balance {
	b := Balance{}
	for _, p := range t.Postings {
		if accounts[p.AccountID] {
			b.Add(p.Amount.Currency, p.Amount.Value)
		}
	}
	return b
}

// NewAmount is a short way to build an Amount.
func NewAmount(v decimal.Decimal, c Currency) Amount { return Amount{Currency: c, Value: v} }
