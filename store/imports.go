package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"moolah/core"
)

// UserProfiles returns the TOML of the user's import profiles by name.
func (q Q) UserProfiles(ctx context.Context) (map[string]string, error) {
	rows, err := q.q.QueryContext(ctx, "SELECT name, toml FROM import_profiles")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, t string
		if err := rows.Scan(&n, &t); err != nil {
			return nil, err
		}
		out[n] = t
	}
	return out, rows.Err()
}

// UpsertProfile stores a user profile.
func (q Q) UpsertProfile(ctx context.Context, name, text string) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO import_profiles (name, toml) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET toml = excluded.toml, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`, name, text)
	return err
}

// DeleteProfile removes a user profile and reports whether it existed.
func (q Q) DeleteProfile(ctx context.Context, name string) (bool, error) {
	res, err := q.q.ExecContext(ctx, "DELETE FROM import_profiles WHERE name = ?", name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ProfileAccounts returns the accounts that a profile was imported into.
func (q Q) ProfileAccounts(ctx context.Context, profile string) ([]int64, error) {
	rows, err := q.q.QueryContext(ctx, "SELECT account_id FROM profile_accounts WHERE profile = ? ORDER BY account_id", profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RememberProfileAccount records that a profile's files belong to an account.
func (q Q) RememberProfileAccount(ctx context.Context, profile string, account int64) error {
	_, err := q.q.ExecContext(ctx, "INSERT OR IGNORE INTO profile_accounts (profile, account_id) VALUES (?, ?)", profile, account)
	return err
}

// InsertBatch records an import and returns its ID.
func (q Q) InsertBatch(ctx context.Context, account int64, profile, file, created string) (int64, error) {
	res, err := q.q.ExecContext(ctx, "INSERT INTO import_batches (account_id, profile, file_name, created_at) VALUES (?, ?, ?, ?)", account, profile, file, created)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteBatch removes an import record (its rows go with it).
func (q Q) DeleteBatch(ctx context.Context, id int64) error {
	_, err := q.q.ExecContext(ctx, "DELETE FROM import_batches WHERE id = ?", id)
	return err
}

// ImportedRow is a row stored for duplicate detection and undo.
type ImportedRow struct {
	AccountID     int64
	BatchID       int64
	Fingerprint   string
	Line          int
	Matched       bool // linked to a transaction that already existed (a transfer imported from the other side)
	Raw           []string
	TransactionID int64
}

// InsertImportedRow stores an imported row.
func (q Q) InsertImportedRow(ctx context.Context, r ImportedRow) error {
	raw, err := json.Marshal(r.Raw)
	if err != nil {
		return err
	}
	_, err = q.q.ExecContext(ctx, `INSERT INTO import_rows (batch_id, account_id, fingerprint, line, raw, transaction_id, matched)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, r.BatchID, r.AccountID, r.Fingerprint, r.Line, string(raw), nullID(r.TransactionID), boolInt(r.Matched))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FingerprintExists reports whether a row was already imported into an account.
func (q Q) FingerprintExists(ctx context.Context, account int64, fp string) (bool, error) {
	var n int
	err := q.q.QueryRowContext(ctx, "SELECT count(*) FROM import_rows WHERE account_id = ? AND fingerprint = ?", account, fp).Scan(&n)
	return n > 0, err
}

// ImportedInto reports whether a transaction came from an import into an account.
func (q Q) ImportedInto(ctx context.Context, account, txID int64) (bool, error) {
	var n int
	err := q.q.QueryRowContext(ctx, "SELECT count(*) FROM import_rows WHERE account_id = ? AND transaction_id = ?", account, txID).Scan(&n)
	return n > 0, err
}

// BatchInfo summarizes one import.
type BatchInfo struct {
	AccountID    int64
	Created      string
	File         string
	ID           int64
	Profile      string
	Rows         int
	Transactions int // created by this import (not counting matched transfers)
}

// Batches lists imports, newest first.
func (q Q) Batches(ctx context.Context) ([]BatchInfo, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT b.id, b.account_id, b.profile, b.file_name, b.created_at,
		count(r.id), count(r.transaction_id) - sum(CASE WHEN r.matched = 1 THEN 1 ELSE 0 END)
		FROM import_batches b LEFT JOIN import_rows r ON r.batch_id = b.id GROUP BY b.id ORDER BY b.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BatchInfo
	for rows.Next() {
		var b BatchInfo
		var created sql.NullInt64
		if err := rows.Scan(&b.ID, &b.AccountID, &b.Profile, &b.File, &b.Created, &b.Rows, &created); err != nil {
			return nil, err
		}
		b.Transactions = int(created.Int64)
		out = append(out, b)
	}
	return out, rows.Err()
}

// BatchCreatedTransactions returns the transactions an import created.
func (q Q) BatchCreatedTransactions(ctx context.Context, batch int64) ([]int64, error) {
	rows, err := q.q.QueryContext(ctx,
		"SELECT transaction_id FROM import_rows WHERE batch_id = ? AND matched = 0 AND transaction_id IS NOT NULL", batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RuleRow is a stored rule.
type RuleRow struct {
	AccountID int64
	ID        int64
	MaxAmount string
	MinAmount string
	Pattern   string
	Position  int
	Tags      []string
}

// Rules returns all rules in matching order.
func (q Q) Rules(ctx context.Context) ([]RuleRow, error) {
	rows, err := q.q.QueryContext(ctx, "SELECT id, position, pattern, account_id, ifnull(min_amount, ''), ifnull(max_amount, ''), tags FROM rules ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleRow
	for rows.Next() {
		var r RuleRow
		var tags string
		if err := rows.Scan(&r.ID, &r.Position, &r.Pattern, &r.AccountID, &r.MinAmount, &r.MaxAmount, &tags); err != nil {
			return nil, err
		}
		if tags != "" {
			r.Tags = strings.Split(tags, ",")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertRule adds a rule at the end and returns its ID.
func (q Q) InsertRule(ctx context.Context, r RuleRow) (int64, error) {
	res, err := q.q.ExecContext(ctx, `INSERT INTO rules (position, pattern, account_id, min_amount, max_amount, tags)
		VALUES ((SELECT ifnull(max(position), 0) + 1 FROM rules), ?, ?, ?, ?, ?)`,
		r.Pattern, r.AccountID, nullStr(r.MinAmount), nullStr(r.MaxAmount), strings.Join(r.Tags, ","))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteRule removes a rule and reports whether it existed.
func (q Q) DeleteRule(ctx context.Context, id int64) (bool, error) {
	res, err := q.q.ExecContext(ctx, "DELETE FROM rules WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TransactionsNear returns transactions with a posting on an account between two dates.
func (q Q) TransactionsNear(ctx context.Context, account int64, from, to core.Date) ([]core.Transaction, error) {
	return q.Transactions(ctx, TxFilter{AccountIDs: []int64{account}, From: from, To: to})
}

// DeleteBatchIfEmpty removes import records that ended up with no rows.
func (q Q) DeleteBatchIfEmpty(ctx context.Context) error {
	_, err := q.q.ExecContext(ctx, "DELETE FROM import_batches WHERE id NOT IN (SELECT DISTINCT batch_id FROM import_rows)")
	return err
}
