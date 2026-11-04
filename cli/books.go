package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"moolah/app"
	"moolah/core"
)

type initFlags struct {
	backupRecipient string
	base            string
	identity        string
	join            bool
	locale          string
	noBackupKey     bool
	noSync          bool
	syncDir         string
}

func (f *initFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.base, "base", "", "base currency for reports, for example ILS")
	cmd.Flags().StringVar(&f.locale, "locale", "", "language and number format: en_US or he_IL")
	cmd.Flags().StringVar(&f.backupRecipient, "backup-recipient", "", "public key that encrypts backups (age1… or ssh-ed25519 …)")
	cmd.Flags().BoolVar(&f.noBackupKey, "no-backup-key", false, "do not set a backup key (backups will ask for a passphrase)")
	cmd.Flags().StringVar(&f.syncDir, "sync-dir", "", "folder that syncs this book with your other computers")
	cmd.Flags().BoolVar(&f.join, "join", false, "with --sync-dir: join the book that is already in the folder")
	cmd.Flags().StringVar(&f.identity, "identity", "", "private key file for sync (default: moolah's age key, else ~/.ssh/id_ed25519)")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// createBook asks the init questions (when interactive and not given as flags) and
// creates the book, or joins one that is already in the sync folder.
func (g *globals) createBook(f initFlags, loc app.Location) (*app.App, error) {
	if fileExists(loc.Path) {
		return nil, fmt.Errorf("book %q already exists", nameOr(loc.Book, loc.Path))
	}
	ask := g.env.Interactive
	var err error
	if f.syncDir == "" && ask && !f.noSync && !f.noBackupKey {
		if f.syncDir, err = g.in.ask("Sync folder for your other computers (Enter to skip)", ""); err != nil {
			return nil, err
		}
	}
	if f.syncDir != "" {
		head, err := app.SyncFolderInfo(f.syncDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if head != nil {
			join := f.join
			if !join && ask {
				if join, err = g.in.confirmDefault(fmt.Sprintf("This folder has moolah data (last change: %s, %s). Join it?",
					head.Machine, head.ModTime.Local().Format("2006-01-02 15:04")), true); err != nil {
					return nil, err
				}
			}
			if !join {
				return nil, fmt.Errorf("%s already has moolah data; use --join to join it, or choose an empty folder", f.syncDir)
			}
			a, err := app.Join(app.JoinParams{Book: loc.Book, Dir: f.syncDir, Identity: f.identity, Path: loc.Path,
				Passphrase: func() (string, error) { return g.in.passphrase(false) }}, g.env.Paths)
			if err != nil {
				return nil, err
			}
			a.SetClock(g.env.Now)
			fmt.Fprintln(g.out(), "Joined the book in the sync folder. Your data is up to date.")
			return a, nil
		}
	}
	if f.base == "" {
		if !ask {
			return nil, errors.New("--base is required, for example --base ILS")
		}
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
	if f.backupRecipient == "" && !f.noBackupKey {
		if f.backupRecipient, err = g.chooseKey(ask, f.syncDir != ""); err != nil {
			return nil, err
		}
	}
	a, err := app.Create(app.InitParams{
		BackupRecipient: f.backupRecipient, BaseCurrency: base, Locale: f.locale, Path: loc.Path,
	}, loc.Book, g.env.Paths)
	if err != nil {
		return nil, err
	}
	a.SetClock(g.env.Now)
	a.Passphrase = func() (string, error) { return g.in.passphrase(false) }
	if f.syncDir != "" {
		if err := a.SyncInit(context.Background(), f.syncDir, f.identity); err != nil {
			_ = a.Close()
			return nil, fmt.Errorf("the book was created, but sync did not start: %w", err)
		}
		fmt.Fprintf(g.out(), "This book syncs through %s.\n", f.syncDir)
	}
	return a, nil
}

// chooseKey picks the public key that encrypts backups and snapshots. When sync is
// wanted, a key is required, so moolah creates its own age key if there is none.
func (g *globals) chooseKey(ask, needed bool) (string, error) {
	if fileExists(g.env.Paths.KeyFile()) {
		pub, _, err := g.env.Paths.Keygen()
		return pub, err
	}
	ssh := app.DefaultSSHPublicKey()
	if ask {
		def := "new"
		if ssh != "" {
			def = "ssh"
		}
		hint := "Encryption key for backups and sync: 'new' (create one), 'ssh' (~/.ssh/id_ed25519.pub), a public key, or 'none' (a passphrase each time)"
		ans, err := g.in.ask(hint, def)
		if err != nil {
			return "", err
		}
		switch ans {
		case "none":
			if needed {
				return "", errors.New("sync needs a key; answer 'new' or 'ssh'")
			}
			return "", nil
		case "ssh":
			if ssh == "" {
				return "", errors.New("~/.ssh/id_ed25519.pub does not exist")
			}
			return ssh, nil
		case "new":
		default:
			return ans, nil
		}
	} else if !needed {
		return "", nil
	}
	pub, _, err := g.env.Paths.Keygen()
	if err != nil {
		return "", err
	}
	fmt.Fprintf(g.out(), "Created the encryption key %s. Keep a copy somewhere safe; copy it to your other computers.\n", g.env.Paths.KeyFile())
	return pub, nil
}

func initCmd(g *globals) *cobra.Command {
	var f initFlags
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create your book, or join one from your sync folder",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			loc, err := g.location()
			if err != nil {
				return err
			}
			f.noSync = g.noSync
			a, err := g.createBook(f, loc)
			if err != nil {
				return err
			}
			defer a.Close()
			base, _ := a.BaseCurrency(cmd.Context())
			fmt.Fprintf(g.out(), "Book %q is ready in %s (base currency %s).\n", nameOr(loc.Book, loc.Path), loc.Path, base)
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
			f.noSync = g.noSync
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
		Use: "remove <name>", Short: "Delete a book and all its data on this computer (cannot be undone)", Args: cobra.ExactArgs(1),
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
