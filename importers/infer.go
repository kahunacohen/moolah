package importers

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ColumnKind is what the values of a column look like.
type ColumnKind int

// Column kinds found by Infer.
const (
	ColumnEmpty  ColumnKind = iota // no values
	ColumnText                     // free text, such as a description
	ColumnDate                     // dates in one format
	ColumnNumber                   // amounts
	ColumnID                       // unique whole numbers, such as a reference number
)

// GuessColumn describes one column of an export file.
type GuessColumn struct {
	Header  string
	Index   int // 0-based position in the row
	Kind    ColumnKind
	Samples []string // up to three different values from the data rows
}

// Guess is moolah's reading of an export file that no profile describes yet. Column
// fields are indexes into Columns, or -1 for none. A caller may change any field
// (for example after asking the user) before calling Profile.
type Guess struct {
	Amount             int   // one signed amount column
	AmountChoice       []int // number columns that could be the amount
	ChargeDate         int   // a second date column
	Columns            []GuessColumn
	Credit             int               // money in, when the file has two amount columns
	Currency           int               // the currency of the amount; -1: the account's currency
	CurrencyMap        map[string]string // values of currency columns that are not ISO codes → ISO codes
	DataRows           []int             // 0-based grid rows with a transaction
	Date               int
	DateLayout         string   // the chosen layout, one of DateLayouts
	DateLayouts        []string // Go layouts that read every date; more than one means day and month are ambiguous
	Debit              int      // money out, when the file has two amount columns
	DecimalComma       bool
	ExternalID         int
	HeaderRow          int    // 0-based
	Installment        int    // a column with "payment 3 of 12" in some language
	InstallmentPattern string // regular expression built from Installment's text
	InstallmentRow     int    // 0-based grid row of an example
	Memo               int    // extra text, saved as the note
	Negate             bool   // positive amounts are money out (usual for cards)
	OrigAmount         int    // the amount before conversion, for foreign purchases
	OrigCurrency       int
	Payee              int
	SkipColumn         int // skip rows whose cell in this column contains SkipValue (pending rows)
	SkipValue          string
	Stop               string // text of the first row after the data, for [table] stop
	StopRow            int    // 0-based grid row of Stop, or -1

	grid Grid
}

// guessLayouts are the date layouts Infer tries, zero-padded first. "1" and "2"
// also read padded values, so each padded layout has an unpadded twin.
var guessLayouts = []string{
	"2006-01-02", "2006-1-2", "2006/01/02", "2006/1/2", "2006.01.02", "2006.1.2",
	"02/01/2006", "2/1/2006", "01/02/2006", "1/2/2006",
	"02.01.2006", "2.1.2006", "01.02.2006", "1.2.2006",
	"02-01-2006", "2-1-2006", "01-02-2006", "1-2-2006",
	"02/01/06", "2/1/06", "01/02/06", "1/2/06",
	"02.01.06", "2.1.06", "01.02.06", "1.2.06",
	"02-01-06", "2-1-06", "01-02-06", "1-2-06",
}

func dateLayoutsOf(v string) []string {
	var out []string
	for _, l := range guessLayouts {
		if _, err := time.Parse(l, v); err == nil {
			out = append(out, l)
		}
	}
	return out
}

