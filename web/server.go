// Package web is moolah's browser UI: server-rendered HTML with htmx, served on
// 127.0.0.1 only. Handlers call the app package, like the CLI does.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"moolah/app"
	"moolah/core"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

//go:embed i18n/*.json
var i18nFS embed.FS

const cookieName = "moolah_session"

// Server serves one book.
type Server struct {
	Addr       string // host:port the browser uses, e.g. 127.0.0.1:41873
	NoSync     bool   // do not sync with other computers
	app        *app.App
	books      int // number of books, to decide whether to show the book name
	csrf       string
	lastChange time.Time
	mu         sync.Mutex
	pages      map[string]*template.Template
	syncMsg    string
	token      string
	tr         map[string]map[string]string
}

// New prepares a server for an open book. addr is the listen address (127.0.0.1:port).
func New(a *app.App, addr string, books int) (*Server, error) {
	s := &Server{Addr: addr, app: a, books: books, pages: map[string]*template.Template{}, tr: map[string]map[string]string{}}
	var err error
	if s.token, err = randomHex(); err != nil {
		return nil, err
	}
	if s.csrf, err = randomHex(); err != nil {
		return nil, err
	}
	files, err := fs.ReadDir(i18nFS, "i18n")
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		b, err := i18nFS.ReadFile("i18n/" + f.Name())
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("i18n %s: %w", f.Name(), err)
		}
		s.tr[strings.TrimSuffix(f.Name(), ".json")] = m
	}
	funcs := template.FuncMap{
		"money": func(v decimal.Decimal, c core.Currency) string { return core.FormatNumber(v, c.Scale()) },
		"bal":   func(b core.Balance) string { return b.String() },
		"neg": func(b core.Balance) bool {
			for _, v := range b {
				if v.Sign() < 0 {
					return true
				}
			}
			return false
		},
		"leaf": func(p string) string { return p[strings.LastIndex(p, ":")+1:] },
		"leafCat": func(p string) string {
			return p[strings.LastIndex(p, ":")+1:]
		},
		"rowctx": func(p page, item app.ReviewItem) reviewRow {
			return reviewRow{CSRF: p.CSRF, Item: item, T: p.T}
		},
	}
	layout, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Name() == "layout.html" {
			continue
		}
		t, err := layout.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := t.ParseFS(templateFS, "templates/"+e.Name()); err != nil {
			return nil, err
		}
		s.pages[strings.TrimSuffix(e.Name(), ".html")] = t
	}
	return s, nil
}

func randomHex() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// URL is the address to open in the browser, with the session token.
func (s *Server) URL() string { return "http://" + s.Addr + "/?token=" + s.token }

// Handler returns the HTTP handler with all protections.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /{$}", s.locked(s.register))
	mux.HandleFunc("GET /add", s.locked(s.addForm))
	mux.HandleFunc("POST /add", s.locked(s.addPost))
	mux.HandleFunc("GET /transfer", s.locked(s.transferForm))
	mux.HandleFunc("POST /transfer", s.locked(s.transferPost))
	mux.HandleFunc("GET /tx/{id}", s.locked(s.editForm))
	mux.HandleFunc("POST /tx/{id}", s.locked(s.editPost))
	mux.HandleFunc("POST /tx/{id}/delete", s.locked(s.deletePost))
	mux.HandleFunc("GET /accounts", s.locked(s.accounts))
	mux.HandleFunc("POST /accounts", s.locked(s.accountsPost))
	mux.HandleFunc("GET /categories", s.locked(s.categories))
	mux.HandleFunc("POST /categories", s.locked(s.categoriesPost))
	mux.HandleFunc("GET /review", s.locked(s.review))
	mux.HandleFunc("POST /review/{id}", s.locked(s.reviewPost))
	mux.HandleFunc("POST /review/transfer/{id}", s.locked(s.reviewTransfer))
	mux.HandleFunc("GET /reports", s.locked(s.reports))
	mux.HandleFunc("GET /import", s.locked(s.importForm))
	mux.HandleFunc("POST /import", s.locked(s.importPost))
	return s.protect(mux)
}

