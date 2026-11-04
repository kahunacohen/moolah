package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"moolah/core"
)

// InsertTransaction stores a transaction with its postings, tags and metadata.
// It does not validate; call core.Transaction.Validate first.
func (q Q) InsertTransaction(ctx context.Context, t core.Transaction) (int64, error) {
	res, err := q.q.ExecContext(ctx,
		"INSERT INTO transactions (date, charge_date, payee, note, kind) VALUES (?, ?, ?, ?, ?)",
		t.Date.String(), nullStr(t.ChargeDate.String()), t.Payee, t.Note, string(t.Kind))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, q.writeDetails(ctx, id, t)
}

// UpdateTransaction replaces a transaction when its version still matches t.Version.
// It returns ErrVersionConflict when the transaction changed since it was loaded.
func (q Q) UpdateTransaction(ctx context.Context, t core.Transaction) error {
	res, err := q.q.ExecContext(ctx, `UPDATE transactions
		SET date = ?, charge_date = ?, payee = ?, note = ?, kind = ?, version = version + 1,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE id = ? AND version = ?`,
		t.Date.String(), nullStr(t.ChargeDate.String()), t.Payee, t.Note, string(t.Kind), t.ID, t.Version)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := q.Transaction(ctx, t.ID); err != nil {
			return err
		}
		return ErrVersionConflict
	}
	for _, table := range []string{"postings", "transaction_tags", "transaction_meta"} {
		if _, err := q.q.ExecContext(ctx, "DELETE FROM "+table+" WHERE transaction_id = ?", t.ID); err != nil {
			return err
		}
	}
	return q.writeDetails(ctx, t.ID, t)
}

func (q Q) writeDetails(ctx context.Context, id int64, t core.Transaction) error {
	for _, p := range t.Postings {
		minor, err := core.ToMinor(p.Amount.Value, p.Amount.Currency)
		if err != nil {
			return err
		}
		var costCur, costMinor any
		if p.Cost != nil {
			m, err := core.ToMinor(p.Cost.Value, p.Cost.Currency)
			if err != nil {
				return err
			}
			costCur, costMinor = string(p.Cost.Currency), m
		}
		if _, err := q.q.ExecContext(ctx,
			"INSERT INTO postings (transaction_id, account_id, currency, amount, cost_currency, cost_amount) VALUES (?, ?, ?, ?, ?, ?)",
			id, p.AccountID, string(p.Amount.Currency), minor, costCur, costMinor); err != nil {
			return err
		}
	}
	for _, tag := range t.Tags {
		if _, err := q.q.ExecContext(ctx, "INSERT INTO tags (name) VALUES (?) ON CONFLICT(name) DO NOTHING", tag); err != nil {
			return err
		}
		if _, err := q.q.ExecContext(ctx,
			"INSERT OR IGNORE INTO transaction_tags (transaction_id, tag_id) SELECT ?, id FROM tags WHERE name = ?", id, tag); err != nil {
			return err
		}
	}
	for k, v := range t.Meta {
		if _, err := q.q.ExecContext(ctx,
			"INSERT INTO transaction_meta (transaction_id, key, value) VALUES (?, ?, ?)", id, k, v); err != nil {
			return err
		}
	}
	return nil
}

// DeleteTransaction removes a transaction and its postings.
func (q Q) DeleteTransaction(ctx context.Context, id int64) error {
	res, err := q.q.ExecContext(ctx, "DELETE FROM transactions WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return fmt.Errorf("transaction %d: %w", id, ErrNotFound)
	}
	return err
}

// Transaction loads one transaction.
func (q Q) Transaction(ctx context.Context, id int64) (core.Transaction, error) {
	txs, err := q.Transactions(ctx, TxFilter{IDs: []int64{id}})
	if err != nil {
		return core.Transaction{}, err
	}
	if len(txs) == 0 {
		return core.Transaction{}, fmt.Errorf("transaction %d: %w", id, ErrNotFound)
	}
	return txs[0], nil
}

