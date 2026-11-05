package store

import (
	"context"

	"github.com/shopspring/decimal"

	"moolah/core"
)

// UpsertPrice stores a rate. A rate for the same pair, date and source is replaced.
func (q Q) UpsertPrice(ctx context.Context, p core.Price) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO prices (date, base, quote, rate, source) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(base, quote, date, source) DO UPDATE SET rate = excluded.rate`,
		p.Date.String(), string(p.Base), string(p.Quote), p.Rate.String(), string(p.Source))
	return err
}

// UsedCurrencies returns every currency used by an account, a posting or a cost.
func (q Q) UsedCurrencies(ctx context.Context) ([]core.Currency, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT currency FROM accounts WHERE currency IS NOT NULL
		UNION SELECT currency FROM postings UNION SELECT cost_currency FROM postings WHERE cost_currency IS NOT NULL
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Currency
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, core.Currency(c))
	}
	return out, rows.Err()
}

// FirstTransactionDate returns the date of the oldest transaction (zero when there is none).
func (q Q) FirstTransactionDate(ctx context.Context) (core.Date, error) {
	var d *string
	if err := q.q.QueryRowContext(ctx, "SELECT min(date) FROM transactions").Scan(&d); err != nil || d == nil {
		return core.Date{}, err
	}
	return core.ParseDate(*d)
}

// LastPriceDate returns the newest date with a rate for a pair from one of the sources.
func (q Q) LastPriceDate(ctx context.Context, base, quote core.Currency, sources ...core.PriceSource) (core.Date, error) {
	args := []any{string(base), string(quote)}
	for _, s := range sources {
		args = append(args, string(s))
	}
	var d *string
	err := q.q.QueryRowContext(ctx, "SELECT max(date) FROM prices WHERE base = ? AND quote = ? AND source IN ("+placeholders(len(sources))+")", args...).Scan(&d)
	if err != nil || d == nil {
		return core.Date{}, err
	}
	return core.ParseDate(*d)
}

// Prices returns all stored rates.
func (q Q) Prices(ctx context.Context) ([]core.Price, error) {
	rows, err := q.q.QueryContext(ctx, "SELECT date, base, quote, rate, source FROM prices ORDER BY date, base, quote, source")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Price
	for rows.Next() {
		var date, base, quote, rate, source string
		if err := rows.Scan(&date, &base, &quote, &rate, &source); err != nil {
			return nil, err
		}
		d, err := core.ParseDate(date)
		if err != nil {
			return nil, err
		}
		r, err := decimal.NewFromString(rate)
		if err != nil {
			return nil, err
		}
		out = append(out, core.Price{Base: core.Currency(base), Date: d, Quote: core.Currency(quote), Rate: r, Source: core.PriceSource(source)})
	}
	return out, rows.Err()
}
