package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/core"
)

// completeAccounts offers account names for shell completion.
func (g *globals) completeAccounts(scope app.Scope) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var out []string
		_ = g.withBook(func(a *app.App) error {
			tree, err := a.Tree(cmd.Context())
			if err != nil {
				return err
			}
			tree.Walk(func(acct core.Account) {
				if acct.IsRoot() {
					return
				}
				switch scope {
				case app.ScopeReal:
					if !acct.Type.IsReal() {
						return
					}
				case app.ScopeCategory:
					if !acct.Type.IsCategory() {
						return
					}
				}
				out = append(out, app.DisplayName(tree, acct.ID, scope))
			})
			return nil
		})
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

type accountJSON struct {
	Account  string `json:"account"`
	ClosedOn string `json:"closed_on,omitempty"`
	Currency string `json:"currency,omitempty"`
	Type     string `json:"type"`
}

// printTree lists accounts of some types as an indented tree.
func (g *globals) printTree(a *app.App, cmd *cobra.Command, types []core.AccountType, scope app.Scope) error {
	tree, err := a.Tree(cmd.Context())
	if err != nil {
		return err
	}
	t := &table{headers: []string{"Name", "Currency", "Closed"}}
	var js []accountJSON
	for _, typ := range types {
		root, ok := tree.Root(typ)
		if !ok {
			continue
		}
		heading := string(typ)
		if scope == app.ScopeCategory && typ == core.Expenses {
			heading = "expenses (spending)"
		}
		t.add(heading, "", "")
		var walk func(id int64, depth int)
		walk = func(id int64, depth int) {
			for _, c := range tree.Children(id) {
				name := app.DisplayName(tree, c.ID, scope)
				if g.format == "table" {
					name = strings.Repeat("  ", depth) + c.Name
				}
				t.add(name, string(c.Currency), c.ClosedOn.String())
				js = append(js, accountJSON{Account: app.DisplayName(tree, c.ID, scope), ClosedOn: c.ClosedOn.String(),
					Currency: string(c.Currency), Type: string(c.Type)})
				walk(c.ID, depth+1)
			}
		}
		walk(root.ID, 1)
	}
	return g.render(t, js)
}

func accountCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "Manage real accounts: banks, cash, cards, loans, investments"}

	var currency, opening, date string
	add := &cobra.Command{
		Use:   "add <assets:…|liabilities:…>",
		Short: "Add an account (without --currency it is a group for other accounts)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseDateFlag(date)
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				acct, err := a.AddAccount(cmd.Context(), app.AddAccountParams{Path: args[0], Currency: currency, Opening: opening, OpeningDate: d})
				if err != nil {
					return err
				}
				if acct.Currency == "" {
					fmt.Fprintf(g.out(), "Added group %s.\n", args[0])
				} else {
					fmt.Fprintf(g.out(), "Added %s (%s).\n", args[0], acct.Currency)
				}
				return nil
			})
		},
	}
	add.Flags().StringVar(&currency, "currency", "", "the account's currency, for example USD or EUR (cannot change later)")
	add.Flags().StringVar(&opening, "opening", "", "balance on the day you start using moolah for this account")
	add.Flags().StringVar(&date, "date", "", "date of the opening balance (default: today)")

	list := &cobra.Command{
		Use: "list", Short: "List accounts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				return g.printTree(a, cmd, []core.AccountType{core.Assets, core.Liabilities}, app.ScopeReal)
			})
		},
	}

	cmd.AddCommand(add, list)
	cmd.AddCommand(changeCmds(g, app.ScopeReal)...)
	return cmd
}

func categoryCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "category", Short: "Manage spending and income categories"}

	var income bool
	add := &cobra.Command{
		Use: "add <name>", Short: "Add a category, for example food:restaurants", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				acct, err := a.AddCategory(cmd.Context(), app.AddCategoryParams{Path: args[0], Income: income})
				if err != nil {
					return err
				}
				tree, err := a.Tree(cmd.Context())
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Added category %s.\n", app.DisplayName(tree, acct.ID, app.ScopeCategory))
				return nil
			})
		},
	}
	add.Flags().BoolVar(&income, "income", false, "an income category (default: spending)")

	list := &cobra.Command{
		Use: "list", Short: "List categories", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				return g.printTree(a, cmd, []core.AccountType{core.Expenses, core.Income}, app.ScopeCategory)
			})
		},
	}

	cmd.AddCommand(add, list)
	cmd.AddCommand(changeCmds(g, app.ScopeCategory)...)
	return cmd
}

// changeCmds builds rename, merge and close for accounts or categories.
func changeCmds(g *globals, scope app.Scope) []*cobra.Command {
	noun := "account"
	if scope == app.ScopeCategory {
		noun = "category"
	}
	complete := g.completeAccounts(scope)

	rename := &cobra.Command{
		Use: "rename <old> <new>", Short: "Rename or move a " + noun + " (history follows)", Args: cobra.ExactArgs(2),
		ValidArgsFunction: complete,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				newPath, err := a.RenameAccount(cmd.Context(), app.RenameParams{Old: args[0], New: args[1], Scope: scope})
				if err != nil {
					return err
				}
				if scope == app.ScopeCategory {
					newPath = strings.TrimPrefix(newPath, "expenses:")
				}
				fmt.Fprintf(g.out(), "Renamed %s to %s.\n", args[0], newPath)
				return nil
			})
		},
	}

	merge := &cobra.Command{
		Use: "merge <from> <into>", Short: "Move all transactions of a " + noun + " into another, then remove it", Args: cobra.ExactArgs(2),
		ValidArgsFunction: complete,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				n, err := a.MergeAccount(cmd.Context(), app.MergeParams{From: args[0], Into: args[1], Scope: scope})
				if err != nil {
					return err
				}
				lines := "lines"
				if n == 1 {
					lines = "line"
				}
				fmt.Fprintf(g.out(), "Merged %s into %s (%d transaction %s moved).\n", args[0], args[1], n, lines)
				return nil
			})
		},
	}

	var date string
	closeC := &cobra.Command{
		Use: "close <name>", Short: "Close a " + noun + ": history stays, no new transactions after the date", Args: cobra.ExactArgs(1),
		ValidArgsFunction: complete,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseDateFlag(date)
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				if err := a.CloseAccount(cmd.Context(), app.CloseParams{Date: d, Name: args[0], Scope: scope}); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Closed %s.\n", args[0])
				return nil
			})
		},
	}
	closeC.Flags().StringVar(&date, "date", "", "close date (default: today)")
	return []*cobra.Command{rename, merge, closeC}
}

func tagCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "tag", Short: "Tags group transactions across categories"}
	list := &cobra.Command{
		Use: "list", Short: "List tags and how many transactions use them", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				tags, err := a.Tags(cmd.Context())
				if err != nil {
					return err
				}
				t := &table{headers: []string{"Tag", "Transactions"}, right: map[int]bool{1: true}}
				for _, tg := range tags {
					t.add(tg.Name, fmt.Sprint(tg.Count))
				}
				return g.render(t, tags)
			})
		},
	}
	cmd.AddCommand(list)
	return cmd
}
