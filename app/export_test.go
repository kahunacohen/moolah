// Tests for the journal export: every field of a transaction survives as text.
package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"moolah/core"
)

func TestJournal(t *testing.T) {
	tree := core.NewTree([]core.Account{
		{ID: 1, Name: "liabilities", Type: core.Liabilities},
		{ID: 2, Name: "card", ParentID: 1, Type: core.Liabilities, Currency: "ILS"},
		{ID: 3, Name: "expenses", Type: core.Expenses},
	})
	ils := func(s string) core.Amount { return core.NewAmount(decimal.RequireFromString(s), "ILS") }
	// The charge date, the details and a payee with journal syntax in it must not
	// be lost or break the line.
	t.Run("card purchase with details", func(t *testing.T) {
		var b bytes.Buffer
		err := writeJournal(&b, exportData{tree: tree, settings: map[string]string{}, transactions: []core.Transaction{{
			ChargeDate: core.MustDate("2026-11-10"), Date: core.MustDate("2026-10-03"), ID: 7, Kind: core.KindNormal,
			Meta: map[string]string{"orig": "38.00 EUR", "installment": "3/12"}, Payee: "Cafe; Bar | Grill",
			Postings: []core.Posting{{AccountID: 2, Amount: ils("-140.00")}, {AccountID: 3, Amount: ils("140.00")}},
		}}}, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"2026-10-03=2026-11-10 (7) Cafe, Bar / Grill\n",
			"    ; installment: 3/12\n    ; orig: 38.00 EUR\n",
			"    liabilities:card  -140.00 ILS\n",
		} {
			if !strings.Contains(b.String(), want) {
				t.Errorf("journal lacks %q:\n%s", want, b.String())
			}
		}
	})
}
