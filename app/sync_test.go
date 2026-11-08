// Tests for syncing one book between two computers through a shared folder.
package app

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"moolah/core"
	"moolah/store"
	"moolah/syncdir"
)

type machine struct {
	app   *App
	paths Paths
}

func machinePaths(root, name string) Paths {
	return Paths{ConfigDir: filepath.Join(root, name, "config"), DataDir: filepath.Join(root, name, "data"), StateDir: filepath.Join(root, name, "state")}
}

// twoMachines sets up a book on computer A that syncs through a folder, and the
// same book joined on computer B with the same key.
func twoMachines(t *testing.T) (a, b *machine, dir string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "Dropbox", "moolah")
	clock := func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) }

	pa := machinePaths(root, "a")
	pub, _, err := pa.Keygen()
	if err != nil {
		t.Fatal(err)
	}
	appA, err := Create(InitParams{BaseCurrency: "ILS", BackupRecipient: pub, Path: pa.BookPath("main")}, "main", pa)
	if err != nil {
		t.Fatal(err)
	}
	appA.SetClock(clock)
	mustDo(t, func() error {
		_, err := appA.AddAccount(ctx, AddAccountParams{Path: "assets:bank:leumi", Currency: "ILS", Opening: "1000", OpeningDate: core.MustDate("2026-10-01")})
		return err
	})
	mustDo(t, func() error { return appA.SyncInit(ctx, dir, "") })

	pb := machinePaths(root, "b")
	key, err := os.ReadFile(pa.KeyFile())
	if err != nil {
		t.Fatal(err)
	}
	mustDo(t, func() error { return os.MkdirAll(pb.ConfigDir, 0o700) })
	mustDo(t, func() error { return os.WriteFile(pb.KeyFile(), key, 0o600) })
	appB, err := Join(JoinParams{Book: "main", Dir: dir, Path: pb.BookPath("main")}, pb)
	if err != nil {
		t.Fatal(err)
	}
	appB.SetClock(clock)
	t.Cleanup(func() { _ = appA.Close(); _ = appB.Close() })
	return &machine{appA, pa}, &machine{appB, pb}, dir
}

// command runs one CLI-like command: sync before, the change, sync after.
func (m *machine) command(t *testing.T, change func(a *App) error) []string {
	t.Helper()
	warnings, err := m.app.SyncBefore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if change != nil {
		mustDo(t, func() error { return change(m.app) })
	}
	mustDo(t, func() error { return m.app.SyncAfter(ctx) })
	return warnings
}

func spend(amount string) func(a *App) error {
	return func(a *App) error {
		_, err := a.Add(ctx, AddParams{Account: "leumi", Amount: amount, Category: "food"})
		return err
	}
}

func netWorth(t *testing.T, a *App) string {
	t.Helper()
	rep, err := a.Balances(ctx, BalanceParams{})
	if err != nil {
		t.Fatal(err)
	}
	return rep.NetWorth.String()
}

