package web

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"moolah/app"
	"moolah/core"
)

type registerData struct {
	Account string
	Choices []string
	Rows    []app.RegisterRow
	Tag     string
}

// names lists account paths; real=true for assets and liabilities that hold money,
// false for categories (shown without "expenses:").
func (s *Server) names(r *http.Request, real bool) ([]string, error) {
	tree, err := s.app.Tree(r.Context())
	if err != nil {
		return nil, err
	}
	var out []string
	tree.Walk(func(a core.Account) {
		switch {
		case a.IsRoot() || a.IsClosed():
		case real && a.Type.IsReal() && !a.IsGroup():
			out = append(out, tree.Path(a.ID))
		case !real && (a.Type.IsCategory() || (a.Type.IsReal() && !a.IsGroup())):
			out = append(out, app.DisplayName(tree, a.ID, app.ScopeCategory))
		}
	})
	return out, nil
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(r, "register", "nav.register")
	d := registerData{Account: r.URL.Query().Get("account"), Tag: r.URL.Query().Get("tag")}
	var err error
	if d.Choices, err = s.names(r, true); err != nil {
		s.fail(w, r, err)
		return
	}
	rep, err := s.app.Register(r.Context(), app.RegisterParams{Account: d.Account, Tag: d.Tag})
	if err != nil {
		p.Error = err.Error()
	}
	// Newest first is easier to read in a browser; the running balance stays correct.
	for i := len(rep.Rows) - 1; i >= 0 && len(d.Rows) < 300; i-- {
		d.Rows = append(d.Rows, rep.Rows[i])
	}
	s.render(w, "register", withData(p, d))
}

type formData struct {
	Categories []string
	Form       map[string]string
	Real       []string
}

func (s *Server) formData(r *http.Request) (formData, error) {
	var d formData
	var err error
	if d.Real, err = s.names(r, true); err != nil {
		return d, err
	}
	d.Categories, err = s.names(r, false)
	d.Form = map[string]string{}
	return d, err
}

func formValues(r *http.Request, keys ...string) map[string]string {
	m := map[string]string{}
	for _, k := range keys {
		m[k] = strings.TrimSpace(r.FormValue(k))
	}
	return m
}

func tags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) addForm(w http.ResponseWriter, r *http.Request) {
	d, err := s.formData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Form["date"] = core.DateOf(timeNow()).String()
	s.render(w, "add", withData(s.newPage(r, "add", "add.title"), d))
}

func (s *Server) addPost(w http.ResponseWriter, r *http.Request) {
	f := formValues(r, "account", "amount", "category", "date", "payee", "note", "tags")
	date, err := core.ParseDate(f["date"])
	if err == nil {
		_, err = s.app.Add(r.Context(), app.AddParams{Account: f["account"], Amount: f["amount"], Category: f["category"],
			TxInfo: app.TxInfo{Date: date, Note: f["note"], Payee: f["payee"], Tags: tags(f["tags"])}})
	}
	if err != nil {
		d, derr := s.formData(r)
		if derr != nil {
			s.fail(w, r, derr)
			return
		}
		d.Form = f
		p := s.newPage(r, "add", "add.title")
		p.Error = err.Error()
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, "add", withData(p, d))
		return
	}
	s.changed()
	redirect(w, r, "/add", "saved")
}

func (s *Server) transferForm(w http.ResponseWriter, r *http.Request) {
	d, err := s.formData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Form["date"] = core.DateOf(timeNow()).String()
	s.render(w, "transfer", withData(s.newPage(r, "transfer", "transfer.title"), d))
}

func (s *Server) transferPost(w http.ResponseWriter, r *http.Request) {
	f := formValues(r, "from", "to", "amount", "to_amount", "fee", "date", "payee")
	date, err := core.ParseDate(f["date"])
	if err == nil {
		_, err = s.app.Transfer(r.Context(), app.TransferParams{From: f["from"], To: f["to"], Amount: f["amount"],
			ToAmount: f["to_amount"], Fee: f["fee"], TxInfo: app.TxInfo{Date: date, Payee: f["payee"]}})
	}
	if err != nil {
		d, derr := s.formData(r)
		if derr != nil {
			s.fail(w, r, derr)
			return
		}
		d.Form = f
		p := s.newPage(r, "transfer", "transfer.title")
		p.Error = err.Error()
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, "transfer", withData(p, d))
		return
	}
	s.changed()
	redirect(w, r, "/", "saved")
}

