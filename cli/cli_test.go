// End-to-end tests of the moolah command line, compared with golden files.
package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"moolah/app"
	"moolah/core"
)

var update = flag.Bool("update", false, "rewrite golden files")

// session runs commands against one temporary set of books and records the output.
type session struct {
	answers string // typed into the prompts of the next command, which then runs as in a terminal
	env     app.Paths
	log     bytes.Buffer
	t       *testing.T
}

// History shows local times; tests show them in UTC on every computer.
func init() { time.Local = time.UTC }

func newSession(t *testing.T) *session {
	dir := t.TempDir()
	return &session{env: app.Paths{ConfigDir: filepath.Join(dir, "config"), DataDir: filepath.Join(dir, "data"), StateDir: filepath.Join(dir, "state")}, t: t}
}

func (s *session) run(line string) {
	s.t.Helper()
	var out, errOut bytes.Buffer
	env := Env{
		Getenv:      func(string) string { return "" },
		Interactive: s.answers != "",
		Now:         func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) },
		Offline:     true,
		Paths:       s.env,
		Stderr:      &errOut,
		Stdin:       strings.NewReader(s.answers),
		Stdout:      &out,
	}
	shown := line
	if s.answers != "" {
		shown += "    # answers: " + strings.ReplaceAll(strings.TrimSuffix(s.answers, "\n"), "\n", " | ")
		s.answers = ""
	}
	code := Run(env, splitArgs(line))
	tmp := filepath.Dir(s.env.DataDir)
	fmt.Fprintf(&s.log, "$ moolah %s\n%s", strings.ReplaceAll(shown, tmp, "$TMP"), strings.ReplaceAll(out.String(), tmp, "$TMP"))
	if errOut.Len() > 0 {
		s.log.WriteString(strings.ReplaceAll(errOut.String(), tmp, "$TMP"))
	}
	if code != 0 {
		fmt.Fprintf(&s.log, "[exit %d]\n", code)
	}
	s.log.WriteString("\n")
}

