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
	Currency    string // required; it cannot change later
	Kind        string // with a plain name: the kind of account, for example bank or card
	Name        string // a name ("Checking"), or a full path ("assets:bank:checking")
	Opening     string // opening balance, optional
	OpeningDate core.Date
}

// AddAccount creates an asset or liability account, with an optional opening balance.
func (a *App) AddAccount(ctx context.Context, p AddAccountParams) (core.Account, error) {
	if strings.TrimSpace(p.Currency) == "" {
		return core.Account{}, fmt.Errorf("give the account's currency, for example --currency USD. It cannot change later")
	}
	cur, err := core.ParseCurrency(p.Currency)
	if err != nil {
		return core.Account{}, err
	}
	name := strings.TrimSpace(p.Name)
	var created core.Account
	err = a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		parts, err := accountPath(tree, name, p.Kind)
		if err != nil {
			return err
		}
		acct, err := insertAccount(ctx, tx, core.AccountType(parts[0]), parts, cur)
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
		if tree, err = tx.Tree(ctx); err != nil {
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

// accountPath finds where a new account goes: under its kind, or at the full path given.
func accountPath(tree *core.Tree, name, kind string) ([]string, error) {
	if strings.Contains(name, ":") {
		if kind != "" {
			return nil, fmt.Errorf("give either a name with --kind, or a full path such as assets:bank:%s, not both", name[strings.LastIndex(name, ":")+1:])
		}
		parts, err := core.SplitPath(name)
		if err != nil {
			return nil, err
		}
		parts[0] = strings.ToLower(parts[0])
		if typ := core.AccountType(parts[0]); !typ.IsReal() {
			if typ.IsCategory() {
				return nil, fmt.Errorf("%s is a category. Use: moolah category add", name)
			}
			return nil, fmt.Errorf("a full account path starts with assets: or liabilities:, for example assets:bank:checking")
		}
		return parts, nil
	}
	if err := core.ValidateName(name); err != nil {
		return nil, err
	}
	if kind == "" {
		return nil, fmt.Errorf("give the kind of account with --kind: %s. To add a kind: moolah account kind add", kindNames(tree))
	}
	k, err := findKind(tree, kind)
	if err != nil {
		return nil, err
	}
	return append(strings.Split(tree.Path(k.ID), ":"), name), nil
}

// AddCategoryParams describes a new category.
type AddCategoryParams struct {
	Income bool   // an income category instead of a spending one
	Name   string // a name ("Restaurants"), or a path without the root ("food:restaurants")
	Parent string // the category to put it in; empty for the top level
}

// AddCategory creates a spending or income category.
func (a *App) AddCategory(ctx context.Context, p AddCategoryParams) (core.Account, error) {
	typ := core.Expenses
	if p.Income {
		typ = core.Income
	}
	name := strings.TrimSpace(p.Name)
	var created core.Account
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		var parts []string
		switch {
		case strings.Contains(name, ":"):
			if p.Parent != "" {
				return fmt.Errorf("give either a name with --in, or a path such as food:%s, not both", name[strings.LastIndex(name, ":")+1:])
			}
			if parts, err = core.SplitPath(string(typ) + ":" + strings.TrimPrefix(name, string(typ)+":")); err != nil {
				return err
			}
		default:
			if err := core.ValidateName(name); err != nil {
				return err
			}
			parent, ok := tree.Root(typ)
			if !ok {
				return fmt.Errorf("%s is missing from this book", typ)
			}
			if p.Parent != "" {
				if parent, err = tree.Resolve(p.Parent, func(c core.Account) bool { return c.Type == typ && !c.IsRoot() }); err != nil {
					return err
				}
			}
			parts = append(strings.Split(tree.Path(parent.ID), ":"), name)
		}
		created, err = insertAccount(ctx, tx, typ, parts, "")
		return err
	})
	return created, err
}

// Kind is a kind of account, such as bank or card: a group directly under assets
// or liabilities. Accounts are added to a kind.
type Kind struct {
	Accounts  int // accounts in it, open or closed
	ID        int64
	Liability bool
	Name      string
}

// kinds lists the kinds in display order.
func kinds(tree *core.Tree) []Kind {
	var out []Kind
	for _, typ := range []core.AccountType{core.Assets, core.Liabilities} {
		root, ok := tree.Root(typ)
		if !ok {
			continue
		}
		for _, c := range tree.Children(root.ID) {
			if c.IsGroup() {
				out = append(out, Kind{Accounts: len(tree.Subtree(c.ID)) - 1, ID: c.ID, Liability: typ == core.Liabilities, Name: c.Name})
			}
		}
	}
	return out
}

func kindNames(tree *core.Tree) string {
	var names []string
	for _, k := range kinds(tree) {
		names = append(names, k.Name)
	}
	return strings.Join(names, ", ")
}

func findKind(tree *core.Tree, name string) (Kind, error) {
	for _, k := range kinds(tree) {
		if strings.EqualFold(k.Name, strings.TrimSpace(name)) {
			return k, nil
		}
	}
	return Kind{}, fmt.Errorf("unknown kind %q. The kinds are: %s. To add a kind: moolah account kind add %s --asset (or --liability)",
		name, kindNames(tree), name)
}

// Kinds lists the kinds of account.
func (a *App) Kinds(ctx context.Context) ([]Kind, error) {
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return nil, err
	}
	return kinds(tree), nil
}

// AddKindParams describes a new kind of account.
type AddKindParams struct {
	Liability bool // money you owe, such as a mortgage; otherwise something you own
	Name      string
}

