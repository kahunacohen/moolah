package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// migrations[i] upgrades the schema from version i to i+1. Never edit a released
// migration; add a new one.
var migrations = []string{
	`
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE accounts (
	id        INTEGER PRIMARY KEY,
	parent_id INTEGER REFERENCES accounts(id),
	name      TEXT NOT NULL COLLATE NOCASE,
	type      TEXT NOT NULL CHECK (type IN ('assets','liabilities','income','expenses','equity')),
	currency  TEXT CHECK (currency IS NULL OR length(currency) = 3),
	closed_on TEXT,
	CHECK (currency IS NULL OR type IN ('assets','liabilities'))
);
CREATE UNIQUE INDEX accounts_parent_name ON accounts(ifnull(parent_id, 0), name);
CREATE UNIQUE INDEX accounts_root_type ON accounts(type) WHERE parent_id IS NULL;

CREATE TABLE transactions (
	id          INTEGER PRIMARY KEY,
	date        TEXT NOT NULL,
	charge_date TEXT,
	payee       TEXT NOT NULL DEFAULT '',
	note        TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL DEFAULT 'normal' CHECK (kind IN ('normal','opening','valuation')),
	version     INTEGER NOT NULL DEFAULT 1,
	created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
	updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX transactions_date ON transactions(date);

CREATE TABLE postings (
	id             INTEGER PRIMARY KEY,
	transaction_id INTEGER NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
	account_id     INTEGER NOT NULL REFERENCES accounts(id),
	currency       TEXT NOT NULL,
	amount         INTEGER NOT NULL,
	cost_currency  TEXT,
	cost_amount    INTEGER,
	CHECK ((cost_currency IS NULL) = (cost_amount IS NULL))
);
CREATE INDEX postings_transaction ON postings(transaction_id);
CREATE INDEX postings_account ON postings(account_id);

CREATE TABLE transaction_meta (
	transaction_id INTEGER NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
	key            TEXT NOT NULL,
	value          TEXT NOT NULL,
	PRIMARY KEY (transaction_id, key)
);

CREATE TABLE tags (
	id   INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE
);

CREATE TABLE transaction_tags (
	transaction_id INTEGER NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
	tag_id         INTEGER NOT NULL REFERENCES tags(id),
	PRIMARY KEY (transaction_id, tag_id)
);
CREATE INDEX transaction_tags_tag ON transaction_tags(tag_id);

CREATE TABLE prices (
	date   TEXT NOT NULL,
	base   TEXT NOT NULL,
	quote  TEXT NOT NULL,
	rate   TEXT NOT NULL,
	source TEXT NOT NULL CHECK (source IN ('boi','ecb','manual','tx')),
	PRIMARY KEY (base, quote, date, source)
);
`,
	// v2: change counter for sync.
	changeCounter("accounts", "transactions", "postings", "transaction_meta", "tags", "transaction_tags", "prices", "settings"),
}

// changeCounter adds triggers that increase meta.change_seq on every write to the
// given tables. Sync uses it to know whether the book has changes not uploaded yet.
// The meta table itself has no trigger, so sync bookkeeping does not count as a change.
func changeCounter(tables ...string) string {
	var b strings.Builder
	b.WriteString("INSERT INTO meta (key, value) VALUES ('change_seq', '0') ON CONFLICT(key) DO NOTHING;\n")
	for _, t := range tables {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			fmt.Fprintf(&b, "CREATE TRIGGER %s_%s_seq AFTER %s ON %s BEGIN "+
				"UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key = 'change_seq'; END;\n",
				t, strings.ToLower(op), op, t)
		}
	}
	return b.String()
}

// SchemaVersion is the schema version this binary writes.
var SchemaVersion = len(migrations)

// ChangeSeq returns the number of writes to user data so far.
func (q Q) ChangeSeq(ctx context.Context) (int64, error) {
	v, ok, err := q.Meta(ctx, "change_seq")
	if err != nil || !ok {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func (s *Store) schemaVersion(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='meta'").Scan(&n)
	if err != nil || n == 0 {
		return 0, err
	}
	v, ok, err := s.Meta(ctx, "schema_version")
	if err != nil || !ok {
		return 0, err
	}
	return strconv.Atoi(v)
}

func (s *Store) migrate(before func(s *Store, from, to int) error) error {
	ctx := context.Background()
	cur, err := s.schemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if cur > SchemaVersion {
		return ErrNewerSchema
	}
	if cur == SchemaVersion {
		return nil
	}
	if cur > 0 && before != nil {
		if err := before(s, cur, SchemaVersion); err != nil {
			return fmt.Errorf("before migration: %w", err)
		}
	}
	return s.InTx(ctx, func(tx *Tx) error {
		for v := cur; v < SchemaVersion; v++ {
			if _, err := tx.q.ExecContext(ctx, migrations[v]); err != nil {
				return fmt.Errorf("migrate schema to version %d: %w", v+1, err)
			}
		}
		return tx.SetMeta(ctx, "schema_version", strconv.Itoa(SchemaVersion))
	})
}

// Meta returns an internal value such as schema_version or db_id.
func (q Q) Meta(ctx context.Context, key string) (string, bool, error) {
	return q.getKV(ctx, "meta", key)
}

// SetMeta sets an internal value.
func (q Q) SetMeta(ctx context.Context, key, value string) error {
	return q.setKV(ctx, "meta", key, value)
}

// Setting returns a user setting.
func (q Q) Setting(ctx context.Context, key string) (string, bool, error) {
	return q.getKV(ctx, "settings", key)
}

// SetSetting sets a user setting.
func (q Q) SetSetting(ctx context.Context, key, value string) error {
	return q.setKV(ctx, "settings", key, value)
}

// DeleteSetting removes a user setting.
func (q Q) DeleteSetting(ctx context.Context, key string) error {
	_, err := q.q.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
	return err
}

// Settings returns all user settings.
func (q Q) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := q.q.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// table is a constant chosen by the caller ("meta" or "settings"), never user input.
func (q Q) getKV(ctx context.Context, table, key string) (string, bool, error) {
	var v string
	err := q.q.QueryRowContext(ctx, "SELECT value FROM "+table+" WHERE key = ?", key).Scan(&v)
	if err != nil {
		if isNoRows(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return v, true, nil
}

func (q Q) setKV(ctx context.Context, table, key, value string) error {
	_, err := q.q.ExecContext(ctx,
		"INSERT INTO "+table+" (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}
