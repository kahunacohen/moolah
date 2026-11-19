package cli

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/core"
)

type infoFlags struct {
	date  string
	note  string
	payee string
	tags  []string
}

func (f *infoFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.date, "date", "", "date YYYY-MM-DD (default: today)")
	cmd.Flags().StringVar(&f.payee, "payee", "", "who you paid or who paid you")
	cmd.Flags().StringVar(&f.note, "note", "", "a free note")
	cmd.Flags().StringArrayVar(&f.tags, "tag", nil, "a tag; repeat for more")
}

func (f *infoFlags) info() (app.TxInfo, error) {
	d, err := parseDateFlag(f.date)
	if err != nil {
		return app.TxInfo{}, err
	}
	return app.TxInfo{Date: d, Note: f.note, Payee: f.payee, Tags: f.tags}, nil
}

func (g *globals) printSaved(a *app.App, cmd *cobra.Command, verb string, t core.Transaction) error {
	tree, err := a.Tree(cmd.Context())
	if err != nil {
		return err
	}
	fmt.Fprintf(g.out(), "%s transaction %d on %s", verb, t.ID, t.Date)
	if t.Payee != "" {
		fmt.Fprintf(g.out(), " (%s)", t.Payee)
	}
	fmt.Fprintln(g.out(), ":")
	for _, p := range t.Postings {
		line := fmt.Sprintf("  %-30s %s", tree.Name(p.AccountID), p.Amount)
		if p.Cost != nil {
			line += " (for " + p.Cost.String() + ")"
		}
		fmt.Fprintln(g.out(), line)
	}
	return nil
}

func addCmd(g *globals) *cobra.Command {
	var f infoFlags
	var postings []string
	cmd := &cobra.Command{
		Use:   "add <account> <amount> <category>",
		Short: "Record a transaction (negative amount = money leaves the account)",
		Long: "Record a transaction. The amount is in the account's currency; negative means money\n" +
			"leaves the account. For a transaction with more lines, use --posting account=amount (repeat).",
		Example: "  moolah add checking -200 food:groceries --payee Supermarket\n" +
			"  moolah add checking 3500 salary\n" +
			"  moolah add --payee Pharmacy --posting checking=-300 --posting health=180 --posting household=120",
		ValidArgsFunction: g.completeAccounts(app.ScopeAny),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := f.info()
			if err != nil {
				return err
			}
			if len(postings) > 0 && len(args) > 0 {
				return errors.New("use either <account> <amount> <category> or --posting, not both")
			}
			if len(postings) == 0 && len(args) != 3 {
				return errors.New("usage: moolah add <account> <amount> <category>, or --posting account=amount (at least two)")
			}
			return g.withBook(func(a *app.App) error {
				var t core.Transaction
				if len(postings) > 0 {
					t, err = a.AddPostings(cmd.Context(), app.AddPostingsParams{TxInfo: info, Postings: postings})
				} else {
					t, err = a.Add(cmd.Context(), app.AddParams{TxInfo: info, Account: args[0], Amount: args[1], Category: args[2]})
				}
				if err != nil {
					return err
				}
				return g.printSaved(a, cmd, "Added", t)
			})
		},
	}
	f.register(cmd)
	cmd.Flags().StringArrayVar(&postings, "posting", nil, "a line account=amount; repeat for each line")
	return cmd
}

func transferCmd(g *globals) *cobra.Command {
	var f infoFlags
	var toAmount, fee string
	cmd := &cobra.Command{
		Use:   "transfer <from> <to> <amount>",
		Short: "Move money between your own accounts (also pays a credit card)",
		Example: "  moolah transfer checking card:visa 3200\n" +
			"  moolah transfer checking card:chase 390 --to-amount 420 --fee 5",
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: g.completeAccounts(app.ScopeReal),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := f.info()
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				t, err := a.Transfer(cmd.Context(), app.TransferParams{TxInfo: info, From: args[0], To: args[1], Amount: args[2],
					ToAmount: toAmount, Fee: fee})
				if err != nil {
					return err
				}
				return g.printSaved(a, cmd, "Added", t)
			})
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&toAmount, "to-amount", "", "amount that arrived, in the target account's currency (needed when the currencies differ)")
	cmd.Flags().StringVar(&fee, "fee", "", "bank fee in the source account's currency (booked to fees)")
	return cmd
}

