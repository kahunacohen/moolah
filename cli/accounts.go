package cli

import (
	"errors"
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
				out = append(out, tree.Name(acct.ID))
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
				name := tree.Path(c.ID)
				if g.format == "table" {
					name = strings.Repeat("  ", depth) + c.Name
				}
				t.add(name, string(c.Currency), c.ClosedOn.String())
				js = append(js, accountJSON{Account: tree.Path(c.ID), ClosedOn: c.ClosedOn.String(),
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

	var currency, kind, opening, date string
	add := &cobra.Command{
		Use:   "add <name> --kind <kind> --currency <code>",
		Short: "Add an account, for example: moolah account add Checking --kind bank --currency EUR",
		Long: "add creates an account. Its name must be new: names are unique, so the name alone\n" +
			"is enough in every other command. The kind (bank, cash, card, …) places it in the\n" +
			"account list; see: moolah account kind list. The currency cannot change later.",
		Example: "  moolah account add Checking --kind bank --currency EUR --opening 2500\n" +
			"  moolah account add \"Visa Gold\" --kind card --currency USD",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseDateFlag(date)
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				ctx := cmd.Context()
				if kind == "" && !strings.Contains(args[0], ":") && g.env.Interactive {
					kinds, err := a.Kinds(ctx)
					if err != nil {
						return err
					}
					var names []string
					for _, k := range kinds {
						names = append(names, k.Name)
					}
					if kind, err = g.in.ask(fmt.Sprintf("Kind of account (%s)", strings.Join(names, ", ")), ""); err != nil {
						return err
					}
				}
				if currency == "" && g.env.Interactive {
					base, err := a.Config(ctx, app.KeyBaseCurrency)
					if err != nil {
						return err
					}
					if currency, err = g.in.ask("Currency (cannot change later)", base); err != nil {
						return err
					}
				}
				acct, err := a.AddAccount(ctx, app.AddAccountParams{Currency: currency, Kind: kind, Name: args[0], Opening: opening, OpeningDate: d})
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Added account %s (%s).\n", acct.Name, acct.Currency)
				return nil
			})
		},
	}
	add.Flags().StringVar(&kind, "kind", "", "kind of account, for example bank, cash, card (see: moolah account kind list)")
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

	cmd.AddCommand(add, list, kindCmd(g))
	cmd.AddCommand(changeCmds(g, app.ScopeReal)...)
	return cmd
}

func kindCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "kind", Short: "Kinds of account, such as bank or card, that group your accounts"}
	var liability, asset bool
	add := &cobra.Command{
		Use: "add <name> --asset|--liability", Short: "Add a kind of account", Args: cobra.ExactArgs(1),
		Example: "  moolah account kind add crypto --asset\n  moolah account kind add mortgage --liability",
		RunE: func(cmd *cobra.Command, args []string) error {
			if asset == liability {
				return errors.New("say whether it is something you own (--asset) or money you owe (--liability)")
			}
			return g.withBook(func(a *app.App) error {
				if err := a.AddKind(cmd.Context(), app.AddKindParams{Liability: liability, Name: args[0]}); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Added kind %s. Add accounts to it with: moolah account add <name> --kind %s --currency <code>\n", args[0], args[0])
				return nil
			})
		},
	}
	add.Flags().BoolVar(&asset, "asset", false, "something you own: money, investments, property")
	add.Flags().BoolVar(&liability, "liability", false, "money you owe: cards, loans, a mortgage")
	list := &cobra.Command{
		Use: "list", Short: "List the kinds of account", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				kinds, err := a.Kinds(cmd.Context())
				if err != nil {
					return err
				}
				t := &table{headers: []string{"Kind", "Type", "Accounts"}, right: map[int]bool{2: true}}
				type kindJSON struct {
					Accounts int    `json:"accounts"`
					Kind     string `json:"kind"`
					Type     string `json:"type"`
				}
				var js []kindJSON
				for _, k := range kinds {
					typ := "asset"
					if k.Liability {
						typ = "liability"
					}
					t.add(k.Name, typ, fmt.Sprint(k.Accounts))
					js = append(js, kindJSON{Accounts: k.Accounts, Kind: k.Name, Type: typ})
				}
				return g.render(t, js)
			})
		},
	}
	rename := &cobra.Command{
		Use: "rename <old> <new>", Short: "Rename a kind of account", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				if err := a.RenameKind(cmd.Context(), args[0], args[1]); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Renamed kind %s to %s.\n", args[0], args[1])
				return nil
			})
		},
	}
	remove := &cobra.Command{
		Use: "remove <name>", Short: "Remove a kind of account that has no accounts", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				if err := a.RemoveKind(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Removed kind %s.\n", args[0])
				return nil
			})
		},
	}
	cmd.AddCommand(add, list, rename, remove)
	return cmd
}

func categoryCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "category", Short: "Manage spending and income categories"}

	var income bool
	var in string
	add := &cobra.Command{
		Use: "add <name> [--in <category>]", Short: "Add a category, for example: moolah category add Restaurants --in food",
		Long: "add creates a spending category (or an income category with --income). Its name must\n" +
			"be new among the spending (or income) categories, so the name alone is enough elsewhere.\n" +
			"With --in, it is a sub-category: its spending also counts for the category it is in.",
		Example: "  moolah category add Pets\n  moolah category add Restaurants --in food\n  moolah category add Bonus --in salary --income",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				acct, err := a.AddCategory(cmd.Context(), app.AddCategoryParams{Income: income, Name: args[0], Parent: in})
				if err != nil {
					return err
				}
				what := "category"
				if income {
					what = "income category"
				}
				fmt.Fprintf(g.out(), "Added %s %s.\n", what, acct.Name)
				return nil
			})
		},
	}
	add.Flags().BoolVar(&income, "income", false, "an income category (default: spending)")
	add.Flags().StringVar(&in, "in", "", "the category to put it in (default: the top level)")

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
	noun := "an account"
	if scope == app.ScopeCategory {
		noun = "a category"
	}
	complete := g.completeAccounts(scope)

	rename := &cobra.Command{
		Use: "rename <old> <new>", Short: "Rename " + noun + " (history follows); a path such as food:snacks also moves it", Args: cobra.ExactArgs(2),
		ValidArgsFunction: complete,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				newName, err := a.RenameAccount(cmd.Context(), app.RenameParams{Old: args[0], New: args[1], Scope: scope})
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Renamed %s to %s.\n", args[0], newName)
				return nil
			})
		},
	}

	merge := &cobra.Command{
		Use: "merge <from> <into>", Short: "Move all transactions of " + noun + " into another, then remove it", Args: cobra.ExactArgs(2),
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
		Use: "close <name>", Short: "Close " + noun + ": history stays, no new transactions after the date", Args: cobra.ExactArgs(1),
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
