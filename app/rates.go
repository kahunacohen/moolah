package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"moolah/core"
	"moolah/prices"
	"moolah/store"
)

// Setting keys for exchange rates.
const (
	KeyOffline     = "offline"      // true: never use the network (this computer only)
	KeyRateSources = "rate_sources" // comma-separated source names, most trusted first
)

// RateSourceInfo describes a rate source.
type RateSourceInfo struct {
	Builtin     bool   `json:"builtin"` // ships with moolah
	Description string `json:"description"`
	Name        string `json:"name"`
	Pairs       string `json:"pairs"` // what it publishes, for display
	Used        bool   `json:"used"`  // in the current order
	User        bool   `json:"user"`  // stored in this book (replaces a built-in with the same name)
}

func (a *App) sourceTexts(ctx context.Context) (map[string]string, map[string]RateSourceInfo, error) {
	builtin, err := prices.Builtin()
	if err != nil {
		return nil, nil, err
	}
	user, err := a.st.UserRateSources(ctx)
	if err != nil {
		return nil, nil, err
	}
	texts := map[string]string{}
	infos := map[string]RateSourceInfo{}
	for n, t := range builtin {
		texts[n] = t
		infos[n] = RateSourceInfo{Builtin: true, Name: n}
	}
	for n, t := range user {
		texts[n] = t
		info := infos[n]
		info.Name, info.User = n, true
		infos[n] = info
	}
	return texts, infos, nil
}

func (a *App) loadSources(ctx context.Context) (map[string]*prices.Source, error) {
	texts, _, err := a.sourceTexts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]*prices.Source{}
	for n, t := range texts {
		s, err := prices.ParseSource(t)
		if err != nil {
			return nil, err
		}
		out[n] = s
	}
	return out, nil
}

