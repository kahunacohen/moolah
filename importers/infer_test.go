// Tests for guessing a profile from an export file that no profile describes yet.
package importers

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// bankCSV is a made-up bank export: title lines without separators, a blank line,
// debit/credit columns with decimal commas, a balance and a total row.
const bankCSV = "Account statement 12-345\nPrinted 05/11/2026\n\n" +
	"Booking date;Text;Reference;Debit;Credit;Balance\n" +
	"03/10/2026;SUPERMARKET;100234;123,40;;1.876,60\n" +
	"04/10/2026;SALARY ACME;100235;;3.500,00;5.376,60\n" +
	"07/10/2026;COFFEE;100236;3,50;;5.373,10\n" +
	";;;;;\nTotal;;;126,90;3.500,00;\n"

func mustInfer(t *testing.T, csv string) (*Guess, Grid) {
	t.Helper()
	g, err := ReadBytes([]byte(csv), "")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := Infer(g)
	if err != nil {
		t.Fatal(err)
	}
	return gs, g
}

func TestInfer(t *testing.T) {
	// A bank export is read without questions except the day/month order: title lines
	// are skipped, debit/credit and decimal commas are found, and the total row stops reading.
	t.Run("bank export with title lines and total", func(t *testing.T) {
		gs, g := mustInfer(t, bankCSV)
		h := func(c int) string {
			if c < 0 {
				return ""
			}
			return gs.Columns[c].Header
		}
		got := []string{h(gs.Date), h(gs.Payee), h(gs.Debit), h(gs.Credit), h(gs.ExternalID), gs.Stop}
		want := []string{"Booking date", "Text", "Debit", "Credit", "Reference", "Total"}
		if strings.Join(got, "|") != strings.Join(want, "|") || !gs.DecimalComma || len(gs.DataRows) != 3 {
			t.Fatalf("got %q, decimal comma %v, %d rows", got, gs.DecimalComma, len(gs.DataRows))
		}
		text, err := gs.Profile("my-bank", "", "")
		if err != nil {
			t.Fatalf("%v\n%s", err, text)
		}
		rows, bad, err := mustProfile(t, text).Parse(g)
		if err != nil || len(bad) != 0 || len(rows) != 3 {
			t.Fatalf("rows %d bad %v err %v\n%s", len(rows), bad, err, text)
		}
		if rows[0].Amount.String() != "-123.4" || rows[1].Amount.String() != "3500" || rows[0].ExternalID != "100234" {
			t.Fatalf("got %s, %s, %q", rows[0].Amount, rows[1].Amount, rows[0].ExternalID)
		}
	})
	// 03/10/2026 can be 3 October or 10 March, so both orders are offered; one day above 12 settles it.
	t.Run("day above 12 settles the date order", func(t *testing.T) {
		gs, _ := mustInfer(t, bankCSV)
		if len(gs.DateLayouts) != 2 {
			t.Fatalf("got %q", gs.DateLayouts)
		}
		gs, _ = mustInfer(t, strings.Replace(bankCSV, "07/10/2026", "25/10/2026", 1))
		if strings.Join(gs.DateLayouts, " ") != "02/01/2006" {
			t.Fatalf("got %q", gs.DateLayouts)
		}
	})
	// A card export shows charges as positive numbers, so the guess is that positive
	// means money out; a balance column filled in every row is offered as a second choice.
	t.Run("card export with positive charges and a charge date", func(t *testing.T) {
		gs, g := mustInfer(t, "Purchase date,Charge date,Merchant,Amount,Points\n"+
			"2026-10-03,2026-11-02,BOOKSHOP,45.90,4\n2026-10-05,2026-11-02,PETROL,250.00,25\n")
		if gs.Amount < 0 || gs.Columns[gs.Amount].Header != "Amount" || len(gs.AmountChoice) != 2 || !gs.Negate {
			t.Fatalf("amount %d choices %v negate %v", gs.Amount, gs.AmountChoice, gs.Negate)
		}
		if gs.ChargeDate < 0 || gs.Columns[gs.ChargeDate].Header != "Charge date" {
			t.Fatalf("charge date %d", gs.ChargeDate)
		}
		text, err := gs.Profile("my-card", "", "")
		if err != nil {
			t.Fatal(err)
		}
		rows, _, err := mustProfile(t, text).Parse(g)
		if err != nil || len(rows) != 2 || rows[0].Amount.String() != "-45.9" || rows[0].ChargeDate.String() != "2026-11-02" {
			t.Fatalf("rows %+v err %v\n%s", rows, err, text)
		}
	})
	// Card details are found without knowing the language: currency columns by their
	// short values, a status column by its few repeated values, and installments by
	// "N word M" (the pattern is built from the file's own word). Text with numbers is not an amount.
	t.Run("card details in any language", func(t *testing.T) {
		gs, g := mustInfer(t, "Date,Merchant,Orig,Cur,Charged,Status,Notes\n"+
			"2026-10-01,7-ELEVEN 12 ST,10.00,€,10.00,final,\n"+
			"2026-10-02,FURNITURE,900.00,€,75.00,final,Rate 3 de 12\n"+
			"2026-10-03,BOOKS,20.00,$,18.40,pending,\n")
		h := func(cols []int) string {
			var names []string
			for _, c := range cols {
				names = append(names, gs.Columns[c].Header)
			}
			return strings.Join(names, ",")
		}
		if got := h(gs.CurrencyColumns()); got != "Cur" {
			t.Fatalf("currency columns %q", got)
		}
		if got := h(gs.StatusColumns()); got != "Status" {
			t.Fatalf("status columns %q", got)
		}
		if gs.Installment < 0 || gs.Columns[gs.Installment].Header != "Notes" || gs.InstallmentPattern != `(\d+) de (\d+)` {
			t.Fatalf("installment %d %q", gs.Installment, gs.InstallmentPattern)
		}
		if got := h(gs.ColumnsOf(ColumnNumber)); got != "Orig,Charged" {
			t.Fatalf("number columns %q", got)
		}
		gs.Amount, gs.OrigAmount, gs.OrigCurrency = 4, 2, 3
		gs.CurrencyMap["€"], gs.CurrencyMap["$"] = "EUR", "USD"
		gs.SkipColumn, gs.SkipValue = 5, "pending"
		text, err := gs.Profile("my-card", "", "")
		if err != nil {
			t.Fatalf("%v\n%s", err, text)
		}
		rows, _, err := mustProfile(t, text).Parse(g)
		if err != nil || len(rows) != 2 || rows[1].InstallmentN != 3 || rows[1].InstallmentOf != 12 || rows[1].OrigCurrency != "EUR" {
			t.Fatalf("rows %+v err %v\n%s", rows, err, text)
		}
	})
	// A file without column names above dates and amounts cannot be guessed; the error says what to do instead.
	t.Run("no header row is an error", func(t *testing.T) {
		g, _ := ReadBytes([]byte("hello,world\nfoo,bar\n"), "")
		if _, err := Infer(g); err == nil || !strings.Contains(err.Error(), "docs/profiles.md") {
			t.Fatalf("got %v", err)
		}
	})
	// A text file in a legacy encoding is shown in each common encoding, so the user can pick the readable one.
	t.Run("legacy encoding needs a choice", func(t *testing.T) {
		data, err := charmap.Windows1255.NewEncoder().Bytes([]byte("תאריך,תיאור,סכום\n2026-10-01,קפה,-12\n"))
		if err != nil {
			t.Fatal(err)
		}
		if !NeedsEncoding(data) || NeedsEncoding([]byte("Date,Amount\n")) {
			t.Fatal("NeedsEncoding is wrong")
		}
		hebrew, _ := ReadBytes(data, "windows-1255")
		western, _ := ReadBytes(data, "windows-1252")
		if SampleText(hebrew) != "תאריך | תיאור | סכום" || SampleText(western) == SampleText(hebrew) {
			t.Fatalf("got %q and %q", SampleText(hebrew), SampleText(western))
		}
	})
}

func TestFormatProfile(t *testing.T) {
	// A written profile reads back as the same profile, including quotes in values.
	t.Run("round trip", func(t *testing.T) {
		p := mustProfile(t, cardProfile)
		p.Table.Stop = `סה"כ`
		again := mustProfile(t, FormatProfile(p))
		if FormatProfile(again) != FormatProfile(p) {
			t.Fatalf("differs:\n%s\n---\n%s", FormatProfile(p), FormatProfile(again))
		}
	})
}
