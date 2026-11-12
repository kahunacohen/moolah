package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"moolah/app"
)

func init() {
	extraCommands = append(extraCommands, exportCmd)
}

func exportCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "export <folder>",
		Short: "Write the whole book as text files that other programs can read",
		Long: "export writes the book into a new or empty folder:\n" +
			"  moolah.journal     accounts, transactions and exchange rates, for hledger and Ledger\n" +
			"  transactions.csv   one row per transaction line, for spreadsheets\n" +
			"  prices.csv         all exchange rates, with their source\n" +
			"  rules.csv, settings.toml, profiles/, rate-sources/   moolah's own setup\n" +
			"The files are not encrypted. History is not exported (it stays in the book).",
		Example: "  moolah export ~/moolah-export\n  hledger -f ~/moolah-export/moolah.journal balance",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				res, err := a.Export(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Exported %s to %s:\n", count(res.Transactions, "transaction", "transactions"), args[0])
				for _, f := range res.Files {
					fmt.Fprintf(g.out(), "  %s\n", f)
				}
				fmt.Fprintln(g.out(), "The files are not encrypted. Keep them somewhere safe, or delete them when done.")
				return nil
			})
		},
	}
}
