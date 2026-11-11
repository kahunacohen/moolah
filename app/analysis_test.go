// Tests for exchange-rate download and the net worth and income reports.
package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"moolah/core"
	"moolah/prices"
)

// fakeFetcher returns fixed rates and counts calls.
type fakeFetcher struct {
	calls  int
	prices []core.Price
}

func (f *fakeFetcher) Name() core.PriceSource { return "fake" }

func (f *fakeFetcher) Supports(base, quote core.Currency) bool {
	return base == "USD" && quote == "ILS"
}

func (f *fakeFetcher) Fetch(ctx context.Context, base, quote core.Currency, from, to core.Date) ([]core.Price, error) {
	f.calls++
	if base != "USD" {
		return nil, prices.ErrUnsupported
	}
	return f.prices, nil
}

func usdPrice(date, rate string) core.Price {
	return core.Price{Base: "USD", Quote: "ILS", Date: core.MustDate(date), Rate: dec(rate), Source: "fake"}
}

func TestReports(t *testing.T) {
	// The net worth change splits into flows at their own dates' rates plus a currency
	// effect, and the lines always add up to the change.
	t.Run("currency effect reconciles net worth", func(t *testing.T) {
		a := newBook(t)
		a.Offline = false
		a.Fetchers = []prices.Fetcher{&fakeFetcher{prices: []core.Price{usdPrice("2026-01-01", "3.70"), usdPrice("2026-10-31", "3.90")}}}
		mustDo(t, func() error {
			_, err := a.AddAccount(ctx, AddAccountParams{Path: "assets:bank:usd", Currency: "USD", Opening: "1000", OpeningDate: core.MustDate("2026-01-01")})
			return err
		})
		rep, err := a.NetWorth(ctx, NetWorthParams{Period: Period{From: core.MustDate("2026-01-01"), To: core.MustDate("2026-10-31")}})
		if err != nil {
			t.Fatal(err)
		}
		// leumi opened 25,000 ILS on 2026-10-01; the USD account 1,000 USD at 3.70.
		if !rep.Opening.Equal(dec("28700")) || !rep.FX.Equal(dec("200")) || !rep.End.Equal(dec("28900")) {
			t.Fatalf("opening %s fx %s end %s", rep.Opening, rep.FX, rep.End)
		}
		sum := rep.Start.Add(rep.Income).Add(rep.Gains).Sub(rep.Spending).Add(rep.Opening).Add(rep.FX)
		if !sum.Equal(rep.End) {
			t.Fatalf("lines add to %s, end is %s", sum, rep.End)
		}
	})
	// Spending uses each purchase's own date rate, so a later rate change does not change it.
	t.Run("income report uses historical rates", func(t *testing.T) {
		a := newBook(t)
		a.Offline = false
		a.Fetchers = []prices.Fetcher{&fakeFetcher{prices: []core.Price{usdPrice("2026-10-05", "3.70"), usdPrice("2026-10-20", "3.80"), usdPrice("2026-10-31", "3.90")}}}
		for _, d := range []string{"2026-10-05", "2026-10-20"} {
			mustDo(t, func() error {
				_, err := a.Add(ctx, AddParams{Account: "card:chase", Amount: "-100", Category: "shopping", TxInfo: TxInfo{Date: core.MustDate(d)}})
				return err
			})
		}
		rep, err := a.Income(ctx, IncomeParams{Period: Period{From: core.MustDate("2026-10-01"), To: core.MustDate("2026-10-31")}})
		if err != nil {
			t.Fatal(err)
		}
		if !rep.TotalExpenses.Equal(dec("750")) {
			t.Fatalf("spending %s, want 370+380", rep.TotalExpenses)
		}
	})
	// With --by charge-date, a September purchase charged in October belongs to October.
	t.Run("group by charge date", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error {
			_, err := a.Add(ctx, AddParams{Account: "leumi", Amount: "-300", Category: "shopping",
				TxInfo: TxInfo{Date: core.MustDate("2026-09-20"), ChargeDate: core.MustDate("2026-10-10")}})
			return err
		})
		oct := Period{From: core.MustDate("2026-10-01"), To: core.MustDate("2026-10-31")}
		byPurchase, err := a.Income(ctx, IncomeParams{Period: oct})
		if err != nil {
			t.Fatal(err)
		}
		byCharge, err := a.Income(ctx, IncomeParams{Period: oct, ByChargeDate: true})
		if err != nil {
			t.Fatal(err)
		}
		if !byPurchase.TotalExpenses.IsZero() || !byCharge.TotalExpenses.Equal(dec("300")) {
			t.Fatalf("by purchase %s, by charge %s", byPurchase.TotalExpenses, byCharge.TotalExpenses)
		}
	})
	// A missing rate is downloaded once and the report is retried; offline, it stays an error.
	t.Run("missing rates are downloaded unless offline", func(t *testing.T) {
		a := newBook(t)
		f := &fakeFetcher{prices: []core.Price{usdPrice("2026-10-01", "3.70")}}
		a.Fetchers = []prices.Fetcher{f}
		mustDo(t, func() error {
			_, err := a.Add(ctx, AddParams{Account: "card:chase", Amount: "-10", Category: "shopping", TxInfo: TxInfo{Date: core.MustDate("2026-10-02")}})
			return err
		})
		a.Offline = true
		var missing *core.MissingRateError
		if _, err := a.Balances(ctx, BalanceParams{}); !errors.As(err, &missing) || f.calls != 0 {
			t.Fatalf("offline: %v, calls %d", err, f.calls)
		}
		a.Offline = false
		if _, err := a.Balances(ctx, BalanceParams{}); err != nil || f.calls != 1 {
			t.Fatalf("online: %v, calls %d", err, f.calls)
		}
		if _, err := a.Balances(ctx, BalanceParams{}); err != nil || f.calls != 1 {
			t.Fatalf("stored rates must be reused: calls %d", f.calls)
		}
	})
}

