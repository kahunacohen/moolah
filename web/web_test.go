// Tests for the browser UI: local-only security checks and the main pages.
package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"moolah/app"
	"moolah/core"
)

const addr = "127.0.0.1:41873"

func newServer(t *testing.T) (*Server, *app.App) {
	t.Helper()
	dir := t.TempDir()
	paths := app.Paths{ConfigDir: filepath.Join(dir, "c"), DataDir: filepath.Join(dir, "d"), StateDir: filepath.Join(dir, "s")}
	a, err := app.Create(app.InitParams{BaseCurrency: "ILS", Path: paths.BookPath("main")}, "main", paths)
	if err != nil {
		t.Fatal(err)
	}
	a.Offline = true
	a.SetClock(func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) })
	t.Cleanup(func() { _ = a.Close() })
	if _, err := a.AddAccount(context.Background(), app.AddAccountParams{Path: "assets:bank:leumi", Currency: "ILS", Opening: "1000", OpeningDate: core.MustDate("2026-10-01")}); err != nil {
		t.Fatal(err)
	}
	s, err := New(a, addr, 1)
	if err != nil {
		t.Fatal(err)
	}
	return s, a
}

type req struct {
	cookie bool
	csrf   bool
	form   url.Values
	host   string
	method string
	origin string
	path   string
	htmx   bool
}

func (s *Server) do(t *testing.T, r req) *httptest.ResponseRecorder {
	t.Helper()
	if r.method == "" {
		r.method = http.MethodGet
	}
	var body io.Reader
	if r.form != nil {
		if r.csrf {
			r.form.Set("csrf", s.csrf)
		}
		body = strings.NewReader(r.form.Encode())
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	hr.Host = addr
	if r.host != "" {
		hr.Host = r.host
	}
	if r.form != nil {
		hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if r.cookie {
		hr.AddCookie(&http.Cookie{Name: cookieName, Value: s.token})
	}
	if r.origin != "" {
		hr.Header.Set("Origin", r.origin)
	}
	if r.htmx {
		hr.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, hr)
	return w
}

func TestSecurity(t *testing.T) {
	s, _ := newServer(t)
	// Without the session cookie, nothing is shown: other local programs cannot read the data.
	t.Run("cookie required", func(t *testing.T) {
		if w := s.do(t, req{path: "/"}); w.Code != http.StatusForbidden {
			t.Fatalf("code %d", w.Code)
		}
	})
	// The printed URL's token signs the browser in and is removed from the address bar.
	t.Run("token sets cookie", func(t *testing.T) {
		w := s.do(t, req{path: "/?token=" + s.token})
		if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Set-Cookie"), cookieName) || strings.Contains(w.Header().Get("Location"), "token") {
			t.Fatalf("code %d headers %v", w.Code, w.Header())
		}
		if w := s.do(t, req{path: "/?token=wrong"}); w.Code != http.StatusForbidden {
			t.Fatalf("wrong token: %d", w.Code)
		}
	})
	// A request for another host name is refused (protects against DNS rebinding).
	t.Run("host checked", func(t *testing.T) {
		if w := s.do(t, req{path: "/", cookie: true, host: "evil.example:41873"}); w.Code != http.StatusForbidden {
			t.Fatalf("code %d", w.Code)
		}
	})
	// Changes need the CSRF token and must not come from another site.
	t.Run("changes need csrf and same origin", func(t *testing.T) {
		form := url.Values{"path": {"food:coffee"}}
		if w := s.do(t, req{method: "POST", path: "/categories", cookie: true, form: form}); w.Code != http.StatusForbidden {
			t.Fatalf("no csrf: %d", w.Code)
		}
		if w := s.do(t, req{method: "POST", path: "/categories", cookie: true, csrf: true, form: url.Values{"path": {"food:coffee"}}, origin: "https://evil.example"}); w.Code != http.StatusForbidden {
			t.Fatalf("other origin: %d", w.Code)
		}
		if w := s.do(t, req{method: "POST", path: "/categories", cookie: true, csrf: true, form: url.Values{"path": {"food:coffee"}}, origin: "http://" + addr}); w.Code != http.StatusSeeOther {
			t.Fatalf("valid: %d %s", w.Code, w.Body)
		}
	})
	// Pages may only load scripts and styles from moolah itself.
	t.Run("content security policy", func(t *testing.T) {
		w := s.do(t, req{path: "/", cookie: true})
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") || w.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatalf("headers %v", w.Header())
		}
	})
}

