package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"moolah/core"
	"moolah/importers"
	"moolah/store"
)

// transferWindow is how many days apart the two sides of a transfer may be booked.
const transferWindow = 5

// rateTolerance is how far the two amounts of a transfer between currencies may be
// from the stored exchange rate (banks add a margin), as a fraction.
var rateTolerance = decimal.RequireFromString("0.05")

// ProfileInfo describes an import profile.
type ProfileInfo struct {
	Builtin     bool // shipped with moolah
	Description string
	Name        string
	User        bool // stored in this book (overrides a built-in with the same name)
}

func (a *App) profileTexts(ctx context.Context) (map[string]string, map[string]ProfileInfo, error) {
	builtin, err := importers.Builtin()
	if err != nil {
		return nil, nil, err
	}
	user, err := a.st.UserProfiles(ctx)
	if err != nil {
		return nil, nil, err
	}
	texts := map[string]string{}
	infos := map[string]ProfileInfo{}
	for n, t := range builtin {
		texts[n] = t
		infos[n] = ProfileInfo{Builtin: true, Name: n}
	}
	for n, t := range user {
		texts[n] = t
		info := infos[n]
		info.Name, info.User = n, true
		infos[n] = info
	}
	return texts, infos, nil
}

func (a *App) loadProfiles(ctx context.Context) (map[string]*importers.Profile, error) {
	texts, _, err := a.profileTexts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]*importers.Profile{}
	for n, t := range texts {
		p, err := importers.ParseProfile(t)
		if err != nil {
			return nil, err
		}
		out[n] = p
	}
	return out, nil
}

