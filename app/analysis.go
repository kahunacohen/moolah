package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"moolah/core"
	"moolah/store"
)

// Period is an inclusive date range. Zero dates mean: from January 1 of the
// to-date's year, to today.
type Period struct {
	From core.Date
	To   core.Date
}

func (a *App) resolvePeriod(p Period) (Period, error) {
	if p.To.IsZero() {
		p.To = a.today()
	}
	if p.From.IsZero() {
		p.From = core.MustDate(p.To.String()[:4] + "-01-01")
	}
	if p.From.After(p.To) {
		return p, fmt.Errorf("the start date %s is after the end date %s", p.From, p.To)
	}
	return p, nil
}

// NetWorthParams selects a net-worth report.
type NetWorthParams struct {
	Period
	In string // report currency; the base currency when empty
}

// NetWorthReport explains how net worth changed in a period.
type NetWorthReport struct {
	Currency core.Currency
	End      decimal.Decimal // net worth at the end of To (current valuation)
	FX       decimal.Decimal // the change that exchange rates caused
	Gains    decimal.Decimal // income:invest, at each transaction's date rate
	Income   decimal.Decimal // other income, at each transaction's date rate
	Opening  decimal.Decimal // opening balances added in the period
	Period   Period
	Spending decimal.Decimal // expenses (positive number), at each transaction's date rate
	Start    decimal.Decimal // net worth at the end of the day before From
}

// NetWorth reports net worth at the start and end of a period, and splits the change
// into income, investment gains, spending, opening balances and the currency effect.
// Flows use each transaction's date rate; the two net worth values use the rate of
// their own date; the currency effect is the rest, so the lines always add up.
func (a *App) NetWorth(ctx context.Context, p NetWorthParams) (NetWorthReport, error) {
	period, err := a.resolvePeriod(p.Period)
	if err != nil {
		return NetWorthReport{}, err
	}
	cur, err := a.reportCurrency(ctx, p.In)
	if err != nil {
		return NetWorthReport{}, err
	}
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return NetWorthReport{}, err
	}
	startBal, err := a.st.Balances(ctx, period.From.AddDays(-1))
	if err != nil {
		return NetWorthReport{}, err
	}
	endBal, err := a.st.Balances(ctx, period.To)
	if err != nil {
		return NetWorthReport{}, err
	}
	txs, err := a.st.Transactions(ctx, store.TxFilter{From: period.From, To: period.To})
	if err != nil {
		return NetWorthReport{}, err
	}
	gainsRoot, hasGains := tree.Find("income:invest")
	gains := map[int64]bool{}
	if hasGains {
		for _, id := range tree.Subtree(gainsRoot.ID) {
			gains[id] = true
		}
	}
	var rep NetWorthReport
	err = a.withRates(ctx, cur, func(ix *core.PriceIndex) error {
		rep = NetWorthReport{Currency: cur, Period: period}
		start, err := balanceReport(tree, startBal, ix, cur, period.From.AddDays(-1))
		if err != nil {
			return err
		}
		end, err := balanceReport(tree, endBal, ix, cur, period.To)
		if err != nil {
			return err
		}
		rep.Start, rep.End = start.NetWorth, end.NetWorth
		for _, t := range txs {
			for _, ps := range t.Postings {
				acct, _ := tree.Get(ps.AccountID)
				if acct.Type.IsReal() {
					continue
				}
				v, err := ix.Convert(ps.Amount.Value, ps.Amount.Currency, cur, t.Date)
				if err != nil {
					return err
				}
				switch {
				case acct.Type == core.Expenses:
					rep.Spending = rep.Spending.Add(v)
				case gains[acct.ID]:
					rep.Gains = rep.Gains.Sub(v)
				case acct.Type == core.Income:
					rep.Income = rep.Income.Sub(v)
				case acct.Type == core.Equity:
					rep.Opening = rep.Opening.Sub(v)
				}
			}
		}
		scale := cur.Scale()
		rep.Income, rep.Gains = rep.Income.Round(scale), rep.Gains.Round(scale)
		rep.Spending, rep.Opening = rep.Spending.Round(scale), rep.Opening.Round(scale)
		rep.FX = rep.End.Sub(rep.Start).Sub(rep.Income).Sub(rep.Gains).Add(rep.Spending).Sub(rep.Opening)
		return nil
	})
	return rep, err
}

