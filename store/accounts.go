package store

import (
	"context"
	"database/sql"
	"errors"

	"moolah/core"
)

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// Accounts returns all accounts.
func (q Q) Accounts(ctx context.Context) ([]core.Account, error) {
	rows, err := q.q.QueryContext(ctx,
		"SELECT id, ifnull(parent_id, 0), name, type, ifnull(currency, ''), ifnull(closed_on, '') FROM accounts")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Account
	for rows.Next() {
		var a core.Account
		var typ, cur, closed string
		if err := rows.Scan(&a.ID, &a.ParentID, &a.Name, &typ, &cur, &closed); err != nil {
			return nil, err
		}
		a.Type = core.AccountType(typ)
		a.Currency = core.Currency(cur)
		if closed != "" {
			if a.ClosedOn, err = core.ParseDate(closed); err != nil {
				return nil, err
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Tree loads all accounts as a core.Tree.
func (q Q) Tree(ctx context.Context) (*core.Tree, error) {
	accts, err := q.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	return core.NewTree(accts), nil
}

// InsertAccount adds an account and returns its ID.
func (q Q) InsertAccount(ctx context.Context, a core.Account) (int64, error) {
	res, err := q.q.ExecContext(ctx,
		"INSERT INTO accounts (parent_id, name, type, currency) VALUES (?, ?, ?, ?)",
		nullID(a.ParentID), a.Name, string(a.Type), nullStr(string(a.Currency)))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MoveAccount changes an account's parent and name.
func (q Q) MoveAccount(ctx context.Context, id, parentID int64, name string) error {
	_, err := q.q.ExecContext(ctx, "UPDATE accounts SET parent_id = ?, name = ? WHERE id = ?", nullID(parentID), name, id)
	return err
}

// SetAccountClosed sets or clears (zero date) an account's close date.
func (q Q) SetAccountClosed(ctx context.Context, id int64, on core.Date) error {
	_, err := q.q.ExecContext(ctx, "UPDATE accounts SET closed_on = ? WHERE id = ?", nullStr(on.String()), id)
	return err
}

// DeleteAccount removes an account. It fails if postings still refer to it.
func (q Q) DeleteAccount(ctx context.Context, id int64) error {
	_, err := q.q.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", id)
	return err
}

// MovePostings moves all postings (and rules and import records) from one account
// to another, and bumps the version of the affected transactions.
func (q Q) MovePostings(ctx context.Context, from, to int64) error {
	if _, err := q.q.ExecContext(ctx,
		"UPDATE transactions SET version = version + 1 WHERE id IN (SELECT transaction_id FROM postings WHERE account_id = ?)", from); err != nil {
		return err
	}
	for _, stmt := range []string{
		"UPDATE postings SET account_id = ? WHERE account_id = ?",
		"UPDATE rules SET account_id = ? WHERE account_id = ?",
		"UPDATE import_rows SET account_id = ? WHERE account_id = ?",
		"UPDATE transfer_rows SET target_id = ? WHERE target_id = ?",
		"UPDATE import_batches SET account_id = ? WHERE account_id = ?",
		"UPDATE OR IGNORE profile_accounts SET account_id = ? WHERE account_id = ?",
	} {
		if _, err := q.q.ExecContext(ctx, stmt, to, from); err != nil {
			return err
		}
	}
	_, err := q.q.ExecContext(ctx, "DELETE FROM profile_accounts WHERE account_id = ?", from)
	return err
}

// CountPostings returns the number of postings on an account.
func (q Q) CountPostings(ctx context.Context, id int64) (int, error) {
	var n int
	err := q.q.QueryRowContext(ctx, "SELECT count(*) FROM postings WHERE account_id = ?", id).Scan(&n)
	return n, err
}

// LastPostingDate returns the date of the latest transaction on an account (zero if none).
func (q Q) LastPostingDate(ctx context.Context, id int64) (core.Date, error) {
	var d sql.NullString
	err := q.q.QueryRowContext(ctx,
		"SELECT max(t.date) FROM postings p JOIN transactions t ON t.id = p.transaction_id WHERE p.account_id = ?", id).Scan(&d)
	if err != nil || !d.Valid {
		return core.Date{}, err
	}
	return core.ParseDate(d.String)
}

// Balances returns the sum of postings per account and currency, for transactions
// on or before asOf (all transactions when asOf is zero).
func (q Q) Balances(ctx context.Context, asOf core.Date) (map[int64]core.Balance, error) {
	query := `SELECT p.account_id, p.currency, sum(p.amount)
		FROM postings p JOIN transactions t ON t.id = p.transaction_id`
	var args []any
	if !asOf.IsZero() {
		query += " WHERE t.date <= ?"
		args = append(args, asOf.String())
	}
	query += " GROUP BY p.account_id, p.currency"
	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]core.Balance{}
	for rows.Next() {
		var id, minor int64
		var cur string
		if err := rows.Scan(&id, &cur, &minor); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = core.Balance{}
		}
		c := core.Currency(cur)
		out[id].Add(c, core.FromMinor(minor, c))
	}
	return out, rows.Err()
}
