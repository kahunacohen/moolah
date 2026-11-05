package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"

	"moolah/core"
	"moolah/crypt"
	"moolah/store"
	"moolah/syncdir"
)

// Sync keeps one local database per computer and exchanges encrypted snapshots
// through a folder that another tool syncs. History is a chain of snapshot
// generations; meta.sync_gen is the generation the local book is based on, and
// meta.synced_seq is change_seq at that time. The book has unsent changes when
// change_seq differs from synced_seq.

const defaultSyncKeep = 20

// SyncConfig is the per-computer sync setup of one book. It is stored outside the
// book, because paths and keys differ between computers.
type SyncConfig struct {
	Dir      string `json:"dir"`
	Identity string `json:"identity,omitempty"` // private key file; the default key when empty
}

// SyncConflictError means two computers changed the book independently.
type SyncConflictError struct {
	Base    int64
	Sides   []string       // machine names, this computer first when it is involved
	Unique  map[string]int // transactions that only one side has
	Machine string         // this computer
}

func (e *SyncConflictError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "conflict: %s both changed data since snapshot %d.\n", strings.Join(e.Sides, " and "), e.Base)
	for _, s := range e.Sides {
		label := s
		if s == e.Machine {
			label += " (this computer)"
		}
		noun := "transactions"
		if e.Unique[s] == 1 {
			noun = "transaction"
		}
		fmt.Fprintf(&b, "  %s: %d %s that the other side does not have\n", label, e.Unique[s], noun)
	}
	keeps := make([]string, len(e.Sides))
	for i, s := range e.Sides {
		keeps[i] = "--keep " + s
	}
	fmt.Fprintf(&b, "Choose the copy to keep: moolah sync resolve %s\nThe other copy is saved as a backup.", strings.Join(keeps, " | "))
	return b.String()
}

var machineCleanRe = regexp.MustCompile(`[^a-z0-9-]+`)

// MachineID returns this computer's name for sync, creating it on first use.
func (p Paths) MachineID() (string, error) {
	file := filepath.Join(p.StateDir, "machine-id")
	if b, err := os.ReadFile(file); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	}
	host, _ := os.Hostname()
	host = strings.Trim(machineCleanRe.ReplaceAllString(strings.ToLower(host), "-"), "-")
	if len(host) > 20 {
		host = host[:20]
	}
	if host == "" {
		host = "computer"
	}
	suffix := make([]byte, 2)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	id := host + "-" + hex.EncodeToString(suffix)
	if err := os.MkdirAll(p.StateDir, 0o700); err != nil {
		return "", err
	}
	return id, os.WriteFile(file, []byte(id+"\n"), 0o600)
}

func (p Paths) syncConfigFile(dbID string) string {
	return filepath.Join(p.ConfigDir, "sync", dbID+".json")
}

// KeyFile is where "moolah sync keygen" writes the age private key.
func (p Paths) KeyFile() string { return filepath.Join(p.ConfigDir, crypt.KeyFileName) }

// Keygen creates an age key for backups and sync, if it does not exist yet, and
// returns its public key.
func (p Paths) Keygen() (pub string, created bool, err error) {
	if _, err := os.Stat(p.KeyFile()); err == nil {
		pub, err := crypt.PublicKeyOf(p.KeyFile())
		return pub, false, err
	}
	pub, err = crypt.GenerateKey(p.KeyFile())
	return pub, err == nil, err
}

func (a *App) dbID(ctx context.Context) (string, error) {
	id, ok, err := a.st.Meta(ctx, "db_id")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("this book has no ID; it was not created by moolah init")
	}
	return id, nil
}

