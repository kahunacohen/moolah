// Package cli is moolah's command-line interface. Commands parse arguments, call
// the app package, and print results. They contain no business rules.
package cli

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/core"
)

// Env is everything the CLI needs from the outside world, so tests can replace it.
type Env struct {
	Getenv       func(string) string
	Interactive  bool // stdin is a terminal
	Now          func() time.Time
	Paths        app.Paths
	ReadPassword func(prompt string) (string, error)
	RunEditor    func(path string) error
	Stderr       io.Writer
	Stdin        io.Reader
	Stdout       io.Writer
}

type globals struct {
	book   string
	db     string
	env    Env
	format string
	in     *prompter
}

// negativeNumber matches arguments such as -85 or -1,530.50, which the flag parser
// would otherwise read as short flags.
var negativeNumber = regexp.MustCompile(`^-[0-9][0-9,]*(\.[0-9]+)?$`)

// Run executes moolah with the given arguments and returns the exit code.
func Run(env Env, args []string) int {
	g := &globals{env: env, in: newPrompter(env)}
	root := newRoot(g)
	fixed := make([]string, len(args))
	for i, a := range args {
		if negativeNumber.MatchString(a) {
			a = "−" + a[1:]
		}
		fixed[i] = a
	}
	root.SetArgs(fixed)
	root.SetIn(env.Stdin)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(env.Stderr, "error: "+err.Error())
		return 1
	}
	return 0
}

func newRoot(g *globals) *cobra.Command {
	root := &cobra.Command{
		Use:           "moolah",
		Short:         "Personal finance for the command line",
		Long:          "moolah tracks bank accounts, cards and retirement accounts in any currency,\nand reports in one base currency that you choose.",
		Version:       app.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&g.book, "book", "", "use this book for one command (default: the current book)")
	root.PersistentFlags().StringVar(&g.db, "db", "", "use this database file instead of a named book")
	root.PersistentFlags().StringVar(&g.format, "format", "table", "output format: table, csv or json")
	root.AddCommand(
		initCmd(g), bookCmd(g), accountCmd(g), categoryCmd(g), tagCmd(g),
		addCmd(g), transferCmd(g), registerCmd(g), balanceCmd(g), valueCmd(g),
		priceCmd(g), configCmd(g), backupCmd(g), restoreCmd(g), checkCmd(g),
		editCmd(g), deleteCmd(g),
	)
	return root
}

func (g *globals) location() (app.Location, error) {
	return g.env.Paths.Resolve(app.ResolveParams{
		BookFlag: g.book, DBFlag: g.db, EnvBook: g.env.Getenv("MOOLAH_BOOK"), EnvDB: g.env.Getenv("MOOLAH_DB"),
	})
}

// open opens the selected book.
func (g *globals) open() (*app.App, error) {
	loc, err := g.location()
	if err != nil {
		return nil, err
	}
	a, err := app.Open(loc.Path, loc.Book, g.env.Paths)
	if err != nil {
		return nil, err
	}
	a.SetClock(g.env.Now)
	return a, nil
}

// withBook opens the book, runs fn and closes the book.
func (g *globals) withBook(fn func(a *app.App) error) error {
	a, err := g.open()
	if err != nil {
		return err
	}
	err = fn(a)
	if cerr := a.Close(); err == nil {
		err = cerr
	}
	return err
}

func (g *globals) out() io.Writer { return g.env.Stdout }

func parseDateFlag(s string) (core.Date, error) {
	if s == "" {
		return core.Date{}, nil
	}
	return core.ParseDate(s)
}

func (g *globals) checkFormat() error {
	switch g.format {
	case "table", "csv", "json":
		return nil
	}
	return errors.New("--format must be table, csv or json")
}