// sources returns the rate sources in the order they are tried. The order comes
// from the rate_sources setting; without it, sources that publish against the
// base currency come first (usually that country's central bank), then sources
// that publish any pair, then the rest, each group by name.
func (a *App) sources(ctx context.Context) ([]prices.Fetcher, error) {
	if a.Fetchers != nil {
		return a.Fetchers, nil
	}
	all, err := a.loadSources(ctx)
	if err != nil {
		return nil, err
	}
	setting, _, err := a.st.Setting(ctx, KeyRateSources)
	if err != nil {
		return nil, err
	}
	var out []prices.Fetcher
	if setting != "" {
		for _, n := range splitList(setting) {
			s, ok := all[n]
			if !ok {
				return nil, fmt.Errorf("rate_sources names %q, which does not exist. See: moolah source list", n)
			}
			out = append(out, s)
		}
		return out, nil
	}
	base, err := a.BaseCurrency(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	group := func(s *prices.Source) int {
		switch {
		case s.Quote == string(base):
			return 0
		case s.Quote == "":
			return 1
		}
		return 2
	}
	sort.Slice(names, func(i, j int) bool {
		gi, gj := group(all[names[i]]), group(all[names[j]])
		if gi != gj {
			return gi < gj
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		out = append(out, all[n])
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// PriceIndex loads all stored rates. Sources are trusted in their current order,
// and cross rates go through the base currency (and the given currencies) first.
func (a *App) PriceIndex(ctx context.Context, pivots ...core.Currency) (*core.PriceIndex, error) {
	all, err := a.st.Prices(ctx)
	if err != nil {
		return nil, err
	}
	var order []core.PriceSource
	if srcs, err := a.sources(ctx); err == nil {
		for _, s := range srcs {
			order = append(order, s.Name())
		}
	}
	if base, err := a.BaseCurrency(ctx); err == nil {
		pivots = append([]core.Currency{base}, pivots...)
	}
	return core.NewPriceIndex(all, core.IndexOptions{Order: order, Pivots: pivots}), nil
}

func (a *App) offline(ctx context.Context) (bool, error) {
	if a.Offline {
		return true, nil
	}
	c, err := a.Paths.LoadConfig()
	return c.Offline, err
}

// FetchResult reports what FetchRates downloaded.
type FetchResult struct {
	Added int
	Pairs []string // "USD→EUR (ecb)"
}

type leg struct {
	base, quote core.Currency
	source      prices.Fetcher
}

// planLegs chooses, for each currency, the source that publishes its rate against
// quote. When no source does, it looks for a currency that both can be converted
// to (a pivot) and plans two legs, which reports combine into a cross rate.
func planLegs(srcs []prices.Fetcher, need []core.Currency, quote core.Currency, pivots []core.Currency) ([]leg, []string) {
	find := func(b, q core.Currency) prices.Fetcher {
		for _, s := range srcs {
			if s.Supports(b, q) {
				return s
			}
		}
		return nil
	}
	var legs []leg
	var missing []string
	seen := map[string]bool{}
	add := func(l leg) {
		k := string(l.base) + string(l.quote)
		if !seen[k] {
			seen[k] = true
			legs = append(legs, l)
		}
	}
	for _, c := range need {
		if c == quote {
			continue
		}
		if s := find(c, quote); s != nil {
			add(leg{c, quote, s})
			continue
		}
		found := false
		for _, p := range pivots {
			if p == c || p == quote {
				continue
			}
			s1, s2 := find(c, p), find(quote, p)
			if s1 != nil && s2 != nil {
				add(leg{c, p, s1})
				add(leg{quote, p, s2})
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, fmt.Sprintf("no rate source publishes %s→%s (add rates with moolah price add, or a source with moolah source add)", c, quote))
		}
	}
	return legs, missing
}

// FetchRates downloads missing daily rates, from a week before the first
// transaction until today, to convert every currency the book uses into quote
// (the base currency when empty).
func (a *App) FetchRates(ctx context.Context, quote core.Currency) (FetchResult, error) {
	var res FetchResult
	if off, err := a.offline(ctx); err != nil || off {
		if err == nil {
			err = errors.New("moolah is offline (setting offline=true or --offline); add rates with: moolah price add")
		}
		return res, err
	}
	first, err := a.st.FirstTransactionDate(ctx)
	if err != nil || first.IsZero() {
		return res, err
	}
	base, err := a.BaseCurrency(ctx)
	if err != nil {
		return res, err
	}
	if quote == "" {
		quote = base
	}
	used, err := a.st.UsedCurrencies(ctx)
	if err != nil {
		return res, err
	}
	need := append([]core.Currency{base}, used...)
	srcs, err := a.sources(ctx)
	if err != nil {
		return res, err
	}
	pivots := []core.Currency{base}
	seenPivot := map[core.Currency]bool{base: true}
	for _, code := range allSupported(srcs) {
		if !seenPivot[code] {
			seenPivot[code] = true
			pivots = append(pivots, code)
		}
	}
	legs, problems := planLegs(srcs, need, quote, pivots)
	today := a.today()
	start := first.AddDays(-7)
	var got []core.Price
	for _, l := range legs {
		last, err := a.st.LastFetchedDate(ctx, l.base, l.quote)
		if err != nil {
			return res, err
		}
		from := start
		if !last.IsZero() && !last.Before(start) {
			from = last.AddDays(1)
		}
		if from.After(today) {
			continue
		}
		ps, err := l.source.Fetch(ctx, l.base, l.quote, from, today)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s→%s from %s: %v", l.base, l.quote, l.source.Name(), err))
			continue
		}
		got = append(got, ps...)
		res.Pairs = append(res.Pairs, fmt.Sprintf("%s→%s (%s)", l.base, l.quote, l.source.Name()))
	}
	err = a.st.InTx(ctx, func(tx *store.Tx) error {
		for _, p := range got {
			if err := tx.UpsertPrice(ctx, p); err != nil {
				return err
			}
		}
		return nil
	})
	res.Added = len(got)
	if err == nil && len(problems) > 0 {
		err = fmt.Errorf("could not download some rates: %s", strings.Join(problems, "; "))
	}
	return res, err
}

// allSupported lists the currencies that sources publish, sorted, for pivots.
func allSupported(srcs []prices.Fetcher) []core.Currency {
	var out []core.Currency
	for _, s := range srcs {
		if src, ok := s.(*prices.Source); ok {
			for _, c := range src.Currencies {
				out = append(out, core.Currency(c))
			}
			if src.Quote != "" {
				out = append(out, core.Currency(src.Quote))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// withRates runs fn with the stored rates. When fn fails because a rate is missing
// and moolah is online, it downloads rates into cur once and runs fn again.
func (a *App) withRates(ctx context.Context, cur core.Currency, fn func(ix *core.PriceIndex) error) error {
	ix, err := a.PriceIndex(ctx, cur)
	if err != nil {
		return err
	}
	err = fn(ix)
	var missing *core.MissingRateError
	if !errors.As(err, &missing) {
		return err
	}
	if off, oerr := a.offline(ctx); oerr != nil || off {
		return err
	}
	if _, ferr := a.FetchRates(ctx, cur); ferr != nil {
		return fmt.Errorf("%w (download failed: %v)", err, ferr)
	}
	if ix, err = a.PriceIndex(ctx, cur); err != nil {
		return err
	}
	return fn(ix)
}

// RateSources lists built-in and user rate sources, with the current order marked.
func (a *App) RateSources(ctx context.Context) ([]RateSourceInfo, error) {
	texts, infos, err := a.sourceTexts(ctx)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	if srcs, err := a.sources(ctx); err == nil {
		for _, s := range srcs {
			used[string(s.Name())] = true
		}
	}
	var out []RateSourceInfo
	for n, info := range infos {
		if s, err := prices.ParseSource(texts[n]); err == nil {
			info.Description = s.Description
			if s.Quote != "" {
				info.Pairs = fmt.Sprintf("%d currencies → %s", len(s.Currencies), s.Quote)
			} else {
				info.Pairs = fmt.Sprintf("any pair among %d currencies", len(s.Currencies))
			}
		}
		info.Used = used[n]
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RateSourceText returns a rate source's TOML.
func (a *App) RateSourceText(ctx context.Context, name string) (string, error) {
	texts, _, err := a.sourceTexts(ctx)
	if err != nil {
		return "", err
	}
	t, ok := texts[name]
	if !ok {
		return "", fmt.Errorf("there is no rate source %q. See: moolah source list", name)
	}
	return t, nil
}

// SaveRateSource validates a TOML rate source and stores it in the book.
func (a *App) SaveRateSource(ctx context.Context, text string) (string, error) {
	s, err := prices.ParseSource(text)
	if err != nil {
		return "", err
	}
	return s.SourceName, a.st.UpsertRateSource(ctx, s.SourceName, text)
}

// RemoveRateSource removes a user rate source. Rates it downloaded stay.
func (a *App) RemoveRateSource(ctx context.Context, name string) error {
	ok, err := a.st.DeleteRateSource(ctx, name)
	if err == nil && !ok {
		err = fmt.Errorf("there is no user rate source %q (built-in sources cannot be removed; leave them out of rate_sources instead)", name)
	}
	return err
}

// TestRateSourceParams asks one source for rates without saving them.
type TestRateSourceParams struct {
	Base, Quote string
	From, To    core.Date
	Name        string
}

// TestRateSource downloads rates from one source and returns them without saving.
func (a *App) TestRateSource(ctx context.Context, p TestRateSourceParams) ([]core.Price, error) {
	all, err := a.loadSources(ctx)
	if err != nil {
		return nil, err
	}
	s, ok := all[p.Name]
	if !ok {
		return nil, fmt.Errorf("there is no rate source %q. See: moolah source list", p.Name)
	}
	base, err := core.ParseCurrency(p.Base)
	if err != nil {
		return nil, err
	}
	quote, err := core.ParseCurrency(p.Quote)
	if err != nil {
		return nil, err
	}
	if !s.Supports(base, quote) {
		return nil, fmt.Errorf("%s does not publish %s→%s (see its currencies and quote)", p.Name, base, quote)
	}
	to := p.To
	if to.IsZero() {
		to = a.today()
	}
	from := p.From
	if from.IsZero() {
		from = to.AddDays(-7)
	}
	ps, err := s.Fetch(ctx, base, quote, from, to)
	sort.Slice(ps, func(i, j int) bool { return ps[i].Date.Before(ps[j].Date) })
	return ps, err
}
