package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"moolah/core"
	"moolah/store"
)

type commandKey struct{}

// WithCommand names the user action that ctx belongs to, for history: a command
// line, or a change made in the browser.
func WithCommand(ctx context.Context, command string) context.Context {
	return context.WithValue(ctx, commandKey{}, command)
}

// CommandOf returns the user action named with WithCommand.
func CommandOf(ctx context.Context) string {
	cmd, _ := ctx.Value(commandKey{}).(string)
	return cmd
}

// attach makes st the book's store and records its changes in history.
func (a *App) attach(st *store.Store) {
	a.st = st
	st.SetAction(func(ctx context.Context) store.Action {
		cmd := CommandOf(ctx)
		if a.machine == "" {
			a.machine, _ = a.Paths.MachineID()
		}
		return store.Action{Command: cmd, Machine: a.machine, Time: a.now()}
	})
}

// HistoryOp says what an action did to one thing.
type HistoryOp string

// What an action did to one thing.
const (
	HistoryAdded   HistoryOp = "added"
	HistoryChanged HistoryOp = "changed"
	HistoryDeleted HistoryOp = "deleted"
)

// HistoryKind is the kind of thing a history entry is about.
type HistoryKind string

// Kinds of things in history.
const (
	KindAccount    HistoryKind = "account"
	KindCategory   HistoryKind = "category"
	KindImport     HistoryKind = "import"
	KindProfile    HistoryKind = "profile"
	KindRateSource HistoryKind = "rate source"
	KindRule       HistoryKind = "rule"
	KindSetting    HistoryKind = "setting"
	KindTag        HistoryKind = "tag"
	KindTx         HistoryKind = "transaction"
)

// HistoryEntry is what one action did to one thing, such as one transaction.
type HistoryEntry struct {
	Details []string // "payee: Cafe → Café" for a changed field; "+ line" / "- line" for an added or removed line
	Kind    HistoryKind
	Name    string // transaction ID, account path, setting key, …
	Op      HistoryOp
}

// HistoryAction is one user action and what it changed.
type HistoryAction struct {
	Command string // empty when moolah did not know it
	Entries []HistoryEntry
	ID      int64
	Machine string
	Time    time.Time
}

// Summary counts the entries, for example "2 transactions added, 1 rule deleted".
func (h HistoryAction) Summary() string {
	type group struct {
		kind HistoryKind
		op   HistoryOp
	}
	counts := map[group]int{}
	var order []group
	for _, e := range h.Entries {
		g := group{e.Kind, e.Op}
		if counts[g] == 0 {
			order = append(order, g)
		}
		counts[g]++
	}
	var parts []string
	for _, g := range order {
		n := counts[g]
		kind := string(g.kind)
		if n > 1 {
			kind = plural(kind)
		}
		parts = append(parts, fmt.Sprintf("%d %s %s", n, kind, g.op))
	}
	return strings.Join(parts, ", ")
}

func plural(s string) string {
	if strings.HasSuffix(s, "y") {
		return s[:len(s)-1] + "ies"
	}
	return s + "s"
}

// HistoryParams selects actions, newest first.
type HistoryParams struct {
	ID          int64 // only this action
	Limit       int   // 0 = all
	Transaction int64 // only actions that changed this transaction
}

// txTables hold a transaction and its parts; their rows are named by transaction ID.
var txTables = []string{"transactions", "postings", "transaction_meta", "transaction_tags"}

// History lists actions and what they changed, newest first.
func (a *App) History(ctx context.Context, p HistoryParams) ([]HistoryAction, error) {
	ap := store.ActionsParams{ID: p.ID, Limit: p.Limit}
	if p.Transaction != 0 {
		ap.Key, ap.Tables = strconv.FormatInt(p.Transaction, 10), txTables
	}
	rows, err := a.st.Actions(ctx, ap)
	if err != nil {
		return nil, err
	}
	if p.ID != 0 && len(rows) == 0 {
		return nil, fmt.Errorf("no action %d in history. List actions with: moolah history", p.ID)
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	changes, err := a.st.History(ctx, ids)
	if err != nil {
		return nil, err
	}
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return nil, err
	}
	tags, err := a.st.TagNames(ctx)
	if err != nil {
		return nil, err
	}
	f := &historyFormatter{tags: tags, tree: tree}
	byAction := map[int64][]store.HistoryRow{}
	for _, c := range changes {
		byAction[c.ActionID] = append(byAction[c.ActionID], c)
	}
	out := make([]HistoryAction, len(rows))
	for i, r := range rows {
		h := HistoryAction{Command: r.Command, ID: r.ID, Machine: r.Machine, Time: r.Time}
		if h.Entries, err = f.entries(byAction[r.ID]); err != nil {
			return nil, err
		}
		if p.Transaction != 0 {
			h.Entries = onlyTransaction(h.Entries, ap.Key)
		}
		out[i] = h
	}
	return out, nil
}

