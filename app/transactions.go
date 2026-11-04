package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	"moolah/core"
	"moolah/store"
)

// ErrChangedElsewhere is returned when an edit is based on an old version.
var ErrChangedElsewhere = store.ErrVersionConflict

// insertTx validates and stores a transaction, and saves the rates its costs imply.
func insertTx(ctx context.Context, tx *store.Tx, tree *core.Tree, t core.Transaction) (int64, error) {
	if t.Kind == "" {
		t.Kind = core.KindNormal
	}
	t.Tags = normalizeTags(t.Tags)
	if err := t.Validate(tree); err != nil {
		return 0, err
	}
	id, err := tx.InsertTransaction(ctx, t)
	if err != nil {
		return 0, err
	}
	for _, p := range t.ImpliedPrices() {
		if err := tx.UpsertPrice(ctx, p); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		t = strings.TrimPrefix(strings.TrimSpace(t), "#")
		if t != "" && !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func (a *App) dateOrToday(d core.Date) core.Date {
	if d.IsZero() {
		return a.today()
	}
	return d
}

// postable resolves a name to an account that can receive postings.
func postable(tree *core.Tree, name string) (core.Account, error) {
	acct, err := tree.Resolve(name, ScopeAny.filter())
	if err != nil {
		return core.Account{}, err
	}
	if acct.IsGroup() {
		return core.Account{}, fmt.Errorf("%s is a group without a currency and cannot receive transactions; use one of its sub-accounts", tree.Path(acct.ID))
	}
	return acct, nil
}

// realAccount resolves a name among assets and liabilities only, so a short name
// such as "pension" is not ambiguous with a category of the same name.
func realAccount(tree *core.Tree, name string) (core.Account, error) {
	acct, err := tree.Resolve(name, ScopeReal.filter())
	if err != nil {
		var unk *core.UnknownAccountError
		if errors.As(err, &unk) {
			if other, err2 := tree.Resolve(name, ScopeAny.filter()); err2 == nil && !other.Type.IsReal() {
				return core.Account{}, fmt.Errorf("%s is a category; this needs a real account (assets or liabilities)", tree.Path(other.ID))
			}
		}
		return core.Account{}, err
	}
	if acct.IsGroup() {
		return core.Account{}, fmt.Errorf("%s is a group without a currency and cannot receive transactions; use one of its sub-accounts", tree.Path(acct.ID))
	}
	return acct, nil
}

func parseAmount(s string, cur core.Currency) (decimal.Decimal, error) {
	v, err := core.ParseDecimal(s)
	if err != nil {
		return v, err
	}
	if err := core.CheckScale(v, cur); err != nil {
		return v, err
	}
	return v, nil
}

// TxInfo is the descriptive part of a new transaction.
type TxInfo struct {
	Date  core.Date // today when zero
	Note  string
	Payee string
	Tags  []string
}

// AddParams describes a simple transaction: an amount on one account, balanced by a category.
type AddParams struct {
	TxInfo
	Account  string
	Amount   string // in the account's currency; negative = money leaves the account
	Category string
}

// Add records a simple transaction and returns it with its ID.
func (a *App) Add(ctx context.Context, p AddParams) (core.Transaction, error) {
	var t core.Transaction
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		acct, err := realAccount(tree, p.Account)
		if err != nil {
			return err
		}
		cat, err := postable(tree, p.Category)
		if err != nil {
			return err
		}
		v, err := parseAmount(p.Amount, acct.Currency)
		if err != nil {
			return err
		}
		t = a.newTx(p.TxInfo, []core.Posting{
			{AccountID: acct.ID, Amount: core.NewAmount(v, acct.Currency)},
			{AccountID: cat.ID, Amount: core.NewAmount(v.Neg(), acct.Currency)},
		})
		return a.store(ctx, tx, tree, &t)
	})
	return t, err
}

func (a *App) newTx(info TxInfo, postings []core.Posting) core.Transaction {
	return core.Transaction{
		Date:     a.dateOrToday(info.Date),
		Kind:     core.KindNormal,
		Note:     strings.TrimSpace(info.Note),
		Payee:    strings.TrimSpace(info.Payee),
		Postings: postings,
		Tags:     info.Tags,
	}
}

