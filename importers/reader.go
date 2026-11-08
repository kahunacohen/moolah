// Package importers turns bank and card export files into normalized rows. Readers
// handle file formats (CSV in any encoding, XLSX, HTML tables saved as .xls);
// TOML profiles describe each bank's layout; rules choose categories. Nothing here
// touches the database.
package importers

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"golang.org/x/net/html"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
)

// Grid is a table of cells, row by row. Cells are trimmed and free of bidi marks.
type Grid [][]string

// DefaultEncoding decodes text files that are not UTF-8 when a profile names no
// encoding. Windows-1252 covers most Western European languages.
const DefaultEncoding = "windows-1252"

// ValidEncoding checks an encoding name such as "windows-1255", "iso-8859-8" or "shift_jis".
func ValidEncoding(name string) error {
	if _, err := htmlindex.Get(name); err != nil {
		return fmt.Errorf("unknown encoding %q: use a name such as utf-8, windows-1252, windows-1255, iso-8859-8 or shift_jis", name)
	}
	return nil
}

// ReadFile reads a CSV, XLSX or HTML-as-XLS file into a grid (the first sheet or
// table). encoding is used for text files that are not UTF-8 (empty: DefaultEncoding).
func ReadFile(path, encoding string) (Grid, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ReadBytes(data, encoding)
}

// ReadBytes detects the format from the content, not the file name.
func ReadBytes(data []byte, encoding string) (Grid, error) {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return readXLSX(data)
	case bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0}):
		return nil, errors.New("this is an old binary Excel file (.xls), which moolah cannot read. Open it in a spreadsheet program and save it as .xlsx or .csv")
	}
	text, err := decodeText(data, encoding)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(text[:min(len(text), 4096)])
	if strings.Contains(lower, "<table") || strings.Contains(lower, "<html") {
		return readHTML(text)
	}
	return readCSV(text)
}

// decodeText returns UTF-8 text. UTF-8 (with or without BOM) and UTF-16 with a BOM
// are recognized; other bytes are decoded with the given encoding.
func decodeText(data []byte, encoding string) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return string(data[3:]), nil
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		out, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder().Bytes(data)
		return string(out), err
	case utf8.Valid(data):
		return string(data), nil
	}
	if encoding == "" {
		encoding = DefaultEncoding
	}
	enc, err := htmlindex.Get(encoding)
	if err != nil {
		return "", ValidEncoding(encoding)
	}
	out, err := enc.NewDecoder().Bytes(data)
	return string(out), err
}

func readCSV(text string) (Grid, error) {
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	}
	delim := ','
	best := strings.Count(first, ",")
	for _, d := range []rune{';', '\t', '|'} {
		if n := strings.Count(first, string(d)); n > best {
			delim, best = d, n
		}
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read CSV: %w", err)
	}
	return clean(recs), nil
}

func readXLSX(data []byte) (Grid, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("read XLSX: %w", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("the XLSX file has no sheets")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("read XLSX: %w", err)
	}
	return clean(rows), nil
}

// readHTML reads the largest table of an HTML page (many banks export
// HTML tables with an .xls name).
func readHTML(text string) (Grid, error) {
	doc, err := html.Parse(strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("read HTML: %w", err)
	}
	var best [][]string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" {
			if rows := tableRows(n); len(rows) > len(best) {
				best = rows
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(best) == 0 {
		return nil, errors.New("the HTML file has no table")
	}
	return clean(best), nil
}

func tableRows(table *html.Node) [][]string {
	var rows [][]string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" && n != table {
			return // nested tables are read on their own
		}
		if n.Type == html.ElementNode && n.Data == "tr" {
			var row []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
					row = append(row, nodeText(c))
				}
			}
			rows = append(rows, row)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(table)
	return rows
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "br" {
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

var cellCleaner = strings.NewReplacer("\u200e", "", "\u200f", "", "\u202a", "", "\u202b", "", "\u202c", "",
	"\u00a0", " ", "\ufeff", "", "\r", " ", "\n", " ", "\t", " ")

// Clean normalizes one cell: no bidi marks, single spaces, trimmed.
func Clean(s string) string {
	return strings.Join(strings.Fields(cellCleaner.Replace(s)), " ")
}

func clean(rows [][]string) Grid {
	out := make(Grid, 0, len(rows))
	for _, r := range rows {
		row := make([]string, len(r))
		for i, c := range r {
			row[i] = Clean(c)
		}
		out = append(out, row)
	}
	return out
}