func valueCmd(g *globals) *cobra.Command {
	var date string
	cmd := &cobra.Command{
		Use:               "value <account> <value>",
		Short:             "Enter an investment account's value from a statement; the difference is booked as gain or loss",
		Example:           "  moolah value pension 412300 --date 2026-10-31",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: g.completeAccounts(app.ScopeReal),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseDateFlag(date)
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				res, err := a.Value(cmd.Context(), app.ValueParams{Account: args[0], Value: args[1], Date: d})
				if err != nil {
					return err
				}
				if res.Tx == nil {
					fmt.Fprintf(g.out(), "No change: the balance is already %s.\n", res.Before)
					return nil
				}
				kind := "gain"
				if res.Delta.Value.Sign() < 0 {
					kind = "loss"
				}
				fmt.Fprintf(g.out(), "Booked a %s of %s (balance was %s). Transaction %d.\n", kind, res.Delta, res.Before, res.Tx.ID)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&date, "date", "", "statement date (default: today)")
	return cmd
}

type balanceJSON struct {
	Account string            `json:"account"`
	Native  map[string]string `json:"native"`
	Value   string            `json:"value"`
}

func balanceCmd(g *globals) *cobra.Command {
	var date, in string
	var all bool
	cmd := &cobra.Command{
		Use:   "balance",
		Short: "Balance of every account, converted to your base currency",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseDateFlag(date)
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				rep, err := a.Balances(cmd.Context(), app.BalanceParams{Date: d, In: in})
				if err != nil {
					return err
				}
				valueHead := "Value (" + string(rep.Currency) + ")"
				t := &table{headers: []string{"Account", "Balance", valueHead}, right: map[int]bool{1: true, 2: true}}
				var js []balanceJSON
				for _, r := range rep.Rows {
					if !all && !r.HasAccounts && r.Depth > 0 {
						continue
					}
					native := ""
					if r.Currency != "" {
						native = core.NewAmount(r.Native[r.Currency], r.Currency).String()
					}
					t.add(g.treeLabel(r.Account, r.Depth), native, core.FormatNumber(r.Value, rep.Currency.Scale()))
					nj := map[string]string{}
					for _, am := range r.Native.Amounts() {
						nj[string(am.Currency)] = am.Value.String()
					}
					js = append(js, balanceJSON{Account: r.Account, Native: nj, Value: r.Value.String()})
				}
				t.add("", "", "")
				t.add("Net worth", "", core.FormatNumber(rep.NetWorth, rep.Currency.Scale()))
				if g.format == "table" {
					fmt.Fprintf(g.out(), "%sBalances on %s, in %s\n\n", g.bookHeader(), rep.Date, rep.Currency)
				}
				return g.render(t, map[string]any{"date": rep.Date, "currency": rep.Currency, "net_worth": rep.NetWorth.String(), "accounts": js})
			})
		},
	}
	cmd.Flags().StringVar(&date, "date", "", "balance at the end of this date (default: today)")
	cmd.Flags().StringVar(&in, "in", "", "report currency (default: your base currency)")
	cmd.Flags().BoolVar(&all, "all", false, "also show empty groups")
	return cmd
}

// bookHeader names the book when the user has more than one.
func (g *globals) bookHeader() string {
	books, err := g.env.Paths.Books()
	if err != nil || len(books) < 2 {
		return ""
	}
	loc, err := g.location()
	if err != nil || loc.Book == "" {
		return ""
	}
	return "Book: " + loc.Book + "\n"
}

type registerJSON struct {
	Amount  map[string]string `json:"amount"`
	Date    core.Date         `json:"date"`
	ID      int64             `json:"id"`
	Note    string            `json:"note,omitempty"`
	Other   string            `json:"other"`
	Payee   string            `json:"payee"`
	Running map[string]string `json:"balance,omitempty"`
	Tags    []string          `json:"tags,omitempty"`
}

func balanceMap(b core.Balance) map[string]string {
	if b == nil {
		return nil
	}
	m := map[string]string{}
	for _, am := range b.Amounts() {
		m[string(am.Currency)] = am.Value.String()
	}
	return m
}

