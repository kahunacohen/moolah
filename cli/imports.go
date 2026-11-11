package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"moolah/app"
)

func init() {
	extraCommands = append(extraCommands, importCmd, profileCmd, ruleCmd, reviewCmd, reconcileCmd)
}

func importCmd(g *globals) *cobra.Command {
	var account, profile string
	run := func(dry bool) func(cmd *cobra.Command, args []string) error {
		return func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				res, err := a.Import(cmd.Context(), app.ImportParams{Account: account, DryRun: dry, File: args[0], Profile: profile})
				if err != nil {
					return err
				}
				if dry {
					t := &table{headers: []string{"Line", "Date", "Payee", "Amount", "Category", "Status"}, right: map[int]bool{0: true, 3: true}}
					for _, r := range res.Rows {
						t.add(strconv.Itoa(r.Line), r.Date.String(), r.Payee, r.Amount.String(), r.Category, r.Status)
					}
					fmt.Fprintf(g.out(), "Profile %s, account %s. Nothing was saved (test only).\n\n", res.Profile, res.Account)
					if err := g.render(t, res.Rows); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(g.out(), "Imported %s into %s with profile %s: %d new", args[0], res.Account, res.Profile, res.New)
					if res.Duplicates > 0 {
						fmt.Fprintf(g.out(), ", %d already imported", res.Duplicates)
					}
					if res.Matched > 0 {
						fmt.Fprintf(g.out(), ", %d linked to transfers already recorded", res.Matched)
					}
					if res.Paired > 0 {
						fmt.Fprintf(g.out(), ", %s completed with the other account's export", count(res.Paired, "transfer", "transfers"))
					}
					fmt.Fprintln(g.out(), ".")
					if res.Waiting > 0 {
						fmt.Fprintf(g.out(), "%s to an account in another currency %s for the other account's export, which has the other amount.\n"+
							"Import it, or give the amount with: moolah review\n", count(res.Waiting, "transfer", "transfers"), plural(res.Waiting, "waits", "wait"))
					}
					if res.Batch != 0 {
						fmt.Fprintf(g.out(), "Import ID %d (undo with: moolah import undo %d).\n", res.Batch, res.Batch)
					}
					if res.Uncategorized > 0 {
						fmt.Fprintf(g.out(), "%s no category yet. Give them one with: moolah review\n", count(res.Uncategorized, "row has", "rows have"))
					}
				}
				for _, e := range res.Errors {
					fmt.Fprintln(g.env.Stderr, "warning: skipped "+e)
				}
				return nil
			})
		}
	}
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import a bank or card export (CSV, XLSX, or HTML saved as .xls)",
		Example: "  moolah import ~/Downloads/max-2026-10.xlsx --account card:max\n" +
			"  moolah import test ~/Downloads/export.xls --profile my-bank",
		Args: cobra.ExactArgs(1),
		RunE: run(false),
	}
	cmd.PersistentFlags().StringVar(&account, "account", "", "the account the file belongs to (remembered after the first import)")
	cmd.PersistentFlags().StringVar(&profile, "profile", "", "the file format (default: detected)")
	test := &cobra.Command{Use: "test <file>", Short: "Show how a file would be imported, without saving anything", Args: cobra.ExactArgs(1), RunE: run(true)}

	list := &cobra.Command{
		Use: "list", Short: "List earlier imports", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				batches, tree, err := a.Imports(cmd.Context())
				if err != nil {
					return err
				}
				t := &table{headers: []string{"ID", "When", "File", "Account", "Profile", "Rows", "Created"}, right: map[int]bool{0: true, 5: true, 6: true}}
				for _, b := range batches {
					t.add(strconv.FormatInt(b.ID, 10), b.Created, b.File, tree.Path(b.AccountID), b.Profile,
						strconv.Itoa(b.Rows), strconv.Itoa(b.Transactions))
				}
				return g.render(t, batches)
			})
		},
	}
	undo := &cobra.Command{
		Use: "undo <id>", Short: "Remove everything an import created", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid import ID %q", args[0])
			}
			return g.withBook(func(a *app.App) error {
				n, err := a.UndoImport(cmd.Context(), id)
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Removed import %d (%s).\n", id, count(n, "transaction", "transactions"))
				return nil
			})
		},
	}
	cmd.AddCommand(test, list, undo)
	return cmd
}

func profileCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "profile", Short: "Import profiles: TOML files that describe a bank's export format"}
	list := &cobra.Command{
		Use: "list", Short: "List built-in and your own profiles", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				ps, err := a.Profiles(cmd.Context())
				if err != nil {
					return err
				}
				t := &table{headers: []string{"Name", "Source", "Description"}}
				for _, p := range ps {
					src := "built-in"
					if p.User && p.Builtin {
						src = "yours (replaces built-in)"
					} else if p.User {
						src = "yours"
					}
					t.add(p.Name, src, p.Description)
				}
				return g.render(t, ps)
			})
		},
	}
	show := &cobra.Command{
		Use: "show <name>", Short: "Print a profile (a good start for your own)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				text, err := a.ProfileText(cmd.Context(), args[0])
				if err == nil {
					_, err = io.WriteString(g.out(), text)
				}
				return err
			})
		},
	}
	export := &cobra.Command{Use: "export <name>", Short: "Print a profile to share it (same as show)", Args: cobra.ExactArgs(1), RunE: show.RunE}
	add := &cobra.Command{
		Use: "add <file.toml>", Short: "Check a profile and save it in the book", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				name, err := a.SaveProfile(cmd.Context(), string(b))
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Saved profile %s. Test it with: moolah import test <file> --profile %s\n", name, name)
				return nil
			})
		},
	}
	edit := &cobra.Command{
		Use: "edit <name>", Short: "Edit a profile in your $EDITOR (checked when you save)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.env.RunEditor == nil {
				return errors.New("no editor available")
			}
			return g.withBook(func(a *app.App) error {
				text, err := a.ProfileText(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				f, err := os.CreateTemp("", "moolah-profile-*.toml")
				if err != nil {
					return err
				}
				defer os.Remove(f.Name())
				for {
					if err := os.WriteFile(f.Name(), []byte(text), 0o600); err != nil {
						return err
					}
					if err := g.env.RunEditor(f.Name()); err != nil {
						return fmt.Errorf("editor: %w", err)
					}
					b, err := os.ReadFile(f.Name())
					if err != nil {
						return err
					}
					text = string(b)
					name, err := a.SaveProfile(cmd.Context(), text)
					if err == nil {
						fmt.Fprintf(g.out(), "Saved profile %s.\n", name)
						return nil
					}
					fmt.Fprintln(g.env.Stderr, "error: "+err.Error())
					again, cerr := g.in.confirm("Edit again?")
					if cerr != nil {
						return cerr
					}
					if !again {
						return errors.New("not saved")
					}
				}
			})
		},
	}
	remove := &cobra.Command{
		Use: "remove <name>", Short: "Remove one of your profiles", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				if err := a.RemoveProfile(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Removed profile %s.\n", args[0])
				return nil
			})
		},
	}
	cmd.AddCommand(list, show, export, add, edit, remove)
	return cmd
}

func ruleCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "rule", Short: "Rules choose a category for imported rows from the payee"}
	var p app.RuleParams
	add := &cobra.Command{
		Use:     "add <pattern> <category>",
		Short:   "Add a rule (a regular expression, not case-sensitive)",
		Example: "  moolah rule add 'supermarket|aldi|lidl' food:groceries\n  moolah rule add 'visa payment' liabilities:card:visa    # card payment = transfer",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p.Pattern, p.Account = args[0], args[1]
			return g.withBook(func(a *app.App) error {
				id, n, err := a.AddRule(cmd.Context(), p)
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Added rule %d.", id)
				if n > 0 {
					fmt.Fprintf(g.out(), " It categorized %s that had no category.", count(n, "transaction", "transactions"))
				}
				fmt.Fprintln(g.out())
				return nil
			})
		},
	}
	add.Flags().StringVar(&p.MinAmount, "min-amount", "", "only when the amount (without sign) is at least this")
	add.Flags().StringVar(&p.MaxAmount, "max-amount", "", "only when the amount (without sign) is at most this")
	add.Flags().StringArrayVar(&p.Tags, "tag", nil, "also add this tag; repeat for more")
	list := &cobra.Command{
		Use: "list", Short: "List rules in the order they are checked", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				rules, err := a.Rules(cmd.Context())
				if err != nil {
					return err
				}
				t := &table{headers: []string{"ID", "Pattern", "Category", "Amount", "Tags"}, right: map[int]bool{0: true}}
				for _, r := range rules {
					limits := ""
					if r.MinAmount != "" || r.MaxAmount != "" {
						limits = r.MinAmount + "…" + r.MaxAmount
					}
					t.add(strconv.FormatInt(r.ID, 10), r.Pattern, r.Account, limits, strings.Join(r.Tags, ", "))
				}
				return g.render(t, rules)
			})
		},
	}
	remove := &cobra.Command{
		Use: "remove <id>", Short: "Remove a rule", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid rule ID %q", args[0])
			}
			return g.withBook(func(a *app.App) error {
				if err := a.RemoveRule(cmd.Context(), id); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Removed rule %d.\n", id)
				return nil
			})
		},
	}
	cmd.AddCommand(add, list, remove)
	return cmd
}

func reviewCmd(g *globals) *cobra.Command {
	var listOnly bool
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Give a category to imported transactions that have none",
		Long: "Shows each uncategorized transaction and asks for a category. You can also save your\n" +
			"choice as a rule. Without a terminal (or with --list) it only lists them.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				items, err := a.Review(cmd.Context())
				if err != nil {
					return err
				}
				waiting, err := a.WaitingTransfers(cmd.Context())
				if err != nil {
					return err
				}
				if len(items) == 0 && len(waiting) == 0 {
					fmt.Fprintln(g.out(), "Nothing to review: every transaction has a category.")
					return nil
				}
				if listOnly || !g.env.Interactive {
					return g.reviewList(items, waiting)
				}
				if len(items) > 0 {
					quit, err := g.reviewLoop(cmd, a, items)
					if err != nil || quit {
						return err
					}
				}
				return g.waitingLoop(cmd, a, waiting)
			})
		},
	}
	cmd.Flags().BoolVar(&listOnly, "list", false, "only list, do not ask")
	set := &cobra.Command{
		Use: "set <transaction-id> <category>", Short: "Set the category of one uncategorized transaction", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid transaction ID %q", args[0])
			}
			return g.withBook(func(a *app.App) error {
				name, err := a.Categorize(cmd.Context(), app.CategorizeParams{TxID: id, Category: args[1]})
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Transaction %d → %s.\n", id, name)
				return nil
			})
		},
	}
	transfer := &cobra.Command{
		Use:     "transfer <id> <amount>",
		Short:   "Complete a transfer between currencies with the amount on the other account",
		Example: "  moolah review transfer 57 1200    # 1,200 arrived on the other account",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid ID %q; see: moolah review --list", args[0])
			}
			return g.withBook(func(a *app.App) error {
				t, err := a.CompleteTransfer(cmd.Context(), app.CompleteTransferParams{ID: id, Amount: args[1]})
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Recorded transaction %d.\n", t.ID)
				return nil
			})
		},
	}
	cmd.AddCommand(set, transfer)
	return cmd
}

