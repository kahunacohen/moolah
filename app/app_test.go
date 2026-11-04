// Tests for moolah's use cases against a real temporary book.
package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"

	"moolah/core"
)

var ctx = context.Background()

func newBook(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	paths := Paths{ConfigDir: filepath.Join(dir, "config"), DataDir: filepath.Join(dir, "data"), StateDir: filepath.Join(dir, "state")}
	a, err := Create(InitParams{BaseCurrency: "ILS", Path: paths.BookPath("main")}, "main", paths)
	if err != nil {
		t.Fatal(err)
	}
	a.SetClock(func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) })
	t.Cleanup(func() { _ = a.Close() })
	mustDo(t, func() error {
		_, err := a.AddAccount(ctx, AddAccountParams{Path: "assets:bank:leumi", Currency: "ILS", Opening: "25000", OpeningDate: core.MustDate("2026-10-01")})
		return err
	})
	mustDo(t, func() error {
		_, err := a.AddAccount(ctx, AddAccountParams{Path: "liabilities:card:chase", Currency: "USD"})
		return err
	})
	return a
}

func mustDo(t *testing.T, fn func() error) {
	t.Helper()
	if err := fn(); err != nil {
		t.Fatal(err)
	}
}

func row(t *testing.T, rep BalanceReport, account string) BalanceRow {
	t.Helper()
	for _, r := range rep.Rows {
		if r.Account == account {
			return r
		}
	}
	t.Fatalf("no row %s", account)
	return BalanceRow{}
}

func TestAccounts(t *testing.T) {
	// The taxonomy is closed: recording on an unknown category fails and creates nothing.
	t.Run("unknown category is rejected", func(t *testing.T) {
		a := newBook(t)
		_, err := a.Add(ctx, AddParams{Account: "leumi", Amount: "-85", Category: "restaurnts"})
		var unk *core.UnknownAccountError
		if !errors.As(err, &unk) || len(unk.Suggestions) == 0 {
			t.Fatalf("got %v", err)
		}
		tree, _ := a.Tree(ctx)
		if _, ok := tree.Find("expenses:restaurnts"); ok {
			t.Fatal("typo created an account")
		}
	})
	// Categories are created under expenses: (or income:) and are named without the root.
	t.Run("category add uses the expenses root", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error { _, err := a.AddCategory(ctx, AddCategoryParams{Path: "food:coffee"}); return err })
		tree, _ := a.Tree(ctx)
		if _, ok := tree.Find("expenses:food:coffee"); !ok {
			t.Fatal("not created")
		}
		if _, err := a.AddCategory(ctx, AddCategoryParams{Path: "nosuch:child"}); err == nil {
			t.Fatal("a missing parent must be an error")
		}
	})
	// A rename keeps history, because transactions point to the account ID.
	t.Run("rename keeps history", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error {
			_, err := a.Add(ctx, AddParams{Account: "leumi", Amount: "-85", Category: "restaurants"})
			return err
		})
		if _, err := a.RenameAccount(ctx, RenameParams{Old: "food:restaurants", New: "food:eating-out", Scope: ScopeCategory}); err != nil {
			t.Fatal(err)
		}
		rep, err := a.Register(ctx, RegisterParams{Account: "eating-out"})
		if err != nil || len(rep.Rows) != 1 {
			t.Fatalf("got %+v, %v", rep, err)
		}
	})
	// Merging moves every transaction and removes the source account.
	t.Run("merge moves transactions", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error {
			_, err := a.Add(ctx, AddParams{Account: "leumi", Amount: "-50", Category: "restaurants"})
			return err
		})
		n, err := a.MergeAccount(ctx, MergeParams{From: "restaurants", Into: "food", Scope: ScopeCategory})
		if err != nil || n != 1 {
			t.Fatalf("moved %d, %v", n, err)
		}
		if problems, _ := a.Check(ctx); len(problems) != 0 {
			t.Fatalf("problems after merge: %v", problems)
		}
	})
	// A real account with money in it cannot be closed; that would hide the money.
	t.Run("close requires zero balance", func(t *testing.T) {
		a := newBook(t)
		if err := a.CloseAccount(ctx, CloseParams{Name: "leumi", Scope: ScopeReal}); err == nil {
			t.Fatal("closed an account with 25,000 ILS")
		}
	})
}

