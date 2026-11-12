// Tests for history: every change is kept under the user action that made it.
package app

import (
	"context"
	"strings"
	"testing"

	"moolah/core"
)

func TestHistory(t *testing.T) {
	add := func(t *testing.T, a *App, cmd, payee string) core.Transaction {
		t.Helper()
		tx, err := a.Add(WithCommand(ctx, cmd), AddParams{Account: "leumi", Amount: "-200", Category: "food",
			TxInfo: TxInfo{Date: core.MustDate("2026-11-01"), Payee: payee}})
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	history := func(t *testing.T, a *App, p HistoryParams) []HistoryAction {
		t.Helper()
		h, err := a.History(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	// An add, an edit and a delete each become one action, newest first, that
	// says what changed, down to the lines of the transaction.
	t.Run("actions record what changed", func(t *testing.T) {
		a := newBook(t)
		tx := add(t, a, "add leumi -200 food", "Shufersal")
		tx.Payee = "Rami Levy"
		mustDo(t, func() error { return a.UpdateTransaction(WithCommand(ctx, "edit"), tx) })
		mustDo(t, func() error { return a.DeleteTransaction(WithCommand(ctx, "delete"), tx.ID) })
		h := history(t, a, HistoryParams{Limit: 3})
		if len(h) != 3 || h[0].Command != "delete" || h[1].Command != "edit" || h[2].Command != "add leumi -200 food" {
			t.Fatalf("actions %+v", h)
		}
		if got := h[2].Summary(); got != "1 transaction added" {
			t.Errorf("add summary %q", got)
		}
		added := strings.Join(h[2].Entries[0].Details, "\n")
		for _, want := range []string{"payee: Shufersal", "+ leumi  -200.00 ILS", "+ food  200.00 ILS"} {
			if !strings.Contains(added, want) {
				t.Errorf("add details lack %q:\n%s", want, added)
			}
		}
		if got := h[1].Entries[0].Details; len(got) != 1 || got[0] != "payee: Shufersal → Rami Levy" {
			t.Errorf("edit details %q", got)
		}
		// The lines are deleted by the database (ON DELETE CASCADE); history keeps them too.
		deleted := strings.Join(h[0].Entries[0].Details, "\n")
		if h[0].Entries[0].Op != HistoryDeleted || !strings.Contains(deleted, "- leumi  -200.00 ILS") {
			t.Errorf("delete %+v", h[0].Entries)
		}
	})

	// Reading the book changes nothing, so it leaves no action behind.
	t.Run("reads leave no action", func(t *testing.T) {
		a := newBook(t)
		before := len(history(t, a, HistoryParams{}))
		if _, err := a.ApplyRules(ctx); err != nil {
			t.Fatal(err)
		}
		if after := len(history(t, a, HistoryParams{})); after != before {
			t.Errorf("%d actions before, %d after", before, after)
		}
	})

	// The history of one transaction shows only the actions, and the entries, about it.
	t.Run("one transaction", func(t *testing.T) {
		a := newBook(t)
		first := add(t, a, "add one", "Shufersal")
		add(t, a, "add two", "Cafe")
		h := history(t, a, HistoryParams{Transaction: first.ID})
		if len(h) != 1 || h[0].Command != "add one" || len(h[0].Entries) != 1 {
			t.Fatalf("history of transaction %d: %+v", first.ID, h)
		}
	})

	// A deleted transaction's ID is never given to a new one, so history of an ID
	// is always about one transaction.
	t.Run("IDs are not reused", func(t *testing.T) {
		a := newBook(t)
		tx := add(t, a, "add", "Shufersal")
		mustDo(t, func() error { return a.DeleteTransaction(ctx, tx.ID) })
		if again := add(t, a, "add", "Cafe"); again.ID == tx.ID {
			t.Errorf("ID %d reused", tx.ID)
		}
	})

	// Pruning removes history, never data, and counts as a change for sync, so
	// other computers drop the same history.
	t.Run("prune", func(t *testing.T) {
		a := newBook(t)
		add(t, a, "add", "Shufersal")
		seq, err := a.st.ChangeSeq(ctx)
		if err != nil {
			t.Fatal(err)
		}
		n, err := a.PruneHistory(context.Background(), core.MustDate("2026-11-05"))
		if err != nil || n == 0 {
			t.Fatalf("pruned %d, %v", n, err)
		}
		if h := history(t, a, HistoryParams{}); len(h) != 0 {
			t.Errorf("history left: %+v", h)
		}
		rep, err := a.Balances(ctx, BalanceParams{})
		if err != nil || !rep.NetWorth.Equal(dec("24800")) {
			t.Errorf("net worth %s, %v", rep.NetWorth, err)
		}
		if after, _ := a.st.ChangeSeq(ctx); after <= seq {
			t.Errorf("change_seq %d before, %d after", seq, after)
		}
	})
}