func (g *globals) reviewList(items []app.ReviewItem, waiting []app.WaitingTransfer) error {
	if len(items) > 0 {
		t := &table{headers: []string{"ID", "Date", "Payee", "Account", "Amount"}, right: map[int]bool{0: true, 4: true}}
		for _, it := range items {
			t.add(strconv.FormatInt(it.Tx.ID, 10), it.Tx.Date.String(), it.Tx.Payee, it.Account, it.Amount.String())
		}
		if g.format == "table" {
			fmt.Fprintf(g.out(), "%d transactions have no category. Set one with: moolah review set <id> <category>\n\n", len(items))
		}
		if err := g.render(t, items); err != nil {
			return err
		}
	}
	if len(waiting) == 0 {
		return nil
	}
	if len(items) > 0 && g.format == "table" {
		fmt.Fprintln(g.out())
	}
	t := &table{headers: []string{"ID", "Date", "Payee", "Account", "Amount", "To", "Probably"}, right: map[int]bool{0: true, 4: true, 6: true}}
	for _, w := range waiting {
		probably := ""
		if w.Suggested != nil {
			probably = w.Suggested.String()
		}
		t.add(strconv.FormatInt(w.ID, 10), w.Date.String(), w.Payee, w.Account, w.Amount.String(), w.Target, probably)
	}
	if g.format == "table" {
		fmt.Fprintf(g.out(), "%s between currencies %s for the other amount. Import the other account's file,\n"+
			"or give the amount with: moolah review transfer <id> <amount>\n\n", count(len(waiting), "transfer", "transfers"), plural(len(waiting), "waits", "wait"))
	}
	return g.render(t, waiting)
}

// reviewLoop asks for a category for each item. It reports whether the user quit.
func (g *globals) reviewLoop(cmd *cobra.Command, a *app.App, items []app.ReviewItem) (bool, error) {
	ctx := cmd.Context()
	skipped := map[int64]bool{}
	done := 0
	for {
		var next *app.ReviewItem
		left := 0
		for i := range items {
			if !skipped[items[i].Tx.ID] {
				if next == nil {
					next = &items[i]
				}
				left++
			}
		}
		if next == nil {
			fmt.Fprintf(g.out(), "Done: %d categorized.\n", done)
			return false, nil
		}
		fmt.Fprintf(g.out(), "\n%s  %s  %s  (%s), %d left\n", next.Tx.Date, next.Tx.Payee, next.Amount, next.Account, left)
		if inst := app.FormatInstallment(next.Tx.Meta); inst != "" {
			fmt.Fprintln(g.out(), "  "+inst)
		}
		ans, err := g.in.ask("Category (Enter = skip, q = quit)", "")
		if err != nil {
			return false, err
		}
		switch ans {
		case "":
			skipped[next.Tx.ID] = true
			continue
		case "q":
			fmt.Fprintf(g.out(), "Stopped: %d categorized. Run moolah review again to continue.\n", done)
			return true, nil
		}
		name, err := a.Categorize(ctx, app.CategorizeParams{TxID: next.Tx.ID, Version: next.Tx.Version, Category: ans})
		if err != nil {
			fmt.Fprintln(g.env.Stderr, "error: "+err.Error())
			continue
		}
		done++
		fmt.Fprintf(g.out(), "  → %s\n", name)
		rule, err := g.in.confirmDefault("  Always use "+name+" for this payee (add a rule)?", false)
		if err != nil {
			return false, err
		}
		if rule {
			pattern, err := g.in.ask("  Pattern", app.SuggestPattern(next.Tx.Payee))
			if err != nil {
				return false, err
			}
			_, n, err := a.AddRule(ctx, app.RuleParams{Pattern: pattern, Account: name})
			if err != nil {
				fmt.Fprintln(g.env.Stderr, "error: "+err.Error())
			} else if n > 0 {
				fmt.Fprintf(g.out(), "  The rule also categorized %d more.\n", n)
				done += n
			}
		}
		if items, err = a.Review(ctx); err != nil {
			return false, err
		}
	}
}

