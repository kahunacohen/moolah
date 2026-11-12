package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// table prints aligned columns. Columns listed in right are right-aligned.
type table struct {
	headers []string
	right   map[int]bool
	rows    [][]string
}

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

func (t *table) print(w io.Writer) error {
	widths := make([]int, len(t.headers))
	for _, r := range append([][]string{t.headers}, t.rows...) {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	line := func(cells []string) string {
		var b strings.Builder
		for i, c := range cells {
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c))
			if i > 0 {
				b.WriteString("  ")
			}
			if t.right[i] {
				b.WriteString(pad + c)
			} else if i < len(cells)-1 {
				b.WriteString(c + pad)
			} else {
				b.WriteString(c)
			}
		}
		return strings.TrimRight(b.String(), " ") + "\n"
	}
	if _, err := io.WriteString(w, line(t.headers)); err != nil {
		return err
	}
	for _, r := range t.rows {
		if _, err := io.WriteString(w, line(r)); err != nil {
			return err
		}
	}
	return nil
}

// csvAmount is one amount as a cell shows it in CSV mode: "-1234.50 EUR".
var csvAmount = regexp.MustCompile(`^(-?[0-9]+(?:\.[0-9]+)?) ([A-Z]{3})$`)

// csv writes the table as CSV. A column whose cells are all single amounts becomes
// two columns, the number and the currency, so spreadsheets can add them up.
func (t *table) csv(w io.Writer) error {
	split := make([]bool, len(t.headers))
	for i := range t.headers {
		for _, r := range t.rows {
			c := strings.TrimSpace(r[i])
			if c == "" || c == "0" {
				continue
			}
			if !csvAmount.MatchString(c) {
				split[i] = false
				break
			}
			split[i] = true
		}
	}
	line := func(cells []string, header bool) []string {
		var out []string
		for i, c := range cells {
			c = strings.TrimSpace(c)
			switch {
			case !split[i]:
				out = append(out, c)
			case header:
				out = append(out, c, c+" currency")
			default:
				m := csvAmount.FindStringSubmatch(c)
				if m == nil {
					out = append(out, c, "")
				} else {
					out = append(out, m[1], m[2])
				}
			}
		}
		return out
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(line(t.headers, true)); err != nil {
		return err
	}
	for _, r := range t.rows {
		if err := cw.Write(line(r, false)); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// render prints a table in the selected format, or v as JSON.
func (g *globals) render(t *table, v any) error {
	if err := g.checkFormat(); err != nil {
		return err
	}
	switch g.format {
	case "json":
		return printJSON(g.out(), v)
	case "csv":
		return t.csv(g.out())
	}
	return t.print(g.out())
}

// count formats a number with a singular or plural noun: "1 transaction", "3 transactions".
func count(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// plural returns singular when n is 1, else plural: a verb that agrees with count.
func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
