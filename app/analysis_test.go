// Tests for exchange-rate download and the net worth and income reports.
package app

import (
	"context"
	"errors"
	"testing"

	"moolah/core"
	"moolah/prices"
)

// fakeFetcher returns fixed rates and counts calls.
type fakeFetcher struct {
	calls  int
	prices []core.Price
}

func (f *fakeFetcher) Source() core.PriceSource { return core.SourceBOI }

func (f *fakeFetcher) Fetch(ctx context.Context, base, quote core.Currency, from, to core.Date) ([]core.Price, error) {
	f.calls++
	if base != "USD" {
		return nil, prices.ErrUnsupported
	}
	return f.prices, nil
}

func usdPrice(date, rate string) core.Price {
	return core.Price{Base: "USD", Quote: "ILS", Date: core.MustDate(date), Rate: dec(rate), Source: core.SourceBOI}
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
