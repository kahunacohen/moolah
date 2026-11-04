package app

import (
	"context"
	"strings"

	"moolah/core"
	"moolah/store"
)

// starterAccounts is the tree that "moolah init" creates. Users change it freely.
var starterAccounts = []string{
	"assets", "assets:bank", "assets:cash", "assets:invest",
	"liabilities", "liabilities:card", "liabilities:loan",
	"income", "income:salary", "income:salary:pension", "income:interest", "income:gifts",
	"income:invest", "income:invest:gains", "income:other",
	"expenses", "expenses:food", "expenses:food:groceries", "expenses:food:restaurants",
	"expenses:housing", "expenses:housing:rent", "expenses:utilities",
	"expenses:transport", "expenses:transport:fuel", "expenses:transport:public",
	"expenses:health", "expenses:household", "expenses:shopping", "expenses:subscriptions",
	"expenses:education", "expenses:travel", "expenses:travel:flights", "expenses:gifts",
	"expenses:fees", "expenses:taxes", "expenses:uncategorized",
	"equity", "equity:opening",
}

// Well-known accounts that moolah itself books to.
const (
	accountOpening       = "equity:opening"
	accountGains         = "income:invest:gains"
	accountFees          = "expenses:fees"
	accountUncategorized = "expenses:uncategorized"
)

func seedAccounts(ctx context.Context, tx *store.Tx) error {
	ids := map[string]int64{}
	for _, path := range starterAccounts {
		parent, name := "", path
		if i := strings.LastIndex(path, ":"); i >= 0 {
			parent, name = path[:i], path[i+1:]
		}
		typ := core.AccountType(strings.SplitN(path, ":", 2)[0])
		id, err := tx.InsertAccount(ctx, core.Account{Name: name, ParentID: ids[parent], Type: typ})
		if err != nil {
			return err
		}
		ids[path] = id
	}
	return nil
}
