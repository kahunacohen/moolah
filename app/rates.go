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

// KeyOffline turns off all network access (exchange-rate downloads).
const KeyOffline = "offline"

// FetchResult reports what FetchRates downloaded.
type FetchResult struct {
	Added int
	Pairs []string // "USD→ILS (boi)"
}

func (a *App) fetchers() []prices.Fetcher {
	if a.Fetchers != nil {
		return a.Fetchers
	}
	return []prices.Fetcher{prices.BOI{}, prices.ECB{}}
}

func (a *App) offline(ctx context.Context) (bool, error) {
	if a.Offline {
		return true, nil
	}
	v, _, err := a.st.Setting(ctx, KeyOffline)
	return v == "true", err
}

// FetchRates downloads missing daily rates against ILS for every currency the book
// uses, from a week before the first transaction until today.
func (a *App) FetchRates(ctx context.Context) (FetchResult, error) {
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
	used, err := a.st.UsedCurrencies(ctx)
	if err != nil {
		return res, err
	}
	need := map[core.Currency]bool{base: true}
	for _, c := range used {
		need[c] = true
	}
	today := a.today()
	start := first.AddDays(-7)
	var all []core.Price
	var problems []string
	for _, c := range sortedCurrencies(need) {
		if c == "ILS" {
			continue
		}
		last, err := a.st.LastPriceDate(ctx, c, "ILS", core.SourceBOI, core.SourceECB)
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
		got := false
		for _, f := range a.fetchers() {
			ps, err := f.Fetch(ctx, c, "ILS", from, today)
			if errors.Is(err, prices.ErrUnsupported) {
				continue
			}
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s→ILS from %s: %v", c, f.Source(), err))
				continue
			}
			all = append(all, ps...)
			res.Pairs = append(res.Pairs, fmt.Sprintf("%s→ILS (%s)", c, f.Source()))
			got = true
			break
		}
		if !got && len(problems) == 0 {
			problems = append(problems, fmt.Sprintf("no source publishes %s→ILS", c))
		}
	}
	err = a.st.InTx(ctx, func(tx *store.Tx) error {
		for _, p := range all {
			if err := tx.UpsertPrice(ctx, p); err != nil {
				return err
			}
		}
		return nil
	})
	res.Added = len(all)
	if err == nil && len(problems) > 0 {
		err = fmt.Errorf("could not download some rates: %s", strings.Join(problems, "; "))
	}
	return res, err
}

func sortedCurrencies(m map[core.Currency]bool) []core.Currency {
	out := make([]core.Currency, 0, len(m))
	for c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// withRates runs fn with the stored rates. When fn fails because a rate is missing
// and moolah is online, it downloads rates once and runs fn again.
func (a *App) withRates(ctx context.Context, fn func(ix *core.PriceIndex) error) error {
	ix, err := a.PriceIndex(ctx)
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
	if _, ferr := a.FetchRates(ctx); ferr != nil {
		return fmt.Errorf("%w (download failed: %v)", err, ferr)
	}
	if ix, err = a.PriceIndex(ctx); err != nil {
		return err
	}
	return fn(ix)
}
