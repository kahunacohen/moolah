package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/core"
)

func profileCreateCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "create <file>",
		Short: "Create a profile from a bank or card export, asking only what is unclear",
		Long: "Create a profile from a bank or card export. moolah reads the file, finds the header row and\n" +
			"the date, payee and amount columns, and asks only what it cannot tell from the file. Before\n" +
			"saving, it shows a summary and the first rows as they would be imported; you can change any\n" +
			"line, or edit the profile text. The profile is saved in the book.",
		Example: "  moolah profile create ~/Downloads/export.xlsx",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !g.env.Interactive {
				return errors.New("profile create asks questions, so it needs a terminal. Without one, write the profile by hand and save it with: moolah profile add <file.toml>")
			}
			f, err := app.OpenExport(args[0])
			if err != nil {
				return err
			}
			return g.withBook(func(a *app.App) error {
				w := &wizard{a: a, f: f, g: g, w: g.env.Stderr}
				return w.run(cmd.Context())
			})
		},
	}
}

// wizard asks the questions of "profile create". Questions and summaries go to stderr.
type wizard struct {
	a      *app.App
	edited string // the profile text after the user edited it by hand
	f      *app.ExportFile
	g      *globals
	gs     *app.ProfileGuess
	name   string
	w      io.Writer
}

func (w *wizard) run(ctx context.Context) error {
	if w.f.NeedsEncoding {
		if err := w.askEncoding(); err != nil {
			return err
		}
	}
	gs, err := w.f.Guess()
	if err != nil {
		return err
	}
	w.gs = gs
	if gs.Amount < 0 && gs.Debit < 0 {
		return errors.New("moolah found no column of amounts in this file. Write a profile by hand (see docs/profiles.md) and save it with: moolah profile add <file.toml>")
	}
	if len(gs.ColumnsOf(app.ColumnText)) == 0 {
		return errors.New("moolah found no text column for the payee in this file. Write a profile by hand (see docs/profiles.md) and save it with: moolah profile add <file.toml>")
	}
	enc := ""
	if w.f.Encoding != "" {
		enc = ", " + w.f.Encoding
	}
	fmt.Fprintf(w.w, "File: %s%s, %s.\n", w.f.Format, enc, count(len(gs.DataRows), "transaction row", "transaction rows"))
	if gs.HeaderRow > 0 {
		fmt.Fprintf(w.w, "Column names are in line %d; the lines above it are skipped.\n", gs.HeaderRow+1)
	}
	for _, q := range w.questions() {
		if err := q.ask(false); err != nil {
			return err
		}
	}
	if err := w.askName(); err != nil {
		return err
	}
	return w.review(ctx)
}

// question is one line of the summary. ask(false) asks only when the file is
// unclear; ask(true) always asks (to change the line).
type question struct {
	ask   func(force bool) error
	label string
	show  func() string
}

// questions are the lines of the summary, in order. The name is the last line.
func (w *wizard) questions() []question {
	gs := w.gs
	none := func(c int) func() string {
		return func() string {
			if c < 0 {
				return "none"
			}
			return w.col(c)
		}
	}
	return []question{
		{w.askDate, "date", func() string { return fmt.Sprintf("%s, %s", w.col(gs.Date), readableLayout(gs.DateLayout)) }},
		{w.askAmount, "amount", w.showAmount},
		{w.askCurrency, "currency", w.showCurrency},
		{w.askOrig, "foreign", w.showOrig},
		{w.askPayee, "payee", func() string { return w.col(gs.Payee) }},
		{w.askMemo, "note", none(gs.Memo)},
		{w.askReference, "reference", none(gs.ExternalID)},
		{w.askChargeDate, "charge date", none(gs.ChargeDate)},
		{w.askInstallment, "installments", w.showInstallment},
		{w.askSkip, "skip rows", w.showSkip},
		{w.askStop, "read until", func() string {
			if gs.Stop == "" {
				return "the end of the file"
			}
			return fmt.Sprintf("the row with %q", gs.Stop)
		}},
	}
}

