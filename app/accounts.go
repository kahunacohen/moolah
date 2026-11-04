package app

import (
	"context"
	"fmt"
	"strings"

	"moolah/core"
	"moolah/store"
)

// Scope tells which part of the tree a name refers to.
type Scope int

// Scopes for account commands.
const (
	ScopeAny      Scope = iota // any account
	ScopeReal                  // assets and liabilities ("moolah account …")
	ScopeCategory              // income and expenses ("moolah category …"), names without the root
)

func (s Scope) filter() core.ResolveFilter {
	switch s {
	case ScopeReal:
		return func(a core.Account) bool { return a.Type.IsReal() && !a.IsRoot() }
	case ScopeCategory:
		return func(a core.Account) bool { return a.Type.IsCategory() && !a.IsRoot() }
	}
	return func(a core.Account) bool { return !a.IsRoot() }
}

// DisplayName is how users see an account in a scope: categories without "expenses:".
func DisplayName(tree *core.Tree, id int64, scope Scope) string {
	p := tree.Path(id)
	a, _ := tree.Get(id)
	if scope == ScopeCategory && a.Type == core.Expenses {
		return strings.TrimPrefix(p, "expenses:")
	}
	return p
}

// AddAccountParams describes a new real account.
type AddAccountParams struct {
	Currency    string // empty creates a group that holds other accounts
	Opening     string // opening balance, optional
	OpeningDate core.Date
	Path        string // full path, for example assets:bank:leumi
}

// AddAccount creates an asset or liability account, with an optional opening balance.
func (a *App) AddAccount(ctx context.Context, p AddAccountParams) (core.Account, error) {
	parts, err := core.SplitPath(p.Path)
	if err != nil {
		return core.Account{}, err
	}
	typ := core.AccountType(strings.ToLower(parts[0]))
	if !typ.IsReal() {
		if typ.IsCategory() {
			return core.Account{}, fmt.Errorf("%s is a category. Use: moolah category add", p.Path)
		}
		return core.Account{}, fmt.Errorf("an account name starts with assets: or liabilities:, for example assets:bank:leumi")
	}
	var cur core.Currency
	if p.Currency != "" {
		if cur, err = core.ParseCurrency(p.Currency); err != nil {
			return core.Account{}, err
		}
	}
	if p.Opening != "" && cur == "" {
		return core.Account{}, fmt.Errorf("an opening balance needs --currency")
	}
	var created core.Account
	err = a.st.InTx(ctx, func(tx *store.Tx) error {
		acct, err := insertAccount(ctx, tx, typ, parts, cur)
		if err != nil {
			return err
		}
		created = acct
		if p.Opening == "" {
			return nil
		}
		v, err := core.ParseDecimal(p.Opening)
		if err != nil {
			return err
		}
		if v.IsZero() {
			return nil
		}
		date := p.OpeningDate
		if date.IsZero() {
			date = a.today()
		}
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		opening, ok := tree.Find(accountOpening)
		if !ok {
			return fmt.Errorf("%s is missing from this book", accountOpening)
		}
		t := core.Transaction{Date: date, Kind: core.KindOpening, Payee: "Opening balance", Postings: []core.Posting{
			{AccountID: acct.ID, Amount: core.NewAmount(v, cur)},
			{AccountID: opening.ID, Amount: core.NewAmount(v.Neg(), cur)},
		}}
		_, err = insertTx(ctx, tx, tree, t)
		return err
	})
	return created, err
}

// AddCategoryParams describes a new category.
type AddCategoryParams struct {
	Income bool   // under income: instead of expenses:
	Path   string // without the root, for example food:restaurants
}

// AddCategory creates a spending or income category.
func (a *App) AddCategory(ctx context.Context, p AddCategoryParams) (core.Account, error) {
	typ := core.Expenses
	if p.Income {
		typ = core.Income
	}
	path := strings.TrimPrefix(strings.TrimSpace(p.Path), string(typ)+":")
	parts, err := core.SplitPath(string(typ) + ":" + path)
	if err != nil {
		return core.Account{}, err
	}
	var created core.Account
	err = a.st.InTx(ctx, func(tx *store.Tx) error {
		created, err = insertAccount(ctx, tx, typ, parts, "")
		return err
	})
	return created, err
}

func insertAccount(ctx context.Context, tx *store.Tx, typ core.AccountType, parts []string, cur core.Currency) (core.Account, error) {
	tree, err := tx.Tree(ctx)
	if err != nil {
		return core.Account{}, err
	}
	full := strings.Join(parts, ":")
	if len(parts) < 2 {
		return core.Account{}, fmt.Errorf("%s is a fixed top level; add an account below it", full)
	}
	if existing, ok := tree.Find(full); ok {
		return core.Account{}, fmt.Errorf("%s already exists", tree.Path(existing.ID))
	}
	parentPath := strings.Join(parts[:len(parts)-1], ":")
	parent, ok := tree.Find(parentPath)
	if !ok {
		return core.Account{}, fmt.Errorf("%s does not exist. Create the parent first", parentPath)
	}
	if !parent.IsRoot() && parent.Type.IsReal() && parent.Currency != "" {
		return core.Account{}, fmt.Errorf("%s holds money, so it cannot have sub-accounts", tree.Path(parent.ID))
	}
	if parent.IsClosed() {
		return core.Account{}, fmt.Errorf("%s is closed", tree.Path(parent.ID))
	}
	acct := core.Account{Currency: cur, Name: parts[len(parts)-1], ParentID: parent.ID, Type: typ}
	id, err := tx.InsertAccount(ctx, acct)
	if err != nil {
		return core.Account{}, err
	}
	acct.ID = id
	return acct, nil
}

