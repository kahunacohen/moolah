// Tests for schema upgrades: a book written by an older moolah, with data, opens
// in this version with nothing lost.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// seeds[v] adds rows right after migration v ran, so that every table of an old
// book has data when it is upgraded. A migration that adds a table adds its rows here.
var seeds = map[int]string{
	1: `
INSERT INTO actions (id, time, machine, command) VALUES (1, '2026-01-05T10:00:00Z', 'laptop-1a2b', 'init');
INSERT INTO meta (key, value) VALUES ('db_id', 'test-book');
INSERT INTO settings (key, value) VALUES ('base_currency', 'EUR');
INSERT INTO accounts (id, parent_id, name, type, currency) VALUES
	(1, NULL, 'assets', 'assets', NULL), (2, 1, 'checking', 'assets', 'EUR'),
	(3, NULL, 'expenses', 'expenses', NULL), (4, 3, 'food', 'expenses', NULL),
	(5, NULL, 'liabilities', 'liabilities', NULL), (6, 5, 'card', 'liabilities', 'USD');
INSERT INTO transactions (id, date, payee) VALUES (1, '2026-01-05', 'Cafe');
INSERT INTO postings (transaction_id, account_id, currency, amount) VALUES (1, 2, 'EUR', -850), (1, 4, 'EUR', 850);
INSERT INTO transaction_meta (transaction_id, key, value) VALUES (1, 'orig', '9.20 USD');
INSERT INTO tags (id, name) VALUES (1, 'trip');
INSERT INTO transaction_tags (transaction_id, tag_id) VALUES (1, 1);
INSERT INTO prices (date, base, quote, rate, source) VALUES ('2026-01-05', 'USD', 'EUR', '0.92', 'ecb');
INSERT INTO rate_sources (name, toml) VALUES ('my-source', 'name = "my-source"');
INSERT INTO import_profiles (name, toml) VALUES ('my-bank', 'name = "my-bank"');
INSERT INTO profile_accounts (profile, account_id) VALUES ('my-bank', 2);
INSERT INTO import_batches (id, account_id, profile, file_name) VALUES (1, 2, 'my-bank', 'export.csv');
INSERT INTO import_rows (id, batch_id, account_id, fingerprint, line, raw, transaction_id) VALUES
	(1, 1, 2, 'abc', 2, '2026-01-05,Cafe,-8.50', 1), (2, 1, 2, 'def', 3, '2026-01-06,Card payment,-100.00', NULL);
INSERT INTO transfer_rows (import_row_id, target_id, date, amount, payee) VALUES (2, 6, '2026-01-06', -10000, 'Card payment');
INSERT INTO rules (position, pattern, account_id) VALUES (1, 'cafe', 4);
UPDATE actions SET open = 0;
`,
}

// bookAtVersion writes a book as moolah at that schema version left it, with test data.
func bookAtVersion(t *testing.T, version int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath()+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for v := 0; v < version; v++ {
		if _, err := db.Exec(migrations[v] + seeds[v+1]); err != nil {
			t.Fatalf("version %d: %v", v+1, err)
		}
	}
	if _, err := db.Exec("INSERT INTO meta (key, value) VALUES ('schema_version', ?)", strconv.Itoa(version)); err != nil {
		t.Fatal(err)
	}
	return path
}

type dbInfo struct {
	counts  map[string]int
	objects []string // tables, indexes and triggers
	seq     string
}

func inspect(t *testing.T, path string) dbInfo {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	info := dbInfo{counts: map[string]int{}}
	rows, err := db.Query("SELECT type, name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			t.Fatal(err)
		}
		info.objects = append(info.objects, typ+" "+name)
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		info.counts[tbl] = n
	}
	_ = db.QueryRow("SELECT value FROM meta WHERE key = 'change_seq'").Scan(&info.seq)
	return info
}

// noChangeCounter lists the tables whose writes do not count as changes for sync:
// sync bookkeeping, and history, which changes only together with other tables
// (PruneHistory counts itself).
var noChangeCounter = map[string]bool{"meta": true, "actions": true, "history": true, "sqlite_sequence": true}

// notInHistory lists the tables whose changes are not kept in history: bookkeeping,
// and data that is downloaded or read from import files again.
var notInHistory = map[string]bool{"meta": true, "actions": true, "history": true, "sqlite_sequence": true,
	"prices": true, "import_rows": true, "transfer_rows": true}