func (w *wizard) review(ctx context.Context) error {
	for {
		text, err := w.text()
		if w.edited == "" {
			w.summary()
		} else {
			fmt.Fprintf(w.w, "\nThe profile, as you edited it:\n%s", indent(text))
		}
		var pv app.ProfilePreview
		if err == nil {
			pv, err = w.f.Preview(text)
		}
		if err != nil {
			fmt.Fprintln(w.w, "error: "+err.Error())
		} else {
			w.preview(pv)
		}
		choices := fmt.Sprintf("[s]ave, [1-%d] change a line, [e]dit the profile text, [q]uit", len(w.questions())+1)
		if w.edited != "" {
			choices = "[s]ave, [e]dit the profile text, [r]estart from the questions, [q]uit"
		}
		def := "s"
		if err != nil {
			def = "e"
		}
		ans, aerr := w.g.in.ask(choices, def)
		if aerr != nil {
			return aerr
		}
		ans = strings.ToLower(ans)
		switch {
		case ans == "s" && err == nil:
			done, err := w.save(ctx, text, pv.Name)
			if done || err != nil {
				return err
			}
		case ans == "s":
			fmt.Fprintln(w.w, "Fix the error first.")
		case ans == "e":
			err := w.g.editLoop(text, "moolah-profile-*.toml", func(t string) error {
				if _, err := w.f.Preview(t); err != nil {
					return err
				}
				w.edited = t
				return nil
			})
			if err != nil {
				fmt.Fprintln(w.w, "The edit was not kept.")
			}
		case ans == "r" && w.edited != "":
			w.edited = ""
		case ans == "q":
			fmt.Fprintln(w.w, "Nothing was saved.")
			return nil
		default:
			n, cerr := strconv.Atoi(ans)
			qs := w.questions()
			switch {
			case cerr != nil || w.edited != "" || n < 1 || n > len(qs)+1:
				fmt.Fprintln(w.w, "Type one of the letters or numbers shown.")
			case n == len(qs)+1:
				if err := w.askName(); err != nil {
					return err
				}
			default:
				if err := qs[n-1].ask(true); err != nil {
					return err
				}
			}
		}
	}
}

// save stores the profile. When the name exists, it shows what changes and asks first.
func (w *wizard) save(ctx context.Context, text, name string) (bool, error) {
	old, exists, err := w.a.ProfileExists(ctx, name)
	if err != nil {
		return false, err
	}
	if exists && old != text {
		fmt.Fprintf(w.w, "A profile named %s already exists. Saving changes it like this:\n%s", name, lineDiff(old, text))
		ok, err := w.g.in.confirm("Replace it?")
		if err != nil {
			return false, err
		}
		if !ok {
			fmt.Fprintf(w.w, "Not saved. Choose another name (line %d), or quit.\n", len(w.questions())+1)
			return false, nil
		}
	}
	if _, _, err := w.a.SaveProfile(ctx, text); err != nil {
		return false, err
	}
	fmt.Fprintf(w.g.out(), "Saved profile %s in this book.\nImport the file with: moolah import %s --account <account>\nChange the profile later with: moolah profile edit %s\n",
		name, w.f.Path, name)
	return true, nil
}

func (w *wizard) text() (string, error) {
	if w.edited != "" {
		return w.edited, nil
	}
	enc := ""
	if w.f.NeedsEncoding {
		enc = w.f.Encoding
	}
	return w.gs.Profile(w.name, "Created from "+filepath.Base(w.f.Path), enc)
}

func (w *wizard) summary() {
	fmt.Fprintln(w.w)
	qs := w.questions()
	for i, q := range qs {
		fmt.Fprintf(w.w, "  %2d  %-12s %s\n", i+1, q.label, q.show())
	}
	fmt.Fprintf(w.w, "  %2d  %-12s %s\n", len(qs)+1, "name", w.name)
	if unused := w.gs.Unused(); len(unused) > 0 {
		names := make([]string, len(unused))
		for i, c := range unused {
			names[i] = w.col(c)
		}
		fmt.Fprintf(w.w, "      Not used: %s\n", strings.Join(names, ", "))
	}
}

func (w *wizard) showAmount() string {
	gs := w.gs
	if gs.Amount < 0 {
		return fmt.Sprintf("debit %s (money out), credit %s (money in)", w.col(gs.Debit), w.col(gs.Credit))
	}
	if gs.Negate {
		return w.col(gs.Amount) + " (positive = money out)"
	}
	return w.col(gs.Amount) + " (negative = money out)"
}

func (w *wizard) showCurrency() string {
	if w.gs.Currency < 0 {
		return "the account's currency"
	}
	return w.col(w.gs.Currency) + w.showMap(w.gs.Currency)
}

func (w *wizard) showOrig() string {
	if w.gs.OrigAmount < 0 {
		return "none"
	}
	return fmt.Sprintf("original amount %s in %s%s", w.col(w.gs.OrigAmount), w.col(w.gs.OrigCurrency), w.showMap(w.gs.OrigCurrency))
}