// Profiles lists built-in and user profiles.
func (a *App) Profiles(ctx context.Context) ([]ProfileInfo, error) {
	texts, infos, err := a.profileTexts(ctx)
	if err != nil {
		return nil, err
	}
	var out []ProfileInfo
	for n, info := range infos {
		if p, err := importers.ParseProfile(texts[n]); err == nil {
			info.Description = p.Description
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ProfileText returns a profile's TOML.
func (a *App) ProfileText(ctx context.Context, name string) (string, error) {
	texts, _, err := a.profileTexts(ctx)
	if err != nil {
		return "", err
	}
	t, ok := texts[name]
	if !ok {
		return "", fmt.Errorf("there is no profile %q. See: moolah profile list", name)
	}
	return t, nil
}

// SaveProfile validates a TOML profile and stores it in the book. It returns the profile name.
func (a *App) SaveProfile(ctx context.Context, text string) (string, error) {
	p, err := importers.ParseProfile(text)
	if err != nil {
		return "", err
	}
	return p.Name, a.st.UpsertProfile(ctx, p.Name, text)
}

// RemoveProfile removes a user profile.
func (a *App) RemoveProfile(ctx context.Context, name string) error {
	ok, err := a.st.DeleteProfile(ctx, name)
	if err == nil && !ok {
		err = fmt.Errorf("there is no user profile %q (built-in profiles cannot be removed)", name)
	}
	return err
}

// ImportParams describes an import.
type ImportParams struct {
	Account string // optional after the first import with this profile
	DryRun  bool   // read and show, write nothing
	File    string
	Profile string // optional; detected from the file
}

// ImportRow is one row in an import result.
type ImportRow struct {
	Amount   core.Amount
	Category string // where the rule (or uncategorized) put it
	Date     core.Date
	Line     int
	Payee    string
	Status   string // new, duplicate, matched (linked to an existing transfer), paired (with a waiting row from the other account), waiting (for the other account's export), error
}

// ImportResult summarizes an import.
type ImportResult struct {
	Account       string
	Batch         int64
	Duplicates    int
	Errors        []string
	Matched       int
	New           int
	Paired        int // transfers between currencies completed with a waiting row
	Profile       string
	Rows          []ImportRow
	Uncategorized int
	Waiting       int // transfers between currencies waiting for the other account's export
}

func (a *App) rules(ctx context.Context, q store.Q) ([]importers.MatchRule, error) {
	rows, err := q.Rules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]importers.MatchRule, 0, len(rows))
	for _, r := range rows {
		re, err := importers.CompilePattern(r.Pattern)
		if err != nil {
			return nil, err
		}
		m := importers.MatchRule{AccountID: r.AccountID, ID: r.ID, Pattern: re, Tags: r.Tags}
		if r.MinAmount != "" {
			m.MinAmount = decimal.NewNullDecimal(decimal.RequireFromString(r.MinAmount))
		}
		if r.MaxAmount != "" {
			m.MaxAmount = decimal.NewNullDecimal(decimal.RequireFromString(r.MaxAmount))
		}
		out = append(out, m)
	}
	return out, nil
}

// Import reads a bank or card export and records each new row as a transaction.
// Rows that were imported before are skipped; rows that no rule matches go to
// expenses:uncategorized for "moolah review"; a row whose rule points to another
// real account is a transfer, and is linked to the same transfer when it was
// already imported from the other account.
func (a *App) Import(ctx context.Context, p ImportParams) (ImportResult, error) {
	var res ImportResult
	data, err := os.ReadFile(p.File)
	if err != nil {
		return res, err
	}
	profiles, err := a.loadProfiles(ctx)
	if err != nil {
		return res, err
	}
	// A text file that is not UTF-8 reads differently in each encoding, so each
	// profile is matched against the file decoded with its own encoding.
	grids := map[string]importers.Grid{}
	gridFor := func(prof *importers.Profile) (importers.Grid, error) {
		if g, ok := grids[prof.Encoding]; ok {
			return g, nil
		}
		g, err := importers.ReadBytes(data, prof.Encoding)
		if err == nil {
			grids[prof.Encoding] = g
		}
		return g, err
	}
	name := p.Profile
	if name == "" {
		var found []string
		for n, prof := range profiles {
			g, err := gridFor(prof)
			if err != nil {
				return res, err
			}
			if prof.Matches(g, p.File) {
				found = append(found, n)
			}
		}
		sort.Strings(found)
		switch len(found) {
		case 0:
			return res, errors.New("no import profile matches this file. Write one (see: moolah profile show generic-csv), or choose one with --profile")
		case 1:
			name = found[0]
		default:
			return res, fmt.Errorf("more than one profile matches this file (%s); choose one with --profile", strings.Join(found, ", "))
		}
	}
	prof, ok := profiles[name]
	if !ok {
		return res, fmt.Errorf("there is no profile %q. See: moolah profile list", name)
	}
	grid, err := gridFor(prof)
	if err != nil {
		return res, err
	}
	res.Profile = name
	rows, bad, err := prof.Parse(grid)
	if err != nil {
		return res, err
	}
	for _, b := range bad {
		res.Errors = append(res.Errors, b.Error())
	}
	ix, err := a.PriceIndex(ctx)
	if err != nil {
		return res, err
	}
	err = a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		acct, err := a.importAccount(ctx, tx, tree, p.Account, name)
		if err != nil {
			return err
		}
		res.Account = tree.Path(acct.ID)
		uncat, ok := tree.Find(accountUncategorized)
		if !ok {
			return fmt.Errorf("the category uncategorized is missing. Create it with: moolah category add uncategorized")
		}
		rules, err := a.rules(ctx, tx.Q)
		if err != nil {
			return err
		}
		if !p.DryRun {
			if res.Batch, err = tx.InsertBatch(ctx, acct.ID, name, filepath.Base(p.File), a.now().Format("2006-01-02 15:04")); err != nil {
				return err
			}
			if err := tx.RememberProfileAccount(ctx, name, acct.ID); err != nil {
				return err
			}
		}
		fps := importers.Fingerprints(rows)
		for i, r := range rows {
			row := ImportRow{Amount: core.NewAmount(r.Amount, acct.Currency), Date: r.Date, Line: r.Line, Payee: r.Payee}
			fail := func(e error) {
				row.Status = "error"
				res.Errors = append(res.Errors, fmt.Sprintf("line %d: %v", r.Line, e))
				res.Rows = append(res.Rows, row)
			}
			if r.Currency != "" && r.Currency != acct.Currency {
				fail(fmt.Errorf("amount is in %s, but %s is a %s account", r.Currency, res.Account, acct.Currency))
				continue
			}
			if err := core.CheckScale(r.Amount, acct.Currency); err != nil {
				fail(err)
				continue
			}
			dup, err := tx.FingerprintExists(ctx, acct.ID, fps[i])
			if err != nil {
				return err
			}
			if dup {
				row.Status = "duplicate"
				res.Duplicates++
				res.Rows = append(res.Rows, row)
				continue
			}
			target, tags := uncat, []string(nil)
			if m := importers.FirstMatch(rules, r.Payee, r.Memo, r.Amount); m != nil {
				if t, ok := tree.Get(m.AccountID); ok && t.ID != acct.ID {
					target, tags = t, m.Tags
				}
			}
			row.Category = DisplayName(tree, target.ID, ScopeCategory)
			if target.ID == uncat.ID {
				res.Uncategorized++
			}
			if target.Type.IsReal() && target.Currency != acct.Currency {
				status, err := a.importCrossTransfer(ctx, tx, crossTransferParams{Acct: acct, Batch: res.Batch, DryRun: p.DryRun,
					Fingerprint: fps[i], Index: ix, Row: r, Tags: tags, Target: target, Tree: tree})
				if err != nil {
					return err
				}
				row.Status = status
				switch status {
				case "matched":
					res.Matched++
				case "paired":
					res.Paired++
				default:
					res.Waiting++
				}
				res.Rows = append(res.Rows, row)
				continue
			}
			var txID int64
			matched := false
			if target.Type.IsReal() {
				txID, err = findTransfer(ctx, tx, findTransferParams{Account: acct.ID, Amount: r.Amount, Date: r.Date, Other: target.ID})
				if err != nil {
					return err
				}
				matched = txID != 0
			}
			if matched {
				row.Status = "matched"
				res.Matched++
			} else {
				row.Status = "new"
				res.New++
			}
			if p.DryRun {
				res.Rows = append(res.Rows, row)
				continue
			}
			if !matched {
				t := importedTx(r, acct, target, tags)
				id, err := insertTx(ctx, tx, tree, t)
				if err != nil {
					fail(err)
					if row.Status == "new" {
						res.New--
					}
					continue
				}
				txID = id
			}
			if _, err := tx.InsertImportedRow(ctx, store.ImportedRow{AccountID: acct.ID, BatchID: res.Batch, Fingerprint: fps[i],
				Line: r.Line, Matched: matched, Raw: r.Raw, TransactionID: txID}); err != nil {
				return err
			}
			res.Rows = append(res.Rows, row)
		}
		if !p.DryRun && res.New+res.Matched+res.Paired+res.Waiting == 0 {
			res.Batch = 0
			return tx.DeleteBatchIfEmpty(ctx)
		}
		return nil
	})
	return res, err
}