// splitArgs splits on spaces, keeping "quoted parts" together.
func splitArgs(line string) []string {
	var args []string
	var cur strings.Builder
	quoted := false
	for _, r := range line {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ' ' && !quoted:
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

func (s *session) golden(name string) {
	s.t.Helper()
	path := filepath.Join("testdata", name+".golden")
	got := s.log.Bytes()
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			s.t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			s.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		s.t.Fatalf("%v (run make golden to create it)", err)
	}
	if !bytes.Equal(got, want) {
		s.t.Errorf("output differs from %s (run make golden and review the diff):\n%s", path, got)
	}
}

func TestScenarios(t *testing.T) {
	// The everyday flow: accounts, purchases in two currencies, a card payment with
	// currency exchange, a pension valuation, and reports in two currencies.
	t.Run("everyday", func(t *testing.T) {
		s := newSession(t)
		s.run("init --base ILS --no-backup-key")
		s.run("account add leumi --kind bank --currency ILS --opening 25000 --date 2026-10-01")
		s.run("account add chase --kind card --currency USD")
		s.run("account add harel --kind pension --currency ILS --opening 380000 --date 2026-01-01")
		s.run(`add leumi -200 food:groceries --payee Shufersal --date 2026-10-03`)
		s.run(`add chase -42.17 shopping --payee Amazon --date 2026-10-05 --tag vacation-2026`)
		s.run("add leumi -85 restaurnts")
		s.run("add leumi -85 food")
		s.run("add --payee Super-Pharm --date 2026-10-20 --posting leumi=-300 --posting health=180 --posting household=120")
		s.run("transfer leumi chase 1530 --to-amount 420 --fee 15 --date 2026-11-01")
		s.run("add harel 2400 salary:pension --date 2026-02-10")
		s.run("value harel 390000 --date 2026-10-31")
		s.run("rates add 2026-10-01 USD ILS 3.70")
		s.run("rates add 2026-11-02 USD ILS 3.80")
		s.run("balance --date 2026-10-31")
		s.run("balance")
		s.run("balance --in USD")
		s.run("register leumi")
		s.run("register --category food")
		s.run("register --tag vacation-2026")
		s.run("tag list")
		s.run("report income --month 2026-10")
		s.run("report income --month 2026-10 --depth 1")
		s.run("report networth --from 2026-10-01 --to 2026-11-04")
		s.run("check")
		// Programs read CSV and JSON, so they ignore the book's number format.
		s.run("config set locale de_DE")
		s.run("balance --date 2026-10-31")
		s.run("--format csv balance --date 2026-10-31")
		s.run("--format csv register leumi")
		// CSV has no indentation, so tree rows carry the full path.
		s.run("--format csv report income --month 2026-10")
		s.run("--format csv category list")
		s.golden("everyday")
	})

	// Every change is kept under the command that made it, with what it changed;
	// pruning removes history, not data.
	t.Run("history", func(t *testing.T) {
		s := newSession(t)
		writeMachineID(t, s, "laptop-1a2b")
		s.run("init --base ILS --no-backup-key")
		s.run("account add leumi --kind bank --currency ILS --opening 1000 --date 2026-10-01")
		s.run("add leumi -50 food --date 2026-10-02 --payee Cafe --tag trip")
		s.run("category rename food eat")
		s.run("rule add cafe eat")
		s.run("config set locale de_DE")
		s.run("delete 2 --yes")
		s.run("history")
		s.run("history 4")
		s.run("history 7")
		s.run("history --tx 2")
		s.run("--format csv history --limit 2")
		s.run("history prune --before 2026-11-05 --yes")
		s.run("history")
		s.run("account list")
		s.golden("history")
	})

	// The export holds every transaction, account, rate and rule as text, and
	// never overwrites files.
	t.Run("export", func(t *testing.T) {
		s := newSession(t)
		s.run("init --base ILS --no-backup-key")
		s.run("account add leumi --kind bank --currency ILS --opening 25000 --date 2026-10-01")
		s.run("account add chase --kind card --currency USD")
		s.run("account add harel --kind pension --currency ILS --opening 380000 --date 2026-10-01")
		s.run("add leumi -200 food --date 2026-10-03 --payee Shufersal --note weekly --tag trip")
		s.run("transfer leumi chase 1530 --to-amount 420 --fee 15 --date 2026-11-01")
		s.run("add chase -42.17 shopping --date 2026-11-02 --payee Amazon")
		s.run("value harel 390000 --date 2026-10-31")
		s.run("rates add 2026-10-31 USD ILS 3.65")
		s.run("rule add amazon shopping")
		dir := filepath.Join(filepath.Dir(s.env.DataDir), "export")
		s.run("export " + dir)
		s.run("export " + dir)
		for _, f := range []string{"moolah.journal", "transactions.csv", "prices.csv", "rules.csv", "settings.toml"} {
			b, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&s.log, "\n==> %s <==\n%s", f, b)
		}
		s.golden("export")
		// hledger reads the journal and gets moolah's balances (when hledger is installed).
		hledger, err := exec.LookPath("hledger")
		if err != nil {
			t.Log("hledger is not installed; the journal was not checked with it")
			return
		}
		out, err := exec.Command(hledger, "-f", filepath.Join(dir, "moolah.journal"), "balance", "--flat", "-N", "assets", "liabilities").CombinedOutput()
		if err != nil {
			t.Fatalf("hledger: %v\n%s", err, out)
		}
		for _, want := range []string{"23255.00 ILS  assets:bank:leumi", "390000.00 ILS  assets:pension:harel", "377.83 USD  liabilities:card:chase"} {
			if !strings.Contains(strings.Join(strings.Fields(string(out)), " "), strings.Join(strings.Fields(want), " ")) {
				t.Errorf("hledger balance lacks %q:\n%s", want, out)
			}
		}
	})

	// Changing the taxonomy only happens through commands, and history follows renames and merges.
	t.Run("taxonomy", func(t *testing.T) {
		s := newSession(t)
		s.run("init --base ILS --no-backup-key")
		s.run("account add leumi --kind bank --currency ILS --opening 1000 --date 2026-10-01")
		// Names are unique (ignoring case), also against kinds, so a name alone is enough.
		s.run("account add Leumi --kind bank --currency ILS")
		s.run("account add cash --kind cash --currency ILS")
		s.run("account add savings --currency ILS")
		s.run("account add savings --kind bnak --currency ILS")
		s.run("account add savings --kind bank")
		// In a terminal, moolah asks for what is missing; the currency defaults to the base currency.
		s.answers = "bank\n\n"
		s.run("account add savings")
		s.run("account kind add crypto --asset")
		s.run("account add wallet --kind crypto --currency USD")
		s.run("account kind remove crypto")
		s.run("account kind rename crypto digital")
		s.run("account kind list")
		s.run("category add restuarants --in food")
		s.run("category add nosuch:child")
		s.run("category add Groceries")
		s.run("add leumi -50 restuarants --date 2026-10-02")
		s.run("category rename restuarants eating-out")
		s.run("register --category eating-out")
		s.run("category merge eating-out restaurants")
		s.run("register --category restaurants")
		s.run("account rename leumi checking")
		s.run("account close checking --date 2026-10-31")
		s.run("add salary 100 food")
		s.run("category add bonus --in salary --income")
		s.run("account list")
		s.golden("taxonomy")
	})

	// Books are separate; the current book is remembered, and --book is for one command.
	t.Run("books", func(t *testing.T) {
		s := newSession(t)
		s.run("init --base ILS --no-backup-key")
		s.run("book add mom --base USD --no-backup-key")
		s.run("book list")
		s.run("book use mom")
		s.run("config get base_currency")
		s.run("--book main config get base_currency")
		s.run("book use main")
		s.run("book list")
		s.run("book use nobody")
		s.run("config set offline true")
		s.run("config set backup_dir relative/path")
		s.run("config list")
		s.golden("books")
	})
}