// RenameParams describes a rename or move.
type RenameParams struct {
	New   string // full path for accounts; path without the root for categories
	Old   string
	Scope Scope
}

// RenameAccount renames or moves an account or category. History follows, because
// transactions refer to the account's ID.
func (a *App) RenameAccount(ctx context.Context, p RenameParams) (string, error) {
	var newPath string
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		old, err := tree.Resolve(p.Old, p.Scope.filter())
		if err != nil {
			return err
		}
		target := strings.TrimSpace(p.New)
		if p.Scope == ScopeCategory {
			target = string(old.Type) + ":" + strings.TrimPrefix(target, string(old.Type)+":")
		}
		parts, err := core.SplitPath(target)
		if err != nil {
			return err
		}
		if core.AccountType(strings.ToLower(parts[0])) != old.Type || len(parts) < 2 {
			return fmt.Errorf("%s must stay under %s", tree.Path(old.ID), old.Type)
		}
		if existing, ok := tree.Find(target); ok && existing.ID != old.ID {
			return fmt.Errorf("%s already exists. To combine them, use merge", tree.Path(existing.ID))
		}
		parent, ok := tree.Find(strings.Join(parts[:len(parts)-1], ":"))
		if !ok {
			return fmt.Errorf("%s does not exist. Create the parent first", strings.Join(parts[:len(parts)-1], ":"))
		}
		for _, id := range tree.Subtree(old.ID) {
			if id == parent.ID {
				return fmt.Errorf("cannot move %s under itself", tree.Path(old.ID))
			}
		}
		if parent.Type.IsReal() && !parent.IsRoot() && parent.Currency != "" {
			return fmt.Errorf("%s holds money, so it cannot have sub-accounts", tree.Path(parent.ID))
		}
		newPath = target
		return tx.MoveAccount(ctx, old.ID, parent.ID, parts[len(parts)-1])
	})
	return newPath, err
}

// MergeParams describes a merge.
type MergeParams struct {
	From  string
	Into  string
	Scope Scope
}

// MergeAccount moves all transactions from one account to another and removes the first.
func (a *App) MergeAccount(ctx context.Context, p MergeParams) (moved int, err error) {
	err = a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		from, err := tree.Resolve(p.From, p.Scope.filter())
		if err != nil {
			return err
		}
		into, err := tree.Resolve(p.Into, p.Scope.filter())
		if err != nil {
			return err
		}
		fromPath, intoPath := tree.Path(from.ID), tree.Path(into.ID)
		switch {
		case from.ID == into.ID:
			return fmt.Errorf("cannot merge %s into itself", fromPath)
		case from.Type != into.Type:
			return fmt.Errorf("%s and %s are different types (%s, %s)", fromPath, intoPath, from.Type, into.Type)
		case len(tree.Children(from.ID)) > 0:
			return fmt.Errorf("%s has sub-accounts; merge or move them first", fromPath)
		case from.Type.IsReal() && from.Currency != into.Currency:
			return fmt.Errorf("%s is in %s and %s is in %s; accounts in different currencies cannot be merged",
				fromPath, from.Currency, intoPath, into.Currency)
		}
		moved, err = tx.CountPostings(ctx, from.ID)
		if err != nil {
			return err
		}
		if moved > 0 && into.IsGroup() {
			return fmt.Errorf("%s is a group and cannot receive transactions", intoPath)
		}
		if into.IsClosed() {
			last, err := tx.LastPostingDate(ctx, from.ID)
			if err != nil {
				return err
			}
			if last.After(into.ClosedOn) {
				return fmt.Errorf("%s is closed since %s, but %s has transactions on %s", intoPath, into.ClosedOn, fromPath, last)
			}
		}
		if err := tx.MovePostings(ctx, from.ID, into.ID); err != nil {
			return err
		}
		return tx.DeleteAccount(ctx, from.ID)
	})
	return moved, err
}

// CloseParams describes closing an account.
type CloseParams struct {
	Date  core.Date
	Name  string
	Scope Scope
}

// CloseAccount closes an account and its sub-accounts on a date. Real accounts must
// have a zero balance on that date, and no account may have later transactions.
func (a *App) CloseAccount(ctx context.Context, p CloseParams) error {
	date := p.Date
	if date.IsZero() {
		date = a.today()
	}
	return a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		acct, err := tree.Resolve(p.Name, p.Scope.filter())
		if err != nil {
			return err
		}
		balances, err := tx.Balances(ctx, date)
		if err != nil {
			return err
		}
		for _, id := range tree.Subtree(acct.ID) {
			sub, _ := tree.Get(id)
			if sub.IsClosed() {
				continue
			}
			last, err := tx.LastPostingDate(ctx, id)
			if err != nil {
				return err
			}
			if last.After(date) {
				return fmt.Errorf("%s has transactions after %s (last on %s)", tree.Path(id), date, last)
			}
			if sub.Type.IsReal() && !balances[id].IsZero() {
				return fmt.Errorf("%s has a balance of %s on %s; move it to another account first",
					tree.Path(id), balances[id], date)
			}
			if err := tx.SetAccountClosed(ctx, id, date); err != nil {
				return err
			}
		}
		return nil
	})
}
