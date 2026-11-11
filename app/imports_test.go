// Tests for importing bank files: duplicates, rules, transfers, review, undo and reconcile.
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moolah/core"
)

func csvFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "export.csv")
	text := "Date,Description,Amount\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func importOK(t *testing.T, a *App, p ImportParams) ImportResult {
	t.Helper()
	res, err := a.Import(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) > 0 {
		t.Fatalf("row errors: %v", res.Errors)
	}
	return res
}

func TestImport(t *testing.T) {
	// Overlapping exports are safe: rows seen before are skipped, but two equal
	// purchases on one day in one file stay two transactions.
	t.Run("duplicates skipped across files", func(t *testing.T) {
		a := newBook(t)
		first := importOK(t, a, ImportParams{File: csvFile(t, "2026-10-01,Coffee,-12", "2026-10-01,Coffee,-12", "2026-10-02,Shufersal,-200"), Account: "leumi"})
		if first.New != 3 || first.Uncategorized != 3 || first.Profile != "generic-csv" {
			t.Fatalf("first %+v", first)
		}
		second := importOK(t, a, ImportParams{File: csvFile(t, "2026-10-02,Shufersal,-200", "2026-10-03,Rami Levy,-80")})
		if second.New != 1 || second.Duplicates != 1 {
			t.Fatalf("second %+v", second)
		}
	})
	// A rule added later categorizes earlier uncategorized rows, and later imports use it directly.
	t.Run("rules categorize", func(t *testing.T) {
		a := newBook(t)
		importOK(t, a, ImportParams{File: csvFile(t, "2026-10-02,SHUFERSAL DEAL 123,-200"), Account: "leumi"})
		_, n, err := a.AddRule(ctx, RuleParams{Pattern: "shufersal", Account: "food:groceries", Tags: []string{"weekly"}})
		if err != nil || n != 1 {
			t.Fatalf("applied %d, %v", n, err)
		}
		res := importOK(t, a, ImportParams{File: csvFile(t, "2026-10-09,SHUFERSAL DEAL 456,-150"), Account: "leumi"})
		if res.Uncategorized != 0 || res.Rows[0].Category != "food:groceries" {
			t.Fatalf("got %+v", res.Rows)
		}
		items, _ := a.Review(ctx)
		if len(items) != 0 {
			t.Fatalf("%d left to review", len(items))
		}
	})
	// A transfer between own accounts appears in both exports but is recorded once.
	t.Run("transfer from both sides is recorded once", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error {
			_, err := a.AddAccount(ctx, AddAccountParams{Path: "assets:bank:savings", Currency: "ILS"})
			return err
		})
		mustDo(t, func() error {
			_, _, err := a.AddRule(ctx, RuleParams{Pattern: "to savings", Account: "assets:bank:savings"})
			return err
		})
		mustDo(t, func() error {
			_, _, err := a.AddRule(ctx, RuleParams{Pattern: "from leumi", Account: "assets:bank:leumi"})
			return err
		})
		importOK(t, a, ImportParams{File: csvFile(t, "2026-10-05,Transfer to savings,-5000"), Account: "leumi"})
		res := importOK(t, a, ImportParams{File: csvFile(t, "2026-10-06,Transfer from leumi,5000"), Account: "savings"})
		if res.Matched != 1 || res.New != 0 {
			t.Fatalf("got %+v", res)
		}
		reg, _ := a.Register(ctx, RegisterParams{Account: "savings"})
		if len(reg.Rows) != 1 {
			t.Fatalf("%d transactions on savings", len(reg.Rows))
		}
	})
	// Two equal transfers a few days apart are each paired with the closest row
	// from the other account's export, so dates stay right.
	t.Run("equal transfers pair by closest date", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error {
			_, err := a.AddAccount(ctx, AddAccountParams{Path: "assets:bank:savings", Currency: "ILS"})
			return err
		})
		mustDo(t, func() error {
			_, _, err := a.AddRule(ctx, RuleParams{Pattern: "to savings", Account: "assets:bank:savings"})
			return err
		})
		mustDo(t, func() error {
			_, _, err := a.AddRule(ctx, RuleParams{Pattern: "from leumi", Account: "assets:bank:leumi"})
			return err
		})
		importOK(t, a, ImportParams{File: csvFile(t, "2026-10-02,Transfer to savings,-500", "2026-10-05,Transfer to savings,-500"), Account: "leumi"})
		importOK(t, a, ImportParams{File: csvFile(t, "2026-10-06,Transfer from leumi,500"), Account: "savings"})
		reg, err := a.Register(ctx, RegisterParams{Account: "savings"})
		if err != nil || len(reg.Rows) != 2 {
			t.Fatalf("%+v %v", reg, err)
		}
		tree, err := a.Tree(ctx)
		if err != nil {
			t.Fatal(err)
		}
		savings, err := realAccount(tree, "savings")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range reg.Rows {
			linked, err := a.st.ImportedInto(ctx, savings.ID, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if want := r.Date.String() == "2026-10-05"; linked != want {
				t.Errorf("transfer on %s linked: %v", r.Date, linked)
			}
		}
	})
	// Undo removes what an import created, and the same file can be imported again.
	t.Run("undo allows import again", func(t *testing.T) {
		a := newBook(t)
		file := csvFile(t, "2026-10-01,Coffee,-12")
		res := importOK(t, a, ImportParams{File: file, Account: "leumi"})
		n, err := a.UndoImport(ctx, res.Batch)
		if err != nil || n != 1 {
			t.Fatalf("undo %d, %v", n, err)
		}
		again := importOK(t, a, ImportParams{File: file, Account: "leumi"})
		if again.New != 1 {
			t.Fatalf("again %+v", again)
		}
	})
	// The account a profile's files belong to is remembered after the first import.
	t.Run("account remembered", func(t *testing.T) {
		a := newBook(t)
		if _, err := a.Import(ctx, ImportParams{File: csvFile(t, "2026-10-01,Coffee,-12")}); err == nil {
			t.Fatal("first import without --account must ask for it")
		}
		importOK(t, a, ImportParams{File: csvFile(t, "2026-10-01,Coffee,-12"), Account: "leumi"})
		res := importOK(t, a, ImportParams{File: csvFile(t, "2026-10-02,Tea,-9")})
		if res.Account != "assets:bank:leumi" {
			t.Fatalf("account %q", res.Account)
		}
	})
	// A dry run shows what would happen and changes nothing.
	t.Run("dry run writes nothing", func(t *testing.T) {
		a := newBook(t)
		res := importOK(t, a, ImportParams{File: csvFile(t, "2026-10-01,Coffee,-12"), Account: "leumi", DryRun: true})
		if res.New != 1 {
			t.Fatalf("got %+v", res)
		}
		items, _ := a.Review(ctx)
		if len(items) != 0 {
			t.Fatal("dry run created a transaction")
		}
	})
}

