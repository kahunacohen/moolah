package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
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

func (t *table) csv(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(t.headers); err != nil {
		return err
	}
	for _, r := range t.rows {
		trimmed := make([]string, len(r))
		for i, c := range r {
			trimmed[i] = strings.TrimSpace(c)
		}
		if err := cw.Write(trimmed); err != nil {
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
