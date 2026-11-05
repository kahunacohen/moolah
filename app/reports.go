package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	"moolah/core"
	"moolah/store"
)

// BalanceParams selects a balance report.
type BalanceParams struct {
	Date core.Date // today when zero
	In   string    // report currency; the base currency when empty
}

// BalanceRow is one account in a balance report. Values include sub-accounts.
type BalanceRow struct {
	Account     string
	Currency    core.Currency // the account's own currency, empty for groups
	Depth       int
	HasAccounts bool            // the row or a sub-account can hold money (false for empty groups)
	Native      core.Balance    // in the accounts' own currencies
	Value       decimal.Decimal // converted to the report currency at the report date
}

// BalanceReport lists assets and liabilities with their value in one currency.
type BalanceReport struct {
	Currency core.Currency
	Date     core.Date
	NetWorth decimal.Decimal
	Rows     []BalanceRow
}

func (a *App) reportCurrency(ctx context.Context, in string) (core.Currency, error) {
	if in != "" {
		return core.ParseCurrency(in)
	}
	return a.BaseCurrency(ctx)
}

// PriceIndex loads all stored exchange rates.
func (a *App) PriceIndex(ctx context.Context) (*core.PriceIndex, error) {
	prices, err := a.st.Prices(ctx)
	if err != nil {
		return nil, err
	}
	return core.NewPriceIndex(prices), nil
}

// Balances reports the balance of every open asset and liability account on a date,
// converted at that date's rates (current valuation).
func (a *App) Balances(ctx context.Context, p BalanceParams) (BalanceReport, error) {
	cur, err := a.reportCurrency(ctx, p.In)
	if err != nil {
		return BalanceReport{}, err
	}
	date := a.dateOrToday(p.Date)
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return BalanceReport{}, err
	}
	balances, err := a.st.Balances(ctx, date)
	if err != nil {
		return BalanceReport{}, err
	}
	var rep BalanceReport
	err = a.withRates(ctx, func(ix *core.PriceIndex) error {
		var err error
		rep, err = balanceReport(tree, balances, ix, cur, date)
		return err
	})
	return rep, err
}

func balanceReport(tree *core.Tree, balances map[int64]core.Balance, ix *core.PriceIndex, cur core.Currency, date core.Date) (BalanceReport, error) {
	rep := BalanceReport{Currency: cur, Date: date}

	var visit func(id int64) (core.Balance, decimal.Decimal, error)
	visit = func(id int64) (core.Balance, decimal.Decimal, error) {
		acct, _ := tree.Get(id)
		idx := len(rep.Rows)
		rep.Rows = append(rep.Rows, BalanceRow{Account: tree.Path(id), Currency: acct.Currency, Depth: tree.Depth(id),
			HasAccounts: !acct.IsGroup()})
		native := core.Balance{}
		native.AddBalance(balances[id])
		value, err := ix.ConvertBalance(balances[id], cur, date)
		if err != nil {
			return nil, decimal.Decimal{}, err
		}
		for _, c := range tree.Children(id) {
			childIdx := len(rep.Rows)
			n, v, err := visit(c.ID)
			if err != nil {
				return nil, decimal.Decimal{}, err
			}
			if childIdx < len(rep.Rows) && rep.Rows[childIdx].HasAccounts {
				rep.Rows[idx].HasAccounts = true
			}
			native.AddBalance(n)
			value = value.Add(v)
		}
		if acct.IsClosed() && native.IsZero() && !acct.ClosedOn.After(date) {
			rep.Rows = rep.Rows[:idx]
			return native, value, nil
		}
		rep.Rows[idx].Native = native
		rep.Rows[idx].Value = value.Round(cur.Scale())
		return native, value, nil
	}
	total := decimal.Zero
	for _, typ := range []core.AccountType{core.Assets, core.Liabilities} {
		root, ok := tree.Root(typ)
		if !ok {
			continue
		}
		_, v, err := visit(root.ID)
		if err != nil {
			return BalanceReport{}, err
		}
		total = total.Add(v)
	}
	rep.NetWorth = total.Round(cur.Scale())
	return rep, nil
}

// RegisterParams selects transactions.
type RegisterParams struct {
	Account string // with sub-accounts; all transactions when empty
	From    core.Date
	Scope   Scope
	Tag     string
	To      core.Date
}

// RegisterRow is one transaction in a register.
type RegisterRow struct {
	Amount  core.Balance // sum of the lines on the selected account(s)
	Date    core.Date
	ID      int64
	Note    string
	Other   string // the other accounts in the transaction
	Payee   string
	Running core.Balance // running balance of the selected account(s); nil without an account
	Tags    []string
}

// RegisterReport is a list of transactions.
type RegisterReport struct {
	Account string
	Rows    []RegisterRow
}