// IncomeParams selects an income and spending report.
type IncomeParams struct {
	Period
	ByChargeDate bool   // group card purchases by the date the card charged them
	Depth        int    // show only this many levels below income/expenses (0 = all)
	In           string // report currency; the base currency when empty
	Tag          string
}

// IncomeRow is one category with its total, including sub-categories.
type IncomeRow struct {
	Category string // without the income: or expenses: prefix
	Depth    int    // 1 for a top-level category
	Value    decimal.Decimal
}

// IncomeReport lists income and spending by category in one currency.
type IncomeReport struct {
	Currency      core.Currency
	Expenses      []IncomeRow
	Income        []IncomeRow
	Net           decimal.Decimal
	Period        Period
	TotalExpenses decimal.Decimal
	TotalIncome   decimal.Decimal
}

// Income reports income and spending by category. Each amount is converted at the
// rate of its transaction's date (historical valuation), so past months do not
// change when exchange rates move.
func (a *App) Income(ctx context.Context, p IncomeParams) (IncomeReport, error) {
	period, err := a.resolvePeriod(p.Period)
	if err != nil {
		return IncomeReport{}, err
	}
	cur, err := a.reportCurrency(ctx, p.In)
	if err != nil {
		return IncomeReport{}, err
	}
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return IncomeReport{}, err
	}
	filter := store.TxFilter{From: period.From, To: period.To, Tag: p.Tag}
	if p.ByChargeDate {
		filter.From = period.From.AddDays(-120) // a purchase is charged at most a few months later
	}
	txs, err := a.st.Transactions(ctx, filter)
	if err != nil {
		return IncomeReport{}, err
	}
	var rep IncomeReport
	err = a.withRates(ctx, cur, func(ix *core.PriceIndex) error {
		own := map[int64]decimal.Decimal{}
		for _, t := range txs {
			eff := t.Date
			if p.ByChargeDate && !t.ChargeDate.IsZero() {
				eff = t.ChargeDate
			}
			if eff.Before(period.From) || eff.After(period.To) {
				continue
			}
			for _, ps := range t.Postings {
				acct, _ := tree.Get(ps.AccountID)
				if !acct.Type.IsCategory() {
					continue
				}
				v, err := ix.Convert(ps.Amount.Value, ps.Amount.Currency, cur, t.Date)
				if err != nil {
					return err
				}
				if acct.Type == core.Income {
					v = v.Neg()
				}
				own[acct.ID] = own[acct.ID].Add(v)
			}
		}
		rep = IncomeReport{Currency: cur, Period: period}
		rep.Income, rep.TotalIncome = categoryRows(tree, core.Income, own, p.Depth, cur)
		rep.Expenses, rep.TotalExpenses = categoryRows(tree, core.Expenses, own, p.Depth, cur)
		rep.Net = rep.TotalIncome.Sub(rep.TotalExpenses)
		return nil
	})
	return rep, err
}

// categoryRows totals each category with its sub-categories and lists the non-zero ones.
func categoryRows(tree *core.Tree, typ core.AccountType, own map[int64]decimal.Decimal, depth int, cur core.Currency) ([]IncomeRow, decimal.Decimal) {
	root, ok := tree.Root(typ)
	if !ok {
		return nil, decimal.Zero
	}
	var rows []IncomeRow
	var total func(id int64) decimal.Decimal
	total = func(id int64) decimal.Decimal {
		sum := own[id]
		for _, c := range tree.Children(id) {
			sum = sum.Add(total(c.ID))
		}
		return sum
	}
	var walk func(id int64, d int)
	walk = func(id int64, d int) {
		for _, c := range tree.Children(id) {
			v := total(c.ID)
			if v.IsZero() {
				continue
			}
			rows = append(rows, IncomeRow{Category: strings.TrimPrefix(tree.Path(c.ID), string(typ)+":"), Depth: d, Value: v.Round(cur.Scale())})
			if depth == 0 || d < depth {
				walk(c.ID, d+1)
			}
		}
	}
	walk(root.ID, 1)
	return rows, total(root.ID).Round(cur.Scale())
}
