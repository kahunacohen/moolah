// Package prices downloads official exchange rates: Bank of Israel representative
// rates (X→ILS) and European Central Bank reference rates through the Frankfurter API.
// Only currency codes and dates are sent.
package prices

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"moolah/core"
)

// ErrUnsupported means the source does not publish this currency pair.
var ErrUnsupported = errors.New("this source does not publish this pair")

// Fetcher downloads daily rates for one pair: 1 base = rate quote.
type Fetcher interface {
	Fetch(ctx context.Context, base, quote core.Currency, from, to core.Date) ([]core.Price, error)
	Source() core.PriceSource
}

var defaultClient = &http.Client{Timeout: 30 * time.Second}

func get(ctx context.Context, client *http.Client, u string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "moolah (personal finance CLI)")
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s answered HTTP %d", req.URL.Host, resp.StatusCode)
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// BOI fetches Bank of Israel representative rates (שער יציג), only against ILS.
type BOI struct {
	BaseURL string // default https://edge.boi.gov.il/FusionEdgeServer/sdmx/v2/data/dataflow/BOI.STATISTICS/EXR/1.0/
	Client  *http.Client
}

// Source implements Fetcher.
func (BOI) Source() core.PriceSource { return core.SourceBOI }

// Fetch implements Fetcher.
func (b BOI) Fetch(ctx context.Context, base, quote core.Currency, from, to core.Date) ([]core.Price, error) {
	if quote != "ILS" || base == "ILS" {
		return nil, ErrUnsupported
	}
	root := b.BaseURL
	if root == "" {
		root = "https://edge.boi.gov.il/FusionEdgeServer/sdmx/v2/data/dataflow/BOI.STATISTICS/EXR/1.0/"
	}
	u := fmt.Sprintf("%sRER_%s_ILS?startperiod=%s&endperiod=%s&format=csv", root, base, from, to)
	body, _, err := get(ctx, b.Client, u)
	if err != nil {
		return nil, err
	}
	return parseBOICSV(body, base)
}

// parseBOICSV reads the SDMX CSV. OBS_VALUE is ILS per 10^UNIT_MULT units (JPY is per 100).
func parseBOICSV(body []byte, base core.Currency) ([]core.Price, error) {
	text := strings.TrimSpace(string(body))
	if !strings.HasPrefix(text, "SERIES_CODE") {
		return nil, ErrUnsupported // BoI answers unknown series with an HTML page
	}
	recs, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("bank of israel: %w", err)
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[h] = i
	}
	for _, need := range []string{"TIME_PERIOD", "OBS_VALUE", "UNIT_MULT"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("bank of israel: the answer has no %s column; the API may have changed", need)
		}
	}
	var out []core.Price
	for _, r := range recs[1:] {
		d, err := core.ParseDate(r[col["TIME_PERIOD"]])
		if err != nil {
			continue
		}
		v, err := decimal.NewFromString(r[col["OBS_VALUE"]])
		if err != nil || v.Sign() <= 0 {
			continue
		}
		mult, _ := strconv.Atoi(r[col["UNIT_MULT"]])
		out = append(out, core.Price{Base: base, Date: d, Quote: "ILS", Rate: v.Shift(int32(-mult)), Source: core.SourceBOI})
	}
	return out, nil
}

// ECB fetches European Central Bank reference rates through the Frankfurter API.
type ECB struct {
	BaseURL string // default https://api.frankfurter.dev/v1/
	Client  *http.Client
}

// Source implements Fetcher.
func (ECB) Source() core.PriceSource { return core.SourceECB }

// Fetch implements Fetcher.
func (e ECB) Fetch(ctx context.Context, base, quote core.Currency, from, to core.Date) ([]core.Price, error) {
	root := e.BaseURL
	if root == "" {
		root = "https://api.frankfurter.dev/v1/"
	}
	u := fmt.Sprintf("%s%s..%s?from=%s&to=%s", root, from, to, url.QueryEscape(string(base)), url.QueryEscape(string(quote)))
	body, _, err := get(ctx, e.Client, u)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Rates map[string]map[string]json.Number `json:"rates"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("frankfurter: %w", err)
	}
	var out []core.Price
	for day, rates := range resp.Rates {
		d, err := core.ParseDate(day)
		if err != nil {
			continue
		}
		v, ok := rates[string(quote)]
		if !ok {
			continue
		}
		r, err := decimal.NewFromString(v.String())
		if err != nil || r.Sign() <= 0 {
			continue
		}
		out = append(out, core.Price{Base: base, Date: d, Quote: quote, Rate: r, Source: core.SourceECB})
	}
	if len(out) == 0 && len(resp.Rates) == 0 {
		return nil, ErrUnsupported
	}
	return out, nil
}