func onlyTransaction(entries []HistoryEntry, id string) []HistoryEntry {
	var out []HistoryEntry
	for _, e := range entries {
		if e.Kind == KindTx && e.Name == id {
			out = append(out, e)
		}
	}
	return out
}

// UnrecordedChanges counts changes made to the book outside moolah, for example with sqlite3.
func (a *App) UnrecordedChanges(ctx context.Context) (int, error) { return a.st.UnrecordedChanges(ctx) }

// PruneHistory deletes the history of actions before a date. The data stays as it is.
func (a *App) PruneHistory(ctx context.Context, before core.Date) (int64, error) {
	var n int64
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		var err error
		n, err = tx.PruneHistory(ctx, before.String())
		return err
	})
	return n, err
}

// historyFormatter turns history rows into entries people can read.
type historyFormatter struct {
	tags map[int64]string
	tree *core.Tree
}

// historyRow is one recorded row as JSON values: nil, json.Number or string.
type historyRow map[string]any

func (r historyRow) str(col string) string {
	switch v := r[col].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func (r historyRow) id(col string) int64 {
	n, _ := strconv.ParseInt(r.str(col), 10, 64)
	return n
}

func parseRow(s string) (historyRow, error) {
	if s == "" {
		return nil, nil
	}
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var r historyRow
	if err := d.Decode(&r); err != nil {
		return nil, fmt.Errorf("history row %q: %w", s, err)
	}
	return r, nil
}

// mainTable is the table of each kind's own row; other tables hold its parts.
var mainTable = map[HistoryKind]string{
	KindAccount: "accounts", KindCategory: "accounts", KindImport: "import_batches", KindProfile: "import_profiles",
	KindRateSource: "rate_sources", KindRule: "rules", KindSetting: "settings", KindTag: "tags", KindTx: "transactions",
}

func (f *historyFormatter) entries(rows []store.HistoryRow) ([]HistoryEntry, error) {
	type key struct {
		kind HistoryKind
		name string
	}
	index := map[key]int{}
	var out []HistoryEntry
	var lines [][]string // per entry: lines of its parts, shown after its own fields
	for _, h := range rows {
		old, err := parseRow(h.Old)
		if err != nil {
			return nil, err
		}
		cur, err := parseRow(h.New)
		if err != nil {
			return nil, err
		}
		r := cur
		if r == nil {
			r = old
		}
		kind, name := f.subject(h.Table, h.Key, r)
		k := key{kind, name}
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, HistoryEntry{Kind: kind, Name: name, Op: HistoryChanged})
			lines = append(lines, nil)
		}
		e := &out[i]
		if h.Table == mainTable[kind] {
			switch h.Op {
			case "insert":
				e.Op = HistoryAdded
			case "delete":
				if e.Op == HistoryAdded {
					e.Op = HistoryChanged
				} else {
					e.Op = HistoryDeleted
				}
			}
			e.Details = append(e.Details, f.fields(h.Table, old, cur)...)
			continue
		}
		if old != nil {
			lines[i] = append(lines[i], "- "+f.line(h.Table, old))
		}
		if cur != nil {
			lines[i] = append(lines[i], "+ "+f.line(h.Table, cur))
		}
	}
	for i := range out {
		out[i].Details = append(out[i].Details, dropUnchanged(lines[i])...)
	}
	return out, nil
}

// dropUnchanged removes a line that was deleted and added again unchanged, as an
// edit does with the lines of a transaction.
func dropUnchanged(details []string) []string {
	added := map[string]int{}
	for _, d := range details {
		if strings.HasPrefix(d, "+ ") {
			added[d[2:]]++
		}
	}
	drop := map[string]int{}
	for _, d := range details {
		if strings.HasPrefix(d, "- ") && added[d[2:]] > 0 {
			added[d[2:]]--
			drop[d[2:]]++
		}
	}
	out := details[:0:0]
	removed := map[string]int{}
	for _, d := range details {
		if len(d) > 2 && (d[:2] == "- " || d[:2] == "+ ") && drop[d[2:]] > 0 {
			key := d
			if removed[key] < drop[d[2:]] {
				removed[key]++
				continue
			}
		}
		out = append(out, d)
	}
	return out
}