func importedTx(r importers.Row, acct, target core.Account, tags []string) core.Transaction {
	meta := map[string]string{}
	if !r.OrigAmount.IsZero() && r.OrigCurrency != "" && r.OrigCurrency != acct.Currency {
		meta["orig_amount"] = r.OrigAmount.String()
		meta["orig_currency"] = string(r.OrigCurrency)
	}
	if r.InstallmentOf > 0 {
		meta["installment"] = fmt.Sprintf("%d/%d", r.InstallmentN, r.InstallmentOf)
	}
	if r.ExternalID != "" {
		meta["bank_id"] = r.ExternalID
	}
	return core.Transaction{
		ChargeDate: r.ChargeDate,
		Date:       r.Date,
		Kind:       core.KindNormal,
		Meta:       meta,
		Note:       r.Memo,
		Payee:      r.Payee,
		Postings: []core.Posting{
			{AccountID: acct.ID, Amount: core.NewAmount(r.Amount, acct.Currency)},
			{AccountID: target.ID, Amount: core.NewAmount(r.Amount.Neg(), acct.Currency)},
		},
		Tags: tags,
	}
}

type findTransferParams struct {
	Account int64           // the account of the imported row
	Amount  decimal.Decimal // the row's amount on Account
	Date    core.Date
	Other   int64 // the account the rule points to
}

