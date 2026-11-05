// Tests for reading export files, TOML profiles, duplicate fingerprints and rules.
package importers

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"

	"moolah/core"
)

// bankProfile is a made-up Hebrew bank layout: debit/credit columns, a title row, a total row.
const bankProfile = `
name = "test-bank"
[detect]
headers = ["תאריך", "תיאור", "חובה", "זכות"]
[table]
stop = "סה\"כ"
[columns]
date = "תאריך"
date_format = "02/01/2006"
payee = "תיאור"
debit = "חובה"
credit = "זכות"
external_id = "אסמכתא"
`

// cardProfile is a made-up card layout: charges as positive numbers, foreign
// purchases with an original amount, installments, and pending rows to skip.
const cardProfile = `
name = "test-card"
currency = "ILS"
[detect]
headers = ["תאריך עסקה", "שם בית העסק", "סכום חיוב"]
[table]
skip = [{ column = "סטטוס", contains = "בקליטה" }]
[columns]
date = "תאריך עסקה"
date_format = "02-01-2006"
charge_date = "תאריך חיוב"
payee = "שם בית העסק"
amount = "סכום חיוב"
negate = true
orig_amount = "סכום עסקה"
orig_currency = "מטבע עסקה"
installment = "הערות"
installment_pattern = 'תשלום (\d+) מתוך (\d+)'
[currency_map]
"₪" = "ILS"
"$" = "USD"
`