type editLine struct {
	Account, Amount, Cost, CostCurrency, Currency string
}

type editData struct {
	Accounts                []string
	Date, Note, Payee, Tags string
	ID, Version             int64
	Lines                   []editLine
}

func (s *Server) editData(r *http.Request, t core.Transaction) (editData, error) {
	tree, err := s.app.Tree(r.Context())
	if err != nil {
		return editData{}, err
	}
	d := editData{Date: t.Date.String(), ID: t.ID, Note: t.Note, Payee: t.Payee, Tags: strings.Join(t.Tags, ", "), Version: t.Version}
	for _, p := range t.Postings {
		l := editLine{Account: tree.Path(p.AccountID), Amount: p.Amount.Value.StringFixed(p.Amount.Currency.Scale()), Currency: string(p.Amount.Currency)}
		if p.Cost != nil {
			l.Cost, l.CostCurrency = p.Cost.Value.StringFixed(p.Cost.Currency.Scale()), string(p.Cost.Currency)
		}
		d.Lines = append(d.Lines, l)
	}
	d.Lines = append(d.Lines, editLine{}, editLine{})
	tree.Walk(func(a core.Account) {
		if !a.IsGroup() {
			d.Accounts = append(d.Accounts, tree.Path(a.ID))
		}
	})
	return d, nil
}

func txID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func (s *Server) editForm(w http.ResponseWriter, r *http.Request) {
	id, err := txID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, err := s.app.Transaction(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.editData(r, t)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "edit", withData(s.newPage(r, "register", "edit.title"), d))
}

func (s *Server) editPost(w http.ResponseWriter, r *http.Request) {
	id, err := txID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	orig, err := s.app.Transaction(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	t, perr := s.parseEdit(r, orig)
	if perr == nil {
		perr = s.app.UpdateTransaction(r.Context(), t)
	}
	if perr == nil {
		s.changed()
		redirect(w, r, "/tx/"+strconv.FormatInt(id, 10), "saved")
		return
	}
	p := s.newPage(r, "register", "edit.title")
	p.Error = perr.Error()
	if errors.Is(perr, app.ErrChangedElsewhere) {
		p.Error = p.T("edit.changed")
	}
	d, err := s.editData(r, t)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusConflict)
	s.render(w, "edit", withData(p, d))
}

// parseEdit builds the edited transaction from the form, keeping orig's ID, kind and metadata.
func (s *Server) parseEdit(r *http.Request, orig core.Transaction) (core.Transaction, error) {
	if err := r.ParseForm(); err != nil {
		return orig, err
	}
	t := orig
	var err error
	if t.Version, err = strconv.ParseInt(r.FormValue("version"), 10, 64); err != nil {
		return orig, errors.New("the form has no version; reload the page")
	}
	if t.Date, err = core.ParseDate(r.FormValue("date")); err != nil {
		return orig, err
	}
	t.Payee, t.Note = strings.TrimSpace(r.FormValue("payee")), strings.TrimSpace(r.FormValue("note"))
	t.Tags = tags(r.FormValue("tags"))
	tree, err := s.app.Tree(r.Context())
	if err != nil {
		return orig, err
	}
	accts, amts, curs := r.Form["acct"], r.Form["amt"], r.Form["cur"]
	costs, costCurs := r.Form["cost"], r.Form["costcur"]
	t.Postings = nil
	for i := range accts {
		name := strings.TrimSpace(accts[i])
		if name == "" {
			continue
		}
		acct, err := tree.Resolve(name, nil)
		if err != nil {
			return t, err
		}
		cur := at(curs, i)
		if cur == "" {
			cur = string(acct.Currency)
		}
		c, err := core.ParseCurrency(cur)
		if err != nil {
			return t, err
		}
		v, err := core.ParseDecimal(at(amts, i))
		if err != nil {
			return t, err
		}
		p := core.Posting{AccountID: acct.ID, Amount: core.NewAmount(v, c)}
		if cv := at(costs, i); cv != "" {
			cc, err := core.ParseCurrency(at(costCurs, i))
			if err != nil {
				return t, err
			}
			cost, err := core.ParseDecimal(cv)
			if err != nil {
				return t, err
			}
			p.Cost = &core.Amount{Currency: cc, Value: cost}
		}
		t.Postings = append(t.Postings, p)
	}
	return t, nil
}