// findTransfer looks for a transfer already recorded from the other account: two
// lines, the row's amount on Account and the opposite on Other (the same amount when
// both accounts have one currency), within a few days, not yet linked to an import
// into Account.
func findTransfer(ctx context.Context, tx *store.Tx, p findTransferParams) (int64, error) {
	txs, err := tx.TransactionsNear(ctx, p.Other, p.Date.AddDays(-transferWindow), p.Date.AddDays(transferWindow))
	if err != nil {
		return 0, err
	}
	var best int64
	bestDays := 0
	for _, t := range txs {
		if len(t.Postings) != 2 {
			continue
		}
		var mine, theirs *core.Posting
		for i, q := range t.Postings {
			switch q.AccountID {
			case p.Account:
				mine = &t.Postings[i]
			case p.Other:
				theirs = &t.Postings[i]
			}
		}
		if mine == nil || theirs == nil || !mine.Amount.Value.Equal(p.Amount) || theirs.Amount.Value.Sign() != -p.Amount.Sign() {
			continue
		}
		if theirs.Amount.Currency == mine.Amount.Currency && !theirs.Amount.Value.Equal(p.Amount.Neg()) {
			continue
		}
		used, err := tx.ImportedInto(ctx, p.Account, t.ID)
		if err != nil {
			return 0, err
		}
		// Of several equal transfers, the closest date is the most likely pair.
		if days := t.Date.DaysApart(p.Date); !used && (best == 0 || days < bestDays) {
			best, bestDays = t.ID, days
		}
	}
	return best, nil
}

type crossTransferParams struct {
	Acct        core.Account // the account the file belongs to
	Batch       int64
	DryRun      bool
	Fingerprint string
	Index       *core.PriceIndex
	Row         importers.Row
	Tags        []string
	Target      core.Account // the account the rule points to, in another currency
	Tree        *core.Tree
}

// importCrossTransfer imports a row that is a transfer to an account in another
// currency. Such a transfer needs both amounts, and each export has only one. The
// row is linked to a transfer already recorded, or completes a row waiting from the
// other account's export, or waits itself. It returns the row's status.
func (a *App) importCrossTransfer(ctx context.Context, tx *store.Tx, p crossTransferParams) (string, error) {
	r := p.Row
	txID, err := findTransfer(ctx, tx, findTransferParams{Account: p.Acct.ID, Amount: r.Amount, Date: r.Date, Other: p.Target.ID})
	if err != nil {
		return "", err
	}
	mine := store.TransferRow{Account: p.Acct.ID, Amount: core.NewAmount(r.Amount, p.Acct.Currency), Date: r.Date,
		Note: r.Memo, Payee: r.Payee, Tags: p.Tags, Target: p.Target.ID}
	var other *store.TransferRow
	if txID == 0 {
		if other, err = pairFor(ctx, tx, p.Index, mine); err != nil {
			return "", err
		}
	}
	status := "waiting"
	switch {
	case txID != 0:
		status = "matched"
	case other != nil:
		status = "paired"
	}
	if p.DryRun {
		return status, nil
	}
	rowID, err := tx.InsertImportedRow(ctx, store.ImportedRow{AccountID: p.Acct.ID, BatchID: p.Batch, Fingerprint: p.Fingerprint,
		Line: r.Line, Matched: txID != 0, Raw: r.Raw, TransactionID: txID})
	if err != nil {
		return "", err
	}
	switch status {
	case "waiting":
		mine.ImportRowID = rowID
		return status, tx.InsertTransferRow(ctx, mine)
	case "paired":
		id, err := insertTx(ctx, tx, p.Tree, transferTx(mine, *other))
		if err != nil {
			return "", err
		}
		if err := tx.LinkImportRow(ctx, rowID, id, false); err != nil {
			return "", err
		}
		return status, tx.LinkImportRow(ctx, other.ImportRowID, id, true)
	}
	return status, nil
}

// counterparts returns the waiting rows that could be the other side of row: from
// the account it points to, pointing back, in the other direction, within a few days.
func counterparts(waiting []store.TransferRow, row store.TransferRow) []store.TransferRow {
	var out []store.TransferRow
	for _, w := range waiting {
		if w.Account == row.Target && w.Target == row.Account && w.Amount.Value.Sign() == -row.Amount.Value.Sign() &&
			w.Date.DaysApart(row.Date) <= transferWindow {
			out = append(out, w)
		}
	}
	return out
}

