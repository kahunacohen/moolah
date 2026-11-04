package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultBook is the name of the first book.
const DefaultBook = "main"

// Paths holds the directories moolah uses.
type Paths struct {
	ConfigDir string // ~/.config/moolah: current-book
	DataDir   string // ~/.local/share/moolah: books/, backups/
}

// DefaultPaths returns the XDG directories for moolah.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return Paths{ConfigDir: filepath.Join(cfg, "moolah"), DataDir: filepath.Join(data, "moolah")}, nil
}

var bookNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ValidateBookName checks a book name: lower-case letters, digits, - and _.
func ValidateBookName(name string) error {
	if !bookNameRe.MatchString(name) {
		return fmt.Errorf("invalid book name %q: use lower-case letters, digits, - and _ (max 40)", name)
	}
	return nil
}

// BooksDir is where book files live.
func (p Paths) BooksDir() string { return filepath.Join(p.DataDir, "books") }

// BookPath returns the file of a named book.
func (p Paths) BookPath(name string) string { return filepath.Join(p.BooksDir(), name+".db") }

// DefaultBackupDir is the default backup folder.
func (p Paths) DefaultBackupDir() string { return filepath.Join(p.DataDir, "backups") }

func (p Paths) currentBookFile() string { return filepath.Join(p.ConfigDir, "current-book") }

// CurrentBook returns the book set with "moolah book use", or "main".
func (p Paths) CurrentBook() (string, error) {
	b, err := os.ReadFile(p.currentBookFile())
	if errors.Is(err, os.ErrNotExist) {
		return DefaultBook, nil
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(b))
	if err := ValidateBookName(name); err != nil {
		return "", fmt.Errorf("%s: %w", p.currentBookFile(), err)
	}
	return name, nil
}

// SetCurrentBook makes a book the current one.
func (p Paths) SetCurrentBook(name string) error {
	if err := ValidateBookName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(p.currentBookFile(), []byte(name+"\n"), 0o600)
}

// BookExists reports whether a named book has a file.
func (p Paths) BookExists(name string) bool {
	_, err := os.Stat(p.BookPath(name))
	return err == nil
}

// Books lists existing books, sorted.
func (p Paths) Books() ([]string, error) {
	entries, err := os.ReadDir(p.BooksDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".db"); ok && !e.IsDir() && ValidateBookName(name) == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// RenameBook renames a book file (and its SQLite side files).
func (p Paths) RenameBook(from, to string) error {
	if err := ValidateBookName(to); err != nil {
		return err
	}
	if !p.BookExists(from) {
		return fmt.Errorf("book %q does not exist", from)
	}
	if p.BookExists(to) {
		return fmt.Errorf("book %q already exists", to)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := p.BookPath(from) + suffix
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.Rename(src, p.BookPath(to)+suffix); err != nil {
			return err
		}
	}
	cur, err := p.CurrentBook()
	if err == nil && cur == from {
		return p.SetCurrentBook(to)
	}
	return nil
}

// RemoveBook deletes a book file. It cannot be undone.
func (p Paths) RemoveBook(name string) error {
	if !p.BookExists(name) {
		return fmt.Errorf("book %q does not exist", name)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(p.BookPath(name) + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Location is a resolved book: a name (may be empty) and a file path.
type Location struct {
	Book string
	Path string
}

// ResolveParams are the inputs for choosing a book.
type ResolveParams struct {
	BookFlag string
	DBFlag   string
	EnvBook  string // MOOLAH_BOOK
	EnvDB    string // MOOLAH_DB
}

// Resolve picks the book: --db, then MOOLAH_DB, then --book, then MOOLAH_BOOK, then the current book.
func (p Paths) Resolve(r ResolveParams) (Location, error) {
	if r.DBFlag != "" {
		return Location{Path: r.DBFlag}, nil
	}
	if r.EnvDB != "" {
		return Location{Path: r.EnvDB}, nil
	}
	name := r.BookFlag
	if name == "" {
		name = r.EnvBook
	}
	if name == "" {
		var err error
		if name, err = p.CurrentBook(); err != nil {
			return Location{}, err
		}
	}
	if err := ValidateBookName(name); err != nil {
		return Location{}, err
	}
	return Location{Book: name, Path: p.BookPath(name)}, nil
}