// showMap lists the currency symbols of a column with their ISO codes.
func (w *wizard) showMap(c int) string {
	vals, _ := w.gs.Distinct(c)
	var parts []string
	for _, v := range vals {
		if iso, ok := w.gs.CurrencyMap[v]; ok {
			parts = append(parts, v+" = "+iso)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func (w *wizard) showInstallment() string {
	if w.gs.Installment < 0 {
		return "none"
	}
	return fmt.Sprintf("%s, pattern '%s'", w.col(w.gs.Installment), w.gs.InstallmentPattern)
}

func (w *wizard) showSkip() string {
	if w.gs.SkipColumn < 0 {
		return "none"
	}
	return fmt.Sprintf("rows with %q in %s", w.gs.SkipValue, w.col(w.gs.SkipColumn))
}

func (w *wizard) preview(pv app.ProfilePreview) {
	fmt.Fprintf(w.w, "\nThe file as it would be imported (%s):\n", count(len(pv.Rows), "row", "rows"))
	for i, r := range pv.Rows {
		if i == 5 {
			fmt.Fprintln(w.w, "  …")
			break
		}
		fmt.Fprintf(w.w, "  line %-4d %s  %s %12s\n", r.Line, r.Date, padRunes(r.Payee, 32), r.Amount.String())
	}
	for i, e := range pv.Errors {
		if i == 3 {
			fmt.Fprintf(w.w, "  and %d more rows that cannot be read\n", len(pv.Errors)-3)
			break
		}
		fmt.Fprintln(w.w, "  cannot read "+e)
	}
	fmt.Fprintln(w.w)
}

func (w *wizard) askEncoding() error {
	samples := w.f.EncodingSamples()
	if len(samples) < 2 {
		return nil
	}
	fmt.Fprintln(w.w, "This file is not UTF-8, so its text can be read in different ways.")
	opts := make([]string, len(samples))
	for i, s := range samples {
		opts[i] = fmt.Sprintf("%-13s %s", s.Encoding, s.Text)
	}
	i, err := w.choose("Which line is readable?", opts, 0)
	if err != nil {
		return err
	}
	return w.f.SetEncoding(samples[i].Encoding)
}

func (w *wizard) askDate(force bool) error {
	gs := w.gs
	example := gs.Columns[gs.Date].Samples[0]
	layouts := gs.DateLayouts
	if len(layouts) == 1 && !force {
		return nil
	}
	if len(layouts) > 0 {
		opts := make([]string, len(layouts))
		def := 0
		for i, l := range layouts {
			t, _ := time.Parse(l, example)
			opts[i] = fmt.Sprintf("%-10s → %s", readableLayout(l), t.Format("2 January 2006"))
			if l == gs.DateLayout {
				def = i
			}
		}
		if len(layouts) > 1 {
			fmt.Fprintf(w.w, "The dates in %s (for example %s) can be read in more than one way.\n", w.col(gs.Date), example)
		}
		i, err := w.choose("Which is right?", opts, def)
		if err != nil {
			return err
		}
		gs.DateLayout = layouts[i]
		return nil
	}
	fmt.Fprintf(w.w, "moolah cannot read the dates in %s (for example %s).\n", w.col(gs.Date), example)
	for {
		l, err := w.g.in.ask("Type the date format as a Go layout (2006 = year, 01 = month, 02 = day), for example 02/01/2006", "")
		if err != nil {
			return err
		}
		if _, perr := time.Parse(l, example); l != "" && perr == nil {
			gs.DateLayout = l
			return nil
		}
		fmt.Fprintf(w.w, "%q does not read %s.\n", l, example)
	}
}

func (w *wizard) askAmount(force bool) error {
	gs := w.gs
	if gs.Debit >= 0 {
		fmt.Fprintf(w.w, "Each row has an amount in either %s or %s.\n", w.col(gs.Debit), w.col(gs.Credit))
		i, err := w.choose("Which column is money out (debit)?", []string{w.colSamples(gs.Debit), w.colSamples(gs.Credit)}, 0)
		if err != nil {
			return err
		}
		if i == 1 {
			gs.Debit, gs.Credit = gs.Credit, gs.Debit
		}
		return nil
	}
	choices := gs.AmountChoice
	if force {
		choices = gs.ColumnsOf(app.ColumnNumber)
	}
	if len(choices) > 1 {
		opts := make([]string, len(choices))
		def := 0
		for i, c := range choices {
			opts[i] = w.colSamples(c)
			if c == gs.Amount {
				def = i
			}
		}
		i, err := w.choose("Which column is the amount?", opts, def)
		if err != nil {
			return err
		}
		gs.Amount = choices[i]
	}
	row, ok := gs.SignSample()
	if !ok {
		gs.Negate = false
		return nil
	}
	q := fmt.Sprintf("Line %d: %q shows %s in %s. Did money leave the account?",
		row+1, gs.Value(row, gs.Payee), gs.Value(row, gs.Amount), w.col(gs.Amount))
	yes, err := w.g.in.confirmDefault(q, gs.Negate)
	gs.Negate = yes
	return err
}

func (w *wizard) askPayee(force bool) error {
	gs := w.gs
	if !force {
		ok, err := w.g.in.confirmDefault(fmt.Sprintf("Payee (description): %s. Right?", w.colSamples(gs.Payee)), true)
		if err != nil || ok {
			return err
		}
	}
	c, err := w.chooseColumn("Which column is the payee (description)?", gs.ColumnsOf(app.ColumnText), gs.Payee, "")
	if err == nil {
		gs.Payee = c
	}
	return err
}

func (w *wizard) askReference(force bool) error {
	gs := w.gs
	if !force {
		if gs.ExternalID < 0 {
			return nil
		}
		ok, err := w.g.in.confirmDefault(fmt.Sprintf("Is %s the bank's reference number? moolah then uses it to recognize rows imported before.", w.colSamples(gs.ExternalID)), true)
		if !ok {
			gs.ExternalID = -1
		}
		return err
	}
	var cols []int
	for _, c := range gs.ColumnsOf(app.ColumnID, app.ColumnText, app.ColumnNumber) {
		if c != gs.Payee && c != gs.Amount && c != gs.Debit && c != gs.Credit {
			cols = append(cols, c)
		}
	}
	c, err := w.chooseColumn("Which column is the bank's reference number?", cols, gs.ExternalID, "none")
	if err == nil {
		gs.ExternalID = c
	}
	return err
}

func (w *wizard) askChargeDate(force bool) error {
	gs := w.gs
	var dates []int
	for _, c := range gs.ColumnsOf(app.ColumnDate) {
		if c != gs.Date {
			dates = append(dates, c)
		}
	}
	if len(dates) == 0 {
		if force {
			fmt.Fprintln(w.w, "The file has no second column of dates.")
		}
		return nil
	}
	if !force {
		if gs.ChargeDate < 0 {
			return nil
		}
		ok, err := w.g.in.confirmDefault(fmt.Sprintf("Is %s the charge date (the day a card charged the purchase)?", w.colSamples(gs.ChargeDate)), true)
		if !ok {
			gs.ChargeDate = -1
		}
		return err
	}
	c, err := w.chooseColumn("Which column is the charge date?", dates, gs.ChargeDate, "none")
	if err == nil {
		gs.ChargeDate = c
	}
	return err
}

func (w *wizard) askStop(force bool) error {
	gs := w.gs
	if !force {
		if gs.Stop == "" {
			return nil
		}
		ok, err := w.g.in.confirmDefault(fmt.Sprintf("Line %d, after the transactions, has %q (a total?). Stop reading there?", gs.StopRow+1, gs.Stop), true)
		if !ok {
			gs.Stop = ""
		}
		return err
	}
	def := gs.Stop
	if def == "" {
		def = "-"
	}
	ans, err := w.g.in.ask("Stop at the first row that contains this text (- to read to the end)", def)
	if err != nil {
		return err
	}
	if ans == "-" {
		ans = ""
	}
	gs.Stop = ans
	return nil
}

// askCurrency asks whether a currency-like column is the currency of the amount.
func (w *wizard) askCurrency(force bool) error {
	gs := w.gs
	var cols []int
	for _, c := range gs.CurrencyColumns() {
		if c != gs.OrigCurrency {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		if force {
			fmt.Fprintln(w.w, "The file has no other column of currencies.")
		}
		return nil
	}
	c, err := w.chooseColumn("Which column is the currency of the amount?", cols, gs.Currency, "none: the account's currency")
	if err != nil {
		return err
	}
	gs.Currency = c
	return w.mapCurrencies(c)
}

// askOrig asks for the original amount and currency of foreign purchases.
func (w *wizard) askOrig(force bool) error {
	gs := w.gs
	var nums, curs []int
	for _, c := range gs.ColumnsOf(app.ColumnNumber) {
		if c != gs.Amount && c != gs.Debit && c != gs.Credit {
			nums = append(nums, c)
		}
	}
	for _, c := range gs.CurrencyColumns() {
		if c != gs.Currency {
			curs = append(curs, c)
		}
	}
	if len(nums) == 0 || len(curs) == 0 {
		if force {
			fmt.Fprintln(w.w, "The file has no other column of amounts with a column of currencies.")
		}
		return nil
	}
	fmt.Fprintln(w.w, "For a purchase in another currency, a card export often shows the original amount too.")
	c, err := w.chooseColumn("Which column is the original amount?", nums, gs.OrigAmount, "none")
	if err != nil || c < 0 {
		gs.OrigAmount = -1
		return err
	}
	gs.OrigAmount = c
	cur := curs[0]
	if len(curs) > 1 {
		if cur, err = w.chooseColumn("Which column is its currency?", curs, gs.OrigCurrency, ""); err != nil {
			return err
		}
	}
	gs.OrigCurrency = cur
	return w.mapCurrencies(cur)
}

// mapCurrencies asks for the ISO code of each value that is not one, such as "₪".
func (w *wizard) mapCurrencies(c int) error {
	if c < 0 {
		return nil
	}
	vals, _ := w.gs.Distinct(c)
	for _, v := range vals {
		if _, known := w.gs.CurrencyMap[v]; known {
			continue
		}
		if _, err := core.ParseCurrency(v); err == nil {
			continue
		}
		for {
			ans, err := w.g.in.ask(fmt.Sprintf("What currency is %q? Type its code, for example USD, EUR or ILS", v), w.gs.CurrencyMap[v])
			if err != nil {
				return err
			}
			iso, perr := core.ParseCurrency(ans)
			if perr == nil {
				w.gs.CurrencyMap[v] = string(iso)
				break
			}
			fmt.Fprintln(w.w, perr.Error()+".")
		}
	}
	return nil
}

func (w *wizard) askMemo(force bool) error {
	gs := w.gs
	skip := map[int]bool{gs.Payee: true, gs.Currency: true, gs.OrigCurrency: true}
	for _, c := range append(gs.StatusColumns(), gs.CurrencyColumns()...) {
		skip[c] = true
	}
	var cols []int
	for _, c := range gs.ColumnsOf(app.ColumnText) {
		if !skip[c] || (force && c != gs.Payee) {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		if force {
			fmt.Fprintln(w.w, "The file has no other text column.")
		}
		return nil
	}
	c, err := w.chooseColumn("Which column has extra details to keep as the note?", cols, gs.Memo, "none")
	if err == nil {
		gs.Memo = c
	}
	return err
}

func (w *wizard) askInstallment(force bool) error {
	gs := w.gs
	if gs.InstallmentPattern == "" {
		if force {
			fmt.Fprintln(w.w, "moolah found no text like \"payment 3 of 12\" in the file. To add it, edit the profile text (installment, installment_pattern).")
		}
		return nil
	}
	if gs.Installment < 0 && !force {
		return nil
	}
	re := regexp.MustCompile(gs.InstallmentPattern)
	col := gs.Installment
	if col < 0 {
		for _, c := range gs.ColumnsOf(app.ColumnText) {
			if re.MatchString(gs.Value(gs.InstallmentRow, c)) {
				col = c
				break
			}
		}
	}
	if col < 0 {
		return nil
	}
	m := re.FindStringSubmatch(gs.Value(gs.InstallmentRow, col))
	if m == nil {
		return nil
	}
	q := fmt.Sprintf("Line %d has %q in %s. Is this payment %s of %s of a purchase in installments?",
		gs.InstallmentRow+1, gs.Value(gs.InstallmentRow, col), w.col(col), m[1], m[2])
	yes, err := w.g.in.confirmDefault(q, true)
	if err != nil {
		return err
	}
	if yes {
		gs.Installment = col
	} else {
		gs.Installment = -1
	}
	return nil
}

// askSkip asks whether rows with one value of a status column should be skipped,
// such as card purchases that are not final yet.
func (w *wizard) askSkip(force bool) error {
	gs := w.gs
	cols := gs.StatusColumns()
	if len(cols) == 0 {
		if force {
			fmt.Fprintln(w.w, "The file has no column with a few repeated values (such as a status). To skip rows, edit the profile text ([table] skip).")
		}
		return nil
	}
	for _, c := range cols {
		vals, n := gs.Distinct(c)
		opts := []string{"none: import every row"}
		def := 0
		for i, v := range vals {
			opts = append(opts, fmt.Sprintf("%q (%s)", v, count(n[v], "row", "rows")))
			if c == gs.SkipColumn && v == gs.SkipValue {
				def = i + 1
			}
		}
		fmt.Fprintf(w.w, "Column %s has only these values. Some exports mark rows that are not final yet (pending).\n", w.col(c))
		i, err := w.choose("Skip the rows with which value?", opts, def)
		if err != nil {
			return err
		}
		if i > 0 {
			gs.SkipColumn, gs.SkipValue = c, vals[i-1]
			return nil
		}
		if c == gs.SkipColumn {
			gs.SkipColumn, gs.SkipValue = -1, ""
		}
	}
	return nil
}

var notNameChar = regexp.MustCompile(`[^a-z0-9_-]+`)

func (w *wizard) askName() error {
	def := w.name
	if def == "" {
		base := strings.TrimSuffix(filepath.Base(w.f.Path), filepath.Ext(w.f.Path))
		def = strings.Trim(notNameChar.ReplaceAllString(strings.ToLower(base), "-"), "-_")
		if len(def) > 40 {
			def = def[:40]
		}
		if app.ValidProfileName(def) != nil {
			def = ""
		}
	}
	for {
		ans, err := w.g.in.ask("Profile name (for example the bank's name)", def)
		if err != nil {
			return err
		}
		if err := app.ValidProfileName(ans); err != nil {
			fmt.Fprintln(w.w, "The "+err.Error()+".")
			continue
		}
		w.name = ans
		return nil
	}
}

// choose shows numbered options and returns the index of the chosen one.
func (w *wizard) choose(question string, opts []string, def int) (int, error) {
	for i, o := range opts {
		fmt.Fprintf(w.w, "  %2d  %s\n", i+1, o)
	}
	for {
		ans, err := w.g.in.ask(question, strconv.Itoa(def+1))
		if err != nil {
			return 0, err
		}
		if n, err := strconv.Atoi(ans); err == nil && n >= 1 && n <= len(opts) {
			return n - 1, nil
		}
		fmt.Fprintf(w.w, "Type a number from 1 to %d.\n", len(opts))
	}
}

// chooseColumn chooses one of cols. With a noneLabel, the first option is no column (-1).
func (w *wizard) chooseColumn(question string, cols []int, current int, noneLabel string) (int, error) {
	var opts []string
	if noneLabel != "" {
		opts = append(opts, noneLabel)
		cols = append([]int{-1}, cols...)
	}
	def := 0
	for i, c := range cols {
		if c >= 0 {
			opts = append(opts, w.colSamples(c))
		}
		if c == current {
			def = i
		}
	}
	i, err := w.choose(question, opts, def)
	if err != nil {
		return -1, err
	}
	return cols[i], nil
}

func (w *wizard) col(c int) string { return strconv.Quote(w.gs.Columns[c].Header) }

func (w *wizard) colSamples(c int) string {
	s := w.gs.Columns[c].Samples
	if len(s) == 0 {
		return w.col(c)
	}
	quoted := make([]string, len(s))
	for i, v := range s {
		quoted[i] = strconv.Quote(v)
	}
	return fmt.Sprintf("%s (for example %s)", w.col(c), strings.Join(quoted, ", "))
}

// readableLayout writes a Go date layout as dd/mm/yyyy.
func readableLayout(l string) string {
	return strings.NewReplacer("2006", "yyyy", "06", "yy", "01", "mm", "02", "dd", "1", "m", "2", "d").Replace(l)
}

func padRunes(s string, n int) string {
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n-1]) + "…"
	}
	return s + strings.Repeat(" ", n-utf8.RuneCountInString(s))
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n  ") + "\n"
}

// lineDiff lists the lines only in old (-) and only in new (+).
func lineDiff(old, new string) string {
	in := func(text string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(text, "\n") {
			m[l] = true
		}
		return m
	}
	oldSet, newSet := in(old), in(new)
	var b strings.Builder
	for _, l := range strings.Split(old, "\n") {
		if l != "" && !newSet[l] {
			b.WriteString("  - " + l + "\n")
		}
	}
	for _, l := range strings.Split(new, "\n") {
		if l != "" && !oldSet[l] {
			b.WriteString("  + " + l + "\n")
		}
	}
	return b.String()
}
