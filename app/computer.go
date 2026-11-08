package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ComputerConfig is ~/.config/moolah/config.toml: settings that apply to this
// computer only and are never synced (paths, keys, offline mode). Everything else
// lives in the book's database.
type ComputerConfig struct {
	BackupDir   string              `toml:"backup_dir,omitempty"`   // where backups go; default ~/.local/share/moolah/backups
	Books       map[string]BookSync `toml:"books,omitempty"`        // sync setup, by book ID
	CurrentBook string              `toml:"current_book,omitempty"` // set with "moolah book use"
	Offline     bool                `toml:"offline,omitempty"`      // never download exchange rates
}

// BookSync is the sync setup of one book on this computer.
type BookSync struct {
	Identity string `toml:"identity,omitempty"` // private key; the default key when empty
	SyncDir  string `toml:"sync_dir"`
}

const configHeader = "# moolah settings for this computer only (never synced).\n" +
	"# Edit by hand, or with: moolah config set <key> <value>. Book settings are in the book.\n\n"

// ConfigFile is the path of this computer's config file.
func (p Paths) ConfigFile() string { return filepath.Join(p.ConfigDir, "config.toml") }

// LoadConfig reads this computer's config. Unknown keys are errors. Older setups
// (a current-book file and sync/*.json files) are moved into it on first use.
func (p Paths) LoadConfig() (ComputerConfig, error) {
	var c ComputerConfig
	data, err := os.ReadFile(p.ConfigFile())
	if errors.Is(err, os.ErrNotExist) {
		return p.migrateLegacyConfig()
	}
	if err != nil {
		return c, err
	}
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		return c, fmt.Errorf("%s: %w", p.ConfigFile(), err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return c, fmt.Errorf("%s: unknown setting %s", p.ConfigFile(), strings.Join(keys, ", "))
	}
	if c.CurrentBook != "" {
		if err := ValidateBookName(c.CurrentBook); err != nil {
			return c, fmt.Errorf("%s: current_book: %w", p.ConfigFile(), err)
		}
	}
	if c.BackupDir != "" && !filepath.IsAbs(expandHome(c.BackupDir)) {
		return c, fmt.Errorf("%s: backup_dir must be an absolute path", p.ConfigFile())
	}
	return c, nil
}

// SaveConfig writes this computer's config atomically.
func (p Paths) SaveConfig(c ComputerConfig) error {
	var buf bytes.Buffer
	buf.WriteString(configHeader)
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		return err
	}
	tmp := p.ConfigFile() + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.ConfigFile())
}

// UpdateConfig loads, changes and saves this computer's config.
func (p Paths) UpdateConfig(change func(c *ComputerConfig) error) error {
	c, err := p.LoadConfig()
	if err != nil {
		return err
	}
	if err := change(&c); err != nil {
		return err
	}
	return p.SaveConfig(c)
}

// migrateLegacyConfig builds config.toml from the files older versions wrote.
func (p Paths) migrateLegacyConfig() (ComputerConfig, error) {
	var c ComputerConfig
	legacy := false
	cur := filepath.Join(p.ConfigDir, "current-book")
	if b, err := os.ReadFile(cur); err == nil {
		if name := strings.TrimSpace(string(b)); ValidateBookName(name) == nil {
			c.CurrentBook = name
		}
		legacy = true
	}
	files, _ := filepath.Glob(filepath.Join(p.ConfigDir, "sync", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return c, err
		}
		var old struct {
			Dir      string `json:"dir"`
			Identity string `json:"identity"`
		}
		if err := json.Unmarshal(b, &old); err != nil {
			return c, fmt.Errorf("%s: %w", f, err)
		}
		if c.Books == nil {
			c.Books = map[string]BookSync{}
		}
		c.Books[strings.TrimSuffix(filepath.Base(f), ".json")] = BookSync{Identity: old.Identity, SyncDir: old.Dir}
		legacy = true
	}
	if !legacy {
		return c, nil
	}
	if err := p.SaveConfig(c); err != nil {
		return c, err
	}
	_ = os.Remove(cur)
	for _, f := range files {
		_ = os.Remove(f)
	}
	_ = os.Remove(filepath.Join(p.ConfigDir, "sync"))
	return c, nil
}

// moveComputerSettings moves settings that older versions kept in the book
// (backup_dir, offline) to this computer's config, and removes them from the book.
func (a *App) moveComputerSettings(ctx context.Context) error {
	dir, hasDir, err := a.st.Setting(ctx, "backup_dir")
	if err != nil {
		return err
	}
	off, hasOff, err := a.st.Setting(ctx, "offline")
	if err != nil {
		return err
	}
	if !hasDir && !hasOff {
		return nil
	}
	err = a.Paths.UpdateConfig(func(c *ComputerConfig) error {
		if hasDir && c.BackupDir == "" {
			c.BackupDir = dir
		}
		if hasOff && off == "true" {
			c.Offline = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range []string{"backup_dir", "offline"} {
		if err := a.st.DeleteSetting(ctx, k); err != nil {
			return err
		}
	}
	return nil
}