// SyncConfig returns this computer's sync setup for the book, or nil when sync is off.
func (a *App) SyncConfig(ctx context.Context) (*SyncConfig, error) {
	id, err := a.dbID(ctx)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(a.Paths.syncConfigFile(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c SyncConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("sync settings: %w", err)
	}
	return &c, nil
}

func (a *App) writeSyncConfig(ctx context.Context, c *SyncConfig) error {
	id, err := a.dbID(ctx)
	if err != nil {
		return err
	}
	file := a.Paths.syncConfigFile(id)
	if c == nil {
		err := os.Remove(file)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, b, 0o600)
}

func (a *App) metaInt(ctx context.Context, key string) (int64, error) {
	v, ok, err := a.st.Meta(ctx, key)
	if err != nil || !ok {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func (a *App) dirty(ctx context.Context) (bool, int64, error) {
	seq, err := a.st.ChangeSeq(ctx)
	if err != nil {
		return false, 0, err
	}
	synced, err := a.metaInt(ctx, "synced_seq")
	return seq != synced, seq - synced, err
}

func (a *App) identities(c *SyncConfig) ([]age.Identity, error) {
	path := c.Identity
	if path == "" {
		path = crypt.DefaultIdentityPath(a.Paths.ConfigDir)
	}
	return crypt.LoadIdentities(path, a.Passphrase)
}

// SyncState describes sync for "moolah sync status".
type SyncState struct {
	Configured bool
	Dir        string
	Head       *syncdir.Snapshot // newest snapshot in the folder
	Heads      int               // more than one means a conflict between other computers
	LocalGen   int64
	Machine    string
	OtherLocks []syncdir.Lock
	Unsent     int64 // local changes not uploaded
}

// SyncStatus reports the sync state without changing anything.
func (a *App) SyncStatus(ctx context.Context) (SyncState, error) {
	var s SyncState
	c, err := a.SyncConfig(ctx)
	if err != nil || c == nil {
		return s, err
	}
	s.Configured, s.Dir = true, c.Dir
	if s.Machine, err = a.Paths.MachineID(); err != nil {
		return s, err
	}
	if s.LocalGen, err = a.metaInt(ctx, "sync_gen"); err != nil {
		return s, err
	}
	if _, s.Unsent, err = a.dirty(ctx); err != nil {
		return s, err
	}
	f := syncdir.Folder{Dir: c.Dir}
	snaps, err := f.Snapshots()
	if err != nil {
		return s, err
	}
	heads := syncdir.Heads(snaps)
	s.Heads = len(heads)
	if len(heads) > 0 {
		h := heads[len(heads)-1]
		s.Head = &h
	}
	s.OtherLocks, err = f.OtherLocks(s.Machine, a.now())
	return s, err
}

// SyncBefore runs before a command: it marks this computer as active in the sync
// folder and loads newer data. It returns warnings for the user.
func (a *App) SyncBefore(ctx context.Context) ([]string, error) {
	c, err := a.SyncConfig(ctx)
	if err != nil || c == nil {
		return nil, err
	}
	machine, err := a.Paths.MachineID()
	if err != nil {
		return nil, err
	}
	f := syncdir.Folder{Dir: c.Dir}
	if _, err := os.Stat(c.Dir); err != nil {
		return []string{fmt.Sprintf("sync folder %s is not available; working with local data only", c.Dir)}, nil
	}
	var warnings []string
	locks, err := f.OtherLocks(machine, a.now())
	if err != nil {
		return nil, err
	}
	for _, l := range locks {
		warnings = append(warnings, fmt.Sprintf("moolah is in use on %s since %s. Changes made here may conflict.",
			l.Machine, l.Since.Local().Format("15:04")))
	}
	if err := f.WriteLock(machine, a.now(), a.now()); err != nil {
		return nil, err
	}
	if err := a.pull(ctx, c, machine); err != nil {
		return warnings, err
	}
	return warnings, nil
}

// SyncAfter runs after a command: it uploads local changes and removes the lock.
func (a *App) SyncAfter(ctx context.Context) error {
	c, err := a.SyncConfig(ctx)
	if err != nil || c == nil {
		return err
	}
	machine, err := a.Paths.MachineID()
	if err != nil {
		return err
	}
	f := syncdir.Folder{Dir: c.Dir}
	if _, err := os.Stat(c.Dir); err != nil {
		return nil
	}
	pushErr := a.push(ctx, c, machine, false)
	if err := f.RemoveLock(machine); err != nil && pushErr == nil {
		pushErr = err
	}
	return pushErr
}

// Heartbeat refreshes this computer's lock (for long-running "moolah serve").
func (a *App) Heartbeat(ctx context.Context, since time.Time) error {
	c, err := a.SyncConfig(ctx)
	if err != nil || c == nil {
		return err
	}
	machine, err := a.Paths.MachineID()
	if err != nil {
		return err
	}
	return syncdir.Folder{Dir: c.Dir}.WriteLock(machine, since, a.now())
}

// ReleaseLock removes this computer's lock.
func (a *App) ReleaseLock(ctx context.Context) error {
	c, err := a.SyncConfig(ctx)
	if err != nil || c == nil {
		return err
	}
	machine, err := a.Paths.MachineID()
	if err != nil {
		return err
	}
	return syncdir.Folder{Dir: c.Dir}.RemoveLock(machine)
}

// SyncNow loads newer data, then uploads local changes. It returns a short status line.
func (a *App) SyncNow(ctx context.Context) (string, error) {
	c, err := a.SyncConfig(ctx)
	if err != nil {
		return "", err
	}
	if c == nil {
		return "", errors.New("sync is off for this book. Start it with: moolah sync init <folder>")
	}
	machine, err := a.Paths.MachineID()
	if err != nil {
		return "", err
	}
	if err := a.pull(ctx, c, machine); err != nil {
		return "", err
	}
	if err := a.push(ctx, c, machine, false); err != nil {
		return "", err
	}
	gen, err := a.metaInt(ctx, "sync_gen")
	return fmt.Sprintf("up to date (snapshot %d)", gen), err
}

// pull loads the newest snapshot when the local book has no unsent changes.
func (a *App) pull(ctx context.Context, c *SyncConfig, machine string) error {
	f := syncdir.Folder{Dir: c.Dir}
	snaps, err := f.Snapshots()
	if err != nil {
		return err
	}
	heads := syncdir.Heads(snaps)
	if len(heads) == 0 {
		return nil
	}
	localGen, err := a.metaInt(ctx, "sync_gen")
	if err != nil {
		return err
	}
	if heads[0].Gen <= localGen {
		return nil
	}
	dirty, _, err := a.dirty(ctx)
	if err != nil {
		return err
	}
	if dirty || len(heads) > 1 {
		return a.conflict(ctx, c, machine, localGen, heads, dirty)
	}
	return a.adopt(ctx, c, heads[0])
}

// adopt replaces the local book with a snapshot.
func (a *App) adopt(ctx context.Context, c *SyncConfig, s syncdir.Snapshot) error {
	ids, err := a.identities(c)
	if err != nil {
		return err
	}
	localID, err := a.dbID(ctx)
	if err != nil {
		return err
	}
	tmp := a.Path() + ".sync"
	_ = os.Remove(tmp)
	if err := (syncdir.Folder{Dir: c.Dir}).Fetch(s, ids, tmp); err != nil {
		return err
	}
	remoteID, err := readDBID(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if remoteID != localID {
		_ = os.Remove(tmp)
		return fmt.Errorf("the sync folder %s holds a different book; check the folder with: moolah sync status", c.Dir)
	}
	if err := a.st.Close(); err != nil {
		return err
	}
	replaceErr := replaceDB(tmp, a.Path())
	st, err := store.Open(a.Path(), store.OpenOptions{BeforeMigrate: a.preMigrateBackup})
	if err != nil {
		return err
	}
	a.st = st
	if replaceErr != nil {
		return replaceErr
	}
	return a.markSynced(ctx, s.Gen)
}

func (a *App) markSynced(ctx context.Context, gen int64) error {
	seq, err := a.st.ChangeSeq(ctx)
	if err != nil {
		return err
	}
	return a.st.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.SetMeta(ctx, "sync_gen", strconv.FormatInt(gen, 10)); err != nil {
			return err
		}
		return tx.SetMeta(ctx, "synced_seq", strconv.FormatInt(seq, 10))
	})
}

func readDBID(path string) (string, error) {
	st, err := store.Open(path, store.OpenOptions{})
	if err != nil {
		return "", err
	}
	defer st.Close()
	id, ok, err := st.Meta(context.Background(), "db_id")
	if err == nil && !ok {
		err = errors.New("the snapshot is not a moolah book")
	}
	return id, err
}

// push uploads the local book as a new snapshot when it has unsent changes (or when forced).
func (a *App) push(ctx context.Context, c *SyncConfig, machine string, force bool) error {
	dirty, _, err := a.dirty(ctx)
	if err != nil || (!dirty && !force) {
		return err
	}
	f := syncdir.Folder{Dir: c.Dir}
	snaps, err := f.Snapshots()
	if err != nil {
		return err
	}
	localGen, err := a.metaInt(ctx, "sync_gen")
	if err != nil {
		return err
	}
	heads := syncdir.Heads(snaps)
	maxGen := localGen
	if len(heads) > 0 && heads[0].Gen > maxGen {
		if !force {
			return a.conflict(ctx, c, machine, localGen, heads, true)
		}
		maxGen = heads[0].Gen
	}
	recipient, err := a.recipient(ctx)
	if err != nil {
		return err
	}
	if recipient == nil {
		return errors.New("sync needs backup_recipient (the key that encrypts snapshots). Run: moolah sync keygen")
	}
	seq, err := a.st.ChangeSeq(ctx)
	if err != nil {
		return err
	}
	gen := maxGen + 1
	plain, cleanup, err := a.plainCopy(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := setSyncMeta(plain, gen, seq); err != nil {
		return err
	}
	if _, err := f.Publish(plain, gen, localGen, machine, recipient); err != nil {
		return err
	}
	if err := a.st.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.SetMeta(ctx, "sync_gen", strconv.FormatInt(gen, 10)); err != nil {
			return err
		}
		return tx.SetMeta(ctx, "synced_seq", strconv.FormatInt(seq, 10))
	}); err != nil {
		return err
	}
	return f.Cleanup(machine, defaultSyncKeep, a.now())
}

// setSyncMeta writes the sync bookkeeping into a database copy before it is uploaded,
// so a computer that loads it knows which generation it is.
func setSyncMeta(path string, gen, seq int64) error {
	st, err := store.Open(path, store.OpenOptions{})
	if err != nil {
		return err
	}
	ctx := context.Background()
	err = st.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.SetMeta(ctx, "sync_gen", strconv.FormatInt(gen, 10)); err != nil {
			return err
		}
		if err := tx.SetMeta(ctx, "change_seq", strconv.FormatInt(seq, 10)); err != nil {
			return err
		}
		return tx.SetMeta(ctx, "synced_seq", strconv.FormatInt(seq, 10))
	})
	if cerr := st.Close(); err == nil {
		err = cerr
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	return err
}

