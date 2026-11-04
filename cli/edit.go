package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/core"
)

// formatTx renders a transaction as editable text.
func formatTx(tree *core.Tree, t core.Transaction) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# moolah transaction %d. Lines starting with # are ignored.\n", t.ID)
	b.WriteString("# Save and close the editor to apply the change. Delete everything to cancel.\n")
	b.WriteString("# A line: <account> <amount> <currency> [@@ <cost amount> <cost currency>]\n")
	fmt.Fprintf(&b, "date: %s\n", t.Date)
	if !t.ChargeDate.IsZero() {
		fmt.Fprintf(&b, "charge_date: %s\n", t.ChargeDate)
	}
	fmt.Fprintf(&b, "payee: %s\n", t.Payee)
	fmt.Fprintf(&b, "note: %s\n", t.Note)
	fmt.Fprintf(&b, "tags: %s\n", strings.Join(t.Tags, ", "))
	b.WriteString("lines:\n")
	width := 0
	for _, p := range t.Postings {
		if n := len([]rune(tree.Path(p.AccountID))); n > width {
			width = n
		}
	}
	for _, p := range t.Postings {
		path := tree.Path(p.AccountID)
		line := fmt.Sprintf("  %s%s  %s %s", path, strings.Repeat(" ", width-len([]rune(path))),
			p.Amount.Value.StringFixed(p.Amount.Currency.Scale()), p.Amount.Currency)
		if p.Cost != nil {
			line += fmt.Sprintf(" @@ %s %s", p.Cost.Value.StringFixed(p.Cost.Currency.Scale()), p.Cost.Currency)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

var errCancelled = errors.New("cancelled: the file was empty, nothing changed")

// parseTx reads text from formatTx back into a transaction based on orig
// (keeping its ID, kind, metadata and version).
func parseTx(tree *core.Tree, text string, orig core.Transaction) (core.Transaction, error) {
	t := orig
	t.Postings = nil
	t.Tags = nil
	t.ChargeDate = core.Date{}
	inLines := false
	seen := false
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		seen = true
		where := fmt.Sprintf("line %d", n+1)
		if inLines && (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) {
			p, err := parsePostingLine(tree, line)
			if err != nil {
				return t, fmt.Errorf("%s: %w", where, err)
			}
			t.Postings = append(t.Postings, p)
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return t, fmt.Errorf("%s: expected \"key: value\"", where)
		}
		val = strings.TrimSpace(val)
		var err error
		switch strings.TrimSpace(key) {
		case "date":
			t.Date, err = core.ParseDate(val)
		case "charge_date":
			if val != "" {
				t.ChargeDate, err = core.ParseDate(val)
			}
		case "payee":
			t.Payee = val
		case "note":
			t.Note = val
		case "tags":
			for _, tg := range strings.Split(val, ",") {
				if tg = strings.TrimSpace(tg); tg != "" {
					t.Tags = append(t.Tags, tg)
				}
			}
		case "lines":
			inLines = true
		default:
			err = fmt.Errorf("unknown key %q", key)
		}
		if err != nil {
			return t, fmt.Errorf("%s: %w", where, err)
		}
	}
	if !seen {
		return t, errCancelled
	}
	return t, nil
}

func parsePostingLine(tree *core.Tree, line string) (core.Posting, error) {
	main, cost, hasCost := strings.Cut(line, "@@")
	f := strings.Fields(main)
	if len(f) != 3 {
		return core.Posting{}, fmt.Errorf("expected <account> <amount> <currency>, got %q", strings.TrimSpace(main))
	}
	acct, err := tree.Resolve(f[0], nil)
	if err != nil {
		return core.Posting{}, err
	}
	amt, err := parseAmountCurrency(f[1], f[2])
	if err != nil {
		return core.Posting{}, err
	}
	p := core.Posting{AccountID: acct.ID, Amount: amt}
	if hasCost {
		cf := strings.Fields(cost)
		if len(cf) != 2 {
			return core.Posting{}, fmt.Errorf("expected @@ <amount> <currency>")
		}
		c, err := parseAmountCurrency(cf[0], cf[1])
		if err != nil {
			return core.Posting{}, err
		}
		p.Cost = &c
	}
	return p, nil
}

func parseAmountCurrency(num, cur string) (core.Amount, error) {
	c, err := core.ParseCurrency(cur)
	if err != nil {
		return core.Amount{}, err
	}
	v, err := core.ParseDecimal(num)
	if err != nil {
		return core.Amount{}, err
	}
	return core.NewAmount(v, c), nil
}

func editCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use: "edit <transaction-id>", Short: "Edit a transaction in your $EDITOR", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid transaction ID %q", args[0])
			}
			if g.env.RunEditor == nil {
				return errors.New("no editor available")
			}
			return g.withBook(func(a *app.App) error {
				orig, err := a.Transaction(cmd.Context(), id)
				if err != nil {
					return err
				}
				tree, err := a.Tree(cmd.Context())
				if err != nil {
					return err
				}
				f, err := os.CreateTemp("", "moolah-edit-*.txt")
				if err != nil {
					return err
				}
				defer os.Remove(f.Name())
				text := formatTx(tree, orig)
				for {
					if err := os.WriteFile(f.Name(), []byte(text), 0o600); err != nil {
						return err
					}
					if err := g.env.RunEditor(f.Name()); err != nil {
						return fmt.Errorf("editor: %w", err)
					}
					edited, err := os.ReadFile(f.Name())
					if err != nil {
						return err
					}
					text = string(edited)
					t, err := parseTx(tree, text, orig)
					if err == nil {
						err = a.UpdateTransaction(cmd.Context(), t)
					}
					if err == nil {
						fmt.Fprintf(g.out(), "Saved transaction %d.\n", id)
						return nil
					}
					if errors.Is(err, errCancelled) || errors.Is(err, app.ErrChangedElsewhere) {
						return err
					}
					fmt.Fprintln(g.env.Stderr, "error: "+err.Error())
					again, cerr := g.in.confirm("Edit again?")
					if cerr != nil {
						return cerr
					}
					if !again {
						return errors.New("not saved")
					}
					text = "# error: " + err.Error() + "\n" + stripErrorLines(text)
				}
			})
		},
	}
}

func stripErrorLines(text string) string {
	var keep []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(l, "# error: ") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}
