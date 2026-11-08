// Tests for where settings live: the book (synced) or this computer (config.toml).
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingPlaces(t *testing.T) {
	// Paths and offline mode stay on this computer: they are never written into the book, so they never sync.
	t.Run("computer settings stay out of the book", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error { return a.SetConfig(ctx, KeyOffline, "true") })
		mustDo(t, func() error { return a.SetConfig(ctx, KeyBackupDir, "/backups/moolah") })
		all, err := a.st.Settings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := all[KeyOffline]; ok {
			t.Fatal("offline was written into the book")
		}
		c, err := a.Paths.LoadConfig()
		if err != nil || !c.Offline || c.BackupDir != "/backups/moolah" {
			t.Fatalf("config %+v, %v", c, err)
		}
		if v, _ := a.Config(ctx, KeyBackupDir); v != "/backups/moolah" {
			t.Fatalf("got %q", v)
		}
	})
	// Setups from older versions (current-book file, sync JSON, settings in the book) move to config.toml.
	t.Run("older setups are migrated", func(t *testing.T) {
		a := newBook(t)
		dir := a.Paths.ConfigDir
		mustDo(t, func() error { return os.MkdirAll(filepath.Join(dir, "sync"), 0o700) })
		mustDo(t, func() error { return os.WriteFile(filepath.Join(dir, "current-book"), []byte("main\n"), 0o600) })
		mustDo(t, func() error {
			return os.WriteFile(filepath.Join(dir, "sync", "abc.json"), []byte(`{"dir":"/sync/x","identity":"/k"}`), 0o600)
		})
		mustDo(t, func() error { return a.st.SetSetting(ctx, "offline", "true") })
		mustDo(t, func() error { return a.moveComputerSettings(ctx) })
		c, err := a.Paths.LoadConfig()
		if err != nil || c.CurrentBook != "main" || c.Books["abc"].SyncDir != "/sync/x" || !c.Offline {
			t.Fatalf("config %+v, %v", c, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "current-book")); err == nil {
			t.Fatal("old file was not removed")
		}
		if _, ok, _ := a.st.Setting(ctx, "offline"); ok {
			t.Fatal("offline is still in the book")
		}
	})
	// A typo in config.toml is an error that names the file and the key.
	t.Run("unknown key in config.toml", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error { return os.MkdirAll(a.Paths.ConfigDir, 0o700) })
		mustDo(t, func() error { return os.WriteFile(a.Paths.ConfigFile(), []byte("ofline = true\n"), 0o600) })
		if _, err := a.Paths.LoadConfig(); err == nil || !strings.Contains(err.Error(), "ofline") {
			t.Fatalf("got %v", err)
		}
	})
	// Any language and region is accepted and written in one form.
	t.Run("locale normalized", func(t *testing.T) {
		for in, want := range map[string]string{"de-DE": "de_DE", "he_IL": "he_IL", "fr": "fr"} {
			if got, err := NormalizeLocale(in); err != nil || got != want {
				t.Fatalf("%s: got %q, %v", in, got, err)
			}
		}
		if _, err := NormalizeLocale("not a locale!"); err == nil {
			t.Fatal("bad locale accepted")
		}
	})
}