// isNumber reports whether v is an amount. ParseAmount ignores letters (for currency
// codes), so text with more than three letters is not a number.
func isNumber(v string) bool {
	if !strings.ContainsAny(v, "0123456789") || len(dateLayoutsOf(v)) > 0 {
		return false
	}
	letters := 0
	for _, r := range v {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters > 3 {
		return false
	}
	_, err := ParseAmount(v, false)
	return err == nil
}

var digitsOnly = regexp.MustCompile(`^[0-9]{4,}$`)

// Infer finds the header row, the data rows and the role of each column. It fails
// only when the file has no header row followed by rows with dates and amounts.
func Infer(g Grid) (*Guess, error) {
	gs := &Guess{Amount: -1, ChargeDate: -1, Credit: -1, Currency: -1, CurrencyMap: map[string]string{}, Date: -1, Debit: -1, ExternalID: -1,
		Installment: -1, Memo: -1, OrigAmount: -1, OrigCurrency: -1, Payee: -1, SkipColumn: -1, StopRow: -1, grid: g}
	gs.HeaderRow = findHeaderRow(g)
	if gs.HeaderRow < 0 {
		return nil, errors.New("moolah could not find a header row (column names) followed by rows with dates and amounts. " +
			"Check that this is a transaction export, or write a profile by hand (see docs/profiles.md)")
	}
	header := g[gs.HeaderRow]
	for i, h := range header {
		gs.Columns = append(gs.Columns, GuessColumn{Header: h, Index: i})
	}
	var below []int
	for i := gs.HeaderRow + 1; i < len(g); i++ {
		if !isEmptyRow(g[i]) {
			below = append(below, i)
		}
	}
	// The date column is the named column with the most dates.
	best := 0
	for c := range gs.Columns {
		if gs.Columns[c].Header == "" {
			continue
		}
		n := 0
		for _, r := range below {
			if len(dateLayoutsOf(cellAt(g[r], c))) > 0 {
				n++
			}
		}
		if n > best {
			best, gs.Date = n, c
		}
	}
	if gs.Date < 0 {
		return nil, errors.New("moolah found column names but no column of dates below them. Write a profile by hand (see docs/profiles.md)")
	}
	last := -1
	for _, r := range below {
		if len(dateLayoutsOf(cellAt(g[r], gs.Date))) > 0 {
			gs.DataRows = append(gs.DataRows, r)
			last = r
		}
	}
	gs.findStop(below, last)
	gs.DateLayouts = commonLayouts(gs.values(gs.Date))
	if len(gs.DateLayouts) > 0 {
		gs.DateLayout = gs.DateLayouts[0]
	}
	gs.Columns[gs.Date].Kind = ColumnDate
	for c := range gs.Columns {
		if c != gs.Date {
			gs.Columns[c].Kind = gs.kindOf(c)
		}
		gs.Columns[c].Samples = samples(gs.values(c))
	}
	gs.findAmounts()
	gs.findOthers()
	gs.findInstallment()
	return gs, nil
}

// findHeaderRow returns the first row of two or more text cells that is followed,
// within five rows, by a row with a date and a number.
func findHeaderRow(g Grid) int {
	for i := 0; i < len(g) && i < 50; i++ {
		texts := 0
		ok := true
		for _, v := range g[i] {
			if v == "" {
				continue
			}
			if isNumber(v) || len(dateLayoutsOf(v)) > 0 {
				ok = false
				break
			}
			texts++
		}
		if !ok || texts < 2 {
			continue
		}
		seen := 0
		for j := i + 1; j < len(g) && seen < 5; j++ {
			if isEmptyRow(g[j]) {
				continue
			}
			seen++
			var date, num bool
			for _, v := range g[j] {
				date = date || len(dateLayoutsOf(v)) > 0
				num = num || isNumber(v)
			}
			if date && num {
				return i
			}
		}
	}
	return -1
}

// findStop sets Stop when rows without a date follow the last data row, such as a
// total row. Its text must not appear in any data row.
func (gs *Guess) findStop(below []int, last int) {
	for _, r := range below {
		if r <= last {
			continue
		}
		for _, v := range gs.grid[r] {
			if v == "" || isNumber(v) || gs.inData(v) {
				continue
			}
			gs.Stop, gs.StopRow = v, r
			return
		}
		return
	}
}

func (gs *Guess) inData(text string) bool {
	for _, r := range gs.DataRows {
		for _, v := range gs.grid[r] {
			if strings.Contains(v, text) {
				return true
			}
		}
	}
	return false
}

// commonLayouts returns the layouts that read every value, one per day/month order
// (the padded form when it fits).
func commonLayouts(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range guessLayouts {
		ok := len(values) > 0
		for _, v := range values {
			if v == "" {
				continue
			}
			if _, err := time.Parse(l, v); err != nil {
				ok = false
				break
			}
		}
		family := strings.NewReplacer("01", "1", "02", "2").Replace(l)
		if ok && !seen[family] {
			seen[family] = true
			out = append(out, l)
		}
	}
	return out
}

func (gs *Guess) values(c int) []string {
	var out []string
	for _, r := range gs.DataRows {
		out = append(out, cellAt(gs.grid[r], c))
	}
	return out
}

func (gs *Guess) kindOf(c int) ColumnKind {
	vals := gs.values(c)
	var filled []string
	for _, v := range vals {
		if v != "" {
			filled = append(filled, v)
		}
	}
	switch {
	case len(filled) == 0 || gs.Columns[c].Header == "":
		return ColumnEmpty
	case len(commonLayouts(filled)) > 0:
		return ColumnDate
	}
	ids := len(filled) == len(vals)
	seen := map[string]bool{}
	for _, v := range filled {
		if !isNumber(v) {
			return ColumnText
		}
		if !digitsOnly.MatchString(v) || seen[v] {
			ids = false
		}
		seen[v] = true
	}
	if ids && len(filled) > 1 {
		return ColumnID
	}
	return ColumnNumber
}

func samples(vals []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		if v != "" && !seen[v] && len(out) < 3 {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

var (
	commaDecimal = regexp.MustCompile(`,[0-9]{1,2}$`)
	dotDecimal   = regexp.MustCompile(`\.[0-9]{1,2}$`)
)

// findAmounts chooses a debit/credit pair (two number columns where each row fills
// exactly one), or else the number column filled in every row.
func (gs *Guess) findAmounts() {
	var nums []int
	for c, col := range gs.Columns {
		if col.Kind == ColumnNumber {
			nums = append(nums, c)
		}
	}
	var comma, dot bool
	for _, c := range nums {
		for _, v := range gs.values(c) {
			v = strings.TrimRight(v, " -)")
			comma = comma || commaDecimal.MatchString(v)
			dot = dot || dotDecimal.MatchString(v)
		}
	}
	gs.DecimalComma = comma && !dot
	nonZero := func(r, c int) bool {
		d, err := ParseAmount(cellAt(gs.grid[r], c), gs.DecimalComma)
		return err == nil && !d.IsZero()
	}
	for i, a := range nums {
		for _, b := range nums[i+1:] {
			pair := true
			for _, r := range gs.DataRows {
				if nonZero(r, a) == nonZero(r, b) {
					pair = false
					break
				}
			}
			if pair {
				gs.Debit, gs.Credit = a, b
				return
			}
		}
	}
	for _, c := range nums {
		full := true
		for _, r := range gs.DataRows {
			full = full && nonZero(r, c)
		}
		if full {
			gs.AmountChoice = append(gs.AmountChoice, c)
		}
	}
	if len(gs.AmountChoice) == 0 {
		gs.AmountChoice = nums
	}
	if len(gs.AmountChoice) > 0 {
		gs.Amount = gs.AmountChoice[0]
		gs.Negate = !gs.hasNegative(gs.Amount)
	}
}

func (gs *Guess) hasNegative(c int) bool {
	for _, r := range gs.DataRows {
		if d, err := ParseAmount(cellAt(gs.grid[r], c), gs.DecimalComma); err == nil && d.IsNegative() {
			return true
		}
	}
	return false
}

// findOthers chooses the payee (the text column with the most different values),
// the reference number and a charge date.
func (gs *Guess) findOthers() {
	most := 0
	for c, col := range gs.Columns {
		switch col.Kind {
		case ColumnText:
			seen := map[string]bool{}
			for _, v := range gs.values(c) {
				seen[v] = true
			}
			if len(seen) > most {
				most, gs.Payee = len(seen), c
			}
		case ColumnID:
			if gs.ExternalID < 0 {
				gs.ExternalID = c
			}
		case ColumnDate:
			if c != gs.Date && gs.ChargeDate < 0 {
				gs.ChargeDate = c
			}
		}
	}
}

// ColumnsOf returns the indexes of the columns of the given kinds.
func (gs *Guess) ColumnsOf(kinds ...ColumnKind) []int {
	var out []int
	for c, col := range gs.Columns {
		for _, k := range kinds {
			if col.Kind == k {
				out = append(out, c)
			}
		}
	}
	return out
}

// Unused returns the named columns that no field uses.
func (gs *Guess) Unused() []int {
	used := map[int]bool{}
	for _, c := range []int{gs.Amount, gs.ChargeDate, gs.Credit, gs.Currency, gs.Date, gs.Debit, gs.ExternalID, gs.Installment,
		gs.Memo, gs.OrigAmount, gs.OrigCurrency, gs.Payee, gs.SkipColumn} {
		used[c] = true
	}
	var out []int
	for c, col := range gs.Columns {
		if !used[c] && col.Kind != ColumnEmpty {
			out = append(out, c)
		}
	}
	return out
}

// Value returns the text of a column in a grid row.
func (gs *Guess) Value(row, c int) string { return cellAt(gs.grid[row], c) }

// SignSample returns a data row whose amount is positive, to ask what a positive
// amount means. ok is false when every amount is negative.
func (gs *Guess) SignSample() (row int, ok bool) {
	if gs.Amount < 0 {
		return 0, false
	}
	for _, r := range gs.DataRows {
		if d, err := ParseAmount(cellAt(gs.grid[r], gs.Amount), gs.DecimalComma); err == nil && d.IsPositive() {
			return r, true
		}
	}
	return 0, false
}

// CurrencyColumns returns the text columns that look like currencies: every value
// is at most three characters, without digits, such as "USD", "$" or "₪".
func (gs *Guess) CurrencyColumns() []int {
	var out []int
	for _, c := range gs.ColumnsOf(ColumnText) {
		ok := true
		for _, v := range gs.values(c) {
			if utf8.RuneCountInString(v) > 3 || strings.ContainsAny(v, "0123456789") {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	return out
}

// Distinct returns the different values of a column in the data rows, in order,
// with the number of rows of each.
func (gs *Guess) Distinct(c int) ([]string, map[string]int) {
	var order []string
	n := map[string]int{}
	for _, v := range gs.values(c) {
		if v == "" {
			continue
		}
		if n[v] == 0 {
			order = append(order, v)
		}
		n[v]++
	}
	return order, n
}

// StatusColumns returns text columns with a few repeated values, such as a
// status column that marks pending rows. The payee and currency columns are left out.
func (gs *Guess) StatusColumns() []int {
	currency := map[int]bool{}
	for _, c := range gs.CurrencyColumns() {
		currency[c] = true
	}
	var out []int
	for _, c := range gs.ColumnsOf(ColumnText) {
		vals, _ := gs.Distinct(c)
		if c != gs.Payee && !currency[c] && len(vals) >= 2 && len(vals) <= 5 && len(vals) < len(gs.DataRows) {
			out = append(out, c)
		}
	}
	return out
}

// installmentRe finds two numbers with one word between them, such as "3 of 12" or
// "3 מתוך 12". Dates and amounts have no word between their numbers.
var installmentRe = regexp.MustCompile(`(?:^|\s)([0-9]+)(\s+\pL+\s+)([0-9]+)(?:\s|$)`)

// findInstallment looks for a text column where some values say "payment N of M"
// in any language, and builds the pattern from the words between the numbers.
func (gs *Guess) findInstallment() {
	for _, c := range gs.ColumnsOf(ColumnText) {
		for _, r := range gs.DataRows {
			m := installmentRe.FindStringSubmatch(cellAt(gs.grid[r], c))
			if m == nil {
				continue
			}
			var n, of int
			if _, err := fmt.Sscan(m[1], &n); err != nil {
				continue
			}
			if _, err := fmt.Sscan(m[3], &of); err != nil {
				continue
			}
			if n >= 1 && of >= 2 && n <= of && of <= 120 {
				gs.Installment, gs.InstallmentRow = c, r
				gs.InstallmentPattern = `(\d+)` + regexp.QuoteMeta(m[2]) + `(\d+)`
				return
			}
		}
	}
}

// Profile writes the guess as a profile and checks it.
func (gs *Guess) Profile(name, description, encoding string) (string, error) {
	h := func(c int) string {
		if c < 0 {
			return ""
		}
		return gs.Columns[c].Header
	}
	p := Profile{Name: name, Description: description, Encoding: encoding}
	p.Columns = Columns{
		Amount: h(gs.Amount), ChargeDate: h(gs.ChargeDate), Credit: h(gs.Credit), Currency: h(gs.Currency), Date: h(gs.Date),
		DateFormat: gs.DateLayout, Debit: h(gs.Debit), DecimalComma: gs.DecimalComma, ExternalID: h(gs.ExternalID),
		Installment: h(gs.Installment), Memo: h(gs.Memo), OrigAmount: h(gs.OrigAmount), Payee: h(gs.Payee),
	}
	if p.Columns.Installment != "" {
		p.Columns.InstallmentPattern = gs.InstallmentPattern
	}
	if p.Columns.OrigAmount != "" {
		p.Columns.OrigCurrency = h(gs.OrigCurrency)
	}
	if gs.SkipColumn >= 0 && gs.SkipValue != "" {
		p.Table.Skip = []Rule{{Column: h(gs.SkipColumn), Contains: gs.SkipValue}}
	}
	if p.Columns.Currency != "" || p.Columns.OrigCurrency != "" {
		p.CurrencyMap = map[string]string{}
		for _, c := range []int{gs.Currency, gs.OrigCurrency} {
			if c < 0 {
				continue
			}
			for _, v := range gs.values(c) {
				if iso, ok := gs.CurrencyMap[v]; ok {
					p.CurrencyMap[v] = iso
				}
			}
		}
	}
	if gs.Amount >= 0 {
		p.Columns.Debit, p.Columns.Credit = "", ""
		p.Columns.Negate = gs.Negate
	} else {
		p.Columns.Amount = ""
	}
	if p.Columns.ChargeDate != "" && gs.Columns[gs.ChargeDate].Kind == ColumnDate {
		if vals := gs.values(gs.ChargeDate); !parsesAll(vals, gs.DateLayout) {
			if l := commonLayouts(vals); len(l) > 0 {
				p.Columns.ChargeDateFormat = l[0]
			}
		}
	}
	for _, c := range []string{p.Columns.Date, p.Columns.Payee, p.Columns.Amount, p.Columns.Debit, p.Columns.Credit} {
		if c != "" {
			p.Detect.Headers = append(p.Detect.Headers, c)
		}
	}
	p.Table.Stop = gs.Stop
	if p.headerRow(gs.grid) != gs.HeaderRow {
		p.Table.HeaderRow = gs.HeaderRow + 1
	}
	text := FormatProfile(&p)
	if _, err := ParseProfile(text); err != nil {
		return text, err
	}
	return text, nil
}

func parsesAll(values []string, layout string) bool {
	for _, v := range values {
		if _, err := time.Parse(layout, v); v != "" && err != nil {
			return false
		}
	}
	return true
}

// FormatProfile writes a profile as TOML, leaving out empty keys.
func FormatProfile(p *Profile) string {
	var b strings.Builder
	str := func(key, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s = %s\n", key, tomlString(v))
		}
	}
	boolean := func(key string, v bool) {
		if v {
			fmt.Fprintf(&b, "%s = true\n", key)
		}
	}
	str("name", p.Name)
	str("description", p.Description)
	str("currency", p.Currency)
	str("encoding", p.Encoding)
	b.WriteString("\n[detect]\n")
	quoted := make([]string, len(p.Detect.Headers))
	for i, h := range p.Detect.Headers {
		quoted[i] = tomlString(h)
	}
	fmt.Fprintf(&b, "headers = [%s]\n", strings.Join(quoted, ", "))
	str("file", p.Detect.File)
	t := p.Table
	if t.HeaderRow > 0 || t.Stop != "" || len(t.Skip) > 0 {
		b.WriteString("\n[table]\n")
		if t.HeaderRow > 0 {
			fmt.Fprintf(&b, "header_row = %d\n", t.HeaderRow)
		}
		str("stop", t.Stop)
		if len(t.Skip) > 0 {
			rules := make([]string, len(t.Skip))
			for i, r := range t.Skip {
				if r.Empty {
					rules[i] = fmt.Sprintf("{ column = %s, empty = true }", tomlString(r.Column))
				} else {
					rules[i] = fmt.Sprintf("{ column = %s, contains = %s }", tomlString(r.Column), tomlString(r.Contains))
				}
			}
			fmt.Fprintf(&b, "skip = [%s]\n", strings.Join(rules, ", "))
		}
	}
	c := p.Columns
	b.WriteString("\n[columns]\n")
	str("date", c.Date)
	str("date_format", c.DateFormat)
	str("charge_date", c.ChargeDate)
	str("charge_date_format", c.ChargeDateFormat)
	str("payee", c.Payee)
	str("memo", c.Memo)
	str("external_id", c.ExternalID)
	str("amount", c.Amount)
	boolean("negate", c.Negate)
	str("debit", c.Debit)
	str("credit", c.Credit)
	boolean("decimal_comma", c.DecimalComma)
	str("currency", c.Currency)
	str("orig_amount", c.OrigAmount)
	str("orig_currency", c.OrigCurrency)
	str("installment", c.Installment)
	if c.InstallmentPattern != "" && !strings.ContainsAny(c.InstallmentPattern, "'\n") {
		fmt.Fprintf(&b, "installment_pattern = '%s'\n", c.InstallmentPattern)
	} else {
		str("installment_pattern", c.InstallmentPattern)
	}
	if len(p.CurrencyMap) > 0 {
		b.WriteString("\n[currency_map]\n")
		keys := make([]string, 0, len(p.CurrencyMap))
		for k := range p.CurrencyMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s = %s\n", tomlString(k), tomlString(p.CurrencyMap[k]))
		}
	}
	return b.String()
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func cellAt(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return row[i]
}

// CommonEncodings are offered when a text file is not UTF-8.
var CommonEncodings = []string{
	"windows-1252", "windows-1250", "windows-1251", "windows-1253", "windows-1254", "windows-1255",
	"windows-1256", "windows-1257", "shift_jis", "gbk", "euc-kr", "big5",
}

// NeedsEncoding reports whether data is a text file that is not Unicode, so that
// reading it needs the right encoding.
func NeedsEncoding(data []byte) bool {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")), bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}),
		bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return false
	}
	return !utf8.Valid(data)
}

// FormatName names the file format that ReadBytes will use.
func FormatName(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return "XLSX"
	case bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0}):
		return "old binary XLS"
	}
	lower := strings.ToLower(string(data[:min(len(data), 4096)]))
	if strings.Contains(lower, "<table") || strings.Contains(lower, "<html") {
		return "HTML table"
	}
	return "CSV"
}

// SampleText returns the first row of g with non-ASCII text, to compare encodings.
func SampleText(g Grid) string {
	for i := 0; i < len(g) && i < 50; i++ {
		line := strings.Join(nonEmpty(g[i]), " | ")
		for _, r := range line {
			if r > unicode.MaxASCII {
				if utf8.RuneCountInString(line) > 70 {
					line = string([]rune(line)[:70]) + "…"
				}
				return line
			}
		}
	}
	return ""
}

func nonEmpty(row []string) []string {
	var out []string
	for _, v := range row {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// GridPreview shows the first rows of a grid, numbered from 1, for error messages.
func GridPreview(g Grid, rows int) string {
	var b strings.Builder
	for i := 0; i < len(g) && i < rows; i++ {
		line := strings.Join(g[i], " | ")
		if utf8.RuneCountInString(line) > 100 {
			line = string([]rune(line)[:100]) + "…"
		}
		fmt.Fprintf(&b, "\n  %d: %s", i+1, line)
	}
	return b.String()
}
