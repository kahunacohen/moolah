package app

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"moolah/core"
	"moolah/store"
)

// ExportResult says what an export wrote.
type ExportResult struct {
	Files        []string // paths relative to the export folder
	Transactions int
}

// exportData is a consistent copy of the book, read in one database transaction.
type exportData struct {
	prices       []core.Price
	profiles     map[string]string
	rateSources  map[string]string
	rules        []store.RuleRow
	settings     map[string]string
	transactions []core.Transaction
	tree         *core.Tree
}

// Export writes the whole book as text into dir, which must be empty or not exist:
// a journal that hledger and Ledger read (accounts, transactions, exchange rates),
// one CSV row per transaction line, and moolah's own rules, settings, profiles and
// rate sources. History is not exported.
func (a *App) Export(ctx context.Context, dir string) (ExportResult, error) {
	if err := emptyDir(dir); err != nil {
		return ExportResult{}, err
	}
	var d exportData
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		var err error
		if d.tree, err = tx.Tree(ctx); err != nil {
			return err
		}
		if d.transactions, err = tx.Transactions(ctx, store.TxFilter{}); err != nil {
			return err
		}
		if d.prices, err = tx.Prices(ctx); err != nil {
			return err
		}
		if d.rules, err = tx.Rules(ctx); err != nil {
			return err
		}
		if d.settings, err = tx.Settings(ctx); err != nil {
			return err
		}
		if d.profiles, err = tx.UserProfiles(ctx); err != nil {
			return err
		}
		d.rateSources, err = tx.UserRateSources(ctx)
		return err
	})
	if err != nil {
		return ExportResult{}, err
	}
	res := ExportResult{Transactions: len(d.transactions)}
	write := func(name string, fn func(w io.Writer) error) error {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if err := fn(f); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		res.Files = append(res.Files, name)
		return nil
	}
	err = write("moolah.journal", func(w io.Writer) error { return writeJournal(w, d, a.Book) })
	if err == nil {
		err = write("transactions.csv", func(w io.Writer) error { return writeTransactionsCSV(w, d) })
	}
	if err == nil {
		err = write("prices.csv", func(w io.Writer) error { return writePricesCSV(w, d) })
	}
	if err == nil {
		err = write("rules.csv", func(w io.Writer) error { return writeRulesCSV(w, d) })
	}
	if err == nil {
		err = write("settings.toml", func(w io.Writer) error { return writeSettings(w, d.settings) })
	}
	for _, name := range slices.Sorted(maps.Keys(d.profiles)) {
		if err == nil {
			err = write(filepath.Join("profiles", name+".toml"), func(w io.Writer) error {
				_, err := io.WriteString(w, d.profiles[name])
				return err
			})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(d.rateSources)) {
		if err == nil {
			err = write(filepath.Join("rate-sources", name+".toml"), func(w io.Writer) error {
				_, err := io.WriteString(w, d.rateSources[name])
				return err
			})
		}
	}
	return res, err
}

func emptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty. Export into a new folder, so no file is overwritten", dir)
	}
	return nil
}

// plainAmount writes an amount without locale separators: "-1530.00".
func plainAmount(a core.Amount) string { return a.Value.StringFixed(a.Currency.Scale()) }

// journalText keeps text from breaking a journal line: ";" starts a comment and
// "|" separates payee from note in hledger.
func journalText(s string) string {
	return strings.NewReplacer(";", ",", "|", "/", "\n", " ").Replace(s)
}