func TestTransactions(t *testing.T) {
	// A purchase on a USD card is recorded in USD; nothing is converted at entry.
	t.Run("foreign card purchase stays in its currency", func(t *testing.T) {
		a := newBook(t)
		tx, err := a.Add(ctx, AddParams{Account: "card:chase", Amount: "-42.17", Category: "shopping"})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range tx.Postings {
			if p.Amount.Currency != "USD" {
				t.Fatalf("posting in %s", p.Amount.Currency)
			}
		}
	})
	// Paying a USD card from ILS records both real amounts and the rate actually used.
	t.Run("cross-currency transfer saves the implied rate", func(t *testing.T) {
		a := newBook(t)
		_, err := a.Transfer(ctx, TransferParams{From: "leumi", To: "card:chase", Amount: "1530", ToAmount: "420", Fee: "15",
			TxInfo: TxInfo{Date: core.MustDate("2026-11-01")}})
		if err != nil {
			t.Fatal(err)
		}
		prices, _ := a.Prices(ctx, "USD")
		if len(prices) != 1 || prices[0].Source != core.SourceTx {
			t.Fatalf("got %+v", prices)
		}
		rep, err := a.Balances(ctx, BalanceParams{})
		if err != nil {
			t.Fatal(err)
		}
		if got := row(t, rep, "assets:bank:leumi").Native["ILS"]; !got.Equal(dec("23455")) {
			t.Fatalf("leumi %s, want 25000-1530-15", got)
		}
	})
	// A cross-currency transfer without the arriving amount cannot be balanced, so it is refused.
	t.Run("cross-currency transfer needs to-amount", func(t *testing.T) {
		a := newBook(t)
		if _, err := a.Transfer(ctx, TransferParams{From: "leumi", To: "card:chase", Amount: "1530"}); err == nil {
			t.Fatal("expected error")
		}
	})
	// A statement value books only the difference after deposits, so deposits are not counted as gains.
	t.Run("valuation books only the difference", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error {
			_, err := a.AddAccount(ctx, AddAccountParams{Path: "assets:invest:pension", Currency: "ILS", Opening: "380000", OpeningDate: core.MustDate("2026-01-01")})
			return err
		})
		mustDo(t, func() error {
			_, err := a.Add(ctx, AddParams{Account: "pension", Amount: "2400", Category: "salary:pension", TxInfo: TxInfo{Date: core.MustDate("2026-02-10")}})
			return err
		})
		res, err := a.Value(ctx, ValueParams{Account: "pension", Value: "390000", Date: core.MustDate("2026-10-31")})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Delta.Value.Equal(dec("7600")) {
			t.Fatalf("delta %s, want 390000-382400", res.Delta.Value)
		}
	})
	// An edit based on an old version must not overwrite a newer change.
	t.Run("stale edit is rejected", func(t *testing.T) {
		a := newBook(t)
		tx, err := a.Add(ctx, AddParams{Account: "leumi", Amount: "-10", Category: "food"})
		if err != nil {
			t.Fatal(err)
		}
		first := tx
		first.Payee = "first"
		mustDo(t, func() error { return a.UpdateTransaction(ctx, first) })
		tx.Payee = "stale"
		if err := a.UpdateTransaction(ctx, tx); !errors.Is(err, ErrChangedElsewhere) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestBalances(t *testing.T) {
	// Balances convert each account at the report date's rate into the base currency.
	t.Run("converts at report date", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error {
			_, err := a.Add(ctx, AddParams{Account: "card:chase", Amount: "-100", Category: "shopping", TxInfo: TxInfo{Date: core.MustDate("2026-10-05")}})
			return err
		})
		mustDo(t, func() error {
			_, err := a.AddPrice(ctx, PriceParams{Base: "USD", Quote: "ILS", Rate: "3.70", Date: core.MustDate("2026-10-05")})
			return err
		})
		mustDo(t, func() error {
			_, err := a.AddPrice(ctx, PriceParams{Base: "USD", Quote: "ILS", Rate: "3.90", Date: core.MustDate("2026-10-31")})
			return err
		})
		rep, err := a.Balances(ctx, BalanceParams{Date: core.MustDate("2026-10-31")})
		if err != nil {
			t.Fatal(err)
		}
		if got := row(t, rep, "liabilities:card:chase").Value; !got.Equal(dec("-390")) {
			t.Fatalf("card value %s", got)
		}
		if !rep.NetWorth.Equal(dec("24610")) {
			t.Fatalf("net worth %s", rep.NetWorth)
		}
		usd, err := a.Balances(ctx, BalanceParams{Date: core.MustDate("2026-10-31"), In: "USD"})
		if err != nil {
			t.Fatal(err)
		}
		if got := row(t, usd, "liabilities:card:chase").Value; !got.Equal(dec("-100")) {
			t.Fatalf("card in USD %s", got)
		}
	})
	// A missing rate is an error naming the pair, never a silent 1:1.
	t.Run("missing rate is an error", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error {
			_, err := a.Add(ctx, AddParams{Account: "card:chase", Amount: "-100", Category: "shopping"})
			return err
		})
		_, err := a.Balances(ctx, BalanceParams{})
		var m *core.MissingRateError
		if !errors.As(err, &m) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestBackupRestore(t *testing.T) {
	// A backup decrypts with the matching key and restores the same data.
	t.Run("round trip with an age key", func(t *testing.T) {
		a := newBook(t)
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		mustDo(t, func() error { return a.SetConfig(ctx, KeyBackupRecipient, id.Recipient().String()) })
		file, err := a.Backup(ctx, BackupParams{Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		keyFile := filepath.Join(t.TempDir(), "key.txt")
		mustDo(t, func() error { return os.WriteFile(keyFile, []byte(id.String()+"\n"), 0o600) })
		target := filepath.Join(t.TempDir(), "restored.db")
		mustDo(t, func() error { return Restore(RestoreParams{File: file, IdentityFile: keyFile, Target: target}) })
		r, err := Open(target, "", a.Paths)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		rep, err := r.Balances(ctx, BalanceParams{})
		if err != nil || !rep.NetWorth.Equal(dec("25000")) {
			t.Fatalf("restored net worth %s, %v", rep.NetWorth, err)
		}
	})
	// Without a recipient, the backup uses a passphrase, and a wrong one cannot open it.
	t.Run("passphrase backup", func(t *testing.T) {
		a := newBook(t)
		file, err := a.Backup(ctx, BackupParams{Dir: t.TempDir(), Passphrase: func() (string, error) { return "correct horse", nil }})
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "r.db")
		err = Restore(RestoreParams{File: file, Target: target, Passphrase: func() (string, error) { return "wrong pass", nil }})
		if err == nil {
			t.Fatal("wrong passphrase accepted")
		}
		mustDo(t, func() error {
			return Restore(RestoreParams{File: file, Target: target, Passphrase: func() (string, error) { return "correct horse", nil }})
		})
	})
}

func TestBooks(t *testing.T) {
	// The current book is remembered, and --book overrides it for one command only.
	t.Run("resolve order", func(t *testing.T) {
		dir := t.TempDir()
		p := Paths{ConfigDir: filepath.Join(dir, "c"), DataDir: filepath.Join(dir, "d"), StateDir: filepath.Join(dir, "s")}
		loc, err := p.Resolve(ResolveParams{})
		if err != nil || loc.Book != DefaultBook {
			t.Fatalf("got %+v, %v", loc, err)
		}
		mustDo(t, func() error { return p.SetCurrentBook("mom") })
		loc, _ = p.Resolve(ResolveParams{})
		if loc.Book != "mom" {
			t.Fatalf("current book %q", loc.Book)
		}
		loc, _ = p.Resolve(ResolveParams{BookFlag: "main"})
		if loc.Book != "main" {
			t.Fatalf("flag ignored: %q", loc.Book)
		}
		loc, _ = p.Resolve(ResolveParams{DBFlag: "/tmp/x.db", BookFlag: "main"})
		if loc.Path != "/tmp/x.db" || loc.Book != "" {
			t.Fatalf("--db must win: %+v", loc)
		}
	})
}
