package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"moolah/app"
)

func syncCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "sync", Short: "Use one book on several computers through a synced folder"}
	var identity string

	initC := &cobra.Command{
		Use: "init <folder>", Short: "Start syncing this book through a folder (Dropbox, Syncthing, …)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBookSync(false, func(a *app.App) error {
				if err := g.ensureRecipient(a, cmd); err != nil {
					return err
				}
				if err := a.SyncInit(cmd.Context(), args[0], identity); err != nil {
					return err
				}
				fmt.Fprintf(g.out(), "This book now syncs through %s.\n", args[0])
				fmt.Fprintln(g.out(), "On another computer, install moolah, copy your private key, and run: moolah init")
				return nil
			})
		},
	}
	initC.Flags().StringVar(&identity, "identity", "", "private key that decrypts snapshots (default: moolah's age key, else ~/.ssh/id_ed25519)")

	join := &cobra.Command{
		Use: "join <folder>", Short: "Create this computer's copy of a book that already syncs through a folder", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			loc, err := g.location()
			if err != nil {
				return err
			}
			a, err := app.Join(app.JoinParams{Book: loc.Book, Dir: args[0], Identity: identity, Path: loc.Path,
				Passphrase: func() (string, error) { return g.in.passphrase(false) }}, g.env.Paths)
			if err != nil {
				return err
			}
			defer a.Close()
			fmt.Fprintf(g.out(), "Joined. Book %q is up to date.\n", nameOr(loc.Book, loc.Path))
			return nil
		},
	}
	join.Flags().StringVar(&identity, "identity", "", "private key that decrypts snapshots")

	status := &cobra.Command{
		Use: "status", Short: "Show the sync state", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBookSync(false, func(a *app.App) error {
				s, err := a.SyncStatus(cmd.Context())
				if err != nil {
					return err
				}
				if !s.Configured {
					fmt.Fprintln(g.out(), "Sync is off for this book. Start it with: moolah sync init <folder>")
					return nil
				}
				fmt.Fprintf(g.out(), "Folder:   %s\nComputer: %s\n", s.Dir, s.Machine)
				switch {
				case s.Heads > 1:
					fmt.Fprintln(g.out(), "State:    conflict between other computers. Run any command to see the details.")
				case s.Head != nil && s.Head.Gen > s.LocalGen && s.Unsent > 0:
					fmt.Fprintln(g.out(), "State:    conflict: this computer and "+s.Head.Machine+" both changed data. Run any command to see the details.")
				case s.Head != nil && s.Head.Gen > s.LocalGen:
					fmt.Fprintf(g.out(), "State:    newer data from %s is available; it loads with your next command\n", s.Head.Machine)
				case s.Unsent > 0:
					fmt.Fprintf(g.out(), "State:    %d changes not uploaded yet\n", s.Unsent)
				case s.Head != nil:
					fmt.Fprintf(g.out(), "State:    up to date (snapshot %d from %s, %s)\n", s.Head.Gen, s.Head.Machine, ago(g.env.Now(), s.Head.ModTime))
				}
				for _, l := range s.OtherLocks {
					fmt.Fprintf(g.out(), "In use:   %s since %s\n", l.Machine, l.Since.Local().Format("15:04"))
				}
				return nil
			})
		},
	}

	now := &cobra.Command{
		Use: "now", Short: "Load newer data and upload local changes now", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBookSync(false, func(a *app.App) error {
				msg, err := a.SyncNow(cmd.Context())
				if err != nil {
					return err
				}
				fmt.Fprintln(g.out(), msg)
				return nil
			})
		},
	}

	var keep string
	resolve := &cobra.Command{
		Use: "resolve --keep <computer>", Short: "End a conflict by keeping one computer's copy (the other is saved as a backup)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if keep == "" {
				return errors.New("choose a copy with --keep <computer> (or --keep local)")
			}
			return g.withBookSync(false, func(a *app.App) error {
				msg, err := a.SyncResolve(cmd.Context(), keep)
				if err != nil {
					return err
				}
				fmt.Fprintln(g.out(), msg)
				return nil
			})
		},
	}
	resolve.Flags().StringVar(&keep, "keep", "", "the computer whose copy to keep, or local")

	off := &cobra.Command{
		Use: "off", Short: "Stop syncing on this computer (local data stays)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withBookSync(false, func(a *app.App) error {
				if err := a.SyncOff(cmd.Context()); err != nil {
					return err
				}
				fmt.Fprintln(g.out(), "Sync is off on this computer.")
				return nil
			})
		},
	}

	keygen := &cobra.Command{
		Use: "keygen", Short: "Create a key that encrypts backups and sync snapshots", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pub, created, err := g.env.Paths.Keygen()
			if err != nil {
				return err
			}
			if created {
				fmt.Fprintf(g.out(), "Created %s\n", g.env.Paths.KeyFile())
			} else {
				fmt.Fprintf(g.out(), "Using the existing key %s\n", g.env.Paths.KeyFile())
			}
			fmt.Fprintf(g.out(), "Public key: %s\n", pub)
			fmt.Fprintln(g.out(), "Copy the key file to your other computers (same path) in a safe way, for example with scp.")
			fmt.Fprintln(g.out(), "Without this file, your backups and snapshots cannot be opened.")
			loc, err := g.location()
			if err == nil && fileExists(loc.Path) {
				return g.withBookSync(false, func(a *app.App) error {
					cur, err := a.Config(cmd.Context(), app.KeyBackupRecipient)
					if err != nil || cur != "" {
						return err
					}
					if err := a.SetConfig(cmd.Context(), app.KeyBackupRecipient, pub); err != nil {
						return err
					}
					fmt.Fprintln(g.out(), "Set backup_recipient for this book.")
					return nil
				})
			}
			return nil
		},
	}

	cmd.AddCommand(initC, join, status, now, resolve, off, keygen)
	return cmd
}

// ensureRecipient makes sure the book has a key for snapshots, creating moolah's age key if needed.
func (g *globals) ensureRecipient(a *app.App, cmd *cobra.Command) error {
	cur, err := a.Config(cmd.Context(), app.KeyBackupRecipient)
	if err != nil || cur != "" {
		return err
	}
	pub, created, err := g.env.Paths.Keygen()
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(g.out(), "Created the encryption key %s. Copy it to your other computers.\n", g.env.Paths.KeyFile())
	}
	return a.SetConfig(cmd.Context(), app.KeyBackupRecipient, pub)
}

func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return t.Local().Format("2006-01-02 15:04")
}
