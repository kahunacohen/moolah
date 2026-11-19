package app

import (
	"context"
	"os"

	"github.com/shopspring/decimal"

	"moolah/core"
	"moolah/importers"
)

// ProfileGuess is moolah's reading of an export file; see importers.Guess.
type ProfileGuess = importers.Guess

// Column kinds of a ProfileGuess.
const (
	ColumnDate   = importers.ColumnDate
	ColumnID     = importers.ColumnID
	ColumnNumber = importers.ColumnNumber
	ColumnText   = importers.ColumnText
)

// ExportFile is a bank or card export opened to create a profile from it.
type ExportFile struct {
	Encoding      string // chosen with SetEncoding; empty for Unicode files and spreadsheets
	Format        string // "CSV", "XLSX" or "HTML table"
	NeedsEncoding bool   // a text file that is not UTF-8: choose its encoding first
	Path          string

	data []byte
	grid importers.Grid
}

// EncodingSample is one line of a file read with one encoding.
type EncodingSample struct {
	Encoding string
	Text     string
}

// OpenExport reads an export file.
func OpenExport(path string) (*ExportFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &ExportFile{Format: importers.FormatName(data), NeedsEncoding: importers.NeedsEncoding(data), Path: path, data: data}
	if f.NeedsEncoding {
		f.Encoding = importers.DefaultEncoding
	}
	f.grid, err = importers.ReadBytes(data, f.Encoding)
	return f, err
}

// EncodingSamples shows the same line read with each common encoding, so the user
// can pick the readable one. Encodings that give the same text are shown once.
func (f *ExportFile) EncodingSamples() []EncodingSample {
	var out []EncodingSample
	seen := map[string]bool{}
	for _, enc := range importers.CommonEncodings {
		g, err := importers.ReadBytes(f.data, enc)
		if err != nil {
			continue
		}
		text := importers.SampleText(g)
		if text != "" && !seen[text] {
			seen[text] = true
			out = append(out, EncodingSample{Encoding: enc, Text: text})
		}
	}
	return out
}

// SetEncoding reads the file again with another encoding.
func (f *ExportFile) SetEncoding(enc string) error {
	if err := importers.ValidEncoding(enc); err != nil {
		return err
	}
	g, err := importers.ReadBytes(f.data, enc)
	if err != nil {
		return err
	}
	f.Encoding, f.grid = enc, g
	return nil
}

// Guess finds the header row, the data rows and the role of each column.
func (f *ExportFile) Guess() (*ProfileGuess, error) { return importers.Infer(f.grid) }

// PreviewRow is one row as the import would read it.
type PreviewRow struct {
	Amount decimal.Decimal
	Date   core.Date
	Line   int
	Payee  string
}

// ProfilePreview is the result of reading the file with a profile.
type ProfilePreview struct {
	Errors []string // rows that cannot be read
	Name   string   // the profile's name
	Rows   []PreviewRow
}

// Preview reads the file with a profile's TOML, as an import would. Nothing is saved.
func (f *ExportFile) Preview(text string) (ProfilePreview, error) {
	var out ProfilePreview
	p, err := importers.ParseProfile(text)
	if err != nil {
		return out, err
	}
	g := f.grid
	if p.Encoding != f.Encoding && f.NeedsEncoding {
		if g, err = importers.ReadBytes(f.data, p.Encoding); err != nil {
			return out, err
		}
	}
	out.Name = p.Name
	rows, bad, err := p.Parse(g)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, PreviewRow{Amount: r.Amount, Date: r.Date, Line: r.Line, Payee: r.Payee})
	}
	for _, b := range bad {
		out.Errors = append(out.Errors, b.Error())
	}
	return out, nil
}

// ProfileExists reports whether a profile with this name exists, and its TOML.
func (a *App) ProfileExists(ctx context.Context, name string) (string, bool, error) {
	texts, _, err := a.profileTexts(ctx)
	if err != nil {
		return "", false, err
	}
	t, ok := texts[name]
	return t, ok, nil
}

// ValidProfileName checks a profile name.
func ValidProfileName(name string) error { return importers.ValidProfileName(name) }
