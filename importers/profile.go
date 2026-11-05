package importers

import (
	"embed"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/shopspring/decimal"

	"moolah/core"
)

// Profile describes one bank's or card's export layout. It is written in TOML;
// docs/profiles.md lists every key.
type Profile struct {
	Columns     Columns           `toml:"columns"`
	Currency    string            `toml:"currency"`     // currency of the amounts when there is no currency column
	CurrencyMap map[string]string `toml:"currency_map"` // values in currency columns → ISO codes, e.g. "₪" = "ILS"
	Description string            `toml:"description"`
	Detect      Detect            `toml:"detect"`
	Name        string            `toml:"name"`
	Table       Table             `toml:"table"`

	installment *regexp.Regexp
}

// Detect says how to recognize the file.
type Detect struct {
	File    string   `toml:"file"`    // optional file name pattern, e.g. "*.xlsx"
	Headers []string `toml:"headers"` // all of these must appear in one row (the header row)
}

// Table says where the data is.
type Table struct {
	HeaderRow int    `toml:"header_row"` // 1-based; 0 = the first row that has all detect.headers
	Skip      []Rule `toml:"skip"`       // skip a row when any rule matches (pending rows, sub-totals)
	Stop      string `toml:"stop"`       // stop at the first row with a cell that contains this text
}

// Rule matches a row by one column.
type Rule struct {
	Column   string `toml:"column"`
	Contains string `toml:"contains"` // the cell contains this text
	Empty    bool   `toml:"empty"`    // the cell is empty
}

// Columns maps header names to fields.
type Columns struct {
	Amount             string `toml:"amount"`      // signed amount: negative = money left the account
	ChargeDate         string `toml:"charge_date"` // card charge date
	ChargeDateFormat   string `toml:"charge_date_format"`
	Credit             string `toml:"credit"` // money in (instead of amount)
	Currency           string `toml:"currency"`
	Date               string `toml:"date"`
	DateFormat         string `toml:"date_format"` // Go layout, e.g. "02/01/2006"; Excel serial numbers always work
	DecimalComma       bool   `toml:"decimal_comma"`
	Debit              string `toml:"debit"` // money out (instead of amount)
	ExternalID         string `toml:"external_id"`
	Installment        string `toml:"installment"`
	InstallmentPattern string `toml:"installment_pattern"` // regex with two groups: payment number, number of payments
	Memo               string `toml:"memo"`
	Negate             bool   `toml:"negate"` // flip the sign of amount (card exports show charges as positive)
	OrigAmount         string `toml:"orig_amount"`
	OrigCurrency       string `toml:"orig_currency"`
	Payee              string `toml:"payee"`
}

// ParseProfile reads and validates a TOML profile. Unknown keys are errors.
func ParseProfile(text string) (*Profile, error) {
	var p Profile
	md, err := toml.Decode(text, &p)
	if err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("profile: unknown key %s (see docs/profiles.md)", strings.Join(keys, ", "))
	}
	return &p, p.validate()
}

var profileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

func (p *Profile) validate() error {
	c := p.Columns
	switch {
	case !profileNameRe.MatchString(p.Name):
		return fmt.Errorf("profile: name %q must use lower-case letters, digits, - and _", p.Name)
	case len(p.Detect.Headers) == 0:
		return fmt.Errorf("profile %s: [detect] headers is required", p.Name)
	case c.Date == "":
		return fmt.Errorf("profile %s: [columns] date is required", p.Name)
	case c.Payee == "":
		return fmt.Errorf("profile %s: [columns] payee is required", p.Name)
	case c.Amount == "" && c.Debit == "" && c.Credit == "":
		return fmt.Errorf("profile %s: [columns] needs amount, or debit and/or credit", p.Name)
	case c.Amount != "" && (c.Debit != "" || c.Credit != ""):
		return fmt.Errorf("profile %s: use either amount or debit/credit, not both", p.Name)
	case (c.Installment == "") != (c.InstallmentPattern == ""):
		return fmt.Errorf("profile %s: installment and installment_pattern go together", p.Name)
	case p.Table.HeaderRow < 0:
		return fmt.Errorf("profile %s: header_row must be 0 (auto) or a row number", p.Name)
	}
	if p.Currency != "" {
		if _, err := core.ParseCurrency(p.Currency); err != nil {
			return fmt.Errorf("profile %s: currency: %w", p.Name, err)
		}
	}
	for k, v := range p.CurrencyMap {
		if _, err := core.ParseCurrency(v); err != nil {
			return fmt.Errorf("profile %s: currency_map %q: %w", p.Name, k, err)
		}
	}
	if c.InstallmentPattern != "" {
		re, err := regexp.Compile(c.InstallmentPattern)
		if err != nil {
			return fmt.Errorf("profile %s: installment_pattern: %w", p.Name, err)
		}
		if re.NumSubexp() != 2 {
			return fmt.Errorf("profile %s: installment_pattern needs exactly two groups, for example 'תשלום (\\d+) מתוך (\\d+)'", p.Name)
		}
		p.installment = re
	}
	for _, r := range p.Table.Skip {
		if r.Column == "" || (r.Contains == "" && !r.Empty) {
			return fmt.Errorf("profile %s: each skip rule needs column, and contains or empty = true", p.Name)
		}
	}
	return nil
}

