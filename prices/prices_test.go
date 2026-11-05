// Tests for the exchange-rate downloaders, against recorded answers.
package prices

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"moolah/core"
)

const boiCSV = `SERIES_CODE,FREQ,BASE_CURRENCY,COUNTER_CURRENCY,UNIT_MEASURE,DATA_TYPE,DATA_SOURCE,TIME_COLLECT,CONF_STATUS,PUB_WEBSITE,UNIT_MULT,COMMENTS,TIME_PERIOD,OBS_VALUE,RELEASE_STATUS
RER_JPY_ILS,D,JPY,ILS,ILS,OF00,BOI_MRKT,V,F,Y,2,,2026-10-01,1.9445,YP
RER_JPY_ILS,D,JPY,ILS,ILS,OF00,BOI_MRKT,V,F,Y,2,,2026-10-02,1.9414,YP
`

func TestBOI(t *testing.T) {
	// BoI quotes some currencies per 100 units; UNIT_MULT scales them to a rate per 1 unit.
	t.Run("unit multiplier", func(t *testing.T) {
		ps, err := parseBOICSV([]byte(boiCSV), "JPY")
		if err != nil || len(ps) != 2 || ps[0].Rate.String() != "0.019445" {
			t.Fatalf("got %+v, %v", ps, err)
		}
	})
	// An unknown series comes back as an HTML page; it means "not published here", so another source is tried.
	t.Run("html answer means unsupported", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<!DOCTYPE html><html dir=\"rtl\">error</html>"))
		}))
		defer srv.Close()
		_, err := BOI{BaseURL: srv.URL + "/"}.Fetch(context.Background(), "XAU", "ILS", core.MustDate("2026-10-01"), core.MustDate("2026-10-02"))
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestECB(t *testing.T) {
	// The Frankfurter answer is parsed into one price per day.
	t.Run("parses daily rates", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("from") != "EUR" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"amount":1.0,"base":"EUR","rates":{"2026-10-01":{"ILS":3.6},"2026-10-02":{"ILS":3.61}}}`))
		}))
		defer srv.Close()
		ps, err := ECB{BaseURL: srv.URL + "/"}.Fetch(context.Background(), "EUR", "ILS", core.MustDate("2026-10-01"), core.MustDate("2026-10-02"))
		if err != nil || len(ps) != 2 {
			t.Fatalf("got %+v, %v", ps, err)
		}
	})
}
