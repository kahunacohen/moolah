// Package prices downloads daily exchange rates from sources that are described in
// TOML (docs/rate-sources.md). No source is built into the code: the sources that
// ship with moolah are TOML files in prices/sources, and users add their own the
// same way. Only currency codes and dates are sent.
package prices

import (
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/shopspring/decimal"

	"moolah/core"
)

// ErrUnsupported means the source does not publish the requested pair.
var ErrUnsupported = errors.New("this source does not publish this pair")

// Fetcher downloads daily rates for one pair: 1 base = rate quote.
type Fetcher interface {
	Fetch(ctx context.Context, base, quote core.Currency, from, to core.Date) ([]core.Price, error)
	Name() core.PriceSource
	Supports(base, quote core.Currency) bool
}

// Source is a rate source described in TOML.
type Source struct {
	Currencies      []string `toml:"currencies"`  // currencies it publishes
	Date            string   `toml:"date"`        // CSV column or JSON path of the date in a row
	DateFormat      string   `toml:"date_format"` // Go layout; default 2006-01-02
	Description     string   `toml:"description"`
	Format          string   `toml:"format"`   // "json" or "csv"
	MaxDays         int      `toml:"max_days"` // longest date range per request; 0 = no limit
	SourceName      string   `toml:"name"`
	Quote           string   `toml:"quote"`              // when set, only rates X→quote; otherwise any pair among currencies
	Rate            string   `toml:"rate"`               // CSV column or JSON path of the rate in a row
	Rows            string   `toml:"rows"`               // JSON path to the list (or date-keyed object) of rows
	RowsKeyedByDate bool     `toml:"rows_keyed_by_date"` // JSON rows are an object whose keys are dates
	UnitPower       string   `toml:"unit_power"`         // CSV column or JSON path: the rate is per 10^n units
	URL             string   `toml:"url"`                // with {base} {quote} {from} {to}

	client     *http.Client
	currencies map[core.Currency]bool
}

var sourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ParseSource reads and validates a TOML rate source. Unknown keys are errors.
func ParseSource(text string) (*Source, error) {
	var s Source
	md, err := toml.Decode(text, &s)
	if err != nil {
		return nil, fmt.Errorf("rate source: %w", err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("rate source: unknown key %s (see docs/rate-sources.md)", strings.Join(keys, ", "))
	}
	return &s, s.validate()
}

func (s *Source) validate() error {
	name := s.SourceName
	switch {
	case !sourceNameRe.MatchString(name):
		return fmt.Errorf("rate source: name %q must use lower-case letters, digits, - and _", name)
	case name == string(core.SourceManual) || name == string(core.SourceTx):
		return fmt.Errorf("rate source: the name %q is reserved", name)
	case s.URL == "":
		return fmt.Errorf("rate source %s: url is required", name)
	case !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://"):
		return fmt.Errorf("rate source %s: url must start with https://", name)
	case s.Format != "json" && s.Format != "csv":
		return fmt.Errorf("rate source %s: format must be json or csv", name)
	case s.Rate == "":
		return fmt.Errorf("rate source %s: rate is required", name)
	case s.Date == "" && !s.RowsKeyedByDate:
		return fmt.Errorf("rate source %s: date is required (or rows_keyed_by_date = true for JSON)", name)
	case s.Format == "json" && s.Rows == "":
		return fmt.Errorf("rate source %s: rows is required for JSON", name)
	case s.Format == "csv" && (s.Rows != "" || s.RowsKeyedByDate):
		return fmt.Errorf("rate source %s: rows and rows_keyed_by_date are only for JSON", name)
	case len(s.Currencies) == 0:
		return fmt.Errorf("rate source %s: currencies is required", name)
	case s.MaxDays < 0:
		return fmt.Errorf("rate source %s: max_days cannot be negative", name)
	}
	s.currencies = map[core.Currency]bool{}
	for _, c := range s.Currencies {
		cur, err := core.ParseCurrency(c)
		if err != nil {
			return fmt.Errorf("rate source %s: currencies: %w", name, err)
		}
		s.currencies[cur] = true
	}
	if s.Quote != "" {
		q, err := core.ParseCurrency(s.Quote)
		if err != nil {
			return fmt.Errorf("rate source %s: quote: %w", name, err)
		}
		s.Quote = string(q)
	}
	return nil
}

// Name implements Fetcher.
func (s *Source) Name() core.PriceSource { return core.PriceSource(s.SourceName) }

// Supports implements Fetcher.
func (s *Source) Supports(base, quote core.Currency) bool {
	if base == quote || !s.currencies[base] {
		return false
	}
	if s.Quote != "" {
		return quote == core.Currency(s.Quote)
	}
	return s.currencies[quote]
}

// SetClient replaces the HTTP client. For tests.
func (s *Source) SetClient(c *http.Client) { s.client = c }

func expand(tmpl string, base, quote core.Currency, from, to core.Date) string {
	return strings.NewReplacer("{base}", string(base), "{quote}", string(quote), "{from}", from.String(), "{to}", to.String()).Replace(tmpl)
}

// Fetch implements Fetcher. It splits long date ranges when max_days is set.
func (s *Source) Fetch(ctx context.Context, base, quote core.Currency, from, to core.Date) ([]core.Price, error) {
	if !s.Supports(base, quote) {
		return nil, ErrUnsupported
	}
	var out []core.Price
	for start := from; !start.After(to); {
		end := to
		if s.MaxDays > 0 && start.AddDays(s.MaxDays-1).Before(to) {
			end = start.AddDays(s.MaxDays - 1)
		}
		ps, err := s.fetchRange(ctx, base, quote, start, end)
		if err != nil {
			return nil, err
		}
		out = append(out, ps...)
		start = end.AddDays(1)
	}
	return out, nil
}

var defaultClient = &http.Client{Timeout: 30 * time.Second}

func (s *Source) fetchRange(ctx context.Context, base, quote core.Currency, from, to core.Date) ([]core.Price, error) {
	u := expand(s.URL, base, quote, from, to)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "moolah (personal finance CLI)")
	client := s.client
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.SourceName, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.SourceName, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered HTTP %d", req.URL.Host, resp.StatusCode)
	}
	if t := strings.TrimSpace(string(body[:min(len(body), 64)])); strings.HasPrefix(t, "<") {
		return nil, ErrUnsupported // an HTML error page instead of data
	}
	var raw []rawRow
	if s.Format == "csv" {
		raw, err = s.csvRows(body, base, quote)
	} else {
		raw, err = s.jsonRows(body, base, quote)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w (check the source definition; the API may have changed)", s.SourceName, err)
	}
	var out []core.Price
	for _, r := range raw {
		layout := s.DateFormat
		if layout == "" {
			layout = "2006-01-02"
		}
		t, err := time.Parse(layout, strings.TrimSpace(r.date))
		if err != nil {
			continue
		}
		rate, err := decimal.NewFromString(strings.TrimSpace(r.rate))
		if err != nil || rate.Sign() <= 0 {
			continue
		}
		if r.power != "" {
			n, err := strconv.Atoi(strings.TrimSpace(r.power))
			if err != nil {
				return nil, fmt.Errorf("%s: unit_power value %q is not a number", s.SourceName, r.power)
			}
			rate = rate.Shift(int32(-n))
		}
		out = append(out, core.Price{Base: base, Date: core.DateOf(t), Quote: quote, Rate: rate, Source: s.Name()})
	}
	return out, nil
}