func TestImportScenario(t *testing.T) {
	// The import flow: detect the format, skip duplicates, review, rules, reconcile and undo.
	t.Run("bank csv", func(t *testing.T) {
		s := newSession(t)
		dir := filepath.Dir(s.env.DataDir)
		write := func(name, text string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("oct.csv", "Date,Description,Amount\n2026-10-01,SHUFERSAL DEAL 1234,-200.00\n2026-10-02,Coffee,-12\n2026-10-02,Coffee,-12\n2026-10-05,Salary,18500\n")
		write("oct-nov.csv", "Date,Description,Amount\n2026-10-05,Salary,18500\n2026-11-01,SHUFERSAL DEAL 9876,-150.00\n")
		s.run("init --base ILS --no-backup-key")
		s.run("account add leumi --kind bank --currency ILS --opening 1000 --date 2026-09-30")
		s.run("profile list")
		s.run("import " + filepath.Join(dir, "oct.csv"))
		s.run("import test " + filepath.Join(dir, "oct.csv") + " --account leumi")
		s.run("import " + filepath.Join(dir, "oct.csv") + " --account leumi")
		s.run("rule add shufersal food:groceries")
		s.run("rule add ^salary$ salary")
		s.run("import " + filepath.Join(dir, "oct-nov.csv"))
		s.run("review")
		s.run("review set 4 restaurants")
		s.run("review set 5 restaurnts")
		s.run("rule list")
		s.run("import list")
		s.run("reconcile leumi --balance 19326 --date 2026-11-01")
		s.run("reconcile leumi --balance 19314 --date 2026-11-01")
		s.run("import undo 2")
		s.run("register leumi")
		s.golden("import")
	})
	// A card payment between two currencies: each export has one amount, so the first
	// side waits and is completed in review (here without a rate, so nothing is guessed).
	t.Run("transfer between currencies", func(t *testing.T) {
		s := newSession(t)
		dir := filepath.Dir(s.env.DataDir)
		write := func(name, text string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("bank.csv", "Date,Description,Amount\n2026-11-02,CHASE PAYMENT,-1100.00\n")
		write("card.csv", "Date,Description,Amount\n2026-11-04,PAYMENT THANK YOU,1200.00\n")
		s.run("init --base EUR --no-backup-key")
		s.run("account add checking --kind bank --currency EUR --opening 5000 --date 2026-10-31")
		s.run("account add chase --kind card --currency USD")
		s.run("rule add chase liabilities:card:chase")
		s.run("rule add thank assets:bank:checking")
		s.run("import " + filepath.Join(dir, "bank.csv") + " --account checking")
		s.run("import " + filepath.Join(dir, "card.csv") + " --account chase")
		s.run("review")
		s.run("review transfer 1 1200")
		s.run("review")
		s.run("register chase")
		s.golden("import-currencies")
	})
}

func TestInitQuestions(t *testing.T) {
	// A new user is asked about sync only after the book's own questions, while
	// someone who already uses moolah joins with no other question.
	t.Run("new user, joining, and an empty folder", func(t *testing.T) {
		first, second := newSession(t), newSession(t)
		folder := filepath.Join(t.TempDir(), "Dropbox")
		empty := filepath.Join(t.TempDir(), "empty")
		writeMachineID(t, first, "laptop-0001")
		writeMachineID(t, second, "desktop-0002")
		var log bytes.Buffer
		step := func(s *session, who, line, answers string) {
			s.log.Reset()
			s.answers = strings.NewReplacer("$FOLDER", folder, "$EMPTY", empty).Replace(answers)
			s.run(line)
			log.WriteString("[" + who + "] " + strings.NewReplacer(folder, "$FOLDER", empty, "$EMPTY").Replace(s.log.String()))
		}
		step(first, "laptop", "init", "n\nEUR\n\n$FOLDER\nnew\n")
		key, err := os.ReadFile(first.env.KeyFile())
		if err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
		if err := os.MkdirAll(second.env.ConfigDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(second.env.KeyFile(), key, 0o600); err != nil {
			t.Fatal(err)
		}
		step(second, "desktop", "init", "y\n$EMPTY\n")
		step(second, "desktop", "init", "y\n$FOLDER\n")
		step(second, "desktop", "config get base_currency", "")
		s := &session{log: log, t: t}
		s.golden("init")
	})
}

func TestSyncScenario(t *testing.T) {
	// Two computers share one book through a folder: setup, changes both ways, a
	// conflict that stops commands, and its resolution.
	t.Run("two computers", func(t *testing.T) {
		laptop, desktop := newSession(t), newSession(t)
		folder := filepath.Join(t.TempDir(), "Dropbox")
		writeMachineID(t, laptop, "laptop-0001")
		writeMachineID(t, desktop, "desktop-0002")
		var log bytes.Buffer
		step := func(s *session, who, line string) {
			s.log.Reset()
			s.run(strings.ReplaceAll(line, "$FOLDER", folder))
			log.WriteString("[" + who + "] " + strings.ReplaceAll(s.log.String(), folder, "$FOLDER"))
		}
		step(laptop, "laptop", "init --base ILS --sync-dir $FOLDER")
		step(laptop, "laptop", "account add leumi --kind bank --currency ILS --opening 1000 --date 2026-10-01")
		key, err := os.ReadFile(laptop.env.KeyFile())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(desktop.env.ConfigDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(desktop.env.KeyFile(), key, 0o600); err != nil {
			t.Fatal(err)
		}
		step(desktop, "desktop", "init --sync-dir $FOLDER --join")
		step(desktop, "desktop", "balance")
		step(desktop, "desktop", "add leumi -100 food")
		step(laptop, "laptop", "register leumi")
		step(laptop, "laptop", "sync status")
		step(laptop, "laptop", "--no-sync add leumi -20 food --payee offline")
		step(desktop, "desktop", "add leumi -7 food --payee coffee")
		step(laptop, "laptop", "balance")
		step(laptop, "laptop", "sync resolve --keep desktop-0002")
		step(laptop, "laptop", "register leumi")
		got := log.String()
		laptop.log.Reset()
		laptop.log.WriteString(regexpBackupTime.ReplaceAllString(got, "<time>"))
		laptop.golden("sync")
	})
}

var regexpBackupTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}`)

func writeMachineID(t *testing.T, s *session, id string) {
	t.Helper()
	if err := os.MkdirAll(s.env.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.env.StateDir, "machine-id"), []byte(id), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEditFormat(t *testing.T) {
	tree := core.NewTree([]core.Account{
		{ID: 1, Name: "assets", Type: core.Assets},
		{ID: 2, Name: "leumi", ParentID: 1, Type: core.Assets, Currency: "ILS"},
		{ID: 3, Name: "liabilities", Type: core.Liabilities},
		{ID: 4, Name: "Visa Gold", ParentID: 3, Type: core.Liabilities, Currency: "USD"},
		{ID: 5, Name: "expenses", Type: core.Expenses},
		{ID: 6, Name: "food", ParentID: 5, Type: core.Expenses},
	})
	dec := func(s string) core.Amount {
		a, err := parseAmountCurrency(strings.Fields(s)[0], strings.Fields(s)[1])
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	orig := core.Transaction{ID: 7, Date: core.MustDate("2026-11-01"), Kind: core.KindNormal, Payee: "Card", Tags: []string{"a"}, Version: 3,
		Postings: []core.Posting{
			{AccountID: 2, Amount: dec("-1530 ILS")},
			{AccountID: 4, Amount: dec("420 USD"), Cost: func() *core.Amount { c := dec("1530 ILS"); return &c }()},
		}}

	// What the editor shows must read back as the same transaction, including the
	// cost and an account name with a space.
	t.Run("round trip", func(t *testing.T) {
		got, err := parseTx(tree, formatTx(tree, orig), orig)
		if err != nil {
			t.Fatal(err)
		}
		if err := got.Validate(tree); err != nil {
			t.Fatal(err)
		}
		if got.Version != 3 || got.Payee != "Card" || len(got.Postings) != 2 || got.Postings[1].Cost == nil {
			t.Fatalf("got %+v", got)
		}
	})
	// Emptying the file cancels the edit instead of deleting the transaction.
	t.Run("empty file cancels", func(t *testing.T) {
		if _, err := parseTx(tree, "# only comments\n\n", orig); err != errCancelled {
			t.Fatalf("got %v", err)
		}
	})
	// A typo in an account name is reported with the line number, not saved.
	t.Run("unknown account reported", func(t *testing.T) {
		text := strings.Replace(formatTx(tree, orig), "leumi ", "leumy ", 1)
		if _, err := parseTx(tree, text, orig); err == nil || !strings.Contains(err.Error(), "line") {
			t.Fatalf("got %v", err)
		}
	})
}