func mustProfile(t *testing.T, text string) *Profile {
	t.Helper()
	p, err := ParseProfile(text)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReaders(t *testing.T) {
	// Hebrew CSV files in Windows-1255 are decoded, and bidi marks are removed from cells.
	t.Run("windows-1255 csv", func(t *testing.T) {
		text := "דוח תנועות\n\u200fתאריך,תיאור,חובה,זכות,אסמכתא\n03/10/2026,שופרסל,\"1,200.50\",,111\n05/10/2026,משכורת,,18500,112\nסה\"כ,,1200.50,18500,\n"
		enc, err := charmap.Windows1255.NewEncoder().String(text)
		if err != nil {
			t.Fatal(err)
		}
		g, err := ReadBytes([]byte(enc))
		if err != nil {
			t.Fatal(err)
		}
		if g[1][0] != "תאריך" || g[2][1] != "שופרסל" {
			t.Fatalf("got %q", g[1])
		}
		rows, bad, err := mustProfile(t, bankProfile).Parse(g)
		if err != nil || len(bad) != 0 || len(rows) != 2 {
			t.Fatalf("rows %+v bad %v err %v", rows, bad, err)
		}
		if !rows[0].Amount.Equal(decimal.RequireFromString("-1200.50")) || !rows[1].Amount.Equal(decimal.NewFromInt(18500)) {
			t.Fatalf("debit/credit gave %s and %s", rows[0].Amount, rows[1].Amount)
		}
	})
	// Many banks save an HTML table with an .xls name; it is read like a sheet.
	t.Run("html saved as xls", func(t *testing.T) {
		page := `<html><body><table><tr><th>תאריך</th><th>תיאור</th><th>חובה</th><th>זכות</th></tr>
			<tr><td>03/10/2026</td><td>רמי <b>לוי</b></td><td>85.00</td><td></td></tr></table></body></html>`
		g, err := ReadBytes([]byte(page))
		if err != nil {
			t.Fatal(err)
		}
		rows, _, err := mustProfile(t, bankProfile).Parse(g)
		if err != nil || len(rows) != 1 || rows[0].Payee != "רמי לוי" {
			t.Fatalf("got %+v, %v", rows, err)
		}
	})
	// XLSX card exports: charges become negative, pending rows are skipped, and the
	// original foreign amount and installments are kept.
	t.Run("xlsx card export", func(t *testing.T) {
		f := excelize.NewFile()
		data := [][]any{
			{"פירוט עסקאות"},
			{"תאריך עסקה", "שם בית העסק", "סכום עסקה", "מטבע עסקה", "סכום חיוב", "תאריך חיוב", "הערות", "סטטוס"},
			{"20-09-2026", "AMAZON", "38.00", "$", "141.20", "10-10-2026", "", ""},
			{"21-09-2026", "מקרר", "1200", "₪", "100.00", "10-10-2026", "תשלום 3 מתוך 12", ""},
			{"04-11-2026", "קפה", "12", "₪", "12.00", "", "", "בקליטה"},
		}
		for i, r := range data {
			cell, _ := excelize.CoordinatesToCellName(1, i+1)
			if err := f.SetSheetRow("Sheet1", cell, &r); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(t.TempDir(), "card.xlsx")
		if err := f.SaveAs(path); err != nil {
			t.Fatal(err)
		}
		g, err := ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p := mustProfile(t, cardProfile)
		if !p.Matches(g, "card.xlsx") {
			t.Fatal("profile did not recognize the file")
		}
		rows, bad, err := p.Parse(g)
		if err != nil || len(bad) != 0 || len(rows) != 2 {
			t.Fatalf("rows %+v bad %v err %v", rows, bad, err)
		}
		a, b := rows[0], rows[1]
		if !a.Amount.Equal(decimal.RequireFromString("-141.20")) || a.OrigCurrency != "USD" || a.ChargeDate != core.MustDate("2026-10-10") {
			t.Fatalf("foreign row %+v", a)
		}
		if b.InstallmentN != 3 || b.InstallmentOf != 12 {
			t.Fatalf("installment row %+v", b)
		}
	})
	// Old binary XLS cannot be read; the error says how to convert it.
	t.Run("binary xls explains the fix", func(t *testing.T) {
		_, err := ReadBytes([]byte{0xD0, 0xCF, 0x11, 0xE0, 0, 0})
		if err == nil || !strings.Contains(err.Error(), ".xlsx") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestProfiles(t *testing.T) {
	// Profiles are strict: a misspelled key is an error, not silently ignored.
	t.Run("unknown key rejected", func(t *testing.T) {
		_, err := ParseProfile(strings.Replace(bankProfile, "payee =", "payeee =", 1))
		if err == nil || !strings.Contains(err.Error(), "payeee") {
			t.Fatalf("got %v", err)
		}
	})
	// Every built-in profile must load.
	t.Run("built-ins are valid", func(t *testing.T) {
		b, err := Builtin()
		if err != nil || len(b) == 0 {
			t.Fatalf("got %d, %v", len(b), err)
		}
	})
	// A row that cannot be read is reported with its line; the other rows still import.
	t.Run("bad row reported with line", func(t *testing.T) {
		g := Grid{{"תאריך", "תיאור", "חובה", "זכות"}, {"32/13/2026", "x", "1", ""}, {"01/10/2026", "y", "2", ""}}
		rows, bad, err := mustProfile(t, bankProfile).Parse(g)
		if err != nil || len(rows) != 1 || len(bad) != 1 || bad[0].Line != 2 {
			t.Fatalf("rows %d bad %+v err %v", len(rows), bad, err)
		}
	})
}

func TestAmounts(t *testing.T) {
	cases := []struct {
		in    string
		comma bool
		want  string
	}{
		{"1,234.56", false, "1234.56"},
		{"₪ 1,234", false, "1234"},
		{"50.00-", false, "-50"},
		{"(50.00)", false, "-50"},
		{"1.234,56", true, "1234.56"},
		{"", false, "0"},
	}
	for _, c := range cases {
		// Bank number formats (symbols, trailing minus, parentheses, decimal comma) read as the same value.
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseAmount(c.in, c.comma)
			if err != nil || !got.Equal(decimal.RequireFromString(c.want)) {
				t.Fatalf("got %s, %v", got, err)
			}
		})
	}
}

func TestFingerprints(t *testing.T) {
	// Two identical purchases on one day stay two rows, and the same file twice gives the same fingerprints.
	t.Run("identical rows stay distinct", func(t *testing.T) {
		r := Row{Date: core.MustDate("2026-10-01"), Amount: decimal.NewFromInt(-12), Payee: "Coffee"}
		fp := Fingerprints([]Row{r, r})
		if fp[0] == fp[1] {
			t.Fatal("same fingerprint for two rows")
		}
		again := Fingerprints([]Row{r, r})
		if fp[0] != again[0] || fp[1] != again[1] {
			t.Fatal("fingerprints are not stable")
		}
	})
}

func TestRules(t *testing.T) {
	re, _ := CompilePattern("shufersal|שופרסל")
	fuel, _ := CompilePattern("paz")
	rules := []MatchRule{
		{ID: 1, Pattern: fuel, AccountID: 20, MaxAmount: decimal.NewNullDecimal(decimal.NewFromInt(500))},
		{ID: 2, Pattern: re, AccountID: 10},
	}
	// Patterns ignore case, amount limits apply to the absolute amount, and the first match wins.
	t.Run("first match with limits", func(t *testing.T) {
		if m := FirstMatch(rules, "SHUFERSAL DEAL", "", decimal.NewFromInt(-200)); m == nil || m.ID != 2 {
			t.Fatalf("got %+v", m)
		}
		if m := FirstMatch(rules, "Paz Yellow", "", decimal.NewFromInt(-900)); m != nil {
			t.Fatalf("amount limit ignored: %+v", m)
		}
	})
}