func at(s []string, i int) string {
	if i < len(s) {
		return strings.TrimSpace(s[i])
	}
	return ""
}

func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	id, err := txID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.app.DeleteTransaction(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.changed()
	redirect(w, r, "/", "deleted")
}

type accountsData struct {
	Form   map[string]string
	Report app.BalanceReport
}

func (s *Server) accounts(w http.ResponseWriter, r *http.Request) {
	s.accountsPage(w, r, map[string]string{"date": core.DateOf(timeNow()).String()}, "")
}

func (s *Server) accountsPage(w http.ResponseWriter, r *http.Request, form map[string]string, errMsg string) {
	p := s.newPage(r, "accounts", "accounts")
	p.Error = errMsg
	rep, err := s.app.Balances(r.Context(), app.BalanceParams{})
	if err != nil && p.Error == "" {
		p.Error = err.Error()
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	s.render(w, "accounts", withData(p, accountsData{Form: form, Report: rep}))
}

func (s *Server) accountsPost(w http.ResponseWriter, r *http.Request) {
	f := formValues(r, "path", "currency", "opening", "date")
	var date core.Date
	var err error
	if f["date"] != "" {
		date, err = core.ParseDate(f["date"])
	}
	if err == nil {
		_, err = s.app.AddAccount(r.Context(), app.AddAccountParams{Path: f["path"], Currency: f["currency"], Opening: f["opening"], OpeningDate: date})
	}
	if err != nil {
		s.accountsPage(w, r, f, err.Error())
		return
	}
	s.changed()
	redirect(w, r, "/accounts", "saved")
}

type categoryRow struct {
	Depth int
	Name  string
}

type categoriesData struct {
	Expenses, Income []categoryRow
	Form             map[string]string
}

func (s *Server) categories(w http.ResponseWriter, r *http.Request) {
	s.categoriesPage(w, r, map[string]string{}, "")
}

func (s *Server) categoriesPage(w http.ResponseWriter, r *http.Request, form map[string]string, errMsg string) {
	p := s.newPage(r, "categories", "categories")
	p.Error = errMsg
	tree, err := s.app.Tree(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := categoriesData{Form: form}
	tree.Walk(func(a core.Account) {
		if a.IsRoot() || !a.Type.IsCategory() || a.IsClosed() {
			return
		}
		row := categoryRow{Depth: tree.Depth(a.ID), Name: a.Name}
		if a.Type == core.Income {
			d.Income = append(d.Income, row)
		} else {
			d.Expenses = append(d.Expenses, row)
		}
	})
	if errMsg != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	s.render(w, "categories", withData(p, d))
}

func (s *Server) categoriesPost(w http.ResponseWriter, r *http.Request) {
	f := formValues(r, "path", "income")
	if _, err := s.app.AddCategory(r.Context(), app.AddCategoryParams{Path: f["path"], Income: f["income"] == "1"}); err != nil {
		s.categoriesPage(w, r, f, err.Error())
		return
	}
	s.changed()
	redirect(w, r, "/categories", "saved")
}

type reviewData struct {
	Categories []string
	Items      []app.ReviewItem
}

func (s *Server) review(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(r, "review", "review.title")
	items, err := s.app.Review(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cats, err := s.names(r, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "review", withData(p, reviewData{Categories: cats, Items: items}))
}

// reviewPost sets a category and returns the updated table row (for htmx).
func (s *Server) reviewPost(w http.ResponseWriter, r *http.Request) {
	id, err := txID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)
	p := s.newPage(r, "review", "review.title")
	items, err := s.app.Review(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	row := reviewRow{CSRF: s.csrf, T: p.T}
	for _, it := range items {
		if it.Tx.ID == id {
			row.Item = it
		}
	}
	if row.Item.Tx.ID == 0 {
		redirect(w, r, "/review", "")
		return
	}
	category := strings.TrimSpace(r.FormValue("category"))
	name, err := s.app.Categorize(r.Context(), app.CategorizeParams{TxID: id, Version: version, Category: category})
	if err != nil {
		row.Error = err.Error()
	} else {
		row.Done = name
		s.changed()
		if r.FormValue("rule") == "1" {
			if _, n, err := s.app.AddRule(r.Context(), app.RuleParams{Pattern: app.SuggestPattern(row.Item.Tx.Payee), Account: name}); err != nil {
				row.Error = err.Error()
			} else if n > 0 && r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Refresh", "true") // other rows changed too
			}
		}
	}
	if r.Header.Get("HX-Request") != "true" {
		redirect(w, r, "/review", "saved")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages["review"].ExecuteTemplate(w, "reviewrow", row); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type reportsData struct {
	Balance   *app.BalanceReport
	ByCharge  bool
	From, To  string
	In        string
	Income    *app.IncomeReport
	Month     string
	NetWorth  *app.NetWorthReport
	StartDate string
	View      string
}

func (s *Server) reports(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := reportsData{View: q.Get("view"), In: q.Get("in"), Month: q.Get("month"), From: q.Get("from"), To: q.Get("to"),
		ByCharge: q.Get("by") == "charge-date"}
	if d.View == "" {
		d.View = "balance"
	}
	p := s.newPage(r, "reports", "nav.reports")
	ctx := r.Context()
	var err error
	switch d.View {
	case "balance":
		var rep app.BalanceReport
		rep, err = s.app.Balances(ctx, app.BalanceParams{In: d.In})
		d.Balance = &rep
	case "income":
		if d.Month == "" {
			d.Month = core.DateOf(timeNow()).String()[:7]
		}
		var from core.Date
		if from, err = core.ParseDate(d.Month + "-01"); err == nil {
			to := core.MustDate(from.AddDays(32).String()[:8] + "01").AddDays(-1)
			var rep app.IncomeReport
			rep, err = s.app.Income(ctx, app.IncomeParams{Period: app.Period{From: from, To: to}, In: d.In, ByChargeDate: d.ByCharge})
			d.Income = &rep
		}
	case "networth":
		var period app.Period
		if period.From, err = optDate(d.From); err == nil {
			if period.To, err = optDate(d.To); err == nil {
				var rep app.NetWorthReport
				rep, err = s.app.NetWorth(ctx, app.NetWorthParams{Period: period, In: d.In})
				d.NetWorth = &rep
				d.StartDate = rep.Period.From.AddDays(-1).String()
			}
		}
	}
	if err != nil {
		p.Error = err.Error()
		d.Balance, d.Income, d.NetWorth = nil, nil, nil
	}
	s.render(w, "reports", withData(p, d))
}

func optDate(s string) (core.Date, error) {
	if s == "" {
		return core.Date{}, nil
	}
	return core.ParseDate(s)
}

type importData struct {
	Profiles []app.ProfileInfo
	Real     []string
	Result   *app.ImportResult
}

func (s *Server) importData(r *http.Request) (importData, error) {
	var d importData
	var err error
	if d.Real, err = s.names(r, true); err != nil {
		return d, err
	}
	d.Profiles, err = s.app.Profiles(r.Context())
	return d, err
}

func (s *Server) importForm(w http.ResponseWriter, r *http.Request) {
	d, err := s.importData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "import", withData(s.newPage(r, "import", "import.title"), d))
}

func (s *Server) importPost(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(r, "import", "import.title")
	d, err := s.importData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.importUpload(r)
	if err != nil {
		p.Error = err.Error()
		w.WriteHeader(http.StatusUnprocessableEntity)
	} else {
		d.Result = &res
		if r.FormValue("dry") != "1" {
			s.changed()
		}
	}
	s.render(w, "import", withData(p, d))
}

// importUpload saves the uploaded file under its own name in a private temporary
// folder (profiles may match on the file name) and imports it.
func (s *Server) importUpload(r *http.Request) (app.ImportResult, error) {
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return app.ImportResult{}, errors.New("choose a file (at most 20 MB)")
	}
	defer f.Close()
	dir, err := os.MkdirTemp("", "moolah-upload-")
	if err != nil {
		return app.ImportResult{}, err
	}
	defer os.RemoveAll(dir)
	name := filepath.Base(hdr.Filename)
	if name == "." || name == "/" || name == "" {
		name = "upload"
	}
	path := filepath.Join(dir, name)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return app.ImportResult{}, err
	}
	_, err = io.Copy(out, f)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return app.ImportResult{}, err
	}
	return s.app.Import(r.Context(), app.ImportParams{Account: r.FormValue("account"), DryRun: r.FormValue("dry") == "1",
		File: path, Profile: r.FormValue("profile")})
}