func TestSync(t *testing.T) {
	// A change on one computer appears on the other after its next command.
	t.Run("changes move both ways", func(t *testing.T) {
		a, b, _ := twoMachines(t)
		if got := netWorth(t, b.app); got != "1000" {
			t.Fatalf("joined book has %s", got)
		}
		a.command(t, spend("-100"))
		b.command(t, nil)
		if got := netWorth(t, b.app); got != "900" {
			t.Fatalf("B after pull: %s", got)
		}
		b.command(t, spend("-50"))
		a.command(t, nil)
		if got := netWorth(t, a.app); got != "850" {
			t.Fatalf("A after pull: %s", got)
		}
	})

	// When both computers changed data, moolah stops and changes nothing, so no work is lost silently.
	t.Run("fork is detected and nothing changes", func(t *testing.T) {
		a, b, _ := twoMachines(t)
		mustDo(t, func() error { return spend("-100")(a.app) }) // A changes but does not upload yet
		b.command(t, spend("-7"))
		_, err := a.app.SyncBefore(ctx)
		var conflict *SyncConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("got %v", err)
		}
		if len(conflict.Sides) != 2 || conflict.Unique[conflict.Sides[0]] != 1 || conflict.Unique[conflict.Sides[1]] != 1 {
			t.Fatalf("summary %+v", conflict)
		}
		if got := netWorth(t, a.app); got != "900" {
			t.Fatalf("local data changed during a conflict: %s", got)
		}
	})

	// Resolving keeps the chosen copy everywhere and saves the other copy as a backup.
	t.Run("resolve keeps the chosen side", func(t *testing.T) {
		a, b, dir := twoMachines(t)
		mustDo(t, func() error { return spend("-100")(a.app) })
		b.command(t, spend("-7"))
		bMachine, _ := b.paths.MachineID()
		msg, err := a.app.SyncResolve(ctx, bMachine)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "Saved the other copy") {
			t.Fatalf("message %q", msg)
		}
		if got := netWorth(t, a.app); got != "993" {
			t.Fatalf("A after resolve: %s", got)
		}
		backups, _ := filepath.Glob(filepath.Join(a.paths.DefaultBackupDir(), "*conflict*"))
		if len(backups) == 0 {
			t.Fatal("the discarded copy was not saved")
		}
		snaps, _ := syncdir.Folder{Dir: dir}.Snapshots()
		if len(syncdir.Heads(snaps)) != 1 {
			t.Fatal("more than one head after resolve")
		}
		a.command(t, spend("-3"))
		b.command(t, nil)
		if got := netWorth(t, b.app); got != "990" {
			t.Fatalf("B after resolve: %s", got)
		}
	})

	// A computer that is in use shows up as a warning on the other one.
	t.Run("lock warns the other computer", func(t *testing.T) {
		a, b, _ := twoMachines(t)
		if _, err := a.app.SyncBefore(ctx); err != nil { // A is "running" and has not finished
			t.Fatal(err)
		}
		warnings := b.command(t, nil)
		if len(warnings) != 1 || !strings.Contains(warnings[0], "in use") {
			t.Fatalf("warnings %v", warnings)
		}
	})

	// Files that a sync tool is still writing (temporary names) are never read as snapshots.
	t.Run("half-written snapshots are ignored", func(t *testing.T) {
		a, _, dir := twoMachines(t)
		mustDo(t, func() error {
			return os.WriteFile(filepath.Join(dir, ".tmp-"+syncdir.FileName(99, 1, "x-0000")+"-123"), []byte("partial"), 0o600)
		})
		mustDo(t, func() error {
			return os.WriteFile(filepath.Join(dir, syncdir.FileName(98, 1, "x-0000")+".part"), []byte("partial"), 0o600)
		})
		snaps, err := syncdir.Folder{Dir: dir}.Snapshots()
		if err != nil {
			t.Fatal(err)
		}
		for _, sn := range snaps {
			if sn.Gen >= 98 {
				t.Fatalf("partial file read as snapshot %d", sn.Gen)
			}
		}
		a.command(t, nil) // a command still works next to them
	})

	// A computer with an older moolah never loads data it cannot read; it says to upgrade.
	t.Run("snapshot from a newer moolah is refused", func(t *testing.T) {
		a, b, dir := twoMachines(t)
		plain, cleanup, err := a.app.plainCopy(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		st, err := store.Open(plain, store.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		mustDo(t, func() error { return st.SetMeta(ctx, "schema_version", strconv.Itoa(store.SchemaVersion+1)) })
		mustDo(t, st.Close)
		r, err := a.app.recipient(ctx)
		if err != nil {
			t.Fatal(err)
		}
		gen, _, err := a.app.st.Meta(ctx, "sync_gen")
		if err != nil {
			t.Fatal(err)
		}
		g, _ := strconv.ParseInt(gen, 10, 64)
		if _, err := (syncdir.Folder{Dir: dir}).Publish(plain, g+1, g, "newer-0000", r); err != nil {
			t.Fatal(err)
		}
		_, err = b.app.SyncBefore(ctx)
		if !errors.Is(err, store.ErrNewerSchema) || !strings.Contains(err.Error(), "Upgrade moolah on this computer") {
			t.Fatalf("got %v", err)
		}
		if got := netWorth(t, b.app); got != "1000" {
			t.Fatalf("local data changed: %s", got)
		}
	})
}