// conflict builds a SyncConflictError with a summary of what each side has.
func (a *App) conflict(ctx context.Context, c *SyncConfig, machine string, base int64, heads []syncdir.Snapshot, localDirty bool) error {
	ids, err := a.identities(c)
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(filepath.Dir(a.Path()), ".conflict-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	type side struct{ name, path string }
	var sides []side
	if localDirty {
		plain, cleanup, err := a.plainCopy(ctx)
		if err != nil {
			return err
		}
		defer cleanup()
		sides = append(sides, side{machine, plain})
	}
	for i, h := range heads {
		p := filepath.Join(tmpDir, fmt.Sprintf("head-%d.db", i))
		if err := (syncdir.Folder{Dir: c.Dir}).Fetch(h, ids, p); err != nil {
			return err
		}
		sides = append(sides, side{h.Machine, p})
	}
	prints := make([]map[string]int, len(sides))
	for i, s := range sides {
		if prints[i], err = fingerprints(s.path); err != nil {
			return err
		}
	}
	e := &SyncConflictError{Base: base, Machine: machine, Unique: map[string]int{}}
	for i, s := range sides {
		others := map[string]int{}
		for j := range sides {
			if j != i {
				for k, v := range prints[j] {
					others[k] += v
				}
			}
		}
		n := 0
		for k, v := range prints[i] {
			if v > others[k] {
				n += v - others[k]
			}
		}
		e.Sides = append(e.Sides, s.name)
		e.Unique[s.name] += n
	}
	return e
}

// fingerprints counts transactions by their content (date, payee, lines), so two
// copies can be compared even when their transaction IDs differ.
func fingerprints(path string) (map[string]int, error) {
	st, err := store.Open(path, store.OpenOptions{})
	if err != nil {
		return nil, err
	}
	defer st.Close()
	ctx := context.Background()
	tree, err := st.Tree(ctx)
	if err != nil {
		return nil, err
	}
	txs, err := st.Transactions(ctx, store.TxFilter{})
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, t := range txs {
		out[txFingerprint(tree, t)]++
	}
	return out, nil
}

func txFingerprint(tree *core.Tree, t core.Transaction) string {
	lines := make([]string, 0, len(t.Postings))
	for _, p := range t.Postings {
		lines = append(lines, tree.Path(p.AccountID)+"="+p.Amount.String())
	}
	sort.Strings(lines)
	return t.Date.String() + "|" + t.Payee + "|" + strings.Join(lines, ";")
}

// SyncResolve ends a conflict by keeping one side. The other sides are saved as
// backups, and a new snapshot makes the kept copy the only head.
func (a *App) SyncResolve(ctx context.Context, keep string) (string, error) {
	c, err := a.SyncConfig(ctx)
	if err != nil {
		return "", err
	}
	if c == nil {
		return "", errors.New("sync is off for this book")
	}
	machine, err := a.Paths.MachineID()
	if err != nil {
		return "", err
	}
	if keep == "local" {
		keep = machine
	}
	f := syncdir.Folder{Dir: c.Dir}
	snaps, err := f.Snapshots()
	if err != nil {
		return "", err
	}
	heads := syncdir.Heads(snaps)
	backupDir, err := a.backupDir(ctx, a.st)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	recipient, err := a.recipient(ctx)
	if err != nil {
		return "", err
	}
	if recipient == nil {
		return "", errors.New("backup_recipient is not set")
	}
	var saved []string
	if keep == machine {
		for _, h := range heads {
			dst := filepath.Join(backupDir, "conflict-"+filepath.Base(h.Path))
			if err := copyFile(h.Path, dst); err != nil {
				return "", err
			}
			saved = append(saved, dst)
		}
		if err := a.push(ctx, c, machine, true); err != nil {
			return "", err
		}
	} else {
		var chosen *syncdir.Snapshot
		for i := range heads {
			if heads[i].Machine == keep {
				chosen = &heads[i]
			}
		}
		if chosen == nil {
			names := []string{machine}
			for _, h := range heads {
				names = append(names, h.Machine)
			}
			return "", fmt.Errorf("%q is not one of the sides: %s", keep, strings.Join(names, ", "))
		}
		path, err := a.encryptedCopy(ctx, backupDir, "conflict-"+machine, recipient)
		if err != nil {
			return "", err
		}
		saved = append(saved, path)
		for _, h := range heads {
			if h.Path != chosen.Path {
				dst := filepath.Join(backupDir, "conflict-"+filepath.Base(h.Path))
				if err := copyFile(h.Path, dst); err != nil {
					return "", err
				}
				saved = append(saved, dst)
			}
		}
		if err := a.adopt(ctx, c, *chosen); err != nil {
			return "", err
		}
		if len(heads) > 1 {
			if err := a.push(ctx, c, machine, true); err != nil {
				return "", err
			}
		}
	}
	return fmt.Sprintf("Kept the copy from %s. Saved the other copy as: %s", keep, strings.Join(saved, ", ")), nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

// SyncInit starts syncing this book through an empty (or new) folder.
func (a *App) SyncInit(ctx context.Context, dir, identity string) error {
	if c, err := a.SyncConfig(ctx); err != nil {
		return err
	} else if c != nil {
		return fmt.Errorf("this book already syncs with %s", c.Dir)
	}
	dir, err := filepath.Abs(expandHome(dir))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	snaps, err := syncdir.Folder{Dir: dir}.Snapshots()
	if err != nil {
		return err
	}
	if len(snaps) > 0 {
		return fmt.Errorf("%s already has moolah data. To use it, create a new book that joins it: moolah book add <name> --join %s", dir, dir)
	}
	c := &SyncConfig{Dir: dir, Identity: identity}
	machine, err := a.Paths.MachineID()
	if err != nil {
		return err
	}
	if err := a.push(ctx, c, machine, true); err != nil {
		return err
	}
	// Check now that this computer can read what it wrote; otherwise every other
	// computer would fail later.
	snaps, err = syncdir.Folder{Dir: dir}.Snapshots()
	if err == nil {
		var ids []age.Identity
		if ids, err = a.identities(c); err == nil {
			tmp := filepath.Join(filepath.Dir(a.Path()), ".sync-check.db")
			err = syncdir.Folder{Dir: dir}.Fetch(snaps[len(snaps)-1], ids, tmp)
			_ = os.Remove(tmp)
		}
	}
	if err != nil {
		for _, s := range snaps {
			_ = os.Remove(s.Path)
		}
		_ = a.st.SetMeta(ctx, "sync_gen", "0")
		return fmt.Errorf("sync not started: this computer cannot decrypt the snapshot (%w). Check backup_recipient and the key file", err)
	}
	return a.writeSyncConfig(ctx, c)
}

// SyncOff stops sync on this computer. The local book and the folder stay.
func (a *App) SyncOff(ctx context.Context) error {
	if err := a.ReleaseLock(ctx); err != nil {
		return err
	}
	return a.writeSyncConfig(ctx, nil)
}

// SyncFolderInfo describes the newest data in a sync folder, for "init" to offer a join.
func SyncFolderInfo(dir string) (*syncdir.Snapshot, error) {
	snaps, err := syncdir.Folder{Dir: expandHome(dir)}.Snapshots()
	if err != nil || len(snaps) == 0 {
		return nil, err
	}
	h := syncdir.Heads(snaps)
	s := h[len(h)-1]
	return &s, nil
}

// JoinParams describes joining existing sync data as a new local book.
type JoinParams struct {
	Book       string
	Dir        string
	Identity   string
	Passphrase func() (string, error)
	Path       string
}

// Join creates a local book from the newest snapshot in a sync folder.
func Join(p JoinParams, paths Paths) (*App, error) {
	if _, err := os.Stat(p.Path); err == nil {
		return nil, fmt.Errorf("%s already exists", p.Path)
	}
	dir, err := filepath.Abs(expandHome(p.Dir))
	if err != nil {
		return nil, err
	}
	snaps, err := syncdir.Folder{Dir: dir}.Snapshots()
	if err != nil {
		return nil, err
	}
	heads := syncdir.Heads(snaps)
	if len(heads) == 0 {
		return nil, fmt.Errorf("%s has no moolah data yet", dir)
	}
	if len(heads) > 1 {
		return nil, errors.New("the sync folder has a conflict between two computers; resolve it on one of them first")
	}
	identity := p.Identity
	if identity == "" {
		identity = crypt.DefaultIdentityPath(paths.ConfigDir)
	}
	ids, err := crypt.LoadIdentities(identity, p.Passphrase)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o700); err != nil {
		return nil, err
	}
	if err := (syncdir.Folder{Dir: dir}).Fetch(heads[0], ids, p.Path); err != nil {
		return nil, err
	}
	a, err := Open(p.Path, p.Book, paths)
	if err != nil {
		_ = os.Remove(p.Path)
		return nil, err
	}
	a.Passphrase = p.Passphrase
	a.Joined = true
	ctx := context.Background()
	if err := a.markSynced(ctx, heads[0].Gen); err != nil {
		return nil, err
	}
	if err := a.writeSyncConfig(ctx, &SyncConfig{Dir: dir, Identity: p.Identity}); err != nil {
		return nil, err
	}
	return a, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