func TestPages(t *testing.T) {
	// Every page renders without errors.
	t.Run("all pages render", func(t *testing.T) {
		s, _ := newServer(t)
		for _, p := range []string{"/", "/add", "/transfer", "/accounts", "/categories", "/review", "/reports", "/reports?view=income", "/reports?view=networth", "/import", "/tx/1"} {
			if w := s.do(t, req{path: p, cookie: true}); w.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", p, w.Code, w.Body)
			}
		}
	})
	// A purchase added in the browser appears in the transactions list.
	t.Run("add a transaction", func(t *testing.T) {
		s, _ := newServer(t)
		form := url.Values{"account": {"assets:bank:leumi"}, "amount": {"-85"}, "category": {"food:restaurants"}, "date": {"2026-11-03"}, "payee": {"Cafe"}}
		if w := s.do(t, req{method: "POST", path: "/add", cookie: true, csrf: true, form: form}); w.Code != http.StatusSeeOther {
			t.Fatalf("code %d %s", w.Code, w.Body)
		}
		w := s.do(t, req{path: "/?account=assets:bank:leumi", cookie: true})
		if !strings.Contains(w.Body.String(), "Cafe") || !strings.Contains(w.Body.String(), "915.00 ILS") {
			t.Fatal("transaction not listed with its running balance")
		}
	})
	// A typo in the category is shown as an error with suggestions; nothing is saved.
	t.Run("closed taxonomy in forms", func(t *testing.T) {
		s, a := newServer(t)
		form := url.Values{"account": {"assets:bank:leumi"}, "amount": {"-85"}, "category": {"restaurnts"}, "date": {"2026-11-03"}}
		w := s.do(t, req{method: "POST", path: "/add", cookie: true, csrf: true, form: form})
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Did you mean") {
			t.Fatalf("code %d", w.Code)
		}
		rep, _ := a.Register(context.Background(), app.RegisterParams{Account: "leumi"})
		if len(rep.Rows) != 1 {
			t.Fatalf("%d transactions", len(rep.Rows))
		}
	})
	// Saving an edit made on an old version shows "changed elsewhere" instead of overwriting.
	t.Run("stale edit is refused", func(t *testing.T) {
		s, a := newServer(t)
		tx, err := a.Add(context.Background(), app.AddParams{Account: "leumi", Amount: "-10", Category: "food"})
		if err != nil {
			t.Fatal(err)
		}
		edit := func(payee string) *httptest.ResponseRecorder {
			form := url.Values{"version": {"1"}, "date": {tx.Date.String()}, "payee": {payee},
				"acct": {"assets:bank:leumi", "expenses:food", ""}, "amt": {"-10", "10", ""}, "cur": {"ILS", "ILS", ""}}
			return s.do(t, req{method: "POST", path: "/tx/" + itoa(tx.ID), cookie: true, csrf: true, form: form})
		}
		if w := edit("first"); w.Code != http.StatusSeeOther {
			t.Fatalf("first edit: %d %s", w.Code, w.Body)
		}
		if w := edit("second"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "changed elsewhere") {
			t.Fatalf("stale edit: %d", w.Code)
		}
	})
	// With the Hebrew locale the page is right-to-left.
	t.Run("hebrew is rtl", func(t *testing.T) {
		s, a := newServer(t)
		if err := a.SetConfig(context.Background(), app.KeyLocale, "he_IL"); err != nil {
			t.Fatal(err)
		}
		w := s.do(t, req{path: "/", cookie: true})
		if !strings.Contains(w.Body.String(), `dir="rtl"`) || !strings.Contains(w.Body.String(), "תנועות") {
			t.Fatal("not right-to-left Hebrew")
		}
	})
	// Review sets the category of one row and answers with the updated row for htmx.
	t.Run("review row", func(t *testing.T) {
		s, a := newServer(t)
		tx, err := a.Add(context.Background(), app.AddParams{Account: "leumi", Amount: "-12", Category: "uncategorized", TxInfo: app.TxInfo{Payee: "Coffee"}})
		if err != nil {
			t.Fatal(err)
		}
		form := url.Values{"category": {"restaurants"}, "version": {itoa(tx.Version)}}
		w := s.do(t, req{method: "POST", path: "/review/" + itoa(tx.ID), cookie: true, csrf: true, form: form, htmx: true})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "food:restaurants") {
			t.Fatalf("code %d body %s", w.Code, w.Body)
		}
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
