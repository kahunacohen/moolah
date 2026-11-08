// Package app holds moolah's use cases: one method per user action. The CLI and
// the web UI call only this package. Each action runs in one database transaction.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"moolah/core"
	"moolah/crypt"
	"moolah/prices"
	"moolah/store"
)

// Version is set at build time.
var Version = "dev"

// App is one open book.
type App struct {
	Book       string                 // book name, or "" when opened with an explicit path
	Fetchers   []prices.Fetcher       // replaces the TOML rate sources (tests)
	Joined     bool                   // created by joining a sync folder
	Offline    bool                   // never use the network (also the offline setting)
	Passphrase func() (string, error) // asked when a private key has a passphrase
	Paths      Paths
	now        func() time.Time
	st         *store.Store
}

// Open opens an existing book file.
func Open(path, book string, paths Paths) (*App, error) {
	if _, err := os.Stat(path); err != nil {
		if book != "" {
			return nil, fmt.Errorf("book %q does not exist yet. Create it with: moolah init", book)
		}
		return nil, fmt.Errorf("%s does not exist. Create a book with: moolah init", path)
	}
	a := &App{Book: book, Paths: paths, now: time.Now}
	st, err := store.Open(path, store.OpenOptions{BeforeMigrate: a.preMigrateBackup})
	if err != nil {
		return nil, err
	}
	a.st = st
	if err := a.moveComputerSettings(context.Background()); err != nil {
		_ = st.Close()
		return nil, err
	}
	return a, nil
}

// InitParams describes a new book.
type InitParams struct {
	BackupRecipient string
	BaseCurrency    core.Currency
	Locale          string
	Path            string
}

// Create makes a new book file with the starter accounts and settings.
func Create(p InitParams, book string, paths Paths) (*App, error) {
	if _, err := os.Stat(p.Path); err == nil {
		return nil, fmt.Errorf("%s already exists", p.Path)
	}
	if p.Locale == "" {
		p.Locale = "en_US"
	}
	locale, err := NormalizeLocale(p.Locale)
	if err != nil {
		return nil, err
	}
	p.Locale = locale
	if p.BackupRecipient != "" {
		if err := validateSetting(KeyBackupRecipient, p.BackupRecipient); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o700); err != nil {
		return nil, err
	}
	st, err := store.Open(p.Path, store.OpenOptions{Create: true})
	if err != nil {
		return nil, err
	}
	a := &App{Book: book, Paths: paths, now: time.Now, st: st}
	ctx := context.Background()
	err = st.InTx(ctx, func(tx *store.Tx) error {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return err
		}
		if err := tx.SetMeta(ctx, "db_id", hex.EncodeToString(id)); err != nil {
			return err
		}
		settings := map[string]string{KeyBaseCurrency: string(p.BaseCurrency), KeyLocale: p.Locale}
		if p.BackupRecipient != "" {
			settings[KeyBackupRecipient] = p.BackupRecipient
		}
		for k, v := range settings {
			if err := tx.SetSetting(ctx, k, v); err != nil {
				return err
			}
		}
		return seedAccounts(ctx, tx)
	})
	if err != nil {
		_ = st.Close()
		_ = os.Remove(p.Path)
		return nil, err
	}
	return a, nil
}

// Close closes the book.
func (a *App) Close() error { return a.st.Close() }

// Path returns the book's file path.
func (a *App) Path() string { return a.st.Path() }

func (a *App) today() core.Date { return core.DateOf(a.now()) }

// SetClock replaces the clock. For tests.
func (a *App) SetClock(now func() time.Time) { a.now = now }

// Tree returns the current account tree.
func (a *App) Tree(ctx context.Context) (*core.Tree, error) { return a.st.Tree(ctx) }

// BaseCurrency returns the book's base currency.
func (a *App) BaseCurrency(ctx context.Context) (core.Currency, error) {
	v, ok, err := a.st.Setting(ctx, KeyBaseCurrency)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("base_currency is not set. Set it with: moolah config set base_currency USD (or your currency)")
	}
	return core.Currency(v), nil
}

func (a *App) preMigrateBackup(s *store.Store, from, to int) error {
	dir, err := a.backupDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ctx := context.Background()
	name := filepath.Join(dir, fmt.Sprintf("pre-migrate-v%d-to-v%d-%s.db", from, to, a.now().UTC().Format("20060102T150405")))
	key, _, err := s.Setting(ctx, KeyBackupRecipient)
	if err != nil {
		return err
	}
	if key == "" {
		// Opening a book cannot stop to ask for a passphrase, so without a key the copy stays plain.
		return s.VacuumInto(ctx, name)
	}
	r, err := crypt.ParseRecipient(key)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(s.Path()), ".copy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	plain := filepath.Join(tmp, "copy.db")
	if err := s.VacuumInto(ctx, plain); err != nil {
		return err
	}
	return crypt.EncryptFile(plain, name+".age", r)
}