// store validates and inserts *t, then reloads it so it has its ID and version.
func (a *App) store(ctx context.Context, tx *store.Tx, tree *core.Tree, t *core.Transaction) error {
	id, err := insertTx(ctx, tx, tree, *t)
	if err != nil {
		return err
	}
	*t, err = tx.Transaction(ctx, id)
	return err
}

// AddPostingsParams describes a transaction with any number of lines.
type AddPostingsParams struct {
	TxInfo
	Postings []string // "account=amount", optionally with a currency for categories: "travel=41.30USD"
}

// AddPostings records a transaction from explicit lines.
func (a *App) AddPostings(ctx context.Context, p AddPostingsParams) (core.Transaction, error) {
	var t core.Transaction
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		type line struct {
			acct   core.Account
			amount string
			cur    core.Currency
		}
		var lines []line
		realCurs := map[core.Currency]bool{}
		for _, raw := range p.Postings {
			name, amount, ok := strings.Cut(raw, "=")
			if !ok {
				return fmt.Errorf("invalid line %q: use account=amount", raw)
			}
			acct, err := postable(tree, name)
			if err != nil {
				return err
			}
			amount, cur, err := splitCurrency(amount)
			if err != nil {
				return err
			}
			if acct.Type.IsReal() {
				if cur != "" && cur != acct.Currency {
					return fmt.Errorf("%s is a %s account; the line says %s", tree.Path(acct.ID), acct.Currency, cur)
				}
				cur = acct.Currency
				realCurs[cur] = true
			}
			lines = append(lines, line{acct, amount, cur})
		}
		var postings []core.Posting
		for _, l := range lines {
			cur := l.cur
			if cur == "" {
				if len(realCurs) != 1 {
					return fmt.Errorf("give a currency for %s, for example %s=120ILS", tree.Path(l.acct.ID), tree.Path(l.acct.ID))
				}
				for c := range realCurs {
					cur = c
				}
			}
			v, err := parseAmount(l.amount, cur)
			if err != nil {
				return err
			}
			postings = append(postings, core.Posting{AccountID: l.acct.ID, Amount: core.NewAmount(v, cur)})
		}
		t = a.newTx(p.TxInfo, postings)
		return a.store(ctx, tx, tree, &t)
	})
	return t, err
}

// splitCurrency splits "41.30USD" or "41.30 USD" into the number and the currency.
func splitCurrency(s string) (string, core.Currency, error) {
	s = strings.TrimSpace(s)
	i := len(s)
	for i > 0 && (s[i-1] >= 'A' && s[i-1] <= 'Z' || s[i-1] >= 'a' && s[i-1] <= 'z') {
		i--
	}
	if i == len(s) {
		return s, "", nil
	}
	cur, err := core.ParseCurrency(s[i:])
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(s[:i]), cur, nil
}

// TransferParams describes money moving between two real accounts.
type TransferParams struct {
	TxInfo
	Amount   string // leaves From, in From's currency
	Fee      string // optional, in From's currency, booked to expenses:fees
	From     string
	To       string
	ToAmount string // arrives in To, in To's currency; required when the currencies differ
}