// TxFilter selects transactions. Empty fields do not filter.
type TxFilter struct {
	AccountIDs []int64 // transactions with at least one posting on these accounts
	From, To   core.Date
	IDs        []int64
	Tag        string
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Transactions loads transactions matching f, ordered by date and ID.
func (q Q) Transactions(ctx context.Context, f TxFilter) ([]core.Transaction, error) {
	var where []string
	var args []any
	if len(f.IDs) > 0 {
		where = append(where, "t.id IN ("+placeholders(len(f.IDs))+")")
		for _, id := range f.IDs {
			args = append(args, id)
		}
	}
	if len(f.AccountIDs) > 0 {
		where = append(where, "t.id IN (SELECT transaction_id FROM postings WHERE account_id IN ("+placeholders(len(f.AccountIDs))+"))")
		for _, id := range f.AccountIDs {
			args = append(args, id)
		}
	}
	if !f.From.IsZero() {
		where = append(where, "t.date >= ?")
		args = append(args, f.From.String())
	}
	if !f.To.IsZero() {
		where = append(where, "t.date <= ?")
		args = append(args, f.To.String())
	}
	if f.Tag != "" {
		where = append(where, "t.id IN (SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.name = ?)")
		args = append(args, f.Tag)
	}
	query := "SELECT t.id, t.date, ifnull(t.charge_date, ''), t.payee, t.note, t.kind, t.version FROM transactions t"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY t.date, t.id"

	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var txs []core.Transaction
	index := map[int64]int{}
	for rows.Next() {
		var t core.Transaction
		var date, charge, kind string
		if err := rows.Scan(&t.ID, &date, &charge, &t.Payee, &t.Note, &kind, &t.Version); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if t.Date, err = core.ParseDate(date); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if charge != "" {
			if t.ChargeDate, err = core.ParseDate(charge); err != nil {
				_ = rows.Close()
				return nil, err
			}
		}
		t.Kind = core.TxKind(kind)
		index[t.ID] = len(txs)
		txs = append(txs, t)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return nil, nil
	}
	if err := q.loadDetails(ctx, txs, index); err != nil {
		return nil, err
	}
	return txs, nil
}

// loadDetails fills postings, tags and metadata with one query each.
func (q Q) loadDetails(ctx context.Context, txs []core.Transaction, index map[int64]int) error {
	ids := make([]any, 0, len(txs))
	for _, t := range txs {
		ids = append(ids, t.ID)
	}
	in := "(" + placeholders(len(ids)) + ")"

	rows, err := q.q.QueryContext(ctx,
		"SELECT transaction_id, account_id, currency, amount, cost_currency, cost_amount FROM postings WHERE transaction_id IN "+in+" ORDER BY id", ids...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var txID, acct, minor int64
		var cur string
		var costCur sql.NullString
		var costMinor sql.NullInt64
		if err := rows.Scan(&txID, &acct, &cur, &minor, &costCur, &costMinor); err != nil {
			_ = rows.Close()
			return err
		}
		c := core.Currency(cur)
		p := core.Posting{AccountID: acct, Amount: core.NewAmount(core.FromMinor(minor, c), c)}
		if costCur.Valid {
			cc := core.Currency(costCur.String)
			p.Cost = &core.Amount{Currency: cc, Value: core.FromMinor(costMinor.Int64, cc)}
		}
		t := &txs[index[txID]]
		t.Postings = append(t.Postings, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	rows, err = q.q.QueryContext(ctx,
		"SELECT tt.transaction_id, g.name FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE tt.transaction_id IN "+in, ids...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var txID int64
		var name string
		if err := rows.Scan(&txID, &name); err != nil {
			_ = rows.Close()
			return err
		}
		t := &txs[index[txID]]
		t.Tags = append(t.Tags, name)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i := range txs {
		sort.Strings(txs[i].Tags)
	}

	rows, err = q.q.QueryContext(ctx,
		"SELECT transaction_id, key, value FROM transaction_meta WHERE transaction_id IN "+in, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var txID int64
		var k, v string
		if err := rows.Scan(&txID, &k, &v); err != nil {
			return err
		}
		t := &txs[index[txID]]
		if t.Meta == nil {
			t.Meta = map[string]string{}
		}
		t.Meta[k] = v
	}
	return rows.Err()
}

// TagCount is a tag with the number of transactions that use it.
type TagCount struct {
	Count int    `json:"transactions"`
	Name  string `json:"tag"`
}

// Tags returns all tags with usage counts, sorted by name.
func (q Q) Tags(ctx context.Context) ([]TagCount, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT g.name, count(tt.transaction_id)
		FROM tags g LEFT JOIN transaction_tags tt ON tt.tag_id = g.id GROUP BY g.id ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Name, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
