// Tests for the TOML rate-source engine, against local test servers.
package prices

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moolah/core"
)

func serve(t *testing.T, body string, check func(r *http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func source(t *testing.T, text string) *Source {
	t.Helper()
	s, err := ParseSource(text)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var oct1, oct3 = core.MustDate("2026-10-01"), core.MustDate("2026-10-03")

func TestShapes(t *testing.T) {
	// JSON whose rows are an object keyed by date (like Frankfurter); the URL gets the pair and dates.
	t.Run("json keyed by date", func(t *testing.T) {
		url := serve(t, `{"base":"EUR","rates":{"2026-10-01":{"USD":1.1},"2026-10-02":{"USD":1.2}}}`, func(r *http.Request) {
			if r.URL.Path != "/2026-10-01..2026-10-03" || r.URL.Query().Get("from") != "EUR" || r.URL.Query().Get("to") != "USD" {
				t.Errorf("request %s", r.URL)
			}
		})
		s := source(t, `name = "x"
currencies = ["EUR", "USD"]
url = "`+url+`/{from}..{to}?from={base}&to={quote}"
format = "json"
rows = "rates"
rows_keyed_by_date = true
rate = "{quote}"`)
		ps, err := s.Fetch(context.Background(), "EUR", "USD", oct1, oct3)
		if err != nil || len(ps) != 2 || ps[0].Source != "x" {
			t.Fatalf("got %+v, %v", ps, err)
		}
	})
	// JSON whose rows are a list with nested values (like the Bank of Canada).
	t.Run("json list with nested path", func(t *testing.T) {
		url := serve(t, `{"observations":[{"d":"2026-10-01","FXUSDCAD":{"v":"1.3712"}},{"d":"2026-10-02","FXUSDCAD":{"v":"1.3720"}}]}`, nil)
		s := source(t, `name = "boc"
quote = "CAD"
currencies = ["USD"]
url = "`+url+`/FX{base}CAD"
format = "json"
rows = "observations"
date = "d"
rate = "FX{base}CAD.v"`)
		ps, err := s.Fetch(context.Background(), "USD", "CAD", oct1, oct3)
		if err != nil || len(ps) != 2 || ps[0].Rate.String() != "1.3712" {
			t.Fatalf("got %+v, %v", ps, err)
		}
	})
	// CSV with a unit column: a rate quoted per 100 units is scaled to a rate per 1 unit.
	t.Run("csv with unit power", func(t *testing.T) {
		url := serve(t, "TIME_PERIOD,OBS_VALUE,UNIT_MULT\n2026-10-01,1.9445,2\n", nil)
		s := source(t, `name = "c"
quote = "ILS"
currencies = ["JPY"]
url = "`+url+`/{base}"
format = "csv"
date = "TIME_PERIOD"
rate = "OBS_VALUE"
unit_power = "UNIT_MULT"`)
		ps, err := s.Fetch(context.Background(), "JPY", "ILS", oct1, oct3)
		if err != nil || len(ps) != 1 || ps[0].Rate.String() != "0.019445" {
			t.Fatalf("got %+v, %v", ps, err)
		}
	})
	// An HTML error page instead of data means "not published here", so another source is tried.
	t.Run("html answer means unsupported", func(t *testing.T) {
		url := serve(t, "<!DOCTYPE html><html>error</html>", nil)
		s := source(t, `name = "h"
currencies = ["EUR", "USD"]
url = "`+url+`"
format = "csv"
date = "d"
rate = "r"`)
		if _, err := s.Fetch(context.Background(), "EUR", "USD", oct1, oct3); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("got %v", err)
		}
	})
	// max_days splits a long range into several requests, for APIs that limit the range.
	t.Run("max days splits requests", func(t *testing.T) {
		calls := 0
		url := serve(t, "d,r\n", func(r *http.Request) { calls++ })
		s := source(t, `name = "m"
currencies = ["EUR", "USD"]
url = "`+url+`/{from}/{to}"
format = "csv"
date = "d"
rate = "r"
max_days = 10`)
		if _, err := s.Fetch(context.Background(), "EUR", "USD", core.MustDate("2026-10-01"), core.MustDate("2026-10-25")); err != nil || calls != 3 {
			t.Fatalf("calls %d, %v", calls, err)
		}
	})
}

func TestDefinitions(t *testing.T) {
	// A source with "quote" publishes only rates into that currency.
	t.Run("quote limits pairs", func(t *testing.T) {
		s := source(t, `name = "q"
quote = "ILS"
currencies = ["USD", "EUR"]
url = "https://example.org/{base}"
format = "csv"
date = "d"
rate = "r"`)
		if !s.Supports("USD", "ILS") || s.Supports("USD", "EUR") || s.Supports("ILS", "USD") {
			t.Fatal("wrong pairs")
		}
	})
	// Definitions are strict: a misspelled key is an error, not silently ignored.
	t.Run("unknown key rejected", func(t *testing.T) {
		_, err := ParseSource("name = \"x\"\ncurrencies = [\"USD\"]\nurl = \"https://e.org\"\nformat = \"csv\"\ndate = \"d\"\nrate = \"r\"\nrats = 1\n")
		if err == nil || !strings.Contains(err.Error(), "rats") {
			t.Fatalf("got %v", err)
		}
	})
	// Every built-in source must load.
	t.Run("built-ins are valid", func(t *testing.T) {
		b, err := Builtin()
		if err != nil || len(b) == 0 {
			t.Fatalf("got %d, %v", len(b), err)
		}
	})
}