// stubSource publishes a fixed set of pairs.
type stubSource struct {
	name  core.PriceSource
	pairs map[[2]core.Currency]bool
}

func (s stubSource) Name() core.PriceSource { return s.name }
func (s stubSource) Supports(b, q core.Currency) bool {
	return s.pairs[[2]core.Currency{b, q}]
}
func (s stubSource) Fetch(context.Context, core.Currency, core.Currency, core.Date, core.Date) ([]core.Price, error) {
	return nil, nil
}

func TestPlanLegs(t *testing.T) {
	local := stubSource{"local", map[[2]core.Currency]bool{{"USD", "XAA"}: true}}
	world := stubSource{"world", map[[2]core.Currency]bool{{"USD", "EUR"}: true, {"THB", "EUR"}: true, {"XAA", "EUR"}: true}}
	// Each currency is fetched against the report currency from the first source that publishes it.
	t.Run("direct pair from the first source", func(t *testing.T) {
		legs, missing := planLegs([]prices.Fetcher{local, world}, []core.Currency{"USD"}, "XAA", nil)
		if len(missing) != 0 || len(legs) != 1 || legs[0].source.Name() != "local" {
			t.Fatalf("legs %+v missing %v", legs, missing)
		}
	})
	// Without a direct source, two legs through a shared currency are fetched instead.
	t.Run("two legs through a pivot", func(t *testing.T) {
		legs, missing := planLegs([]prices.Fetcher{world}, []core.Currency{"THB"}, "USD", []core.Currency{"EUR"})
		if len(missing) != 0 || len(legs) != 2 || legs[0].quote != "EUR" || legs[1].base != "USD" {
			t.Fatalf("legs %+v missing %v", legs, missing)
		}
	})
	// A currency that no source publishes is reported, with how to add rates by hand.
	t.Run("unpublished currency reported", func(t *testing.T) {
		_, missing := planLegs([]prices.Fetcher{world}, []core.Currency{"NGN"}, "USD", []core.Currency{"EUR"})
		if len(missing) != 1 || !strings.Contains(missing[0], "rates add") {
			t.Fatalf("missing %v", missing)
		}
	})
}
