// Tests for where settings live: the book (synced) or this computer (config.toml).
package app

import (
	"os"
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
	// A typo in config.toml is an error that names the file and the key.
	t.Run("unknown key in config.toml", func(t *testing.T) {
		a := newBook(t)
		mustDo(t, func() error { return os.MkdirAll(a.Paths.ConfigDir, 0o700) })
		mustDo(t, func() error { return os.WriteFile(a.Paths.ConfigFile(), []byte("ofline = true\n"), 0o600) })
		if _, err := a.Paths.LoadConfig(); err == nil || !strings.Contains(err.Error(), "ofline") {
			t.Fatalf("got %v", err)
		}
	})
	// Number separators come from the language: 1,234.5 in English, 1.234,5 in German.
	t.Run("number style from locale", func(t *testing.T) {
		if st := NumberStyleFor("de_DE"); st.Decimal != "," || st.Group != "." {
			t.Fatalf("de_DE: %+v", st)
		}
		if st := NumberStyleFor("en_US"); st.Decimal != "." || st.Group != "," {
			t.Fatalf("en_US: %+v", st)
		}
	})
	// Right-to-left follows the language's script, so Arabic works like Hebrew.
	t.Run("right to left by script", func(t *testing.T) {
		for loc, want := range map[string]bool{"he_IL": true, "ar_EG": true, "fa": true, "en_US": false, "de": false} {
			if IsRightToLeft(loc) != want {
				t.Fatalf("%s: want %v", loc, want)
			}
		}
	})
	// init suggests the language and base currency from the system's locale variables.
	t.Run("system defaults", func(t *testing.T) {
		env := map[string]string{"LANG": "de_DE.UTF-8", "LC_MONETARY": "he_IL.UTF-8"}
		loc, cur := SystemDefaults(func(k string) string { return env[k] })
		if loc != "de_DE" || cur != "ILS" {
			t.Fatalf("got %s %s", loc, cur)
		}
		if loc, cur := SystemDefaults(func(string) string { return "C" }); loc != "" || cur != "" {
			t.Fatalf("C locale gave %s %s", loc, cur)
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
