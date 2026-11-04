// Package crypt wraps age encryption for backups and sync snapshots: parsing keys,
// and encrypting or decrypting whole files with atomic writes.
package crypt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

// KeyFileName is the age private key that "moolah sync keygen" creates in the config folder.
const KeyFileName = "age-key.txt"

// ParseRecipient parses an age public key (age1…) or an SSH public key (ssh-ed25519 …).
func ParseRecipient(s string) (age.Recipient, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "age1"):
		return age.ParseX25519Recipient(s)
	case strings.HasPrefix(s, "ssh-"):
		return agessh.ParseRecipient(s)
	}
	return nil, errors.New("the key must be an age public key (age1…) or an SSH public key (ssh-ed25519 …)")
}

// EncryptFile encrypts src into dst. dst appears only when it is complete.
func EncryptFile(src, dst string, r age.Recipient) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	w, err := age.Encrypt(tmp, r)
	if err == nil {
		_, err = io.Copy(w, in)
	}
	if err == nil {
		err = w.Close()
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("encrypt: %w", err)
	}
	return os.Rename(tmp.Name(), dst)
}

// DecryptFile decrypts src into dst (mode 0600), replacing dst only when complete.
func DecryptFile(src, dst string, ids []age.Identity) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := age.Decrypt(f, ids...)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return fmt.Errorf("cannot decrypt %s: wrong key or passphrase", filepath.Base(src))
		}
		return fmt.Errorf("decrypt %s: %w", filepath.Base(src), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	_, err = io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("decrypt %s: %w", filepath.Base(src), err)
	}
	return os.Rename(tmp.Name(), dst)
}

// UsesPassphrase reports whether an age file was encrypted with a passphrase.
func UsesPassphrase(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	first, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "age-encryption.org/") {
		return false, fmt.Errorf("%s is not an encrypted moolah file", filepath.Base(path))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("%s is damaged", filepath.Base(path))
		}
		if strings.HasPrefix(line, "---") {
			return false, nil
		}
		if strings.HasPrefix(line, "-> scrypt ") {
			return true, nil
		}
	}
}

// PassphraseIdentity returns an identity for passphrase-encrypted files.
func PassphraseIdentity(pass string) ([]age.Identity, error) {
	id, err := age.NewScryptIdentity(pass)
	if err != nil {
		return nil, err
	}
	return []age.Identity{id}, nil
}

// DefaultIdentityPath returns the moolah age key if it exists, otherwise ~/.ssh/id_ed25519.
func DefaultIdentityPath(configDir string) string {
	if p := filepath.Join(configDir, KeyFileName); fileExists(p) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "id_ed25519")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// LoadIdentities reads an age key file or an SSH private key. For an SSH key with a
// passphrase, passphrase is called when the key is first used.
func LoadIdentities(path string, passphrase func() (string, error)) ([]age.Identity, error) {
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

// GenerateKey writes a new age private key to path (which must not exist) and
// returns its public key.
func GenerateKey(path string) (string, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, err = fmt.Fprintf(f, "# moolah key; public key: %s\n%s\n", id.Recipient(), id)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return id.Recipient().String(), err
}

// PublicKeyOf reads an age key file and returns its public key.
func PublicKeyOf(path string) (string, error) {
	ids, err := LoadIdentities(path, nil)
	if err != nil {
		return "", err
	}
	x, ok := ids[0].(*age.X25519Identity)
	if !ok {
		return "", errors.New("not an age key")
	}
	return x.Recipient().String(), nil
}