// Transfer records a transfer, including currency exchange and a fee.
func (a *App) Transfer(ctx context.Context, p TransferParams) (core.Transaction, error) {
	var t core.Transaction
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		from, err := realAccount(tree, p.From)
		if err != nil {
			return err
		}
		to, err := realAccount(tree, p.To)
		if err != nil {
			return err
		}
		if from.ID == to.ID {
			return fmt.Errorf("cannot transfer from an account to itself")
		}
		amt, err := parseAmount(p.Amount, from.Currency)
		if err != nil {
			return err
		}
		if amt.Sign() <= 0 {
			return fmt.Errorf("the transfer amount must be positive")
		}
		fee := decimal.Zero
		if p.Fee != "" {
			if fee, err = parseAmount(p.Fee, from.Currency); err != nil {
				return err
			}
			if fee.Sign() < 0 {
				return fmt.Errorf("the fee must be positive")
			}
		}
		toPosting := core.Posting{AccountID: to.ID, Amount: core.NewAmount(amt, to.Currency)}
		if from.Currency != to.Currency {
			if p.ToAmount == "" {
				return fmt.Errorf("%s is in %s and %s is in %s; give the amount that arrived with --to-amount",
					tree.Path(from.ID), from.Currency, tree.Path(to.ID), to.Currency)
			}
			toAmt, err := parseAmount(p.ToAmount, to.Currency)
			if err != nil {
				return err
			}
			if toAmt.Sign() <= 0 {
				return fmt.Errorf("--to-amount must be positive")
			}
			toPosting = core.Posting{AccountID: to.ID, Amount: core.NewAmount(toAmt, to.Currency),
				Cost: &core.Amount{Currency: from.Currency, Value: amt}}
		} else if p.ToAmount != "" {
			toAmt, err := parseAmount(p.ToAmount, to.Currency)
			if err != nil {
				return err
			}
			if !toAmt.Equal(amt) {
				return fmt.Errorf("both accounts are in %s, so --to-amount must equal the amount (or use --fee)", from.Currency)
			}
		}
		postings := []core.Posting{
			{AccountID: from.ID, Amount: core.NewAmount(amt.Add(fee).Neg(), from.Currency)},
			toPosting,
		}
		if fee.Sign() > 0 {
			fees, ok := tree.Find(accountFees)
			if !ok {
				return fmt.Errorf("the category fees does not exist. Create it with: moolah category add fees")
			}
			postings = append(postings, core.Posting{AccountID: fees.ID, Amount: core.NewAmount(fee, from.Currency)})
		}
		info := p.TxInfo
		if info.Payee == "" {
			info.Payee = "Transfer"
		}
		t = a.newTx(info, postings)
		return a.store(ctx, tx, tree, &t)
	})
	return t, err
}

// ValueParams describes a statement value for an investment account.
type ValueParams struct {
	Account string
	Date    core.Date
	Value   string
}

// ValueResult is what Value booked.
type ValueResult struct {
	Before core.Amount
	Delta  core.Amount
	Tx     *core.Transaction // nil when the value did not change
}

// Value records the value of an account on a date. It books the difference from
// the current balance as investment gain or loss (income:invest:gains).
func (a *App) Value(ctx context.Context, p ValueParams) (ValueResult, error) {
	var res ValueResult
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		acct, err := realAccount(tree, p.Account)
		if err != nil {
			return err
		}
		v, err := parseAmount(p.Value, acct.Currency)
		if err != nil {
			return err
		}
		date := a.dateOrToday(p.Date)
		balances, err := tx.Balances(ctx, date)
		if err != nil {
			return err
		}
		before := balances[acct.ID][acct.Currency]
		delta := v.Sub(before)
		res.Before = core.NewAmount(before, acct.Currency)
		res.Delta = core.NewAmount(delta, acct.Currency)
		if delta.IsZero() {
			return nil
		}
		gains, ok := tree.Find(accountGains)
		if !ok {
			return fmt.Errorf("the category %s does not exist. Create it with: moolah category add invest:gains --income", accountGains)
		}
		t := core.Transaction{Date: date, Kind: core.KindValuation, Payee: "Valuation", Postings: []core.Posting{
			{AccountID: acct.ID, Amount: core.NewAmount(delta, acct.Currency)},
			{AccountID: gains.ID, Amount: core.NewAmount(delta.Neg(), acct.Currency)},
		}}
		if err := a.store(ctx, tx, tree, &t); err != nil {
			return err
		}
		res.Tx = &t
		return nil
	})
	return res, err
}

// Transaction loads one transaction.
func (a *App) Transaction(ctx context.Context, id int64) (core.Transaction, error) {
	t, err := a.st.Transaction(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return t, fmt.Errorf("there is no transaction %d", id)
	}
	return t, err
}

// UpdateTransaction saves an edited transaction. It fails with ErrChangedElsewhere
// when the transaction changed after it was loaded.
func (a *App) UpdateTransaction(ctx context.Context, t core.Transaction) error {
	return a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		t.Tags = normalizeTags(t.Tags)
		if err := t.Validate(tree); err != nil {
			return err
		}
		if err := tx.UpdateTransaction(ctx, t); err != nil {
			return err
		}
		for _, p := range t.ImpliedPrices() {
			if err := tx.UpsertPrice(ctx, p); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteTransaction removes a transaction.
func (a *App) DeleteTransaction(ctx context.Context, id int64) error {
	err := a.st.DeleteTransaction(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("there is no transaction %d", id)
	}
	return err
}

// Tags returns all tags with how many transactions use them.
func (a *App) Tags(ctx context.Context) ([]store.TagCount, error) { return a.st.Tags(ctx) }
