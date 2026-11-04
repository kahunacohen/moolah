// Tests for the SQLite store: file creation, schema versions and optimistic locking.
package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/shopspring/decimal"

	"moolah/core"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "b.db"), OpenOptions{Create: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore(t *testing.T) {
	ctx := context.Background()

	// The data file holds private finances, so only the owner may read it.
	t.Run("file is created with mode 0600", func(t *testing.T) {
		s := openTemp(t)
		info, err := os.Stat(s.Path())
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", info.Mode().Perm())
		}
	})

	// Opening a missing file without Create must not create an empty book by accident.
	t.Run("missing file without create fails", func(t *testing.T) {
		if _, err := Open(filepath.Join(t.TempDir(), "none.db"), OpenOptions{}); err == nil {
			t.Fatal("expected error")
		}
	})

	// An older binary must refuse a schema it does not know, instead of damaging it.
	t.Run("newer schema refused", func(t *testing.T) {
		s := openTemp(t)
		if err := s.SetMeta(ctx, "schema_version", strconv.Itoa(SchemaVersion+1)); err != nil {
			t.Fatal(err)
		}
		path := s.Path()
		_ = s.Close()
		if _, err := Open(path, OpenOptions{}); !errors.Is(err, ErrNewerSchema) {
			t.Fatalf("got %v", err)
		}
	})

	// An edit based on an old version must not overwrite a newer change.
	t.Run("stale version is rejected", func(t *testing.T) {
		s := openTemp(t)
		a1, _ := s.InsertAccount(ctx, core.Account{Name: "assets", Type: core.Assets})
		a2, _ := s.InsertAccount(ctx, core.Account{Name: "cash", ParentID: a1, Type: core.Assets, Currency: "ILS"})
		e1, _ := s.InsertAccount(ctx, core.Account{Name: "expenses", Type: core.Expenses})
		e2, _ := s.InsertAccount(ctx, core.Account{Name: "food", ParentID: e1, Type: core.Expenses})
		ten := decimal.NewFromInt(10)
		id, err := s.InsertTransaction(ctx, core.Transaction{Date: core.MustDate("2026-11-01"), Kind: core.KindNormal,
			Tags: []string{"trip"}, Postings: []core.Posting{
				{AccountID: a2, Amount: core.NewAmount(ten.Neg(), "ILS")},
				{AccountID: e2, Amount: core.NewAmount(ten, "ILS")}}})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := s.Transaction(ctx, id)
		if err != nil || len(tx.Postings) != 2 || len(tx.Tags) != 1 {
			t.Fatalf("loaded %+v, %v", tx, err)
		}
		tx.Payee = "first edit"
		if err := s.UpdateTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
		tx.Payee = "stale edit"
		if err := s.UpdateTransaction(ctx, tx); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("got %v", err)
		}
	})
}