func registerCmd(g *globals) *cobra.Command {
	var from, to, tag, category, search string
	cmd := &cobra.Command{
		Use:     "transactions [account]",
		Aliases: []string{"register", "transaction"},
		Short:   "List transactions, for one account with a running balance; edit or delete one",
		Example: "  moolah transactions checking\n  moolah transactions --category food --from 2026-10-01\n  moolah transactions --search amazon\n  moolah transactions edit 42",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
					return fmt.Errorf("unknown command %q. Did you mean: moolah transactions %s?", args[0], s[0])
				}
			}
			return cobra.MaximumNArgs(1)(cmd, args)
		},
		ValidArgsFunction: g.completeAccounts(app.ScopeAny),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := app.RegisterParams{Search: search, Tag: tag, Scope: app.ScopeAny}
			var err error
			if p.From, err = parseDateFlag(from); err != nil {
				return err
			}
			if p.To, err = parseDateFlag(to); err != nil {
				return err
			}
			if len(args) == 1 && category != "" {
				return errors.New("give an account or --category, not both")
			}
			if len(args) == 1 {
				p.Account = args[0]
			}
			if category != "" {
				p.Account, p.Scope = category, app.ScopeCategory
			}
			return g.withBook(func(a *app.App) error {
				rep, err := a.Register(cmd.Context(), p)
				if err != nil {
					// A mistyped subcommand ("eidt") arrives here as an account name.
					if s := cmd.SuggestionsFor(p.Account); len(args) == 1 && len(s) > 0 {
						return fmt.Errorf("%w\nDid you mean: moolah transactions %s?", err, s[0])
					}
					return err
				}
				headers := []string{"ID", "Date", "Payee", "Note", "Other side", "Amount"}
				if rep.Account != "" {
					headers = append(headers, "Balance")
				}
				t := &table{headers: headers, right: map[int]bool{0: true, 5: true, 6: true}}
				js := []registerJSON{}
				for _, r := range rep.Rows {
					payee := r.Payee
					for _, tg := range r.Tags {
						payee += " #" + tg
					}
					cells := []string{strconv.FormatInt(r.ID, 10), r.Date.String(), payee, r.Note, r.Other, r.Amount.String()}
					if rep.Account != "" {
						cells = append(cells, r.Running.String())
					}
					t.add(cells...)
					js = append(js, registerJSON{Amount: balanceMap(r.Amount), Date: r.Date, ID: r.ID, Note: r.Note,
						Other: r.Other, Payee: r.Payee, Running: balanceMap(r.Running), Tags: r.Tags})
				}
				if g.format == "table" {
					if h := g.bookHeader(); h != "" {
						fmt.Fprint(g.out(), h+"\n")
					}
					if len(rep.Rows) == 0 {
						fmt.Fprintln(g.out(), "No transactions.")
						return nil
					}
				}
				return g.render(t, js)
			})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "first date")
	cmd.Flags().StringVar(&to, "to", "", "last date")
	cmd.Flags().StringVar(&tag, "tag", "", "only transactions with this tag")
	cmd.Flags().StringVar(&category, "category", "", "a category instead of an account")
	cmd.Flags().StringVar(&search, "search", "", "only transactions whose payee or note contains this text (any case), or with this amount")
	cmd.SuggestionsMinimumDistance = 2
	cmd.AddCommand(editCmd(g), deleteCmd(g))
	return cmd
}

func deleteCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "delete <transaction-id>", Short: "Delete a transaction", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid transaction ID %q", args[0])
			}
			return g.withBook(func(a *app.App) error {
				t, err := a.Transaction(cmd.Context(), id)
				if err != nil {
					return err
				}
				if !yes {
					if err := g.printSaved(a, cmd, "Delete", t); err != nil {
						return err
					}
					ok, err := g.in.confirm("Delete this transaction?")
					if err != nil {
						return err
					}
					if !ok {
						return errors.New("not deleted (use --yes to confirm without a prompt)")
					}
				}
				if err := a.DeleteTransaction(cmd.Context(), id); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Deleted transaction %d.\n", id)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}
