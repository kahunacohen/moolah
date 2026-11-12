// Package store keeps moolah's data in one SQLite file. It is the only package
// that contains SQL. It converts between rows and core types and enforces what
// the database can enforce (foreign keys, CHECK constraints, versions).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" driver
)

// ErrNotBook is returned with RequireBook for a database that moolah did not create.
var ErrNotBook = errors.New("this is not a moolah book")

// ErrNewerSchema is returned when the book was written by a newer moolah.
var ErrNewerSchema = errors.New("this book was created by a newer version of moolah; upgrade moolah to open it")

// ErrVersionConflict is returned when a transaction changed after it was loaded.
var ErrVersionConflict = errors.New("changed elsewhere, reload and try again")

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// querier is what *sql.DB and *sql.Tx have in common.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Q has all read and write methods. Store and Tx both embed it.
type Q struct {
	q querier
}

// Store is an open moolah database file.
type Store struct {
	Q
	db   *sql.DB
	path string
}

// Tx is a database transaction. All methods of Q work inside it.
type Tx struct {
	Q
}

// OpenOptions controls Open.
type OpenOptions struct {
	BeforeMigrate func(s *Store, from, to int) error // called before upgrading an existing file
	Create        bool                               // create the file if it does not exist
	RequireBook   bool                               // the file must already be a moolah book (no new schema is written)
}

// Open opens (and if needed creates and migrates) the database at path.
func Open(path string, opts OpenOptions) (*Store, error) {
	_, statErr := os.Stat(path)
	exists := statErr == nil
	if !exists && !opts.Create {
		return nil, fmt.Errorf("%s does not exist", path)
	}
	if !exists {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{Q: Q{q: db}, db: db, path: path}
	if opts.RequireBook {
		v, err := s.schemaVersion(context.Background())
		if err == nil && v == 0 {
			err = ErrNotBook
		}
		if err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if err := s.migrate(opts.BeforeMigrate); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err == nil {
			if err := os.Chmod(p, 0o600); err != nil {
				_ = db.Close()
				return nil, err
			}
		}
	}
	return s, nil
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// InTx runs fn in one database transaction. It commits when fn returns nil.
func (s *Store) InTx(ctx context.Context, fn func(tx *Tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&Tx{Q: Q{q: sqlTx}}); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	return sqlTx.Commit()
}

// VacuumInto writes a consistent copy of the database to path, which must not exist.
func (s *Store) VacuumInto(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

// CopyBook copies the SQLite file src to dst with VACUUM INTO, including changes
// still in src's write-ahead log. src is opened read-only and is not migrated.
func CopyBook(src, dst string) error {
	db, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: src}).EscapedPath()+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	return nil
}

// IntegrityProblems runs SQLite's integrity and foreign key checks.
func (s *Store) IntegrityProblems(ctx context.Context) ([]string, error) {
	var out []string
	rows, err := s.db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if msg != "ok" {
			out = append(out, "sqlite: "+msg)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	fk, err := s.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, err
	}
	defer fk.Close()
	for fk.Next() {
		var table, parent string
		var rowid, fkid sql.NullInt64
		if err := fk.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("sqlite: row %d in %s refers to a missing %s row", rowid.Int64, table, parent))
	}
	return out, fk.Err()
}