// writeJournal writes the hledger/Ledger journal format: account declarations,
// market prices (P lines) and transactions, with the transaction ID as its code,
// the card charge date as secondary date, and tags and metadata as comment tags.
func writeJournal(w io.Writer, d exportData, book string) error {
	ew := &errWriter{w: w}
	ew.printf("; moolah export")
	if book != "" {
		ew.printf(" of book %q", book)
	}
	ew.printf(". Base currency: %s.\n", d.settings[KeyBaseCurrency])
	ew.printf("; Read it with hledger (hledger -f moolah.journal balance) or Ledger.\n\n")

	d.tree.Walk(func(acc core.Account) {
		ew.printf("account %s\n", d.tree.Path(acc.ID))
		if acc.Currency != "" {
			ew.printf("    ; currency: %s\n", acc.Currency)
		}
		if !acc.ClosedOn.IsZero() {
			ew.printf("    ; closed: %s\n", acc.ClosedOn)
		}
	})

	// Rates implied by transactions are left out: hledger infers them from "@@",
	// and moolah uses them only when no other rate exists. prices.csv has them all.
	first := true
	for _, p := range d.prices {
		if p.Source == core.SourceTx {
			continue
		}
		if first {
			ew.printf("\n")
			first = false
		}
		ew.printf("P %s %s %s %s\n", p.Date, p.Base, p.Rate.String(), p.Quote)
	}

	for _, t := range d.transactions {
		ew.printf("\n%s", t.Date)
		if !t.ChargeDate.IsZero() && t.ChargeDate != t.Date {
			ew.printf("=%s", t.ChargeDate)
		}
		ew.printf(" (%d)", t.ID)
		desc := journalText(t.Payee)
		if t.Note != "" {
			desc += " | " + journalText(t.Note)
		}
		if desc != "" {
			ew.printf(" %s", desc)
		}
		ew.printf("\n")
		if t.Kind != core.KindNormal && t.Kind != "" {
			ew.printf("    ; kind: %s\n", t.Kind)
		}
		for _, tag := range t.Tags {
			ew.printf("    ; %s:\n", tag)
		}
		for _, k := range slices.Sorted(maps.Keys(t.Meta)) {
			ew.printf("    ; %s: %s\n", k, journalText(t.Meta[k]))
		}
		width := 0
		for _, p := range t.Postings {
			width = max(width, len([]rune(d.tree.Path(p.AccountID))))
		}
		for _, p := range t.Postings {
			path := d.tree.Path(p.AccountID)
			ew.printf("    %s%s  %s %s", path, strings.Repeat(" ", width-len([]rune(path))), plainAmount(p.Amount), p.Amount.Currency)
			if p.Cost != nil {
				// A total cost ("@@") is written without sign; it takes the sign of the amount.
				c := core.NewAmount(p.Cost.Value.Abs(), p.Cost.Currency)
				ew.printf(" @@ %s %s", plainAmount(c), c.Currency)
			}
			ew.printf("\n")
		}
	}
	return ew.err
}

// writeTransactionsCSV writes one row per transaction line, with the transaction's
// fields repeated on each of its lines.
func writeTransactionsCSV(w io.Writer, d exportData) error {
	cw := csv.NewWriter(w)
	rows := [][]string{{"transaction_id", "date", "charge_date", "payee", "note", "kind", "tags", "meta",
		"account", "amount", "currency", "cost_amount", "cost_currency"}}
	for _, t := range d.transactions {
		var meta []string
		for _, k := range slices.Sorted(maps.Keys(t.Meta)) {
			meta = append(meta, k+": "+t.Meta[k])
		}
		charge := ""
		if !t.ChargeDate.IsZero() {
			charge = t.ChargeDate.String()
		}
		for _, p := range t.Postings {
			costAmount, costCur := "", ""
			if p.Cost != nil {
				costAmount, costCur = plainAmount(*p.Cost), string(p.Cost.Currency)
			}
			rows = append(rows, []string{fmt.Sprint(t.ID), t.Date.String(), charge, t.Payee, t.Note, string(t.Kind),
				strings.Join(t.Tags, ","), strings.Join(meta, "; "), d.tree.Path(p.AccountID),
				plainAmount(p.Amount), string(p.Amount.Currency), costAmount, costCur})
		}
	}
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	return cw.Error()
}

func writePricesCSV(w io.Writer, d exportData) error {
	cw := csv.NewWriter(w)
	rows := [][]string{{"date", "from", "to", "rate", "source"}}
	for _, p := range d.prices {
		rows = append(rows, []string{p.Date.String(), string(p.Base), string(p.Quote), p.Rate.String(), string(p.Source)})
	}
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	return cw.Error()
}

func writeRulesCSV(w io.Writer, d exportData) error {
	cw := csv.NewWriter(w)
	rows := [][]string{{"position", "pattern", "account", "min_amount", "max_amount", "tags"}}
	for _, r := range d.rules {
		rows = append(rows, []string{fmt.Sprint(r.Position), r.Pattern, d.tree.Path(r.AccountID), r.MinAmount, r.MaxAmount,
			strings.Join(r.Tags, ",")})
	}
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	return cw.Error()
}

func writeSettings(w io.Writer, settings map[string]string) error {
	if _, err := io.WriteString(w, "# Book settings. Set them in another book with: moolah config set <key> <value>\n"); err != nil {
		return err
	}
	return toml.NewEncoder(w).Encode(settings)
}

// errWriter keeps the first write error, so formatting code can write freely.
type errWriter struct {
	err error
	w   io.Writer
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintf(e.w, format, args...)
	}
}