// pairFor returns the one waiting row whose amount fits row's amount at the stored
// exchange rate. Without a rate, or with more than one that fits, it returns nil:
// the rows wait, and the user completes them in review.
func pairFor(ctx context.Context, tx *store.Tx, ix *core.PriceIndex, row store.TransferRow) (*store.TransferRow, error) {
	waiting, err := tx.WaitingTransfers(ctx)
	if err != nil {
		return nil, err
	}
	have := row.Amount.Value.Abs()
	var fits []store.TransferRow
	for _, w := range counterparts(waiting, row) {
		want, err := ix.Convert(w.Amount.Value.Abs(), w.Amount.Currency, row.Amount.Currency, row.Date)
		if err != nil {
			continue
		}
		if want.Sub(have).Abs().LessThanOrEqual(have.Mul(rateTolerance)) {
			fits = append(fits, w)
		}
	}
	if len(fits) != 1 {
		return nil, nil
	}
	return &fits[0], nil
}

// transferTx builds one transaction from the two sides of a transfer between
// currencies. The side the money leaves gives the date, payee and note, and its
// amount is the cost of the other side, which also records the rate you got.
func transferTx(a, b store.TransferRow) core.Transaction {
	out, in := a, b
	if out.Amount.Value.Sign() > 0 {
		out, in = b, a
	}
	cost := core.Amount{Currency: out.Amount.Currency, Value: out.Amount.Value.Neg()}
	return core.Transaction{
		Date:  out.Date,
		Kind:  core.KindNormal,
		Note:  out.Note,
		Payee: out.Payee,
		Postings: []core.Posting{
			{AccountID: out.Account, Amount: out.Amount},
			{AccountID: in.Account, Amount: in.Amount, Cost: &cost},
		},
		Tags: append(append([]string{}, out.Tags...), in.Tags...),
	}
}

// WaitingTransfer is an imported transfer between currencies that waits for its
// other amount.
type WaitingTransfer struct {
	Account   string      // the account of the imported row
	Amount    core.Amount // on Account
	Date      core.Date
	ID        int64
	Payee     string
	Suggested *core.Amount // the amount of the only waiting row from Target that could be the other side
	Target    string
	TargetCur core.Currency
}

// WaitingTransfers lists imported transfers between currencies that wait for the
// other account's export, oldest first.
func (a *App) WaitingTransfers(ctx context.Context) ([]WaitingTransfer, error) {
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return nil, err
	}
	waiting, err := a.st.WaitingTransfers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WaitingTransfer, 0, len(waiting))
	for _, w := range waiting {
		target, _ := tree.Get(w.Target)
		item := WaitingTransfer{Account: tree.Path(w.Account), Amount: w.Amount, Date: w.Date, ID: w.ImportRowID,
			Payee: w.Payee, Target: tree.Path(w.Target), TargetCur: target.Currency}
		if c := counterparts(waiting, w); len(c) == 1 {
			s := core.NewAmount(c[0].Amount.Value.Abs(), c[0].Amount.Currency)
			item.Suggested = &s
		}
		out = append(out, item)
	}
	return out, nil
}

// CompleteTransferParams gives the other amount of a waiting transfer.
type CompleteTransferParams struct {
	Amount string // on the other account, in its currency, without a sign
	ID     int64  // the WaitingTransfer ID
}