// notRecorded lists columns that history leaves out: they change with every edit.
var notRecorded = map[string]bool{"transactions.version": true, "transactions.created_at": true, "transactions.updated_at": true,
	"rate_sources.updated_at": true, "import_profiles.updated_at": true, "import_batches.created_at": true}

func TestHistorySchema(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: bookAtVersion(t, SchemaVersion)}).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	query := func(q string, args ...any) []string {
		t.Helper()
		rows, err := db.Query(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		return out
	}
	// Every table either counts changes for sync and keeps history, or is listed
	// above as an exception, so a new table needs a decision.
	t.Run("every table has triggers", func(t *testing.T) {
		for _, tbl := range query("SELECT name FROM sqlite_master WHERE type = 'table'") {
			for _, op := range []string{"insert", "update", "delete"} {
				if !noChangeCounter[tbl] && len(query("SELECT name FROM sqlite_master WHERE name = ?", tbl+"_"+op+"_seq")) == 0 {
					t.Errorf("table %s has no %s trigger for sync", tbl, op)
				}
				if !notInHistory[tbl] && len(query("SELECT name FROM sqlite_master WHERE name = ?", tbl+"_"+op+"_history")) == 0 {
					t.Errorf("table %s has no %s trigger for history: add it to historyTriggers, or to notInHistory here", tbl, op)
				}
			}
		}
	})
	// A column missing from the history triggers would be lost silently when it changes.
	t.Run("every column is recorded", func(t *testing.T) {
		for _, tbl := range query("SELECT name FROM sqlite_master WHERE type = 'table'") {
			if notInHistory[tbl] {
				continue
			}
			trig := query("SELECT sql FROM sqlite_master WHERE name = ?", tbl+"_update_history")
			if len(trig) == 0 {
				t.Errorf("table %s has no history triggers: add it to historyTriggers, or to notInHistory here", tbl)
				continue
			}
			for _, col := range query("SELECT name FROM pragma_table_info(?)", tbl) {
				if !notRecorded[tbl+"."+col] && !strings.Contains(trig[0], "NEW."+col+")") && !strings.Contains(trig[0], "NEW."+col+",") {
					t.Errorf("column %s.%s is not in its history triggers", tbl, col)
				}
			}
		}
	})
}

func TestUpgrade(t *testing.T) {
	ctx := context.Background()

	// The upgrade test only proves something for tables that have rows.
	t.Run("every table has test data", func(t *testing.T) {
		for tbl, n := range inspect(t, bookAtVersion(t, SchemaVersion)).counts {
			if n == 0 {
				t.Errorf("table %s has no rows in the upgrade test data: add them to seeds in migrate_test.go", tbl)
			}
		}
	})

	for from := 1; from < SchemaVersion; from++ {
		// A book from any older version keeps all its rows, stays consistent, and
		// still counts every change for sync.
		t.Run(fmt.Sprintf("from version %d", from), func(t *testing.T) {
			path := bookAtVersion(t, from)
			before := inspect(t, path)
			var called bool
			s, err := Open(path, OpenOptions{BeforeMigrate: func(*Store, int, int) error { called = true; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			problems, err := s.IntegrityProblems(ctx)
			if err != nil || len(problems) > 0 {
				t.Fatalf("integrity: %v %v", problems, err)
			}
			_ = s.Close()
			after := inspect(t, path)
			if !called {
				t.Error("no copy was made before the upgrade")
			}
			for tbl, n := range before.counts {
				// History may grow: data a migration changes is kept there.
				if grows := tbl == "actions" || tbl == "history"; grows && after.counts[tbl] < n || !grows && tbl != "meta" && after.counts[tbl] != n {
					t.Errorf("table %s: %d rows before the upgrade, %d after", tbl, n, after.counts[tbl])
				}
			}
			for tbl := range after.counts {
				for _, op := range []string{"insert", "update", "delete"} {
					if !noChangeCounter[tbl] && !slices.Contains(after.objects, fmt.Sprintf("trigger %s_%s_seq", tbl, op)) {
						t.Errorf("table %s has no %s trigger for sync", tbl, op)
					}
					if !notInHistory[tbl] && !slices.Contains(after.objects, fmt.Sprintf("trigger %s_%s_history", tbl, op)) {
						t.Errorf("table %s has no %s trigger for history", tbl, op)
					}
				}
			}
			// An upgrade is not a user change, so sync must not see unsent changes.
			if before.seq != "" && after.seq != before.seq {
				t.Errorf("change_seq %s before, %s after", before.seq, after.seq)
			}
		})
	}
}
