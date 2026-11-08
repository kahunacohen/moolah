package cli

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/core"
	"moolah/crypt"
)

func priceCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "price", Short: "Exchange rates"}

	add := &cobra.Command{
		Use: "add <date> <from> <to> <rate>", Short: "Enter a rate: 1 <from> = <rate> <to>", Args: cobra.ExactArgs(4),
		Example: "  moolah price add 2026-10-31 USD ILS 3.65",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := core.ParseDate(args[0])
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				p, err := a.AddPrice(cmd.Context(), app.PriceParams{Date: d, Base: args[1], Quote: args[2], Rate: args[3]})
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Saved 1 %s = %s %s on %s.\n", p.Base, p.Rate, p.Quote, p.Date)
				return nil
			})
		},
	}

	imp := &cobra.Command{
		Use: "import <file.csv>", Short: "Import rates from a CSV file with columns date,from,to,rate", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			rows, err := readPriceCSV(f)
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				if err := a.AddPrices(cmd.Context(), rows); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Imported %d rates.\n", len(rows))
				return nil
			})
		},
	}

	list := &cobra.Command{
		Use: "list [currency]", Short: "List stored rates", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cur := ""
			if len(args) == 1 {
				cur = args[0]
			}
			return g.withBook(func(a *app.App) error {
				prices, err := a.Prices(cmd.Context(), cur)
				if err != nil {
					return err
				}
				t := &table{headers: []string{"Date", "From", "To", "Rate", "Source"}, right: map[int]bool{3: true}}
				type pj struct {
					Date   core.Date `json:"date"`
					From   string    `json:"from"`
					Rate   string    `json:"rate"`
					Source string    `json:"source"`
					To     string    `json:"to"`
				}
				js := []pj{}
				for _, p := range prices {
					t.add(p.Date.String(), string(p.Base), string(p.Quote), p.Rate.String(), string(p.Source))
					js = append(js, pj{p.Date, string(p.Base), p.Rate.String(), string(p.Source), string(p.Quote)})
				}
				return g.render(t, js)
			})
		},
	}
	cmd.AddCommand(add, imp, list)
	return cmd
}

func readPriceCSV(r io.Reader) ([]app.PriceParams, error) {
	recs, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, err
	}
	var out []app.PriceParams
	for i, rec := range recs {
		if len(rec) != 4 {
			return nil, fmt.Errorf("line %d: expected 4 columns date,from,to,rate", i+1)
		}
		if i == 0 && strings.EqualFold(strings.TrimSpace(rec[0]), "date") {
			continue
		}
		d, err := core.ParseDate(strings.TrimSpace(rec[0]))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, app.PriceParams{Date: d, Base: rec[1], Quote: rec[2], Rate: rec[3]})
	}
	return out, nil
}

func configCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Settings: most are stored in the book; paths and offline mode only on this computer"}
	get := &cobra.Command{
		Use: "get <key>", Short: "Show a setting", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				v, err := a.Config(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				fmt.Fprintln(g.out(), v)
				return nil
			})
		},
	}
	set := &cobra.Command{
		Use: "set <key> <value>", Short: "Change a setting (an empty value removes it)", Args: cobra.ExactArgs(2),
		ValidArgs: app.SettingKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				if err := a.SetConfig(cmd.Context(), args[0], args[1]); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Set %s.\n", args[0])
				return nil
			})
		},
	}
	list := &cobra.Command{
		Use: "list", Short: "Show all settings", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				kv, err := a.ConfigList(cmd.Context())
				if err != nil {
					return err
				}
				t := &table{headers: []string{"Setting", "Value", "Stored in"}}
				for _, p := range kv {
					v := p.Value
					if len(v) > 50 {
						v = v[:47] + "..."
					}
					t.add(p.Key, v, string(p.Place))
				}
				if err := g.render(t, kv); err != nil {
					return err
				}
				if g.format == "table" {
					fmt.Fprintf(g.out(), "\n\"book\" settings sync with your other computers; \"this computer\" settings are in %s.\n", g.env.Paths.ConfigFile())
				}
				return nil
			})
		},
	}
	cmd.AddCommand(get, set, list)
	return cmd
}

func backupCmd(g *globals) *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use: "backup", Short: "Write an encrypted backup of the book", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				path, err := a.Backup(cmd.Context(), app.BackupParams{Dir: to, Passphrase: func() (string, error) { return g.in.passphrase(true) }})
				if err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "Backup written to %s\n", path)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "folder for the backup (default: backup_dir)")
	return cmd
}

func restoreCmd(g *globals) *cobra.Command {
	var identity string
	var yes bool
	cmd := &cobra.Command{
		Use: "restore <backup.db.age>", Short: "Replace the book with a backup", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			loc, err := g.location()
			if err != nil {
				return err
			}
			if _, err := os.Stat(loc.Path); err == nil && !yes {
				ok, err := g.in.confirm(fmt.Sprintf("Replace %s with this backup? Its current data will be lost", nameOr(loc.Book, loc.Path)))
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("not restored (use --yes to confirm without a prompt)")
				}
			}
			if identity == "" {
				identity = crypt.DefaultIdentityPath(g.env.Paths.ConfigDir)
			}
			err = app.Restore(app.RestoreParams{File: args[0], IdentityFile: identity, Target: loc.Path,
				Passphrase: func() (string, error) { return g.in.passphrase(false) }})
			if err != nil {
				return err
			}
			fmt.Fprintf(g.out(), "Restored %s from %s.\n", nameOr(loc.Book, loc.Path), args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&identity, "identity", "", "private key file (default: moolah's age key, else ~/.ssh/id_ed25519)")
	cmd.Flags().BoolVar(&yes, "yes", false, "replace without asking")
	return cmd
}

func checkCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use: "check", Short: "Check the book for problems", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBook(func(a *app.App) error {
				problems, err := a.Check(cmd.Context())
				if err != nil {
					return err
				}
				if len(problems) == 0 {
					fmt.Fprintln(g.out(), "No problems found.")
					return nil
				}
				for _, p := range problems {
					fmt.Fprintln(g.out(), "- "+p)
				}
				return fmt.Errorf("%d problems found", len(problems))
			})
		},
	}
}
