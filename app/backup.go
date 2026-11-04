package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"

	"moolah/store"
)

// BackupParams controls a backup.
type BackupParams struct {
	Dir        string                 // backup folder; the backup_dir setting or the default when empty
	Passphrase func() (string, error) // asked when backup_recipient is not set
}

// Backup writes an encrypted, consistent copy of the book and returns its path.
// It encrypts to backup_recipient, or with a passphrase when that is not set.
func (a *App) Backup(ctx context.Context, p BackupParams) (string, error) {
	recipientStr, _, err := a.st.Setting(ctx, KeyBackupRecipient)
	if err != nil {
		return "", err
	}
	var recipient age.Recipient
	if recipientStr != "" {
		if recipient, err = parseRecipient(recipientStr); err != nil {
			return "", err
		}
	} else {
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
		if dir, err = a.backupDir(ctx, a.st); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := a.Book
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(a.Path()), ".db")
	}
	out := filepath.Join(dir, fmt.Sprintf("moolah-%s-%s.db.age", name, a.now().Format("2006-01-02T15-04-05")))

	plainDir, err := os.MkdirTemp(filepath.Dir(a.Path()), ".backup-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(plainDir)
	plain := filepath.Join(plainDir, "copy.db")
	if err := a.st.VacuumInto(ctx, plain); err != nil {
		return "", fmt.Errorf("copy database: %w", err)
	}
	if err := encryptFile(plain, out, recipient); err != nil {
		return "", err
	}
	return out, nil
}

func encryptFile(src, dst string, r age.Recipient) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w, err := age.Encrypt(f, r)
	if err == nil {
		_, err = io.Copy(w, in)
	}
	if err == nil {
		err = w.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("encrypt backup: %w", err)
	}
	return os.Rename(tmp, dst)
}

// RestoreParams controls a restore.
type RestoreParams struct {
	File         string                 // the .age backup
	IdentityFile string                 // age or SSH private key; ~/.ssh/id_ed25519 when empty
	Passphrase   func() (string, error) // asked for passphrase-encrypted backups and SSH keys
	Target       string                 // book file to write
}

// Restore decrypts a backup into Target, replacing it. The caller confirms first.
func Restore(p RestoreParams) error {
	f, err := os.Open(p.File)
	if err != nil {
		return err
	}
	defer f.Close()
	scrypt, err := usesPassphrase(f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
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
		id, err := age.NewScryptIdentity(pass)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	} else {
		if ids, err = loadIdentities(p.IdentityFile, p.Passphrase); err != nil {
			return err
		}
	}
	r, err := age.Decrypt(f, ids...)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return fmt.Errorf("cannot decrypt %s: wrong key or passphrase", p.File)
		}
		return fmt.Errorf("decrypt %s: %w", p.File, err)
	}
	if err := os.MkdirAll(filepath.Dir(p.Target), 0o700); err != nil {
		return err
	}
	tmp := p.Target + ".restore"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("decrypt %s: %w", p.File, err)
	}
	st, err := store.Open(tmp, store.OpenOptions{})
	if err == nil {
		var problems []string
		problems, err = st.IntegrityProblems(context.Background())
		if err == nil && len(problems) > 0 {
			err = fmt.Errorf("the backup is damaged: %s", strings.Join(problems, "; "))
		}
		_ = st.Close()
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(tmp + suffix)
		if err := os.Remove(p.Target + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(tmp, p.Target)
}

// usesPassphrase reports whether an age file was encrypted with a passphrase.
func usesPassphrase(r io.Reader) (bool, error) {
	br := bufio.NewReader(r)
	first, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "age-encryption.org/") {
		return false, fmt.Errorf("this is not a moolah backup (not an age file)")
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("this backup file is damaged")
		}
		if strings.HasPrefix(line, "---") {
			return false, nil
		}
		if strings.HasPrefix(line, "-> scrypt ") {
			return true, nil
		}
	}
}

func loadIdentities(path string, passphrase func() (string, error)) ([]age.Identity, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".ssh", "id_ed25519")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w. Give the key with --identity", path, err)
	}
	if strings.Contains(string(data), "AGE-SECRET-KEY-") {
		return age.ParseIdentities(strings.NewReader(string(data)))
	}
	id, err := agessh.ParseIdentity(data)
	if err == nil {
		return []age.Identity{id}, nil
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) || missing.PublicKey == nil {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	if passphrase == nil {
		return nil, fmt.Errorf("the key %s has a passphrase", path)
	}
	enc, err := agessh.NewEncryptedSSHIdentity(missing.PublicKey, data, func() ([]byte, error) {
		s, err := passphrase()
		return []byte(s), err
	})
	if err != nil {
		return nil, err
	}
	return []age.Identity{enc}, nil
}
