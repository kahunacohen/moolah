// Tests for the core domain rules: money, account names, transactions and exchange rates.
package core

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func testTree() *Tree {
	return NewTree([]Account{
		{ID: 1, Name: "assets", Type: Assets},
		{ID: 2, Name: "bank", ParentID: 1, Type: Assets},
		{ID: 3, Name: "leumi", ParentID: 2, Type: Assets, Currency: "ILS"},
		{ID: 4, Name: "liabilities", Type: Liabilities},
		{ID: 5, Name: "card", ParentID: 4, Type: Liabilities},
		{ID: 6, Name: "chase", ParentID: 5, Type: Liabilities, Currency: "USD"},
		{ID: 7, Name: "expenses", Type: Expenses},
		{ID: 8, Name: "food", ParentID: 7, Type: Expenses},
		{ID: 9, Name: "restaurants", ParentID: 8, Type: Expenses},
		{ID: 10, Name: "income", Type: Income},
		{ID: 11, Name: "food", ParentID: 10, Type: Income},
		{ID: 12, Name: "old", ParentID: 2, Type: Assets, Currency: "ILS", ClosedOn: MustDate("2026-01-31")},
		{ID: 13, Name: "fees", ParentID: 7, Type: Expenses},
	})
}

func posting(acct int64, v string, c Currency) Posting {
	return Posting{AccountID: acct, Amount: NewAmount(dec(v), c)}
}

func TestMoney(t *testing.T) {
	// Storage uses integer minor units; an amount finer than the currency allows must be rejected, not rounded.
	t.Run("minor units reject extra decimals", func(t *testing.T) {
		if _, err := ToMinor(dec("1.005"), "ILS"); err == nil {
			t.Fatal("expected error for 3 decimals in ILS")
		}
		m, err := ToMinor(dec("-1530.5"), "ILS")
		if err != nil || m != -153050 {
			t.Fatalf("got %d, %v", m, err)
		}
		if got := FromMinor(m, "ILS"); !got.Equal(dec("-1530.50")) {
			t.Fatalf("round trip gave %s", got)
		}
	})
	// JPY has no minor unit, so its scale is 0.
	t.Run("currency scale follows ISO", func(t *testing.T) {
		if s := Currency("JPY").Scale(); s != 0 {
			t.Fatalf("JPY scale %d", s)
		}
	})
	// Display uses thousands separators and the currency's decimals.
	t.Run("format", func(t *testing.T) {
		if got := NewAmount(dec("-1234567.5"), "ILS").String(); got != "-1,234,567.50 ILS" {
			t.Fatalf("got %q", got)
		}
	})
	// Unknown currency codes are rejected at input.
	t.Run("unknown currency rejected", func(t *testing.T) {
		if _, err := ParseCurrency("XYZQ"); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestResolve(t *testing.T) {
	tree := testTree()
	// A unique last level works as a short name.
	t.Run("unique short name", func(t *testing.T) {
		a, err := tree.Resolve("leumi", nil)
		if err != nil || a.ID != 3 {
			t.Fatalf("got %v, %v", a, err)
		}
		a, err = tree.Resolve("card:chase", nil)
		if err != nil || a.ID != 6 {
			t.Fatalf("got %v, %v", a, err)
		}
	})
	// A short name that matches two accounts must not pick one silently.
	t.Run("ambiguous short name", func(t *testing.T) {
		_, err := tree.Resolve("food", nil)
		var amb *AmbiguousAccountError
		if !errors.As(err, &amb) || len(amb.Matches) != 2 {
			t.Fatalf("got %v", err)
		}
	})
	// The taxonomy is closed: a typo is an error with suggestions, never a new account.
	t.Run("typo gives suggestions", func(t *testing.T) {
		_, err := tree.Resolve("restaurnts", nil)
		var unk *UnknownAccountError
		if !errors.As(err, &unk) || len(unk.Suggestions) == 0 || unk.Suggestions[0] != "expenses:food:restaurants" {
			t.Fatalf("got %v", err)
		}
	})
	// A filter limits matches, which also resolves ambiguity (categories only see expenses/income).
	t.Run("filter", func(t *testing.T) {
		a, err := tree.Resolve("food", func(a Account) bool { return a.Type == Expenses })
		if err != nil || a.ID != 8 {
			t.Fatalf("got %v, %v", a, err)
		}
	})
}

func TestTransactionValidate(t *testing.T) {
	tree := testTree()
	d := MustDate("2026-11-03")
	cases := []struct {
		name    string
		tx      Transaction
		wantErr bool
	}{
		{"same currency balances", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(6, "-42.17", "USD"), posting(9, "42.17", "USD")}}, false},
		{"unbalanced rejected", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(3, "-200", "ILS"), posting(9, "190", "ILS")}}, true},
		{"two currencies without cost rejected", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(3, "-1530", "ILS"), posting(6, "420", "USD")}}, true},
		{"cost balances two currencies", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(3, "-1530", "ILS"),
			{AccountID: 6, Amount: NewAmount(dec("420"), "USD"), Cost: &Amount{Currency: "ILS", Value: dec("1530")}}}}, false},
		{"real account wrong currency rejected", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(3, "-10", "USD"), posting(9, "10", "USD")}}, true},
		{"category accepts any currency", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(3, "-10", "ILS"), posting(9, "10", "ILS")}}, false},
		{"group account rejected", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(2, "-10", "ILS"), posting(9, "10", "ILS")}}, true},
		{"posting after close date rejected", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(12, "-10", "ILS"), posting(9, "10", "ILS")}}, true},
		{"too many decimals rejected", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(3, "-10.001", "ILS"), posting(9, "10.001", "ILS")}}, true},
		{"single posting rejected", Transaction{Date: d, Kind: KindNormal, Postings: []Posting{
			posting(3, "0", "ILS")}}, true},
	}
	for _, c := range cases {
		// Only balanced transactions on postable, open accounts in the right currency may be stored.
		t.Run(c.name, func(t *testing.T) {
			err := c.tx.Validate(tree)
			if (err != nil) != c.wantErr {
				t.Fatalf("wantErr=%v, got %v", c.wantErr, err)
			}
		})
	}
	// A cost implies an exchange rate, which becomes a price with source tx.
	t.Run("implied price", func(t *testing.T) {
		tx := cases[3].tx
		ps := tx.ImpliedPrices()
		if len(ps) != 1 || ps[0].Base != "USD" || ps[0].Quote != "ILS" || !ps[0].Rate.Equal(dec("3.6428571429")) {
			t.Fatalf("got %+v", ps)
		}
	})
}