func TestReviewAndReconcile(t *testing.T) {
	// Review moves a transaction from uncategorized to the chosen category.
	t.Run("categorize", func(t *testing.T) {
		a := newBook(t)
		importOK(t, a, ImportParams{File: csvFile(t, "2026-10-01,Coffee,-12"), Account: "leumi"})
		items, err := a.Review(ctx)
		if err != nil || len(items) != 1 {
			t.Fatalf("items %d, %v", len(items), err)
		}
		name, err := a.Categorize(ctx, CategorizeParams{TxID: items[0].Tx.ID, Category: "restaurants"})
		if err != nil || name != "food:restaurants" {
			t.Fatalf("got %q, %v", name, err)
		}
	})
	// A difference with the statement lists possible duplicates to check.
	t.Run("reconcile finds duplicates", func(t *testing.T) {
		a := newBook(t)
		for i := 0; i < 2; i++ {
			mustDo(t, func() error {
				_, err := a.Add(ctx, AddParams{Account: "leumi", Amount: "-300", Category: "food", TxInfo: TxInfo{Payee: "Market"}})
				return err
			})
		}
		res, err := a.Reconcile(ctx, ReconcileParams{Account: "leumi", Balance: "24700"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Difference.Value.String() != "300" || len(res.Duplicates) != 1 {
			t.Fatalf("got %+v", res)
		}
	})
	// A user profile with a built-in's name replaces the built-in for imports.
	t.Run("user profile replaces built-in", func(t *testing.T) {
		a := newBook(t)
		text, err := a.ProfileText(ctx, "generic-csv")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.SaveProfile(ctx, strings.Replace(text, `date_format = "2006-01-02"`, `date_format = "02/01/2006"`, 1)); err != nil {
			t.Fatal(err)
		}
		res := importOK(t, a, ImportParams{File: csvFile(t, "05/10/2026,Coffee,-12"), Account: "leumi"})
		if res.Rows[0].Date.String() != "2026-10-05" {
			t.Fatalf("user profile not used: %+v", res.Rows[0])
		}
	})
}

// crossBook is a book with an ILS bank account, a USD card, and rules that make the
// card payment a transfer in both exports.
func crossBook(t *testing.T, rate string) *App {
	t.Helper()
	a := newBook(t)
	if rate != "" {
		mustDo(t, func() error {
			_, err := a.AddPrice(ctx, PriceParams{Base: "USD", Date: core.MustDate("2026-10-01"), Quote: "ILS", Rate: rate})
			return err
		})
	}
	mustDo(t, func() error {
		_, _, err := a.AddRule(ctx, RuleParams{Pattern: "chase payment", Account: "liabilities:card:chase"})
		return err
	})
	mustDo(t, func() error {
		_, _, err := a.AddRule(ctx, RuleParams{Pattern: "payment thank you", Account: "assets:bank:leumi"})
		return err
	})
	return a
}

func waitingCount(t *testing.T, a *App) int {
	t.Helper()
	w, err := a.WaitingTransfers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return len(w)
}

func TestCrossCurrencyTransfers(t *testing.T) {
	bankFile := func(t *testing.T) string { return csvFile(t, "2026-10-05,Chase payment,-3700") }
	cardFile := func(t *testing.T, amount string) string {
		return csvFile(t, "2026-10-07,Payment thank you,"+amount)
	}

	// Each export has only one of the two amounts, so the first row waits and the
	// second completes it: one transaction with both amounts, in either order.
	t.Run("first side waits, second side completes it", func(t *testing.T) {
		a := crossBook(t, "3.70")
		res := importOK(t, a, ImportParams{File: bankFile(t), Account: "leumi"})
		if res.Waiting != 1 || waitingCount(t, a) != 1 {
			t.Fatalf("bank import %+v", res)
		}
		res = importOK(t, a, ImportParams{File: cardFile(t, "1000"), Account: "chase"})
		if res.Paired != 1 || waitingCount(t, a) != 0 {
			t.Fatalf("card import %+v", res)
		}
		reg, err := a.Register(ctx, RegisterParams{Account: "chase"})
		if err != nil || len(reg.Rows) != 1 || reg.Rows[0].Amount.String() != "1,000.00 USD" || reg.Rows[0].Date.String() != "2026-10-05" {
			t.Fatalf("card register %+v %v", reg.Rows, err)
		}
		bal, err := a.Balances(ctx, BalanceParams{})
		if err != nil || row(t, bal, "assets:bank:leumi").Native.String() != "21,300.00 ILS" {
			t.Fatalf("bank balance %v", err)
		}
	})

	// An amount that does not fit the exchange rate is not the same transfer.
	t.Run("amount far from the rate is not paired", func(t *testing.T) {
		a := crossBook(t, "3.70")
		importOK(t, a, ImportParams{File: bankFile(t), Account: "leumi"})
		res := importOK(t, a, ImportParams{File: cardFile(t, "1200"), Account: "chase"})
		if res.Paired != 0 || waitingCount(t, a) != 2 {
			t.Fatalf("got %+v", res)
		}
	})

	// Without a rate nothing is guessed. Review offers the other side's amount, and
	// completing one side also links the other, so the transfer is recorded once.
	t.Run("without a rate both wait and review completes them", func(t *testing.T) {
		a := crossBook(t, "")
		importOK(t, a, ImportParams{File: bankFile(t), Account: "leumi"})
		importOK(t, a, ImportParams{File: cardFile(t, "1000"), Account: "chase"})
		waiting, err := a.WaitingTransfers(ctx)
		if err != nil || len(waiting) != 2 || waiting[0].Suggested == nil || waiting[0].Suggested.String() != "1,000.00 USD" {
			t.Fatalf("waiting %+v %v", waiting, err)
		}
		tx, err := a.CompleteTransfer(ctx, CompleteTransferParams{ID: waiting[0].ID, Amount: "1000"})
		if err != nil {
			t.Fatal(err)
		}
		if waitingCount(t, a) != 0 || len(tx.Postings) != 2 {
			t.Fatalf("still waiting, or %+v", tx)
		}
		reg, _ := a.Register(ctx, RegisterParams{Account: "chase"})
		if len(reg.Rows) != 1 {
			t.Fatalf("%d transactions on the card", len(reg.Rows))
		}
	})

	// Undoing the import that completed a transfer removes it, and the other side waits again.
	t.Run("undo makes the other side wait again", func(t *testing.T) {
		a := crossBook(t, "3.70")
		importOK(t, a, ImportParams{File: bankFile(t), Account: "leumi"})
		res := importOK(t, a, ImportParams{File: cardFile(t, "1000"), Account: "chase"})
		if _, err := a.UndoImport(ctx, res.Batch); err != nil {
			t.Fatal(err)
		}
		reg, _ := a.Register(ctx, RegisterParams{Account: "chase"})
		if len(reg.Rows) != 0 || waitingCount(t, a) != 1 {
			t.Fatalf("%d card transactions, %d waiting", len(reg.Rows), waitingCount(t, a))
		}
	})

	// A transfer typed by hand with both amounts is found by both imports, so nothing is added.
	t.Run("transfer entered by hand is matched from both sides", func(t *testing.T) {
		a := crossBook(t, "")
		mustDo(t, func() error {
			_, err := a.Transfer(ctx, TransferParams{From: "leumi", To: "chase", Amount: "3700", ToAmount: "1000",
				TxInfo: TxInfo{Date: core.MustDate("2026-10-05")}})
			return err
		})
		bank := importOK(t, a, ImportParams{File: bankFile(t), Account: "leumi"})
		card := importOK(t, a, ImportParams{File: cardFile(t, "1000"), Account: "chase"})
		if bank.Matched != 1 || card.Matched != 1 || waitingCount(t, a) != 0 {
			t.Fatalf("bank %+v card %+v", bank, card)
		}
	})
}
