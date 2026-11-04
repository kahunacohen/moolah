package core

import (
	"fmt"
	"sort"
	"strings"
)

// AccountType is the root an account belongs to.
type AccountType string

// The five fixed account roots.
const (
	Assets      AccountType = "assets"
	Liabilities AccountType = "liabilities"
	Income      AccountType = "income"
	Expenses    AccountType = "expenses"
	Equity      AccountType = "equity"
)

// AccountTypes lists the roots in display order.
var AccountTypes = []AccountType{Assets, Liabilities, Income, Expenses, Equity}

// Valid reports whether t is one of the five roots.
func (t AccountType) Valid() bool {
	for _, x := range AccountTypes {
		if t == x {
			return true
		}
	}
	return false
}

// IsReal reports whether accounts of this type are real places that hold money
// (assets, liabilities), as opposed to categories (income, expenses) and equity.
func (t AccountType) IsReal() bool { return t == Assets || t == Liabilities }

// IsCategory reports whether accounts of this type are categories.
func (t AccountType) IsCategory() bool { return t == Income || t == Expenses }

// Account is a node in the account tree. A real account with a currency can
// receive postings; a real account without one is a group.
type Account struct {
	ClosedOn Date     // zero when open
	Currency Currency // only for real accounts that receive postings
	ID       int64
	Name     string // this level only, for example "leumi"
	ParentID int64  // 0 for a root
	Type     AccountType
}

// IsRoot reports whether a is one of the five fixed roots.
func (a Account) IsRoot() bool { return a.ParentID == 0 }

// IsGroup reports whether a cannot receive postings: a root, or a real account without a currency.
func (a Account) IsGroup() bool { return a.IsRoot() || (a.Type.IsReal() && a.Currency == "") }

// IsClosed reports whether a has a close date.
func (a Account) IsClosed() bool { return !a.ClosedOn.IsZero() }

// ValidateName checks one level of an account name.
func ValidateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("account name has an empty level")
	case strings.TrimSpace(name) != name:
		return fmt.Errorf("account name %q has spaces at the start or end", name)
	case strings.ContainsAny(name, ":#=\n\t"):
		return fmt.Errorf("account name %q contains a character that is not allowed (: # = or a tab)", name)
	}
	return nil
}