// waitingLoop asks for the other amount of each transfer between currencies.
func (g *globals) waitingLoop(cmd *cobra.Command, a *app.App, waiting []app.WaitingTransfer) error {
	ctx := cmd.Context()
	skipped := map[int64]bool{}
	done := 0
	for {
		var next *app.WaitingTransfer
		for i := range waiting {
			if !skipped[waiting[i].ID] {
				next = &waiting[i]
				break
			}
		}
		if next == nil {
			if done > 0 || len(skipped) > 0 {
				fmt.Fprintf(g.out(), "Done: %s recorded.\n", count(done, "transfer", "transfers"))
			}
			return nil
		}
		fmt.Fprintf(g.out(), "\n%s  %s  %s  (%s → %s), waits for the amount on %s\n",
			next.Date, next.Payee, next.Amount, next.Account, next.Target, next.Target)
		if next.Suggested != nil {
			fmt.Fprintf(g.out(), "  Probably the other side: %s, imported from %s\n", next.Suggested, next.Target)
		}
		ans, err := g.in.ask(fmt.Sprintf("Amount in %s (Enter = keep waiting, q = quit)", next.TargetCur), "")
		if err != nil {
			return err
		}
		switch ans {
		case "":
			skipped[next.ID] = true
			continue
		case "q":
			return nil
		}
		t, err := a.CompleteTransfer(ctx, app.CompleteTransferParams{ID: next.ID, Amount: ans})
		if err != nil {
			fmt.Fprintln(g.env.Stderr, "error: "+err.Error())
			continue
		}
		done++
		fmt.Fprintf(g.out(), "  → transaction %d\n", t.ID)
		if waiting, err = a.WaitingTransfers(ctx); err != nil {
			return err
		}
	}
}

func reconcileCmd(g *globals) *cobra.Command {
	var balance, date string
	cmd := &cobra.Command{
		Use:     "reconcile <account> --balance <amount>",
		Short:   "Compare an account with the balance on your statement",
		Example: "  moolah reconcile card:max --balance -4210.50 --date 2026-10-31",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if balance == "" {
				return errors.New("give the statement balance with --balance")
			}
			d, err := parseDateFlag(date)
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				res, err := a.Reconcile(cmd.Context(), app.ReconcileParams{Account: args[0], Balance: balance, Date: d})
				if err != nil {
					return err
				}
				if res.Difference.Value.IsZero() {
					fmt.Fprintf(g.out(), "OK: moolah and the statement both say %s.\n", res.Book)
					return nil
				}
				fmt.Fprintf(g.out(), "Different: the statement says %s, moolah says %s (difference %s).\n", res.Statement, res.Book, res.Difference)
				if len(res.Waiting) > 0 {
					fmt.Fprintln(g.out(), "\nImported transfers that are not recorded yet (complete them with: moolah review):")
					for _, w := range res.Waiting {
						fmt.Fprintf(g.out(), "  %s %s %s → %s\n", w.Date, w.Payee, w.Amount, w.Target)
					}
				}
				if len(res.Duplicates) > 0 {
					fmt.Fprintln(g.out(), "\nPossible duplicates:")
					for _, d := range res.Duplicates {
						fmt.Fprintf(g.out(), "  %d and %d: %s %s %s\n", d[0].ID, d[1].ID, d[0].Date, d[0].Payee, d[0].Amount)
					}
				}
				fmt.Fprintln(g.out(), "\nLatest transactions (check for missing ones):")
				t := &table{headers: []string{"ID", "Date", "Payee", "Amount", "Balance"}, right: map[int]bool{0: true, 3: true, 4: true}}
				for _, r := range res.Recent {
					t.add(strconv.FormatInt(r.ID, 10), r.Date.String(), r.Payee, r.Amount.String(), r.Running.String())
				}
				return t.print(g.out())
			})
		},
	}
	cmd.Flags().StringVar(&balance, "balance", "", "the balance on the statement (negative for money owed on a card)")
	cmd.Flags().StringVar(&date, "date", "", "the statement date (default: today)")
	return cmd
}