// Register lists transactions, optionally for one account (with its sub-accounts)
// and with a running balance.
func (a *App) Register(ctx context.Context, p RegisterParams) (RegisterReport, error) {
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return RegisterReport{}, err
	}
	var rep RegisterReport
	selected := map[int64]bool{}
	var filter store.TxFilter
	if p.Account != "" {
		f := p.Scope.filter()
		acct, err := tree.Resolve(p.Account, func(a core.Account) bool { return a.IsRoot() || f(a) })
		if err != nil {
			return RegisterReport{}, err
		}
		rep.Account = tree.Path(acct.ID)
		for _, id := range tree.Subtree(acct.ID) {
			selected[id] = true
			filter.AccountIDs = append(filter.AccountIDs, id)
		}
	}
	filter.Tag = p.Tag
	filter.To = p.To
	if p.Account == "" {
		filter.From = p.From
	}
	txs, err := a.st.Transactions(ctx, filter)
	if err != nil {
		return RegisterReport{}, err
	}
	running := core.Balance{}
	for _, t := range txs {
		row := RegisterRow{Date: t.Date, ID: t.ID, Note: t.Note, Payee: t.Payee, Tags: t.Tags}
		var others []string
		if p.Account != "" {
			row.Amount = t.Sum(selected)
			running.AddBalance(row.Amount)
			row.Running = core.Balance{}
			row.Running.AddBalance(running)
		} else {
			row.Amount = core.Balance{}
		}
		for _, ps := range t.Postings {
			if selected[ps.AccountID] {
				continue
			}
			others = append(others, tree.Path(ps.AccountID))
			if p.Account == "" && ps.Amount.Value.Sign() > 0 {
				row.Amount.Add(ps.Amount.Currency, ps.Amount.Value)
			}
		}
		row.Other = strings.Join(uniq(others), ", ")
		if !p.From.IsZero() && t.Date.Before(p.From) {
			continue
		}
		rep.Rows = append(rep.Rows, row)
	}
	return rep, nil
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// PriceParams describes a rate entered by the user.
type PriceParams struct {
	Base  string
	Date  core.Date
	Quote string
	Rate  string
}

func parsePrice(p PriceParams, source core.PriceSource) (core.Price, error) {
	base, err := core.ParseCurrency(p.Base)
	if err != nil {
		return core.Price{}, err
	}
	quote, err := core.ParseCurrency(p.Quote)
	if err != nil {
		return core.Price{}, err
	}
	if base == quote {
		return core.Price{}, fmt.Errorf("the two currencies are the same")
	}
	rate, err := core.ParseDecimal(p.Rate)
	if err != nil {
		return core.Price{}, err
	}
	if rate.Sign() <= 0 {
		return core.Price{}, fmt.Errorf("the rate must be positive")
	}
	if p.Date.IsZero() {
		return core.Price{}, fmt.Errorf("the rate needs a date")
	}
	return core.Price{Base: base, Date: p.Date, Quote: quote, Rate: rate, Source: source}, nil
}

// AddPrice stores a rate entered by the user (source manual).
func (a *App) AddPrice(ctx context.Context, p PriceParams) (core.Price, error) {
	price, err := parsePrice(p, core.SourceManual)
	if err != nil {
		return price, err
	}
	return price, a.st.UpsertPrice(ctx, price)
}

// AddPrices stores many manual rates in one transaction.
func (a *App) AddPrices(ctx context.Context, ps []PriceParams) error {
	return a.st.InTx(ctx, func(tx *store.Tx) error {
		for i, p := range ps {
			price, err := parsePrice(p, core.SourceManual)
			if err != nil {
				return fmt.Errorf("row %d: %w", i+1, err)
			}
			if err := tx.UpsertPrice(ctx, price); err != nil {
				return err
			}
		}
		return nil
	})
}

// Prices returns stored rates, optionally only for one currency.
func (a *App) Prices(ctx context.Context, currency string) ([]core.Price, error) {
	all, err := a.st.Prices(ctx)
	if err != nil || currency == "" {
		return all, err
	}
	c, err := core.ParseCurrency(currency)
	if err != nil {
		return nil, err
	}
	var out []core.Price
	for _, p := range all {
		if p.Base == c || p.Quote == c {
			out = append(out, p)
		}
	}
	return out, nil
}

// Check finds problems in the book: SQLite integrity, broken references, and
// transactions that break the rules (unbalanced, closed or group accounts).
func (a *App) Check(ctx context.Context) ([]string, error) {
	problems, err := a.st.IntegrityProblems(ctx)
	if err != nil {
		return nil, err
	}
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return nil, err
	}
	txs, err := a.st.Transactions(ctx, store.TxFilter{})
	if err != nil {
		return nil, err
	}
	for _, t := range txs {
		if err := t.Validate(tree); err != nil {
			problems = append(problems, fmt.Sprintf("transaction %d (%s): %v", t.ID, t.Date, err))
		}
	}
	for _, typ := range core.AccountTypes {
		if _, ok := tree.Root(typ); !ok {
			problems = append(problems, fmt.Sprintf("the top-level account %s is missing", typ))
		}
	}
	sort.Strings(problems)
	return problems, nil
}
