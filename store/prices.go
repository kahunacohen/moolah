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