// CompleteTransfer records a waiting transfer with the amount on the other account.
// A waiting row from the other account with exactly that amount is the same
// transfer, so it is linked instead of waiting on.
func (a *App) CompleteTransfer(ctx context.Context, p CompleteTransferParams) (core.Transaction, error) {
	var t core.Transaction
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		waiting, err := tx.WaitingTransfers(ctx)
		if err != nil {
			return err
		}
		var mine *store.TransferRow
		for i := range waiting {
			if waiting[i].ImportRowID == p.ID {
				mine = &waiting[i]
			}
		}
		if mine == nil {
			return fmt.Errorf("transfer %d is not waiting. See: moolah review --list", p.ID)
		}
		target, _ := tree.Get(mine.Target)
		v, err := parseAmount(p.Amount, target.Currency)
		if err != nil {
			return err
		}
		if v.Sign() <= 0 {
			return fmt.Errorf("give the amount on %s as a positive number", tree.Path(target.ID))
		}
		if mine.Amount.Value.Sign() > 0 {
			v = v.Neg()
		}
		other := store.TransferRow{Account: target.ID, Amount: core.NewAmount(v, target.Currency), Date: mine.Date, Target: mine.Account}
		var twin *store.TransferRow
		for _, c := range counterparts(waiting, *mine) {
			if c.Amount.Value.Equal(v) {
				twin = &c
				other = c
				break
			}
		}
		t = transferTx(*mine, other)
		if t.ID, err = insertTx(ctx, tx, tree, t); err != nil {
			return err
		}
		if err := tx.LinkImportRow(ctx, mine.ImportRowID, t.ID, false); err != nil {
			return err
		}
		if twin != nil {
			return tx.LinkImportRow(ctx, twin.ImportRowID, t.ID, true)
		}
		return nil
	})
	return t, err
}

func (a *App) importAccount(ctx context.Context, tx *store.Tx, tree *core.Tree, name, profile string) (core.Account, error) {
	if name != "" {
		return realAccount(tree, name)
	}
	ids, err := tx.ProfileAccounts(ctx, profile)
	if err != nil {
		return core.Account{}, err
	}
	switch len(ids) {
	case 0:
		return core.Account{}, fmt.Errorf("which account is this file for? Add --account, for example --account card:max (moolah remembers it for next time)")
	case 1:
		acct, _ := tree.Get(ids[0])
		return acct, nil
	}
	var names []string
	for _, id := range ids {
		names = append(names, tree.Path(id))
	}
	return core.Account{}, fmt.Errorf("profile %s is used for several accounts (%s); choose one with --account", profile, strings.Join(names, ", "))
}

// Imports lists earlier imports, newest first.
func (a *App) Imports(ctx context.Context) ([]store.BatchInfo, *core.Tree, error) {
	b, err := a.st.Batches(ctx)
	if err != nil {
		return nil, nil, err
	}
	tree, err := a.st.Tree(ctx)
	return b, tree, err
}

// UndoImport removes the transactions an import created and forgets its rows, so
// the same file can be imported again.
func (a *App) UndoImport(ctx context.Context, batch int64) (int, error) {
	var n int
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		ids, err := tx.BatchCreatedTransactions(ctx, batch)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := tx.DeleteTransaction(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		n = len(ids)
		return tx.DeleteBatch(ctx, batch)
	})
	return n, err
}

// RuleParams describes a new rule.
type RuleParams struct {
	Account   string // category, or a real account for transfers
	MaxAmount string
	MinAmount string
	Pattern   string // regular expression, not case-sensitive, matched against payee and note
	Tags      []string
}

// RuleView is a rule for display.
type RuleView struct {
	Account   string   `json:"account"`
	ID        int64    `json:"id"`
	MaxAmount string   `json:"max_amount,omitempty"`
	MinAmount string   `json:"min_amount,omitempty"`
	Pattern   string   `json:"pattern"`
	Tags      []string `json:"tags,omitempty"`
}

// AddRule stores a rule and applies all rules to uncategorized transactions.
// It returns the rule ID and how many transactions got a category.
func (a *App) AddRule(ctx context.Context, p RuleParams) (int64, int, error) {
	if _, err := importers.CompilePattern(p.Pattern); err != nil {
		return 0, 0, err
	}
	for _, v := range []string{p.MinAmount, p.MaxAmount} {
		if v != "" {
			if _, err := core.ParseDecimal(v); err != nil {
				return 0, 0, err
			}
		}
	}
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return 0, 0, err
	}
	target, err := postable(tree, p.Account)
	if err != nil {
		return 0, 0, err
	}
	for _, t := range p.Tags {
		if err := core.ValidateTag(strings.TrimPrefix(t, "#")); err != nil {
			return 0, 0, err
		}
	}
	id, err := a.st.InsertRule(ctx, store.RuleRow{AccountID: target.ID, MaxAmount: p.MaxAmount, MinAmount: p.MinAmount,
		Pattern: p.Pattern, Tags: normalizeTags(p.Tags)})
	if err != nil {
		return 0, 0, err
	}
	n, err := a.ApplyRules(ctx)
	return id, n, err
}