// SplitPath splits "assets:bank:leumi" into its levels and validates each one.
func SplitPath(path string) ([]string, error) {
	parts := strings.Split(strings.TrimSpace(path), ":")
	for _, p := range parts {
		if err := ValidateName(p); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// UnknownAccountError is returned when a name matches no account.
type UnknownAccountError struct {
	Name        string
	Suggestions []string
}

func (e *UnknownAccountError) Error() string {
	msg := fmt.Sprintf("unknown account %q", e.Name)
	if len(e.Suggestions) > 0 {
		msg += ". Did you mean " + strings.Join(e.Suggestions, " or ") + "?"
	}
	return msg
}

// AmbiguousAccountError is returned when a short name matches more than one account.
type AmbiguousAccountError struct {
	Matches []string
	Name    string
}

func (e *AmbiguousAccountError) Error() string {
	return fmt.Sprintf("%q is ambiguous: %s. Write more of the name", e.Name, strings.Join(e.Matches, ", "))
}

// Tree is an in-memory index of all accounts.
type Tree struct {
	byID     map[int64]*Account
	byPath   map[string]*Account // lower-case full path
	children map[int64][]int64
	paths    map[int64]string
}

// NewTree builds a tree from a flat list of accounts.
func NewTree(accounts []Account) *Tree {
	t := &Tree{
		byID:     map[int64]*Account{},
		byPath:   map[string]*Account{},
		children: map[int64][]int64{},
		paths:    map[int64]string{},
	}
	for i := range accounts {
		a := accounts[i]
		t.byID[a.ID] = &a
		t.children[a.ParentID] = append(t.children[a.ParentID], a.ID)
	}
	for id := range t.byID {
		p := t.buildPath(id)
		t.paths[id] = p
		t.byPath[strings.ToLower(p)] = t.byID[id]
	}
	for _, ids := range t.children {
		sort.Slice(ids, func(i, j int) bool { return t.paths[ids[i]] < t.paths[ids[j]] })
	}
	return t
}

func (t *Tree) buildPath(id int64) string {
	a := t.byID[id]
	if a.ParentID == 0 {
		return a.Name
	}
	return t.buildPath(a.ParentID) + ":" + a.Name
}

// Get returns the account with the given ID.
func (t *Tree) Get(id int64) (Account, bool) {
	a, ok := t.byID[id]
	if !ok {
		return Account{}, false
	}
	return *a, true
}

// Path returns the full name of an account, for example "assets:bank:leumi".
func (t *Tree) Path(id int64) string { return t.paths[id] }

// Find returns the account with exactly this full path (case-insensitive).
func (t *Tree) Find(path string) (Account, bool) {
	a, ok := t.byPath[strings.ToLower(strings.TrimSpace(path))]
	if !ok {
		return Account{}, false
	}
	return *a, true
}

// Root returns the root account of a type.
func (t *Tree) Root(typ AccountType) (Account, bool) { return t.Find(string(typ)) }

// Children returns the direct children of an account, sorted by path. Use 0 for the roots.
func (t *Tree) Children(id int64) []Account {
	var out []Account
	for _, c := range t.children[id] {
		out = append(out, *t.byID[c])
	}
	return out
}

// Subtree returns the IDs of an account and all its descendants.
func (t *Tree) Subtree(id int64) []int64 {
	out := []int64{id}
	for _, c := range t.children[id] {
		out = append(out, t.Subtree(c)...)
	}
	return out
}

// Depth returns 0 for a root, 1 for its children, and so on.
func (t *Tree) Depth(id int64) int { return strings.Count(t.paths[id], ":") }

// Walk visits accounts depth-first in display order (roots in AccountTypes order).
func (t *Tree) Walk(visit func(a Account)) {
	var rec func(id int64)
	rec = func(id int64) {
		visit(*t.byID[id])
		for _, c := range t.children[id] {
			rec(c)
		}
	}
	for _, typ := range AccountTypes {
		if r, ok := t.Root(typ); ok {
			rec(r.ID)
		}
	}
}

// ResolveFilter limits which accounts Resolve may return.
type ResolveFilter func(Account) bool

// Resolve finds an account by its full path or by a unique short name made of the
// last levels of a path ("leumi", "card:max"). The filter may be nil.
func (t *Tree) Resolve(name string, filter ResolveFilter) (Account, error) {
	in := strings.ToLower(strings.TrimSpace(name))
	if in == "" {
		return Account{}, fmt.Errorf("account name is empty")
	}
	ok := func(a *Account) bool { return filter == nil || filter(*a) }
	if a, found := t.byPath[in]; found && ok(a) {
		return *a, nil
	}
	var matches []string
	var match *Account
	for id, p := range t.paths {
		a := t.byID[id]
		if !ok(a) {
			continue
		}
		lp := strings.ToLower(p)
		if strings.HasSuffix(lp, ":"+in) {
			matches = append(matches, p)
			match = a
		}
	}
	switch len(matches) {
	case 1:
		return *match, nil
	case 0:
		return Account{}, &UnknownAccountError{Name: name, Suggestions: t.suggest(in, ok)}
	default:
		sort.Strings(matches)
		return Account{}, &AmbiguousAccountError{Name: name, Matches: matches}
	}
}

// suggest returns up to three paths whose full name or short-name suffix is close to in.
func (t *Tree) suggest(in string, ok func(*Account) bool) []string {
	type cand struct {
		dist int
		path string
	}
	limit := len([]rune(in)) / 3
	if limit < 2 {
		limit = 2
	}
	var cands []cand
	inLevels := strings.Count(in, ":") + 1
	for id, p := range t.paths {
		a := t.byID[id]
		if !ok(a) {
			continue
		}
		lp := strings.ToLower(p)
		best := levenshtein(in, lp)
		levels := strings.Split(lp, ":")
		if len(levels) >= inLevels {
			suffix := strings.Join(levels[len(levels)-inLevels:], ":")
			if d := levenshtein(in, suffix); d < best {
				best = d
			}
		}
		if best <= limit {
			cands = append(cands, cand{best, p})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		return cands[i].path < cands[j].path
	})
	var out []string
	for i := 0; i < len(cands) && i < 3; i++ {
		out = append(out, cands[i].path)
	}
	return out
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