type rawRow struct{ date, power, rate string }

func (s *Source) csvRows(body []byte, base, quote core.Currency) ([]rawRow, error) {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(body), "\ufeff")))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[strings.TrimSpace(h)] = i
	}
	idx := func(name string) (int, error) {
		if name == "" {
			return -1, nil
		}
		i, ok := col[name]
		if !ok {
			return -1, fmt.Errorf("the answer has no column %q", name)
		}
		return i, nil
	}
	di, err := idx(expand(s.Date, base, quote, core.Date{}, core.Date{}))
	if err != nil {
		return nil, err
	}
	ri, err := idx(expand(s.Rate, base, quote, core.Date{}, core.Date{}))
	if err != nil {
		return nil, err
	}
	pi, err := idx(expand(s.UnitPower, base, quote, core.Date{}, core.Date{}))
	if err != nil {
		return nil, err
	}
	var out []rawRow
	for _, rec := range recs[1:] {
		if di >= len(rec) || ri >= len(rec) {
			continue
		}
		row := rawRow{date: rec[di], rate: rec[ri]}
		if pi >= 0 && pi < len(rec) {
			row.power = rec[pi]
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Source) jsonRows(body []byte, base, quote core.Currency) ([]rawRow, error) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	ex := func(p string) string { return expand(p, base, quote, core.Date{}, core.Date{}) }
	rows, ok := lookup(doc, ex(s.Rows))
	if !ok {
		return nil, nil // no data for this range (for example a weekend)
	}
	read := func(row any, date string) (rawRow, bool) {
		r := rawRow{date: date}
		if !s.RowsKeyedByDate {
			v, ok := lookup(row, ex(s.Date))
			if !ok {
				return r, false
			}
			r.date = scalar(v)
		}
		v, ok := lookup(row, ex(s.Rate))
		if !ok {
			return r, false
		}
		r.rate = scalar(v)
		if s.UnitPower != "" {
			if v, ok := lookup(row, ex(s.UnitPower)); ok {
				r.power = scalar(v)
			}
		}
		return r, true
	}
	var out []rawRow
	switch v := rows.(type) {
	case []any:
		if s.RowsKeyedByDate {
			return nil, fmt.Errorf("rows is a list, but rows_keyed_by_date = true")
		}
		for _, row := range v {
			if r, ok := read(row, ""); ok {
				out = append(out, r)
			}
		}
	case map[string]any:
		if !s.RowsKeyedByDate {
			return nil, fmt.Errorf("rows is an object; set rows_keyed_by_date = true if its keys are dates")
		}
		for date, row := range v {
			if r, ok := read(row, date); ok {
				out = append(out, r)
			}
		}
	default:
		return nil, fmt.Errorf("rows %q is not a list or an object", s.Rows)
	}
	return out, nil
}

// lookup follows a dot path ("data.rates", "FXUSDCAD.v") through JSON objects.
func lookup(v any, path string) (any, bool) {
	if path == "" || path == "." {
		return v, true
	}
	for _, key := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[key]; !ok {
			return nil, false
		}
	}
	return v, true
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

//go:embed sources/*.toml
var builtinFS embed.FS

// Builtin returns the TOML of the rate sources that ship with moolah, by name.
func Builtin() (map[string]string, error) {
	entries, err := builtinFS.ReadDir("sources")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := builtinFS.ReadFile("sources/" + e.Name())
		if err != nil {
			return nil, err
		}
		s, err := ParseSource(string(b))
		if err != nil {
			return nil, fmt.Errorf("built-in %s: %w", e.Name(), err)
		}
		out[s.SourceName] = string(b)
	}
	return out, nil
}
