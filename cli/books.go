package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/core"
)

type initFlags struct {
	backupRecipient string
	base            string
	locale          string
	noBackupKey     bool
}

func (f *initFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.base, "base", "", "base currency for reports, for example ILS")
	cmd.Flags().StringVar(&f.locale, "locale", "", "language and number format: en_US or he_IL")
	cmd.Flags().StringVar(&f.backupRecipient, "backup-recipient", "", "public key that encrypts backups (age1… or ssh-ed25519 …)")
	cmd.Flags().BoolVar(&f.noBackupKey, "no-backup-key", false, "do not set a backup key (backups will ask for a passphrase)")
}

// createBook asks the init questions (when interactive and not given as flags) and creates the book.
func (g *globals) createBook(f initFlags, loc app.Location) (*app.App, error) {
	if loc.Book != "" && g.env.Paths.BookExists(loc.Book) {
		return nil, fmt.Errorf("book %q already exists", loc.Book)
	}
	ask := g.env.Interactive
	if f.base == "" {
		if !ask {
			return nil, errors.New("--base is required, for example --base ILS")
		}
		var err error
		if f.base, err = g.in.ask("Base currency", "ILS"); err != nil {
			return nil, err
		}
	}
	base, err := core.ParseCurrency(f.base)
	if err != nil {
		return nil, err
	}
	if f.locale == "" {
		f.locale = "en_US"
		if ask {
			if f.locale, err = g.in.ask("Language (en_US, he_IL)", "en_US"); err != nil {
				return nil, err
			}
		}
	}
	if f.backupRecipient == "" && !f.noBackupKey && ask {
		def := app.DefaultSSHPublicKey()
		hint := "none"
		if def != "" {
			hint = "~/.ssh/id_ed25519.pub"
		}
		ans, err := g.in.ask("Encryption key for backups (public key, or 'none' for a passphrase)", hint)
		if err != nil {
			return nil, err
		}
		switch ans {
		case "none", "":
		case "~/.ssh/id_ed25519.pub":
			f.backupRecipient = def
		default:
			f.backupRecipient = ans
		}
	}
	a, err := app.Create(app.InitParams{
		BackupRecipient: f.backupRecipient, BaseCurrency: base, Locale: f.locale, Path: loc.Path,
	}, loc.Book, g.env.Paths)
	if err != nil {
		return nil, err
	}
	a.SetClock(g.env.Now)
	return a, nil
}

func initCmd(g *globals) *cobra.Command {
	var f initFlags
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create your book (asks for base currency, language and backup key)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			loc, err := g.location()
			if err != nil {
				return err
			}
			a, err := g.createBook(f, loc)
			if err != nil {
				return err
			}
			defer a.Close()
			base, _ := a.BaseCurrency(cmd.Context())
			fmt.Fprintf(g.out(), "Created book %q in %s (base currency %s).\n", nameOr(loc.Book, loc.Path), loc.Path, base)
			fmt.Fprintln(g.out(), "Next: add your accounts, for example:")
			fmt.Fprintln(g.out(), "  moolah account add assets:bank:mybank --currency "+string(base)+" --opening 1000")
			fmt.Fprintln(g.out(), "  moolah category list")
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

func nameOr(name, path string) string {
	if name != "" {
		return name
	}
	return path
}

func bookCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "book", Short: "Manage separate sets of books (for example, for a family member)"}

	list := &cobra.Command{
		Use: "list", Short: "List books (* = current)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			books, err := g.env.Paths.Books()
			if err != nil {
				return err
			}
			cur, err := g.env.Paths.CurrentBook()
			if err != nil {
				return err
			}
			if len(books) == 0 {
				fmt.Fprintln(g.out(), "No books yet. Create one with: moolah init")
				return nil
			}
			for _, b := range books {
				mark := " "
				if b == cur {
					mark = "*"
				}
				fmt.Fprintf(g.out(), "%s %s\n", mark, b)
			}
			return nil
		},
	}

	var f initFlags
	add := &cobra.Command{
		Use: "add <name>", Short: "Create a new book (asks the same questions as init)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.ValidateBookName(args[0]); err != nil {
				return err
			}
			loc := app.Location{Book: args[0], Path: g.env.Paths.BookPath(args[0])}
			a, err := g.createBook(f, loc)
			if err != nil {
				return err
			}
			defer a.Close()
			fmt.Fprintf(g.out(), "Created book %q. Switch to it with: moolah book use %s\n", args[0], args[0])
			return nil
		},
	}
	f.register(add)

	use := &cobra.Command{
		Use: "use <name>", Short: "Make a book the current one", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !g.env.Paths.BookExists(args[0]) {
				return fmt.Errorf("book %q does not exist. Create it with: moolah book add %s", args[0], args[0])
			}
			if err := g.env.Paths.SetCurrentBook(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(g.out(), "Now using book %q.\n", args[0])
			return nil
		},
	}

	rename := &cobra.Command{
		Use: "rename <old> <new>", Short: "Rename a book", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.env.Paths.RenameBook(args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(g.out(), "Renamed book %q to %q.\n", args[0], args[1])
			return nil
		},
	}

	var yes bool
	remove := &cobra.Command{
		Use: "remove <name>", Short: "Delete a book and all its data (cannot be undone)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				ok, err := g.in.confirm(fmt.Sprintf("Delete book %q and all its data? Make a backup first", args[0]))
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("not deleted (use --yes to confirm without a prompt)")
				}
			}
			if err := g.env.Paths.RemoveBook(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(g.out(), "Deleted book %q.\n", args[0])
			return nil
		},
	}
	remove.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")

	cmd.AddCommand(list, add, use, rename, remove)
	return cmd
}
