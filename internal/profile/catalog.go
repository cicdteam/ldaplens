// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Limits on the profiles saved from the UI.
const (
	MaxSaved        = 200      // saved profiles
	MaxProfileBytes = 4 << 10  // one profile as JSON, without caPEM
	MaxCAPEMBytes   = 16 << 10 // one profile's caPEM
)

// Errors returned by Catalog.
var (
	ErrNotFound       = errors.New("profile not found")
	ErrFixed          = errors.New("profile is defined in the config file and cannot be changed here")
	ErrSavingDisabled = errors.New("saving profiles needs --data-dir")
	ErrLimit          = errors.New("profile limit reached")

	// errNotWritable is returned by lockDir for a data dir it cannot write.
	errNotWritable = errors.New("data dir is not writable")
)

// Catalog combines profiles from the config file ("fixed") with profiles
// saved from the UI. It is safe for concurrent use.
type Catalog struct {
	fixed       []Profile
	path        string   // JSON file for saved profiles; empty disables saving
	lock        *os.File // holds the data dir lock; nil without one
	unlocked    bool
	quarantined string
	quarantine  error // why quarantined was moved aside

	mu    sync.Mutex
	saved []Profile
}

// NewCatalog returns a catalog of fixed plus the profiles previously saved
// in dataDir/profiles.json. An empty dataDir disables saving. Config-file
// profiles shadow saved ones with the same name: such saved entries are
// dropped, and the next Save removes them from disk.
//
// A profiles.json that does not parse or holds an invalid profile is moved
// to profiles.json.bad, and the catalog starts with no saved profiles; see
// Quarantined. The catalog locks dataDir against other processes until
// Close; a dataDir it cannot write is used without the lock, see Unlocked.
func NewCatalog(fixed []Profile, dataDir string) (*Catalog, error) {
	fixed = slices.Clone(fixed)
	if err := normalizeAll(fixed); err != nil {
		return nil, err
	}
	c := &Catalog{fixed: fixed}
	if dataDir == "" {
		return c, nil
	}
	lock, err := lockDir(dataDir)
	switch {
	case errors.Is(err, errNotWritable):
		// The saved profiles can still be read; Save fails.
		c.unlocked = true
	case err != nil:
		return nil, err
	}
	c.lock = lock
	c.path = filepath.Join(dataDir, "profiles.json")
	if err := c.load(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// load reads the saved profiles, moving a broken file aside: one bad file
// must not keep the server from starting.
func (c *Catalog) load() error {
	b, err := os.ReadFile(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.saved, err = parseSaved(b, c.fixed); err == nil {
		return nil
	}
	bad := c.path + ".bad"
	if err := os.Rename(c.path, bad); err != nil {
		return err
	}
	c.quarantined, c.quarantine = bad, err
	return nil
}

func parseSaved(b []byte, fixed []Profile) ([]Profile, error) {
	var saved []Profile
	if err := json.Unmarshal(b, &saved); err != nil {
		return nil, err
	}
	saved = slices.DeleteFunc(saved, func(s Profile) bool {
		return slices.ContainsFunc(fixed, func(f Profile) bool { return f.Name == s.Name })
	})
	if err := normalizeAll(saved); err != nil {
		return nil, err
	}
	return saved, nil
}

// Quarantined returns the path that a broken profiles.json was moved to
// when the catalog was created and why: the JSON or validation error, which
// holds no secrets. It is "", nil if there was none.
func (c *Catalog) Quarantined() (path string, reason error) {
	return c.quarantined, c.quarantine
}

// Unlocked reports whether the data dir was not writable when the catalog
// was created, so it runs without the lock and Save fails.
func (c *Catalog) Unlocked() bool {
	return c.unlocked
}

// Close releases the lock on the data dir. Call it once, when the catalog
// is no longer used.
func (c *Catalog) Close() error {
	if c.lock == nil {
		return nil
	}
	return c.lock.Close()
}

// All returns copies of the fixed and the saved profiles.
func (c *Catalog) All() (fixed, saved []Profile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.fixed), slices.Clone(c.saved)
}

// Find returns the profile called name.
func (c *Catalog) Find(name string) (Profile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range slices.Concat(c.fixed, c.saved) {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

// Save validates p, creates or replaces the saved profile with the same
// name, and writes all saved profiles to disk. Saved profiles never refer
// to local files or environment variables. It returns ErrLimit for a new
// profile when MaxSaved are saved, for a caPEM over MaxCAPEMBytes, or for a
// profile over MaxProfileBytes without its caPEM.
func (c *Catalog) Save(p Profile) (Profile, error) {
	p.PasswordEnv, p.PasswordFile, p.CAFile = "", "", ""
	if err := p.normalize(); err != nil {
		return Profile{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		return Profile{}, ErrSavingDisabled
	}
	if slices.ContainsFunc(c.fixed, func(f Profile) bool { return f.Name == p.Name }) {
		return Profile{}, fmt.Errorf("%w: %s", ErrFixed, p.Name)
	}
	if len(p.CAPEM) > MaxCAPEMBytes {
		return Profile{}, fmt.Errorf("%w: %s: caPEM is over %d bytes", ErrLimit, p.Name, MaxCAPEMBytes)
	}
	rest := p
	rest.CAPEM = ""
	b, err := json.Marshal(rest)
	if err != nil {
		return Profile{}, err
	}
	if len(b) > MaxProfileBytes {
		return Profile{}, fmt.Errorf("%w: %s is over %d bytes without caPEM", ErrLimit, p.Name, MaxProfileBytes)
	}
	next := slices.Clone(c.saved)
	switch i := slices.IndexFunc(next, func(s Profile) bool { return s.Name == p.Name }); {
	case i >= 0:
		next[i] = p
	case len(next) >= MaxSaved:
		return Profile{}, fmt.Errorf("%w: at most %d saved profiles", ErrLimit, MaxSaved)
	default:
		next = append(next, p)
	}
	if err := writeJSONAtomic(c.path, next); err != nil {
		return Profile{}, err
	}
	c.saved = next
	return p, nil
}

// writeJSONAtomic replaces path with v as JSON so that a crash leaves
// either the old or the new file, never a partial one.
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "profiles-*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return syncDir(dir)
}