// Rules lists rules in matching order.
func (a *App) Rules(ctx context.Context) ([]RuleView, error) {
	rows, err := a.st.Rules(ctx)
	if err != nil {
		return nil, err
	}
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RuleView, 0, len(rows))
	for _, r := range rows {
		out = append(out, RuleView{Account: DisplayName(tree, r.AccountID, ScopeCategory), ID: r.ID, MaxAmount: r.MaxAmount,
			MinAmount: r.MinAmount, Pattern: r.Pattern, Tags: r.Tags})
	}
	return out, nil
}

// RemoveRule deletes a rule. Transactions it already categorized do not change.
func (a *App) RemoveRule(ctx context.Context, id int64) error {
	ok, err := a.st.DeleteRule(ctx, id)
	if err == nil && !ok {
		err = fmt.Errorf("there is no rule %d. See: moolah rule list", id)
	}
	return err
}

// ApplyRules gives a category to uncategorized transactions that a rule matches,
// and returns how many changed.
func (a *App) ApplyRules(ctx context.Context) (int, error) {
	n := 0
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		uncat, ok := tree.Find(accountUncategorized)
		if !ok {
			return nil
		}
		rules, err := a.rules(ctx, tx.Q)
		if err != nil {
			return err
		}
		txs, err := tx.Transactions(ctx, store.TxFilter{AccountIDs: []int64{uncat.ID}})
		if err != nil {
			return err
		}
		for _, t := range txs {
			own, idx, ok := reviewSides(tree, t, uncat.ID)
			if !ok {
				continue
			}
			m := importers.FirstMatch(rules, t.Payee, t.Note, t.Postings[own].Amount.Value)
			if m == nil || m.AccountID == t.Postings[own].AccountID {
				continue
			}
			target, _ := tree.Get(m.AccountID)
			if target.Type.IsReal() && target.Currency != t.Postings[idx].Amount.Currency {
				continue
			}
			t.Postings[idx].AccountID = m.AccountID
			t.Tags = normalizeTags(append(t.Tags, m.Tags...))
			if err := t.Validate(tree); err != nil {
				continue
			}
			if err := tx.UpdateTransaction(ctx, t); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// reviewSides finds, in a two-line transaction, the line on the real account and
// the line on uncategorized.
func reviewSides(tree *core.Tree, t core.Transaction, uncat int64) (own, idx int, ok bool) {
	if len(t.Postings) != 2 {
		return 0, 0, false
	}
	for i, p := range t.Postings {
		if p.AccountID == uncat {
			return 1 - i, i, true
		}
	}
	return 0, 0, false
}

// ReviewItem is an uncategorized transaction waiting for a category.
type ReviewItem struct {
	Account string
	Amount  core.Amount // on the account: negative = money out
	Tx      core.Transaction
}

// Review lists uncategorized transactions, oldest first.
func (a *App) Review(ctx context.Context) ([]ReviewItem, error) {
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return nil, err
	}
	uncat, ok := tree.Find(accountUncategorized)
	if !ok {
		return nil, nil
	}
	txs, err := a.st.Transactions(ctx, store.TxFilter{AccountIDs: []int64{uncat.ID}})
	if err != nil {
		return nil, err
	}
	var out []ReviewItem
	for _, t := range txs {
		own, _, ok := reviewSides(tree, t, uncat.ID)
		if !ok {
			continue
		}
		out = append(out, ReviewItem{Account: tree.Path(t.Postings[own].AccountID), Amount: t.Postings[own].Amount, Tx: t})
	}
	return out, nil
}

// CategorizeParams sets the category of an uncategorized transaction.
type CategorizeParams struct {
	Category string // a category, or a real account to make it a transfer
	TxID     int64
	Version  int64 // the version the user saw; 0 = the current one
}

// Categorize moves a transaction from uncategorized to a category.
func (a *App) Categorize(ctx context.Context, p CategorizeParams) (string, error) {
	var name string
	err := a.st.InTx(ctx, func(tx *store.Tx) error {
		tree, err := tx.Tree(ctx)
		if err != nil {
			return err
		}
		uncat, _ := tree.Find(accountUncategorized)
		t, err := tx.Transaction(ctx, p.TxID)
		if err != nil {
			return fmt.Errorf("there is no transaction %d", p.TxID)
		}
		if p.Version != 0 {
			t.Version = p.Version
		}
		own, idx, ok := reviewSides(tree, t, uncat.ID)
		if !ok {
			return fmt.Errorf("transaction %d is not uncategorized", p.TxID)
		}
		target, err := postable(tree, p.Category)
		if err != nil {
			return err
		}
		if target.ID == t.Postings[own].AccountID {
			return fmt.Errorf("the category cannot be the transaction's own account")
		}
		t.Postings[idx].AccountID = target.ID
		if err := t.Validate(tree); err != nil {
			return err
		}
		name = DisplayName(tree, target.ID, ScopeCategory)
		return tx.UpdateTransaction(ctx, t)
	})
	return name, err
}

// SuggestPattern turns a payee into a rule pattern: its first words, escaped.
func SuggestPattern(payee string) string {
	words := strings.Fields(payee)
	var keep []string
	digits := regexp.MustCompile(`\d{3,}`)
	for _, w := range words {
		if digits.MatchString(w) || len(keep) == 2 {
			break
		}
		keep = append(keep, w)
	}
	if len(keep) == 0 {
		keep = words
	}
	return regexp.QuoteMeta(strings.Join(keep, " "))
}

// ReconcileParams compares the book with a statement balance.
type ReconcileParams struct {
	Account string
	Balance string
	Date    core.Date
}

// ReconcileResult is the comparison.
type ReconcileResult struct {
	Book       core.Amount
	Difference core.Amount // statement − book
	Duplicates [][2]RegisterRow
	Recent     []RegisterRow
	Statement  core.Amount
	Waiting    []WaitingTransfer // imported transfers on this account that are not recorded yet
}

// Reconcile compares the account's balance on a date with the statement. When they
// differ, it lists possible duplicates and the latest transactions to check.
func (a *App) Reconcile(ctx context.Context, p ReconcileParams) (ReconcileResult, error) {
	var res ReconcileResult
	tree, err := a.st.Tree(ctx)
	if err != nil {
		return res, err
	}
	acct, err := realAccount(tree, p.Account)
	if err != nil {
		return res, err
	}
	stmt, err := parseAmount(p.Balance, acct.Currency)
	if err != nil {
		return res, err
	}
	date := a.dateOrToday(p.Date)
	balances, err := a.st.Balances(ctx, date)
	if err != nil {
		return res, err
	}
	book := balances[acct.ID][acct.Currency]
	res.Book = core.NewAmount(book, acct.Currency)
	res.Statement = core.NewAmount(stmt, acct.Currency)
	res.Difference = core.NewAmount(stmt.Sub(book), acct.Currency)
	if res.Difference.Value.IsZero() {
		return res, nil
	}
	waiting, err := a.WaitingTransfers(ctx)
	if err != nil {
		return res, err
	}
	for _, w := range waiting {
		if w.Account == tree.Path(acct.ID) && !w.Date.After(date) {
			res.Waiting = append(res.Waiting, w)
		}
	}
	reg, err := a.Register(ctx, RegisterParams{Account: tree.Path(acct.ID), To: date})
	if err != nil {
		return res, err
	}
	rows := reg.Rows
	for i := range rows {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].Date.After(rows[i].Date.AddDays(3)) {
				break
			}
			if rows[i].Amount.String() == rows[j].Amount.String() && rows[i].Payee == rows[j].Payee {
				res.Duplicates = append(res.Duplicates, [2]RegisterRow{rows[i], rows[j]})
			}
		}
	}
	from := len(rows) - 10
	if from < 0 {
		from = 0
	}
	res.Recent = rows[from:]
	return res, nil
}

// FormatInstallment shows "3/12" metadata as "payment 3 of 12".
func FormatInstallment(meta map[string]string) string {
	v, ok := meta["installment"]
	if !ok {
		return ""
	}
	n, of, _ := strings.Cut(v, "/")
	if _, err := strconv.Atoi(n); err != nil {
		return ""
	}
	return "payment " + n + " of " + of
}
