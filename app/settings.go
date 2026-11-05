package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"moolah/core"
	"moolah/crypt"
	"moolah/store"
)

// Setting keys.
const (
	KeyBackupDir       = "backup_dir"
	KeyBackupRecipient = "backup_recipient"
	KeyBaseCurrency    = "base_currency"
	KeyLocale          = "locale"
)

// SettingKeys lists the settings a user may change.
var SettingKeys = []string{KeyBackupDir, KeyBackupRecipient, KeyBaseCurrency, KeyLocale, KeyOffline}

var locales = []string{"en_US", "he_IL"}

func validateSetting(key, value string) error {
	switch key {
	case KeyBaseCurrency:
		_, err := core.ParseCurrency(value)
		return err
	case KeyLocale:
		for _, l := range locales {
			if value == l {
				return nil
			}
		}
		return fmt.Errorf("unknown locale %q: use one of %s", value, strings.Join(locales, ", "))
	case KeyBackupDir:
		if !filepath.IsAbs(value) {
			return fmt.Errorf("backup_dir must be an absolute path")
		}
		return nil
	case KeyBackupRecipient:
		_, err := crypt.ParseRecipient(value)
		return err
	case KeyOffline:
		if value != "true" && value != "false" {
			return fmt.Errorf("offline must be true or false")
		}
		return nil
	}
	return fmt.Errorf("unknown setting %q: use one of %s", key, strings.Join(SettingKeys, ", "))
}

// Config returns one setting.
func (a *App) Config(ctx context.Context, key string) (string, error) {
	if err := knownKey(key); err != nil {
		return "", err
	}
	v, _, err := a.st.Setting(ctx, key)
	return v, err
}

// ConfigList returns all settings, including unset ones with an empty value.
func (a *App) ConfigList(ctx context.Context) ([][2]string, error) {
	all, err := a.st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	keys := append([]string(nil), SettingKeys...)
	sort.Strings(keys)
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, all[k]})
	}
	return out, nil
}

// SetConfig validates and changes one setting. An empty value removes it
// (not allowed for base_currency and locale).
func (a *App) SetConfig(ctx context.Context, key, value string) error {
	if err := knownKey(key); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		if key == KeyBaseCurrency || key == KeyLocale {
			return fmt.Errorf("%s cannot be empty", key)
		}
		return a.st.DeleteSetting(ctx, key)
	}
	if key == KeyBaseCurrency {
		c, err := core.ParseCurrency(value)
		if err != nil {
			return err
		}
		value = string(c)
	}
	if err := validateSetting(key, value); err != nil {
		return err
	}
	return a.st.SetSetting(ctx, key, value)
}

func knownKey(key string) error {
	for _, k := range SettingKeys {
		if k == key {
			return nil
		}
	}
	return fmt.Errorf("unknown setting %q: use one of %s", key, strings.Join(SettingKeys, ", "))
}

func (a *App) backupDir(ctx context.Context, s *store.Store) (string, error) {
	v, ok, err := s.Setting(ctx, KeyBackupDir)
	if err != nil {
		return "", err
	}
	if ok {
		return v, nil
	}
	return a.Paths.DefaultBackupDir(), nil
}

// DefaultSSHPublicKey returns the user's ed25519 SSH public key, if there is one.
func DefaultSSHPublicKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".ssh", "id_ed25519.pub"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
