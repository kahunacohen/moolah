// Package syncdir manages moolah's sync folder: a folder that the user syncs between
// computers with Dropbox, Syncthing or similar. It holds only encrypted, write-once
// snapshot files and small lock files; it never holds a live database.
package syncdir

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"filippo.io/age"

	"moolah/crypt"
)

// Snapshot is one encrypted copy of a book in the sync folder.
type Snapshot struct {
	Gen     int64 // increases by one for each upload
	Machine string
	ModTime time.Time
	Parent  int64 // the Gen this snapshot was based on
	Path    string
}

var snapRe = regexp.MustCompile(`^snap-(\d{10})-(\d{10})-([a-z0-9-]+)\.age$`)

// FileName returns the file name for a snapshot.
func FileName(gen, parent int64, machine string) string {
	return fmt.Sprintf("snap-%010d-%010d-%s.age", gen, parent, machine)
}

// Folder is a sync folder.
type Folder struct {
	Dir string
}

// Snapshots lists complete snapshots, oldest first. Files still being written are ignored.
func (f Folder) Snapshots() ([]Snapshot, error) {
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		return nil, fmt.Errorf("sync folder %s: %w", f.Dir, err)
	}
	var out []Snapshot
	for _, e := range entries {
		m := snapRe.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		gen, _ := strconv.ParseInt(m[1], 10, 64)
		parent, _ := strconv.ParseInt(m[2], 10, 64)
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Snapshot{Gen: gen, Machine: m[3], ModTime: info.ModTime(), Parent: parent, Path: filepath.Join(f.Dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Gen != out[j].Gen {
			return out[i].Gen < out[j].Gen
		}
		return out[i].Machine < out[j].Machine
	})
	return out, nil
}

// Heads returns the snapshots with the highest Gen. More than one means two
// computers uploaded at the same time (a fork).
func Heads(snaps []Snapshot) []Snapshot {
	if len(snaps) == 0 {
		return nil
	}
	maxGen := snaps[len(snaps)-1].Gen
	var out []Snapshot
	for _, s := range snaps {
		if s.Gen == maxGen {
			out = append(out, s)
		}
	}
	return out
}

// Publish encrypts a database copy into the folder as a new snapshot.
func (f Folder) Publish(plainDB string, gen, parent int64, machine string, r age.Recipient) (Snapshot, error) {
	path := filepath.Join(f.Dir, FileName(gen, parent, machine))
	if _, err := os.Stat(path); err == nil {
		return Snapshot{}, fmt.Errorf("snapshot %s already exists", filepath.Base(path))
	}
	if err := crypt.EncryptFile(plainDB, path, r); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Gen: gen, Machine: machine, ModTime: time.Now(), Parent: parent, Path: path}, nil
}

// Fetch decrypts a snapshot into dst.
func (f Folder) Fetch(s Snapshot, ids []age.Identity, dst string) error {
	return crypt.DecryptFile(s.Path, dst, ids)
}

// Lock says that moolah is in use on a machine.
type Lock struct {
	Heartbeat time.Time `json:"heartbeat"`
	Machine   string    `json:"machine"`
	Since     time.Time `json:"since"`
}

// LockStale is how old a heartbeat may be before the lock is ignored.
const LockStale = 2 * time.Minute

func (f Folder) lockPath(machine string) string {
	return filepath.Join(f.Dir, "lock-"+machine+".json")
}

// WriteLock creates or refreshes this machine's lock.
func (f Folder) WriteLock(machine string, since, now time.Time) error {
	b, err := json.Marshal(Lock{Heartbeat: now, Machine: machine, Since: since})
	if err != nil {
		return err
	}
	tmp := f.lockPath(machine) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.lockPath(machine))
}

// RemoveLock removes this machine's lock.
func (f Folder) RemoveLock(machine string) error {
	err := os.Remove(f.lockPath(machine))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// OtherLocks returns fresh locks of other machines.
func (f Folder) OtherLocks(machine string, now time.Time) ([]Lock, error) {
	matches, err := filepath.Glob(filepath.Join(f.Dir, "lock-*.json"))
	if err != nil {
		return nil, err
	}
	var out []Lock
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var l Lock
		if json.Unmarshal(b, &l) != nil || l.Machine == machine || now.Sub(l.Heartbeat) > LockStale {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

// Cleanup deletes this machine's old snapshots. It keeps the newest `keep`
// snapshots in the folder, plus the newest snapshot of each day for 30 days, and
// never deletes the head.
func (f Folder) Cleanup(machine string, keep int, now time.Time) error {
	snaps, err := f.Snapshots()
	if err != nil || len(snaps) <= keep {
		return err
	}
	keepSet := map[string]bool{}
	for i := len(snaps) - 1; i >= 0 && len(snaps)-i <= keep; i-- {
		keepSet[snaps[i].Path] = true
	}
	perDay := map[string]bool{}
	for i := len(snaps) - 1; i >= 0; i-- {
		s := snaps[i]
		day := s.ModTime.Format("2006-01-02")
		if now.Sub(s.ModTime) <= 30*24*time.Hour && !perDay[day] {
			perDay[day] = true
			keepSet[s.Path] = true
		}
	}
	for _, h := range Heads(snaps) {
		keepSet[h.Path] = true
	}
	for _, s := range snaps {
		if s.Machine == machine && !keepSet[s.Path] {
			if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