// AddKind adds a kind of account.
func (a *App) AddKind(ctx context.Context, p AddKindParams) error {
	name := strings.TrimSpace(p.Name)
	if err := core.ValidateName(name); err != nil {
		return err
	}
	typ := core.Assets
	if p.Liability {
		typ = core.Liabilities
	}
	return a.st.InTx(ctx, func(tx *store.Tx) error {
		_, err := insertAccount(ctx, tx, typ, []string{string(typ), name}, "")
		return err
	})
}

// RenameKind renames a kind of account.
func (a *App) RenameKind(ctx context.Context, old, name string) error {
	name = strings.TrimSpace(name)
	if err := core.ValidateName(name); err != nil {
		return err
	}
	return a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		k, err := findKind(tree, old)
		if err != nil {
			return err
		}
		acct, _ := tree.Get(k.ID)
		return moveAccount(ctx, tx, tree, acct, acct.ParentID, name)
	})
}

// RemoveKind removes a kind of account that has no accounts.
func (a *App) RemoveKind(ctx context.Context, name string) error {
	return a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		k, err := findKind(tree, name)
		if err != nil {
			return err
		}
		if k.Accounts > 0 {
			var names []string
			for _, c := range tree.Children(k.ID) {
				names = append(names, c.Name)
			}
			return fmt.Errorf("kind %s still has accounts (%s). Merge them into other accounts first", k.Name, strings.Join(names, ", "))
		}
		return tx.DeleteAccount(ctx, k.ID)
	})
}

// insertAccount adds an account under an existing parent.
func insertAccount(ctx context.Context, tx *store.Tx, typ core.AccountType, parts []string, cur core.Currency) (core.Account, error) {
	tree, err := tx.Tree(ctx)
	if err != nil {
		return core.Account{}, err
	}
	if len(parts) < 2 {
		return core.Account{}, fmt.Errorf("%s is a fixed top level; add an account below it", parts[0])
	}
	if err := uniqueName(tree, typ, parts[len(parts)-1], 0); err != nil {
		return core.Account{}, err
	}
	parentPath := strings.Join(parts[:len(parts)-1], ":")
	parent, ok := tree.Find(parentPath)
	if !ok {
		return core.Account{}, fmt.Errorf("%s does not exist", parentPath)
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

// nameGroup is the set of names that must be unique: all accounts (with kinds), or
// the categories of one type.
func nameGroup(t core.AccountType) string {
	if t.IsReal() {
		return "accounts"
	}
	return string(t)
}

// uniqueName checks that no other account in the same name group (see the
// accounts_name index) has this name. except is the account being renamed.
func uniqueName(tree *core.Tree, typ core.AccountType, name string, except int64) error {
	var dup *core.Account
	tree.Walk(func(a core.Account) {
		if dup == nil && a.ID != except && !a.IsRoot() && nameGroup(a.Type) == nameGroup(typ) && strings.EqualFold(a.Name, name) {
			dup = &a
		}
	})
	if dup == nil {
		return nil
	}
	what := "an account"
	switch {
	case dup.Type.IsReal() && dup.IsGroup() && tree.Depth(dup.ID) == 1:
		what = "a kind of account"
	case dup.Type.IsReal() && dup.IsGroup():
		what = "a group of accounts"
	case dup.Type == core.Expenses:
		what = "a spending category"
	case dup.Type == core.Income:
		what = "an income category"
	}
	return fmt.Errorf("%s is already the name of %s (%s). Names must be unique, so choose another name", name, what, tree.Path(dup.ID))
}

// RenameParams describes a rename or move.
type RenameParams struct {
	New   string // a new name; or a full path for accounts / a path without the root for categories, to move it
	Old   string
	Scope Scope
}

// RenameAccount renames or moves an account or category. History follows, because
// transactions refer to the account's ID. It returns the new name.
func (a *App) RenameAccount(ctx context.Context, p RenameParams) (string, error) {
	var newName string
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
		if !strings.Contains(target, ":") {
			newName = target
			if err := core.ValidateName(target); err != nil {
				return err
			}
			return moveAccount(ctx, tx, tree, old, old.ParentID, target)
		}
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
		parent, ok := tree.Find(strings.Join(parts[:len(parts)-1], ":"))
		if !ok {
			return fmt.Errorf("%s does not exist", strings.Join(parts[:len(parts)-1], ":"))
		}
		newName = parts[len(parts)-1]
		return moveAccount(ctx, tx, tree, old, parent.ID, newName)
	})
	return newName, err
}

// moveAccount gives an account a new parent and name, after checking that the
// result is a valid tree.
func moveAccount(ctx context.Context, tx *store.Tx, tree *core.Tree, acct core.Account, parentID int64, name string) error {
	parent, ok := tree.Get(parentID)
	if !ok {
		return fmt.Errorf("account #%d does not exist", parentID)
	}
	if err := uniqueName(tree, acct.Type, name, acct.ID); err != nil {
		return err
	}
	for _, id := range tree.Subtree(acct.ID) {
		if id == parent.ID {
			return fmt.Errorf("cannot move %s under itself", tree.Path(acct.ID))
		}
	}
	if parent.Type.IsReal() && !parent.IsRoot() && parent.Currency != "" {
		return fmt.Errorf("%s holds money, so it cannot have sub-accounts", tree.Path(parent.ID))
	}
	return tx.MoveAccount(ctx, acct.ID, parent.ID, name)
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
