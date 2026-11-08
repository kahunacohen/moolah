package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"moolah/app"
)

func init() {
	extraCommands = append(extraCommands, sourceCmd)
}

func sourceCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Exchange-rate sources: TOML files that describe a public rates API",
		Long: "moolah downloads exchange rates only from the sources listed here. Built-in sources ship\n" +
			"with moolah; you can add your own (see docs/rate-sources.md). The setting rate_sources\n" +
			"chooses which sources are used and in which order.",
	}
	list := &cobra.Command{
		Use: "list", Short: "List rate sources (* = used, in order of rate_sources)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				srcs, err := a.RateSources(cmd.Context())
				if err != nil {
					return err
				}
				t := &table{headers: []string{"", "Name", "Source", "Publishes", "Description"}}
				for _, s := range srcs {
					mark, from := "", "built-in"
					if s.Used {
						mark = "*"
					}
					if s.User && s.Builtin {
						from = "yours (replaces built-in)"
					} else if s.User {
						from = "yours"
					}
					t.add(mark, s.Name, from, s.Pairs, s.Description)
				}
				if err := g.render(t, srcs); err != nil {
					return err
				}
				if g.format == "table" {
					order, _ := a.Config(cmd.Context(), app.KeyRateSources)
					if order == "" {
						order = "automatic (sources for your base currency first)"
					}
					fmt.Fprintf(g.out(), "\nOrder: %s. Change it with: moolah config set rate_sources name1,name2\n", order)
				}
				return nil
			})
		},
	}
	show := &cobra.Command{
		Use: "show <name>", Short: "Print a source (a good start for your own)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				text, err := a.RateSourceText(cmd.Context(), args[0])
				if err == nil {
					_, err = io.WriteString(g.out(), text)
				}
				return err
			})
		},
	}
	add := &cobra.Command{
		Use: "add <file.toml>", Short: "Check a source and save it in the book", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				name, err := a.SaveRateSource(cmd.Context(), string(b))
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Saved rate source %s. Try it with: moolah source test %s <from> <to>\n", name, name)
				return nil
			})
		},
	}
	edit := &cobra.Command{
		Use: "edit <name>", Short: "Edit a source in your $EDITOR (checked when you save)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				text, err := a.RateSourceText(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return g.editLoop(text, "moolah-source-*.toml", func(t string) error {
					name, err := a.SaveRateSource(cmd.Context(), t)
					if err == nil {
						fmt.Fprintf(g.out(), "Saved rate source %s.\n", name)
					}
					return err
				})
			})
		},
	}
	remove := &cobra.Command{
		Use: "remove <name>", Short: "Remove one of your sources (downloaded rates stay)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				if err := a.RemoveRateSource(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Removed rate source %s.\n", args[0])
				return nil
			})
		},
	}
	var from, to string
	test := &cobra.Command{
		Use: "test <name> <from-currency> <to-currency>", Short: "Download a few days of rates from one source, without saving",
		Example: "  moolah source test ecb USD EUR\n  moolah source test boc USD CAD --from 2026-10-01 --to 2026-10-07",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := parseDateFlag(from)
			if err != nil {
				return err
			}
			t, err := parseDateFlag(to)
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				ps, err := a.TestRateSource(cmd.Context(), app.TestRateSourceParams{Name: args[0], Base: args[1], Quote: args[2], From: f, To: t})
				if err != nil {
					return err
				}
				if len(ps) == 0 {
					fmt.Fprintln(g.out(), "The source answered, but with no rates for these dates (weekends and holidays have none).")
					return nil
				}
				tb := &table{headers: []string{"Date", "Rate"}, right: map[int]bool{1: true}}
				for _, p := range ps {
					tb.add(p.Date.String(), fmt.Sprintf("1 %s = %s %s", p.Base, p.Rate, p.Quote))
				}
				fmt.Fprintf(g.out(), "%s from %s (nothing was saved):\n\n", strings.ToUpper(args[1])+"→"+strings.ToUpper(args[2]), args[0])
				return tb.print(g.out())
			})
		},
	}
	test.Flags().StringVar(&from, "from", "", "first date (default: a week ago)")
	test.Flags().StringVar(&to, "to", "", "last date (default: today)")
	cmd.AddCommand(list, show, add, edit, remove, test)
	return cmd
}