func TestPriceIndex(t *testing.T) {
	ix := NewPriceIndex([]Price{
		{Base: "USD", Quote: "ILS", Date: MustDate("2026-10-01"), Rate: dec("3.70"), Source: SourceBOI},
		{Base: "USD", Quote: "ILS", Date: MustDate("2026-10-20"), Rate: dec("3.80"), Source: SourceBOI},
		{Base: "USD", Quote: "ILS", Date: MustDate("2026-10-20"), Rate: dec("3.90"), Source: SourceManual},
		{Base: "EUR", Quote: "ILS", Date: MustDate("2026-10-01"), Rate: dec("4.00"), Source: SourceManual},
	})
	// The rate used is the latest one on or before the date (weekends use the last business day).
	t.Run("latest on or before", func(t *testing.T) {
		r, err := ix.Rate("USD", "ILS", MustDate("2026-10-19"))
		if err != nil || !r.Equal(dec("3.70")) {
			t.Fatalf("got %s, %v", r, err)
		}
	})
	// On the same date, the official source beats a manual rate.
	t.Run("source priority", func(t *testing.T) {
		r, _ := ix.Rate("USD", "ILS", MustDate("2026-10-25"))
		if !r.Equal(dec("3.80")) {
			t.Fatalf("got %s", r)
		}
	})
	// A rate stored in one direction also converts the other way.
	t.Run("inverse", func(t *testing.T) {
		v, err := ix.Convert(dec("370"), "ILS", "USD", MustDate("2026-10-02"))
		if err != nil || !v.Round(2).Equal(dec("100")) {
			t.Fatalf("got %s, %v", v, err)
		}
	})
	// Pairs without a direct rate are converted through ILS.
	t.Run("cross rate", func(t *testing.T) {
		r, err := ix.Rate("EUR", "USD", MustDate("2026-10-02"))
		if err != nil || !r.Round(4).Equal(dec("1.0811")) {
			t.Fatalf("got %s, %v", r, err)
		}
	})
	// A missing rate is an error that names the pair and date; never a silent 1:1.
	t.Run("missing rate is an error", func(t *testing.T) {
		_, err := ix.Rate("USD", "ILS", MustDate("2026-09-01"))
		var m *MissingRateError
		if !errors.As(err, &m) {
			t.Fatalf("got %v", err)
		}
	})
}