func norm(s string) string { return strings.ToLower(Clean(s)) }

// headerRow returns the 0-based index of the header row, or -1.
func (p *Profile) headerRow(g Grid) int {
	if p.Table.HeaderRow > 0 {
		if p.Table.HeaderRow <= len(g) {
			return p.Table.HeaderRow - 1
		}
		return -1
	}
	for i, row := range g {
		have := map[string]bool{}
		for _, c := range row {
			have[norm(c)] = true
		}
		ok := true
		for _, h := range p.Detect.Headers {
			if !have[norm(h)] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// Matches reports whether the profile recognizes the file.
func (p *Profile) Matches(g Grid, fileName string) bool {
	if p.Detect.File != "" {
		if ok, _ := path.Match(strings.ToLower(p.Detect.File), strings.ToLower(path.Base(fileName))); !ok {
			return false
		}
	}
	i := p.headerRow(g)
	if i < 0 {
		return false
	}
	have := map[string]bool{}
	for _, c := range g[i] {
		have[norm(c)] = true
	}
	for _, h := range p.Detect.Headers {
		if !have[norm(h)] {
			return false
		}
	}
	return true
}

// Row is one normalized transaction from a file.
type Row struct {
	Amount        decimal.Decimal
	ChargeDate    core.Date
	Currency      core.Currency // empty: the account's currency
	Date          core.Date
	ExternalID    string
	InstallmentN  int
	InstallmentOf int
	Line          int // 1-based line in the file
	Memo          string
	OrigAmount    decimal.Decimal
	OrigCurrency  core.Currency
	Payee         string
	Raw           []string
}

// RowError is a row that could not be read.
type RowError struct {
	Err  error
	Line int
}

func (e RowError) Error() string { return fmt.Sprintf("line %d: %v", e.Line, e.Err) }

// Parse converts the grid into rows. Rows that cannot be read are returned as errors,
// so one bad line does not stop the import.
func (p *Profile) Parse(g Grid) ([]Row, []RowError, error) {
	hi := p.headerRow(g)
	if hi < 0 {
		return nil, nil, fmt.Errorf("profile %s: the header row was not found", p.Name)
	}
	col := map[string]int{}
	for i, h := range g[hi] {
		if _, dup := col[norm(h)]; !dup {
			col[norm(h)] = i
		}
	}
	idx := func(name string) (int, error) {
		if name == "" {
			return -1, nil
		}
		i, ok := col[norm(name)]
		if !ok {
			return -1, fmt.Errorf("profile %s: the file has no column %q", p.Name, name)
		}
		return i, nil
	}
	c := p.Columns
	names := []string{c.Date, c.ChargeDate, c.Payee, c.Memo, c.ExternalID, c.Amount, c.Debit, c.Credit, c.Currency, c.OrigAmount, c.OrigCurrency, c.Installment}
	// Date, payee and amount columns are required; the others may be missing,
	// because exports of the same bank often differ a little.
	required := map[int]bool{0: true, 2: true, 5: true}
	cols := make([]int, len(names))
	for i, n := range names {
		var err error
		if cols[i], err = idx(n); err != nil && required[i] && n != "" {
			return nil, nil, err
		}
	}
	if c.Amount == "" && cols[6] < 0 && cols[7] < 0 {
		return nil, nil, fmt.Errorf("profile %s: the file has neither column %q nor %q", p.Name, c.Debit, c.Credit)
	}
	skipCols := make([]int, len(p.Table.Skip))
	for i, r := range p.Table.Skip {
		var err error
		if skipCols[i], err = idx(r.Column); err != nil {
			return nil, nil, err
		}
	}
	cell := func(row []string, i int) string {
		if i < 0 || i >= len(row) {
			return ""
		}
		return row[i]
	}
	var rows []Row
	var bad []RowError
rowLoop:
	for li := hi + 1; li < len(g); li++ {
		row := g[li]
		if isEmptyRow(row) {
			continue
		}
		if p.Table.Stop != "" {
			for _, v := range row {
				if strings.Contains(v, p.Table.Stop) {
					break rowLoop
				}
			}
		}
		for i, r := range p.Table.Skip {
			v := cell(row, skipCols[i])
			if (r.Empty && v == "") || (r.Contains != "" && strings.Contains(v, r.Contains)) {
				continue rowLoop
			}
		}
		out, err := p.parseRow(row, cols, cell)
		if err != nil {
			bad = append(bad, RowError{Err: err, Line: li + 1})
			continue
		}
		out.Line = li + 1
		out.Raw = row
		rows = append(rows, out)
	}
	return rows, bad, nil
}

func isEmptyRow(row []string) bool {
	for _, c := range row {
		if c != "" {
			return false
		}
	}
	return true
}

func (p *Profile) parseRow(row []string, cols []int, cell func([]string, int) string) (Row, error) {
	c := p.Columns
	var r Row
	var err error
	if r.Date, err = parseDate(cell(row, cols[0]), c.DateFormat); err != nil {
		return r, fmt.Errorf("date: %w", err)
	}
	if v := cell(row, cols[1]); v != "" {
		if r.ChargeDate, err = parseDate(v, firstNonEmpty(c.ChargeDateFormat, c.DateFormat)); err != nil {
			return r, fmt.Errorf("charge date: %w", err)
		}
	}
	r.Payee = cell(row, cols[2])
	r.Memo = cell(row, cols[3])
	r.ExternalID = cell(row, cols[4])
	if c.Amount != "" {
		if r.Amount, err = ParseAmount(cell(row, cols[5]), c.DecimalComma); err != nil {
			return r, fmt.Errorf("amount: %w", err)
		}
		if c.Negate {
			r.Amount = r.Amount.Neg()
		}
	} else {
		debit, err := ParseAmount(cell(row, cols[6]), c.DecimalComma)
		if err != nil {
			return r, fmt.Errorf("debit: %w", err)
		}
		credit, err := ParseAmount(cell(row, cols[7]), c.DecimalComma)
		if err != nil {
			return r, fmt.Errorf("credit: %w", err)
		}
		r.Amount = credit.Abs().Sub(debit.Abs())
	}
	if r.Amount.IsZero() {
		return r, fmt.Errorf("the amount is zero or empty")
	}
	if r.Currency, err = p.currency(cell(row, cols[8]), p.Currency); err != nil {
		return r, err
	}
	if v := cell(row, cols[9]); v != "" {
		if r.OrigAmount, err = ParseAmount(v, c.DecimalComma); err != nil {
			return r, fmt.Errorf("original amount: %w", err)
		}
		if r.OrigCurrency, err = p.currency(cell(row, cols[10]), ""); err != nil {
			return r, err
		}
	}
	if p.installment != nil {
		if m := p.installment.FindStringSubmatch(cell(row, cols[11])); m != nil {
			r.InstallmentN, _ = strconv.Atoi(m[1])
			r.InstallmentOf, _ = strconv.Atoi(m[2])
		}
	}
	return r, nil
}

func (p *Profile) currency(v, def string) (core.Currency, error) {
	if v == "" {
		v = def
	}
	if v == "" {
		return "", nil
	}
	if mapped, ok := p.CurrencyMap[v]; ok {
		v = mapped
	}
	c, err := core.ParseCurrency(v)
	if err != nil {
		return "", fmt.Errorf("currency %q: add it to [currency_map] in the profile", v)
	}
	return c, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var defaultDateLayouts = []string{"2006-01-02", "02/01/2006", "02.01.2006", "02-01-2006", "02/01/06", "02.01.06", "02-01-06"}

// parseDate parses with the profile's layout, or common day-first layouts, or an
// Excel serial number.
func parseDate(s, layout string) (core.Date, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return core.Date{}, fmt.Errorf("empty")
	}
	layouts := defaultDateLayouts
	if layout != "" {
		layouts = []string{layout}
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return core.DateOf(t), nil
		}
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil && n > 20000 && n < 80000 {
		base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
		return core.DateOf(base.AddDate(0, 0, int(n))), nil
	}
	if layout != "" {
		return core.Date{}, fmt.Errorf("%q does not match date_format %q", s, layout)
	}
	return core.Date{}, fmt.Errorf("%q is not a date; set date_format in the profile", s)
}

var amountCleaner = strings.NewReplacer("₪", "", "$", "", "€", "", "£", "", "NIS", "", "ILS", "", " ", "", "−", "-")

// ParseAmount reads "1,234.56", "-50", "50-", "(50.00)", "₪ 1,234" and, with
// decimalComma, "1.234,56". An empty cell is zero.
func ParseAmount(s string, decimalComma bool) (decimal.Decimal, error) {
	s = amountCleaner.Replace(strings.TrimSpace(s))
	if s == "" {
		return decimal.Zero, nil
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg, s = true, s[1:len(s)-1]
	}
	if strings.HasSuffix(s, "-") {
		neg, s = !neg, strings.TrimSuffix(s, "-")
	}
	if decimalComma {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%q is not a number", s)
	}
	if neg {
		d = d.Neg()
	}
	return d, nil
}

//go:embed profiles/*.toml
var builtinFS embed.FS

// Builtin returns the profiles shipped with moolah, by name.
func Builtin() (map[string]string, error) {
	entries, err := builtinFS.ReadDir("profiles")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := builtinFS.ReadFile("profiles/" + e.Name())
		if err != nil {
			return nil, err
		}
		p, err := ParseProfile(string(b))
		if err != nil {
			return nil, fmt.Errorf("built-in %s: %w", e.Name(), err)
		}
		out[p.Name] = string(b)
	}
	return out, nil
}

// DetectProfiles returns the names of the profiles that recognize the file, sorted.
func DetectProfiles(profiles map[string]*Profile, g Grid, fileName string) []string {
	var out []string
	for name, p := range profiles {
		if p.Matches(g, fileName) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Fingerprints identify rows across imports, so overlapping files do not create
// duplicates. A bank ID is used when there is one; otherwise date, amount, currency
// and payee, plus the position among identical rows in the same file (two equal
// coffees on one day stay two rows).
func Fingerprints(rows []Row) []string {
	seen := map[string]int{}
	out := make([]string, len(rows))
	for i, r := range rows {
		if r.ExternalID != "" {
			out[i] = "id|" + r.ExternalID
			continue
		}
		key := strings.Join([]string{r.Date.String(), r.Amount.String(), string(r.Currency), norm(r.Payee)}, "|")
		seen[key]++
		out[i] = key + "|" + strconv.Itoa(seen[key])
	}
	return out
}

// MatchRule chooses an account for imported rows.
type MatchRule struct {
	AccountID int64
	ID        int64
	MaxAmount decimal.NullDecimal // compared with the absolute amount
	MinAmount decimal.NullDecimal
	Pattern   *regexp.Regexp // matched against payee and memo
	Tags      []string
}

// CompilePattern compiles a rule pattern, case-insensitive.
func CompilePattern(p string) (*regexp.Regexp, error) {
	re, err := regexp.Compile("(?i)" + p)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", p, err)
	}
	return re, nil
}

// FirstMatch returns the first rule that matches, or nil.
func FirstMatch(rules []MatchRule, payee, memo string, amount decimal.Decimal) *MatchRule {
	abs := amount.Abs()
	for i := range rules {
		r := &rules[i]
		if !r.Pattern.MatchString(payee) && (memo == "" || !r.Pattern.MatchString(memo)) {
			continue
		}
		if r.MinAmount.Valid && abs.LessThan(r.MinAmount.Decimal) {
			continue
		}
		if r.MaxAmount.Valid && abs.GreaterThan(r.MaxAmount.Decimal) {
			continue
		}
		return r
	}
	return nil
}
