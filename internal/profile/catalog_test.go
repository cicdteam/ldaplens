// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestCatalogSaveAndReload checks that a saved profile comes back from disk
// with every field the saved JSON carries.
func TestCatalogSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCatalog(nil, dir)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	saved := Profile{
		Name: "lab", URL: "ldap://lab:389", StartTLS: true, BaseDN: "dc=lab,dc=example", BindDN: "cn=admin,dc=lab,dc=example",
		CAPEM: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n", InsecureSkipVerify: true, Color: "red", ReadOnly: true,
	}
	if _, err := c.Save(saved); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reloaded, err := NewCatalog(nil, dir)
	if err != nil {
		t.Fatalf("NewCatalog() reload error = %v", err)
	}
	got, err := reloaded.Find("lab")
	if err != nil {
		t.Fatalf("Find(lab) error = %v", err)
	}
	// The timeouts come from the config file only, so a saved profile has
	// the defaults.
	want := saved
	want.ConnectTimeout, want.SearchTimeout = defaultConnectTimeout, defaultSearchTimeout
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Find(lab) after a reload mismatch (-want +got):\n%s", diff)
	}
}

func TestCatalogSaveStripsFileReferences(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCatalog(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Save(Profile{
		Name: "x", URL: "ldap://h", PasswordFile: "/etc/passwd", PasswordEnv: "HOME", CAFile: "/etc/hosts",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if got.PasswordFile != "" || got.PasswordEnv != "" || got.CAFile != "" {
		t.Errorf("Save() = %+v, want file and env fields cleared", got)
	}
}

// A saved profile can ask each time for a username: the template points at
// no local file or variable.
func TestCatalogSaveKeepsBindDNTemplate(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCatalog(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	const tmpl = "uid={user},ou=people,dc=example,dc=org"
	if _, err := c.Save(Profile{Name: "x", URL: "ldap://h", BindDNTemplate: tmpl}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := c.Save(Profile{Name: "y", URL: "ldap://h", BindDNTemplate: "uid=jdoe,dc=example,dc=org"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Save(template without {user}) error = %v, want ErrInvalid", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewCatalog(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Find("x")
	if err != nil {
		t.Fatalf("Find(x) after a reload error = %v", err)
	}
	if got.BindDNTemplate != tmpl || !got.UserLogin() {
		t.Errorf("Find(x).BindDNTemplate = %q, want %q", got.BindDNTemplate, tmpl)
	}
}

func TestCatalogSaveErrors(t *testing.T) {
	fixed := []Profile{{Name: "corp", URL: "ldap://corp"}}
	withDir, err := NewCatalog(fixed, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	noDir, err := NewCatalog(fixed, "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		c    *Catalog
		p    Profile
		want error
	}{
		{name: "fixed name", c: withDir, p: Profile{Name: "corp", URL: "ldap://other"}, want: ErrFixed},
		{name: "no data dir", c: noDir, p: Profile{Name: "x", URL: "ldap://h"}, want: ErrSavingDisabled},
		{name: "invalid", c: withDir, p: Profile{Name: "x", URL: "http://h"}, want: ErrInvalid},
		{name: "url credentials", c: withDir, p: Profile{Name: "x", URL: "ldap://cn=admin:s3cret@h:389"}, want: ErrInvalid},
	}
	for _, tc := range tests {
		if _, err := tc.c.Save(tc.p); !errors.Is(err, tc.want) {
			t.Errorf("%s: Save() error = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestCatalogFixedShadowsSaved(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(`[{"name":"corp","url":"ldap://saved"},{"name":"lab","url":"ldap://lab"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewCatalog([]Profile{{Name: "corp", URL: "ldap://corp"}}, dir)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	fixed, saved := c.All()
	names := func(ps []Profile) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name+"="+p.URL)
		}
		return out
	}
	if diff := cmp.Diff([]string{"corp=ldap://corp"}, names(fixed)); diff != "" {
		t.Errorf("All() fixed mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"lab=ldap://lab"}, names(saved)); diff != "" {
		t.Errorf("All() saved mismatch (-want +got):\n%s", diff)
	}
}

func TestCatalogFind(t *testing.T) {
	c, err := NewCatalog([]Profile{{Name: "corp", URL: "ldap://corp"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if p, err := c.Find("corp"); err != nil || p.ConnectTimeout != defaultConnectTimeout {
		t.Errorf("Find(corp) = %+v, %v, want normalized profile", p, err)
	}
	if _, err := c.Find("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Find(nope) error = %v, want ErrNotFound", err)
	}
}

// newCatalog returns a catalog on dir that is closed when the test ends.
func newCatalog(t *testing.T, dir string) *Catalog {
	t.Helper()
	c, err := NewCatalog(nil, dir)
	if err != nil {
		t.Fatalf("NewCatalog(nil, %q) error = %v", dir, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func writePath(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readProfiles decodes dir/profiles.json field by field, so that a field
// the code no longer writes shows up in a diff.
func readProfiles(t *testing.T, dir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "profiles.json")) //nolint:gosec // dir is a test temp dir
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode profiles.json %q: %v", b, err)
	}
	return out
}

func TestSaveLimit(t *testing.T) {
	dir := t.TempDir()
	full := make([]Profile, MaxSaved)
	for i := range full {
		full[i] = Profile{Name: fmt.Sprintf("p%d", i), URL: "ldap://h"}
	}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	writePath(t, filepath.Join(dir, "profiles.json"), string(b))
	c := newCatalog(t, dir)

	if _, err := c.Save(Profile{Name: "one-more", URL: "ldap://h"}); !errors.Is(err, ErrLimit) {
		t.Errorf("Save(one-more) at %d profiles error = %v, want ErrLimit", MaxSaved, err)
	}
	if _, err := c.Save(Profile{Name: "p0", URL: "ldap://other"}); err != nil {
		t.Errorf("Save(p0) replacing at %d profiles error = %v, want nil", MaxSaved, err)
	}
	if _, saved := c.All(); len(saved) != MaxSaved {
		t.Errorf("All() saved %d profiles, want %d", len(saved), MaxSaved)
	}
}

func TestSaveProfileTooLarge(t *testing.T) {
	long := "dc=" + strings.Repeat("a", MaxProfileBytes)
	tests := []struct {
		name string
		p    Profile
		want error
	}{
		{name: "10 KiB caPEM", p: Profile{CAPEM: strings.Repeat("a", 10<<10)}},
		{name: "17 KiB caPEM", p: Profile{CAPEM: strings.Repeat("a", 17<<10)}, want: ErrLimit},
		{name: "long baseDN", p: Profile{BaseDN: long}, want: ErrLimit},
		{name: "long baseDN and small caPEM", p: Profile{BaseDN: long, CAPEM: "pem"}, want: ErrLimit},
	}
	c := newCatalog(t, t.TempDir())
	for _, tc := range tests {
		tc.p.Name, tc.p.URL = "big", "ldap://h"
		if _, err := c.Save(tc.p); !errors.Is(err, tc.want) {
			t.Errorf("%s: Save() error = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestNewCatalogQuarantinesBadFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		invalid bool // the reason is ErrInvalid, not a JSON error
	}{
		{name: "empty", content: ""},
		{name: "truncated", content: `[{"name":"lab","url":"ldap://lab"`},
		{name: "trailing garbage", content: `[{"name":"lab","url":"ldap://lab"}] x`},
		{name: "invalid profile", content: `[{"name":"lab","url":"http://x"}]`, invalid: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bad := filepath.Join(dir, "profiles.json.bad")
			writePath(t, bad, "older bad file")
			writePath(t, filepath.Join(dir, "profiles.json"), tc.content)

			c := newCatalog(t, dir)
			if _, saved := c.All(); len(saved) != 0 {
				t.Errorf("All() saved = %v, want none", saved)
			}
			moved, reason := c.Quarantined()
			if moved != bad {
				t.Errorf("Quarantined() path = %q, want %q", moved, bad)
			}
			if reason == nil || errors.Is(reason, ErrInvalid) != tc.invalid {
				t.Errorf("Quarantined() reason = %v, want an error, ErrInvalid: %v", reason, tc.invalid)
			}
			if got, err := os.ReadFile(bad); err != nil || string(got) != tc.content { //nolint:gosec // bad is in a test temp dir
				t.Errorf("profiles.json.bad = %q, %v, want the old bytes %q", got, err, tc.content)
			}

			if _, err := c.Save(Profile{Name: "fresh", URL: "ldap://h"}); err != nil {
				t.Fatalf("Save(fresh) error = %v", err)
			}
			got := readProfiles(t, dir)
			if len(got) != 1 || got[0]["name"] != "fresh" {
				t.Errorf("profiles.json after Save = %v, want only fresh", got)
			}
		})
	}
}

func TestNewCatalogKeepsUnreadableFileError(t *testing.T) {
	dir := t.TempDir()
	// A directory in place of the file cannot be read: that is an operator
	// problem to report, not a broken file to move aside.
	if err := os.Mkdir(filepath.Join(dir, "profiles.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if c, err := NewCatalog(nil, dir); err == nil {
		_ = c.Close()
		t.Errorf("NewCatalog() with unreadable profiles.json error = nil, want an error")
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	c := newCatalog(t, dir)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if _, err := c.Save(Profile{Name: fmt.Sprintf("p%d", i), URL: "ldap://h"}); err != nil {
				t.Errorf("Save(p%d) error = %v", i, err)
			}
		})
	}
	wg.Wait()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "profiles.json" && e.Name() != ".lock" {
			t.Errorf("data dir holds %s, want only profiles.json and .lock", e.Name())
		}
	}
	if got := readProfiles(t, dir); len(got) != 20 {
		t.Errorf("profiles.json holds %d profiles, want 20", len(got))
	}
}
