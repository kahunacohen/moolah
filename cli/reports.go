package cli

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/core"
)

func init() {
	extraCommands = append(extraCommands, reportCmd, ratesCmd)
}

type periodFlags struct {
	from, month, to, year string
}

func (f *periodFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.from, "from", "", "first date (default: January 1)")
	cmd.Flags().StringVar(&f.to, "to", "", "last date (default: today)")
	cmd.Flags().StringVar(&f.month, "month", "", "a whole month, YYYY-MM")
	cmd.Flags().StringVar(&f.year, "year", "", "a whole year, YYYY")
}

func (f *periodFlags) period() (app.Period, error) {
	switch {
	case f.month != "":
		start, err := core.ParseDate(f.month + "-01")
		if err != nil {
			return app.Period{}, fmt.Errorf("invalid --month %q: use YYYY-MM", f.month)
		}
		next := start.AddDays(32)
		end := core.MustDate(next.String()[:8] + "01").AddDays(-1)
		return app.Period{From: start, To: end}, nil
	case f.year != "":
		start, err := core.ParseDate(f.year + "-01-01")
		if err != nil {
			return app.Period{}, fmt.Errorf("invalid --year %q: use YYYY", f.year)
		}
		return app.Period{From: start, To: core.MustDate(f.year + "-12-31")}, nil
	}
	from, err := parseDateFlag(f.from)
	if err != nil {
		return app.Period{}, err
	}
	to, err := parseDateFlag(f.to)
	return app.Period{From: from, To: to}, err
}

func money(v decimal.Decimal, c core.Currency) string { return core.FormatNumber(v, c.Scale()) }

func reportCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "report", Short: "Net worth and income/spending reports in your base currency"}

	var nwp periodFlags
	var nwIn string
	networth := &cobra.Command{
		Use: "networth", Short: "How net worth changed: income, gains, spending and the currency effect", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			period, err := nwp.period()
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				r, err := a.NetWorth(cmd.Context(), app.NetWorthParams{Period: period, In: nwIn})
				if err != nil {
					return err
				}
				c := r.Currency
				t := &table{headers: []string{"", "Value (" + string(c) + ")"}, right: map[int]bool{1: true}}
				t.add("Net worth "+r.Period.From.AddDays(-1).String(), money(r.Start, c))
				if !r.Opening.IsZero() {
					t.add("  + opening balances", money(r.Opening, c))
				}
				t.add("  + income", money(r.Income, c))
				t.add("  − spending", money(r.Spending.Neg(), c))
				t.add("  + investment gains", money(r.Gains, c))
				t.add("  + currency effect", money(r.FX, c))
				t.add("Net worth "+r.Period.To.String(), money(r.End, c))
				if g.format == "table" {
					fmt.Fprintf(g.out(), "%sNet worth %s to %s, in %s\n\n", g.bookHeader(), r.Period.From, r.Period.To, c)
				}
				return g.render(t, map[string]any{"currency": c, "from": r.Period.From, "to": r.Period.To,
					"start": r.Start.String(), "opening": r.Opening.String(), "income": r.Income.String(), "spending": r.Spending.String(),
					"gains": r.Gains.String(), "currency_effect": r.FX.String(), "end": r.End.String()})
			})
		},
	}
	nwp.register(networth)
	networth.Flags().StringVar(&nwIn, "in", "", "report currency (default: your base currency)")

	var ip periodFlags
	var in, tag, by string
	var depth int
	income := &cobra.Command{
		Use: "income", Short: "Income and spending by category", Args: cobra.NoArgs,
		Example: "  moolah report income --month 2026-10\n  moolah report income --year 2026 --depth 1\n  moolah report income --month 2026-10 --by charge-date",
		RunE: func(cmd *cobra.Command, args []string) error {
			period, err := ip.period()
			if err != nil {
				return err
			}
			if by != "" && by != "date" && by != "charge-date" {
				return fmt.Errorf("--by must be date or charge-date")
			}
			return g.withBook(func(a *app.App) error {
				r, err := a.Income(cmd.Context(), app.IncomeParams{Period: period, In: in, Tag: tag, Depth: depth, ByChargeDate: by == "charge-date"})
				if err != nil {
					return err
				}
				c := r.Currency
				t := &table{headers: []string{"Category", "Value (" + string(c) + ")"}, right: map[int]bool{1: true}}
				type rowJSON struct {
					Category string `json:"category"`
					Value    string `json:"value"`
				}
				var inc, exp []rowJSON
				t.add("Income", "")
				for _, row := range r.Income {
					t.add(g.treeLabel(row.Category, row.Depth), money(row.Value, c))
					inc = append(inc, rowJSON{row.Category, row.Value.String()})
				}
				t.add("Total income", money(r.TotalIncome, c))
				t.add("", "")
				t.add("Spending", "")
				for _, row := range r.Expenses {
					t.add(g.treeLabel(row.Category, row.Depth), money(row.Value, c))
					exp = append(exp, rowJSON{row.Category, row.Value.String()})
				}
				t.add("Total spending", money(r.TotalExpenses, c))
				t.add("", "")
				t.add("Net (income − spending)", money(r.Net, c))
				if g.format == "table" {
					title := "Income and spending"
					if by == "charge-date" {
						title += " by card charge date"
					}
					fmt.Fprintf(g.out(), "%s%s %s to %s, in %s\n\n", g.bookHeader(), title, r.Period.From, r.Period.To, c)
				}
				return g.render(t, map[string]any{"currency": c, "from": r.Period.From, "to": r.Period.To, "income": inc,
					"spending": exp, "total_income": r.TotalIncome.String(), "total_spending": r.TotalExpenses.String(), "net": r.Net.String()})
			})
		},
	}
	ip.register(income)
	income.Flags().StringVar(&in, "in", "", "report currency (default: your base currency)")
	income.Flags().StringVar(&tag, "tag", "", "only transactions with this tag")
	income.Flags().IntVar(&depth, "depth", 0, "show only this many category levels (0 = all)")
	income.Flags().StringVar(&by, "by", "date", "group card purchases by purchase date (date) or charge date (charge-date)")

	cmd.AddCommand(networth, income)
	return cmd
}

