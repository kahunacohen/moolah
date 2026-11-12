package store

import (
	"context"
	"strings"
	"time"
)

// Action describes one user action: a command or a change in the browser.
type Action struct {
	Command string
	Machine string
	Time    time.Time
}

// SetAction makes every write transaction an action in history, described by f.
func (s *Store) SetAction(f func(context.Context) Action) { s.action = f }

func (s *Store) openAction(ctx context.Context, tx *Tx) error {
	if s.action == nil {
		return nil
	}
	a := s.action(ctx)
	_, err := tx.q.ExecContext(ctx, "INSERT INTO actions (time, machine, command) VALUES (?, ?, ?)",
		a.Time.UTC().Format(time.RFC3339), a.Machine, a.Command)
	return err
}

func (s *Store) closeAction(ctx context.Context, tx *Tx) error {
	if s.action == nil {
		return nil
	}
	return closeOpenAction(ctx, tx)
}

// closeOpenAction ends the open action, and drops it if it changed nothing, so
// reads leave no trace.
func closeOpenAction(ctx context.Context, tx *Tx) error {
	if _, err := tx.q.ExecContext(ctx, `DELETE FROM actions WHERE open = 1
		AND NOT EXISTS (SELECT 1 FROM history WHERE action_id = actions.id)`); err != nil {
		return err
	}
	_, err := tx.q.ExecContext(ctx, "UPDATE actions SET open = 0 WHERE open = 1")
	return err
}

// ActionRow is one action in history.
type ActionRow struct {
	Command string
	ID      int64
	Machine string
	Time    time.Time
}

// HistoryRow is one change to one row, as JSON objects of the recorded columns.
type HistoryRow struct {
	ActionID int64
	Key      string
	New      string // empty for a delete
	Old      string // empty for an insert
	Op       string // insert, update or delete
	Table    string
}

// ActionsParams selects actions, newest first.
type ActionsParams struct {
	ID     int64    // only this action
	Key    string   // with Tables: only actions that changed this row
	Limit  int      // 0 = all
	Tables []string // with Key
}

// Actions lists actions in history, newest first.
func (q Q) Actions(ctx context.Context, p ActionsParams) ([]ActionRow, error) {
	query := "SELECT id, time, machine, command FROM actions WHERE open = 0"
	var args []any
	if p.ID != 0 {
		query += " AND id = ?"
		args = append(args, p.ID)
	}
	if len(p.Tables) > 0 {
		query += " AND id IN (SELECT action_id FROM history WHERE row_key = ? AND tbl IN (?" + strings.Repeat(", ?", len(p.Tables)-1) + "))"
		args = append(args, p.Key)
		for _, t := range p.Tables {
			args = append(args, t)
		}
	}
	query += " ORDER BY id DESC"
	if p.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, p.Limit)
	}
	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActionRow
	for rows.Next() {
		var a ActionRow
		var t string
		if err := rows.Scan(&a.ID, &t, &a.Machine, &a.Command); err != nil {
			return nil, err
		}
		if a.Time, err = time.Parse(time.RFC3339, t); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// History returns the changes of the given actions, in the order they were made.
func (q Q) History(ctx context.Context, actionIDs []int64) ([]HistoryRow, error) {
	if len(actionIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(actionIDs))
	for i, id := range actionIDs {
		args[i] = id
	}
	rows, err := q.q.QueryContext(ctx, `SELECT action_id, tbl, row_key, op, ifnull(old, ''), ifnull(new, '')
		FROM history WHERE action_id IN (?`+strings.Repeat(", ?", len(args)-1)+") ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var h HistoryRow
		if err := rows.Scan(&h.ActionID, &h.Table, &h.Key, &h.Op, &h.Old, &h.New); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// UnrecordedChanges counts changes made outside moolah, which belong to no action.
func (q Q) UnrecordedChanges(ctx context.Context) (int, error) {
	var n int
	err := q.q.QueryRowContext(ctx, "SELECT count(*) FROM history WHERE action_id IS NULL").Scan(&n)
	return n, err
}

// PruneHistory deletes actions before a date (YYYY-MM-DD, UTC), with their changes,
// and changes made outside moolah. It counts as a change for sync, so other
// computers get it too.
func (tx *Tx) PruneHistory(ctx context.Context, before string) (int64, error) {
	res, err := tx.q.ExecContext(ctx, "DELETE FROM actions WHERE open = 0 AND time < ?", before)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.q.ExecContext(ctx, "DELETE FROM history WHERE action_id IS NULL"); err != nil {
		return 0, err
	}
	_, err = tx.q.ExecContext(ctx, "UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key = 'change_seq'")
	return n, err
}

// TagNames maps tag IDs to names.
func (q Q) TagNames(ctx context.Context) (map[int64]string, error) {
	rows, err := q.q.QueryContext(ctx, "SELECT id, name FROM tags")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