// protect adds security headers and checks Host, the session cookie, and for
// changes the Origin header and CSRF token.
func (s *Server) protect(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.Addr)
	allowedHosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if !allowedHosts[r.Host] {
			http.Error(w, "moolah only answers on "+s.Addr, http.StatusForbidden)
			return
		}
		if tok := r.URL.Query().Get("token"); tok != "" {
			if !equal(tok, s.token) {
				http.Error(w, "wrong token: open the address that moolah serve printed", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			q := r.URL.Query()
			q.Del("token")
			u := *r.URL
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || !equal(c.Value, s.token) {
			http.Error(w, "not signed in: open the address that moolah serve printed (it contains ?token=…)", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || !allowedHosts[u.Host] {
					http.Error(w, "request from another site refused", http.StatusForbidden)
					return
				}
			}
			tok := r.Header.Get("X-CSRF-Token")
			if tok == "" {
				tok = r.FormValue("csrf")
			}
			if !equal(tok, s.csrf) {
				http.Error(w, "the form expired: reload the page and try again", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// locked runs a handler while holding the book (one request at a time).
func (s *Server) locked(h func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		h(w, r)
	}
}

// changed records a change, so the sync loop uploads it soon.
func (s *Server) changed() { s.lastChange = time.Now() }

// page is what every template gets.
type page struct {
	Book        string
	CSRF        string
	Data        any
	Dir         string
	Error       string
	Flash       string
	Lang        string
	Nav         string
	ReviewCount int
	SyncMsg     string
	Title       string
	en          map[string]string
	tr          map[string]string
}

// T translates a UI text key; English when the language has no translation for it.
func (p page) T(key string) string {
	if v, ok := p.tr[key]; ok {
		return v
	}
	if v, ok := p.en[key]; ok {
		return v
	}
	return key
}

// translation returns the texts for a locale: "pt_BR.json", else "pt.json", else English.
func (s *Server) translation(locale string) map[string]string {
	if m, ok := s.tr[locale]; ok {
		return m
	}
	lang, _, _ := strings.Cut(locale, "_")
	if m, ok := s.tr[lang]; ok {
		return m
	}
	return s.tr["en"]
}

type reviewRow struct {
	CSRF  string
	Done  string
	Error string
	Item  app.ReviewItem
	T     func(string) string
}

func (s *Server) newPage(r *http.Request, nav, title string) page {
	ctx := r.Context()
	loc, _ := s.app.Config(ctx, app.KeyLocale)
	p := page{CSRF: s.csrf, Dir: "ltr", Lang: strings.ReplaceAll(loc, "_", "-"), Nav: nav, SyncMsg: s.syncMsg,
		tr: s.translation(loc), en: s.tr["en"]}
	if app.IsRightToLeft(loc) {
		p.Dir = "rtl"
	}
	core.SetNumberStyle(app.NumberStyleFor(loc))
	p.Title = p.T(title)
	if s.books > 1 {
		p.Book = s.app.Book
	}
	if items, err := s.app.Review(ctx); err == nil {
		p.ReviewCount = len(items)
		if waiting, err := s.app.WaitingTransfers(ctx); err == nil {
			p.ReviewCount += len(waiting)
		}
	}
	if msg := r.URL.Query().Get("ok"); msg != "" {
		p.Flash = p.T(msg)
	}
	return p
}

func (s *Server) render(w http.ResponseWriter, name string, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "no page "+name, http.StatusInternalServerError)
		return
	}
	if err := t.ExecuteTemplate(w, "layout", p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	p := s.newPage(r, "", "error")
	p.Error = err.Error()
	w.WriteHeader(http.StatusInternalServerError)
	s.render(w, "register", withData(p, registerData{}))
}

func withData(p page, d any) page {
	p.Data = d
	return p
}

// redirect sends the browser to path with a short message key.
func redirect(w http.ResponseWriter, r *http.Request, path, msg string) {
	if msg != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + "ok=" + url.QueryEscape(msg)
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", path)
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// Run serves until ctx ends. It syncs with other computers in the background:
// it loads newer data when there are no local changes, and uploads changes a few
// seconds after the last one, and once more when it stops.
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	started := time.Now()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	lastPull := time.Now()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := srv.Shutdown(shutdown)
			cancel()
			s.mu.Lock()
			if s.NoSync {
				s.mu.Unlock()
				return err
			}
			if serr := s.app.SyncAfter(context.Background()); serr != nil && err == nil {
				err = fmt.Errorf("your changes are saved on this computer, but were not uploaded: %w", serr)
			}
			s.mu.Unlock()
			return err
		case err := <-errc:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case now := <-tick.C:
			if s.NoSync {
				continue
			}
			s.mu.Lock()
			bg := context.Background()
			due := !s.lastChange.IsZero() && now.Sub(s.lastChange) >= 5*time.Second
			if due || now.Sub(lastPull) >= 30*time.Second {
				if _, err := s.app.SyncNow(bg); err != nil && !strings.Contains(err.Error(), "sync is off") {
					s.syncMsg = err.Error()
				} else {
					s.syncMsg = ""
				}
				if due {
					s.lastChange = time.Time{}
				}
				lastPull = now
				_ = s.app.Heartbeat(bg, started)
			}
			s.mu.Unlock()
		}
	}
}

// timeNow is the clock for form defaults; tests replace it.
var timeNow = time.Now
