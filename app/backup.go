package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"moolah/crypt"
	"moolah/store"
)

// BackupParams controls a backup.
type BackupParams struct {
	Dir        string                 // backup folder; the backup_dir setting or the default when empty
	Passphrase func() (string, error) // asked when backup_recipient is not set
}

func (a *App) recipient(ctx context.Context) (age.Recipient, error) {
	s, _, err := a.st.Setting(ctx, KeyBackupRecipient)
	if err != nil || s == "" {
		return nil, err
	}
	return crypt.ParseRecipient(s)
}

// Backup writes an encrypted, consistent copy of the book and returns its path.
// It encrypts to backup_recipient, or with a passphrase when that is not set.
func (a *App) Backup(ctx context.Context, p BackupParams) (string, error) {
	recipient, err := a.recipient(ctx)
	if err != nil {
		return "", err
	}
	if recipient == nil {
		if p.Passphrase == nil {
			return "", fmt.Errorf("backup_recipient is not set and no passphrase was given")
		}
		pass, err := p.Passphrase()
		if err != nil {
			return "", err
		}
		if len(pass) < 8 {
			return "", fmt.Errorf("the passphrase must have at least 8 characters")
		}
		if recipient, err = age.NewScryptRecipient(pass); err != nil {
			return "", err
		}
	}
	dir := p.Dir
	if dir == "" {
		if dir, err = a.backupDir(); err != nil {
			return "", err
		}
	}
	return a.encryptedCopy(ctx, dir, "", recipient)
}

// encryptedCopy writes moolah-<book>[-label]-<time>.db.age into dir.
func (a *App) encryptedCopy(ctx context.Context, dir, label string, r age.Recipient) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := a.Book
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(a.Path()), ".db")
	}
	if label != "" {
		name += "-" + label
	}
	out := filepath.Join(dir, fmt.Sprintf("moolah-%s-%s.db.age", name, a.now().Format("2006-01-02T15-04-05")))
	plain, cleanup, err := a.plainCopy(ctx)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if err := crypt.EncryptFile(plain, out, r); err != nil {
		return "", err
	}
	return out, nil
}

// plainCopy writes a consistent unencrypted copy next to the book (same file system,
// same permissions) and returns a function that removes it.
func (a *App) plainCopy(ctx context.Context) (string, func(), error) {
	dir, err := os.MkdirTemp(filepath.Dir(a.Path()), ".copy-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	plain := filepath.Join(dir, "copy.db")
	if err := a.st.VacuumInto(ctx, plain); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("copy database: %w", err)
	}
	return plain, cleanup, nil
}

// RestoreParams controls a restore.
type RestoreParams struct {
	File         string                 // the .age backup
	IdentityFile string                 // age or SSH private key
	Passphrase   func() (string, error) // asked for passphrase-encrypted backups and SSH keys
	Target       string                 // book file to write
}

// Restore decrypts a backup into Target, replacing it. The caller confirms first.
func Restore(p RestoreParams) error {
	scrypt, err := crypt.UsesPassphrase(p.File)
	if err != nil {
		return err
	}
	var ids []age.Identity
	if scrypt {
		if p.Passphrase == nil {
			return fmt.Errorf("this backup needs its passphrase")
		}
		pass, err := p.Passphrase()
		if err != nil {
			return err
		}
		if ids, err = crypt.PassphraseIdentity(pass); err != nil {
			return err
		}
	} else if ids, err = crypt.LoadIdentities(p.IdentityFile, p.Passphrase); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.Target), 0o700); err != nil {
		return err
	}
	tmp := p.Target + ".restore"
	_ = os.Remove(tmp)
	if err := crypt.DecryptFile(p.File, tmp, ids); err != nil {
		return err
	}
	return replaceDB(tmp, p.Target)
}

// replaceDB checks that src is a healthy moolah database and moves it over dst.
func replaceDB(src, dst string) error {
	st, err := store.Open(src, store.OpenOptions{})
	if err == nil {
		var problems []string
		problems, err = st.IntegrityProblems(context.Background())
		if err == nil && len(problems) > 0 {
			err = fmt.Errorf("the copy is damaged: %s", strings.Join(problems, "; "))
		}
		if cerr := st.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		_ = os.Remove(src)
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(src + suffix)
		if err := os.Remove(dst + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(src, dst)
}