// treeLabel shows an account or category path in a report row. A table shows
// the last level, indented by depth; CSV and JSON need the full path, because
// they have no indentation to show where the row belongs.
func (g *globals) treeLabel(path string, depth int) string {
	if g.format != "table" {
		return path
	}
	parts := strings.Split(path, ":")
	if depth <= len(parts) {
		path = parts[len(parts)-1]
	}
	return strings.Repeat("  ", depth) + path
}

func ratesCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rates",
		Short: "Exchange rates: download, enter and list them, and manage where they come from",
		Long: "moolah converts amounts with daily exchange rates stored in the book. It downloads them\n" +
			"from rate sources (moolah rates source list) when a report needs them, or with\n" +
			"moolah rates fetch. Rates you enter with add or import are used when no source has one.",
	}
	var in string
	fetch := &cobra.Command{
		Use: "fetch", Short: "Download missing daily rates for the currencies this book uses", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var quote core.Currency
			if in != "" {
				c, err := core.ParseCurrency(in)
				if err != nil {
					return err
				}
				quote = c
			}
			return g.withBook(func(a *app.App) error {
				res, err := a.FetchRates(cmd.Context(), quote)
				if res.Added > 0 || err == nil {
					fmt.Fprintf(g.out(), "Downloaded %s", count(res.Added, "rate", "rates"))
					if len(res.Pairs) > 0 {
						fmt.Fprintf(g.out(), " (%s)", strings.Join(res.Pairs, ", "))
					}
					fmt.Fprintln(g.out(), ".")
				}
				return err
			})
		},
	}
	fetch.Flags().StringVar(&in, "in", "", "download rates into this currency (default: your base currency)")
	cmd.AddCommand(fetch, sourceCmd(g))
	cmd.AddCommand(storedRateCmds(g)...)
	return cmd
}
