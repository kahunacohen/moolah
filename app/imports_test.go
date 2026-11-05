// Tests for importing bank files: duplicates, rules, transfers, review, undo and reconcile.
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// Review moves a transaction from uncategorized to the chosen category; typos are still rejected.
	t.Run("categorize", func(t *testing.T) {
		a := newBook(t)
		importOK(t, a, ImportParams{File: csvFile(t, "2026-10-01,Coffee,-12"), Account: "leumi"})
		items, err := a.Review(ctx)
		if err != nil || len(items) != 1 {
			t.Fatalf("items %d, %v", len(items), err)
		}
		if _, err := a.Categorize(ctx, CategorizeParams{TxID: items[0].Tx.ID, Category: "restaurnts"}); err == nil {
			t.Fatal("typo accepted")
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
	// User profiles are validated when saved and replace a built-in with the same name.
	t.Run("profile save is strict", func(t *testing.T) {
		a := newBook(t)
		if _, err := a.SaveProfile(ctx, "name = \"x\"\nbogus = 1\n"); err == nil {
			t.Fatal("unknown key accepted")
		}
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
