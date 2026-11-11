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
	"testing"
)

// seeds[v] adds rows right after migration v ran, so that every table of an old
// book has data when it is upgraded. A migration that adds a table adds its rows here.
var seeds = map[int]string{
	1: `
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
				if tbl != "meta" && after.counts[tbl] != n {
					t.Errorf("table %s: %d rows before the upgrade, %d after", tbl, n, after.counts[tbl])
				}
			}
			for tbl := range after.counts {
				for _, op := range []string{"insert", "update", "delete"} {
					if tbl != "meta" && !slices.Contains(after.objects, fmt.Sprintf("trigger %s_%s_seq", tbl, op)) {
						t.Errorf("table %s has no %s trigger for sync", tbl, op)
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
