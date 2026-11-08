package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	xcurrency "golang.org/x/text/currency"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"

	"moolah/core"
	"moolah/crypt"
)

// Setting keys.
const (
	KeyBackupDir       = "backup_dir"
	KeyBackupRecipient = "backup_recipient"
	KeyBaseCurrency    = "base_currency"
	KeyLocale          = "locale"
)

// SettingPlace tells where a setting is stored.
type SettingPlace string

// Setting places.
const (
	PlaceBook     SettingPlace = "book"          // in the book's database: synced and backed up
	PlaceComputer SettingPlace = "this computer" // in config.toml: never synced
)

var settingPlaces = map[string]SettingPlace{
	KeyBackupDir:       PlaceComputer,
	KeyBackupRecipient: PlaceBook,
	KeyBaseCurrency:    PlaceBook,
	KeyLocale:          PlaceBook,
	KeyOffline:         PlaceComputer,
	KeyRateSources:     PlaceBook,
}

// SettingKeys lists the settings a user may change, sorted.
var SettingKeys = func() []string {
	keys := make([]string, 0, len(settingPlaces))
	for k := range settingPlaces {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}()

// NumberStyleFor returns how numbers are written in a locale, from the Unicode
// CLDR data: it formats 1234.5 and reads the separators back. English when unknown.
func NumberStyleFor(locale string) core.NumberStyle {
	def := core.NumberStyle{Decimal: ".", Group: ","}
	tag, err := language.Parse(strings.ReplaceAll(locale, "_", "-"))
	if err != nil {
		return def
	}
	s := message.NewPrinter(tag).Sprint(number.Decimal(1234.5, number.Scale(1)))
	i, j := strings.Index(s, "1"), strings.Index(s, "234")
	k := strings.LastIndex(s, "5")
	if i < 0 || j <= i || k <= j+3 {
		return def // the language does not write Latin digits; keep English
	}
	return core.NumberStyle{Decimal: s[j+3 : k], Group: s[i+1 : j]}
}

// IsRightToLeft reports whether a locale's language is written right to left
// (Hebrew, Arabic, Persian, Urdu, …), from its script.
func IsRightToLeft(locale string) bool {
	tag, err := language.Parse(strings.ReplaceAll(locale, "_", "-"))
	if err != nil {
		return false
	}
	script, _ := tag.Script()
	switch script.String() {
	case "Arab", "Hebr", "Thaa", "Syrc", "Nkoo", "Adlm", "Rohg", "Mand", "Samr":
		return true
	}
	return false
}

// SystemDefaults suggests a locale and a base currency from the system's locale
// variables (LC_ALL, LC_MONETARY, LANG), for example de_DE.UTF-8 → de_DE and EUR.
// Empty values mean the system gives no usable hint.
func SystemDefaults(getenv func(string) string) (locale string, currency core.Currency) {
	clean := func(v string) string {
		v, _, _ = strings.Cut(v, ".")
		v, _, _ = strings.Cut(v, "@")
		if v == "C" || v == "POSIX" {
			return ""
		}
		return v
	}
	for _, k := range []string{"LC_ALL", "LANG"} {
		if v := clean(getenv(k)); v != "" {
			if l, err := NormalizeLocale(v); err == nil {
				locale = l
				break
			}
		}
	}
	for _, k := range []string{"LC_ALL", "LC_MONETARY", "LANG"} {
		tag, err := language.Parse(strings.ReplaceAll(clean(getenv(k)), "_", "-"))
		if err != nil {
			continue
		}
		region, conf := tag.Region()
		if conf != language.Exact {
			continue
		}
		if unit, ok := xcurrency.FromRegion(region); ok {
			if c, err := core.ParseCurrency(unit.String()); err == nil {
				return locale, c
			}
		}
	}
	return locale, ""
}

// NormalizeLocale checks a locale such as "de_DE", "he-IL" or "fr" and returns it
// as language_REGION (or just the language).
func NormalizeLocale(s string) (string, error) {
	tag, err := language.Parse(strings.ReplaceAll(strings.TrimSpace(s), "_", "-"))
	if err != nil || s == "" {
		return "", fmt.Errorf("unknown locale %q: use a language and region such as en_US, de_DE or he_IL", s)
	}
	base, _ := tag.Base()
	out := base.String()
	if region, conf := tag.Region(); conf == language.Exact {
		out += "_" + region.String()
	}
	return out, nil
}

func validateSetting(key, value string) error {
	switch key {
	case KeyBaseCurrency:
		_, err := core.ParseCurrency(value)
		return err
	case KeyLocale:
		_, err := NormalizeLocale(value)
		return err
	case KeyBackupDir:
		if !filepath.IsAbs(expandHome(value)) {
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
	case KeyRateSources:
		return nil // checked against the existing sources in SetConfig
	}
	return fmt.Errorf("unknown setting %q: use one of %s", key, strings.Join(SettingKeys, ", "))
}

// Config returns one setting, from the book or from this computer.
func (a *App) Config(ctx context.Context, key string) (string, error) {
	if err := knownKey(key); err != nil {
		return "", err
	}
	if settingPlaces[key] == PlaceComputer {
		c, err := a.Paths.LoadConfig()
		if err != nil {
			return "", err
		}
		switch key {
		case KeyBackupDir:
			return c.BackupDir, nil
		case KeyOffline:
			return strconv.FormatBool(c.Offline), nil
		}
	}
	v, _, err := a.st.Setting(ctx, key)
	return v, err
}

// SettingView is one setting for display.
type SettingView struct {
	Key   string       `json:"key"`
	Place SettingPlace `json:"stored_in"`
	Value string       `json:"value"`
}

// ConfigList returns all settings, including unset ones with an empty value.
func (a *App) ConfigList(ctx context.Context) ([]SettingView, error) {
	out := make([]SettingView, 0, len(SettingKeys))
	for _, k := range SettingKeys {
		v, err := a.Config(ctx, k)
		if err != nil {
			return nil, err
		}
		out = append(out, SettingView{Key: k, Place: settingPlaces[k], Value: v})
	}
	return out, nil
}

// SetConfig validates and changes one setting, in the book or on this computer.
// An empty value removes it (not allowed for base_currency and locale).
func (a *App) SetConfig(ctx context.Context, key, value string) error {
	if err := knownKey(key); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value != "" {
		if err := validateSetting(key, value); err != nil {
			return err
		}
	}
	switch key {
	case KeyBaseCurrency:
		if value == "" {
			return fmt.Errorf("%s cannot be empty", key)
		}
		c, _ := core.ParseCurrency(value)
		value = string(c)
	case KeyLocale:
		if value == "" {
			return fmt.Errorf("%s cannot be empty", key)
		}
		value, _ = NormalizeLocale(value)
	case KeyRateSources:
		all, err := a.loadSources(ctx)
		if err != nil {
			return err
		}
		for _, n := range splitList(value) {
			if _, ok := all[n]; !ok {
				return fmt.Errorf("there is no rate source %q. See: moolah source list", n)
			}
		}
		value = strings.Join(splitList(value), ",")
	}
	if settingPlaces[key] == PlaceComputer {
		return a.Paths.UpdateConfig(func(c *ComputerConfig) error {
			switch key {
			case KeyBackupDir:
				c.BackupDir = value
			case KeyOffline:
				c.Offline = value == "true"
			}
			return nil
		})
	}
	if value == "" {
		return a.st.DeleteSetting(ctx, key)
	}
	return a.st.SetSetting(ctx, key, value)
}

func knownKey(key string) error {
	if _, ok := settingPlaces[key]; !ok {
		return fmt.Errorf("unknown setting %q: use one of %s", key, strings.Join(SettingKeys, ", "))
	}
	return nil
}

// backupDir is this computer's backup folder.
func (a *App) backupDir() (string, error) {
	c, err := a.Paths.LoadConfig()
	if err != nil {
		return "", err
	}
	if c.BackupDir != "" {
		return expandHome(c.BackupDir), nil
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