// subject names the thing a row belongs to.
func (f *historyFormatter) subject(table, key string, r historyRow) (HistoryKind, string) {
	switch table {
	case "transactions", "postings", "transaction_meta", "transaction_tags":
		return KindTx, key
	case "accounts":
		kind := KindAccount
		if t := core.AccountType(r.str("type")); t == core.Income || t == core.Expenses {
			kind = KindCategory
		}
		return kind, f.accountPath(r.id("id"), r)
	case "settings":
		return KindSetting, key
	case "tags":
		return KindTag, r.str("name")
	case "rules":
		return KindRule, key
	case "import_profiles", "profile_accounts":
		return KindProfile, key
	case "rate_sources":
		return KindRateSource, key
	case "import_batches":
		return KindImport, key
	}
	return HistoryKind(table), key
}

// accountPath is the account's name today, or for a deleted account, its name then.
func (f *historyFormatter) accountPath(id int64, r historyRow) string {
	if p := f.tree.Name(id); p != "" {
		return p
	}
	if r == nil {
		return fmt.Sprintf("#%d", id)
	}
	return r.str("name") + " (deleted)"
}

func (f *historyFormatter) account(col string, r historyRow) string {
	id := r.id(col)
	if id == 0 {
		return ""
	}
	if p := f.tree.Name(id); p != "" {
		return p
	}
	return fmt.Sprintf("#%d (deleted)", id)
}

// value shows one column of a row the way moolah shows it elsewhere.
func (f *historyFormatter) value(table, col string, r historyRow) string {
	switch col {
	case "account_id", "parent_id":
		return f.account(col, r)
	case "tag_id":
		if name, ok := f.tags[r.id(col)]; ok {
			return name
		}
	case "amount", "cost_amount":
		curCol := "currency"
		if col == "cost_amount" {
			curCol = "cost_currency"
		}
		if r[col] == nil {
			return ""
		}
		c := core.Currency(r.str(curCol))
		return core.NewAmount(core.FromMinor(r.id(col), c), c).String()
	}
	return r.str(col)
}

// hiddenFields are shown elsewhere in the entry, or are not useful to read.
var hiddenFields = map[string]bool{"id": true, "toml": true, "transaction_id": true}

// shownInName are the fields of an added or deleted account that its path already shows.
var shownInName = map[string]bool{"parent_id": true, "name": true, "type": true}

// fields lists a kind's own row: every field when added or deleted, the changed
// fields when changed.
func (f *historyFormatter) fields(table string, old, cur historyRow) []string {
	var out []string
	cols := historyColumns(old, cur)
	for _, c := range cols {
		switch {
		case old != nil && cur != nil:
			o, n := f.value(table, c, old), f.value(table, c, cur)
			if c == "toml" && o != n {
				out = append(out, "TOML changed")
			} else if o != n && !hiddenFields[c] {
				out = append(out, fmt.Sprintf("%s: %s → %s", fieldLabel(c), orNone(o), orNone(n)))
			}
		case !hiddenFields[c] && !(table == "accounts" && shownInName[c]):
			r := cur
			if r == nil {
				r = old
			}
			if v := f.value(table, c, r); v != "" && !(c == "kind" && v == string(core.KindNormal)) {
				out = append(out, fieldLabel(c)+": "+v)
			}
		}
	}
	return out
}

// fieldLabel names a column for people: parent_id is "parent".
func fieldLabel(col string) string {
	return strings.ReplaceAll(strings.TrimSuffix(col, "_id"), "_", " ")
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// historyColumns lists a row's columns in a fixed order.
var columnOrder = []string{"id", "date", "charge_date", "payee", "note", "kind", "parent_id", "name", "type", "currency",
	"closed_on", "key", "value", "position", "pattern", "account_id", "min_amount", "max_amount", "tags", "profile",
	"file_name", "toml"}

func historyColumns(rows ...historyRow) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range columnOrder {
		for _, r := range rows {
			if _, ok := r[c]; ok && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// line shows a part of a transaction or profile as one line.
func (f *historyFormatter) line(table string, r historyRow) string {
	switch table {
	case "postings":
		s := f.account("account_id", r) + "  " + f.value(table, "amount", r)
		if cost := f.value(table, "cost_amount", r); cost != "" {
			s += " @@ " + cost
		}
		return s
	case "transaction_meta":
		return r.str("key") + ": " + r.str("value")
	case "transaction_tags":
		return "tag " + f.value(table, "tag_id", r)
	case "profile_accounts":
		return "account " + f.account("account_id", r)
	}
	b, _ := json.Marshal(r)
	return string(b)
}
