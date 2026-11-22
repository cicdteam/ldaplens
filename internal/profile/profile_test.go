// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package profile

import (
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "connections.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeFile(t, `
connections:
  - name: corp-ad
    url: ldaps://dc1.corp.example:636
    baseDN: dc=corp,dc=example
    bindDN: cn=reader,dc=corp,dc=example
    passwordEnv: CORP_PW
    readOnly: true
    color: red
    searchTimeout: 1m
  - name: local
    url: ldap://localhost:389
    startTLS: true
`)
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []Profile{
		{
			Name: "corp-ad", URL: "ldaps://dc1.corp.example:636",
			BaseDN: "dc=corp,dc=example", BindDN: "cn=reader,dc=corp,dc=example",
			PasswordEnv: "CORP_PW", ReadOnly: true, Color: "red",
			ConnectTimeout: 10 * time.Second, SearchTimeout: time.Minute,
		},
		{
			Name: "local", URL: "ldap://localhost:389", StartTLS: true,
			ConnectTimeout: 10 * time.Second, SearchTimeout: 30 * time.Second,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Load() mismatch (-want +got):\n%s", diff)
	}
	if got, err := Load(writeFile(t, "")); err != nil || len(got) != 0 {
		t.Errorf("Load(empty file) = %v, %v, want no profiles and no error", got, err)
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantInvalid bool
	}{
		{name: "bad scheme", yaml: "connections: [{name: a, url: 'http://h'}]", wantInvalid: true},
		{name: "starttls with ldaps", yaml: "connections: [{name: a, url: 'ldaps://h', startTLS: true}]", wantInvalid: true},
		{name: "no host", yaml: "connections: [{name: a, url: 'ldap:///dc=x'}]", wantInvalid: true},
		{name: "bad name", yaml: "connections: [{name: 'a b', url: 'ldap://h'}]", wantInvalid: true},
		{name: "duplicate", yaml: "connections: [{name: a, url: 'ldap://h'}, {name: a, url: 'ldap://g'}]", wantInvalid: true},
		{name: "url credentials", yaml: "connections: [{name: a, url: 'ldap://cn=admin:s3cret@h:389'}]", wantInvalid: true},
		{name: "unknown field", yaml: "connections: [{name: a, url: 'ldap://h', pasword: x}]", wantInvalid: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tc.yaml))
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if got := errors.Is(err, ErrInvalid); got != tc.wantInvalid {
				t.Errorf("errors.Is(%v, ErrInvalid) = %v, want %v", err, got, tc.wantInvalid)
			}
		})
	}
}

func TestLoadErrorHidesURLPassword(t *testing.T) {
	_, err := Load(writeFile(t, "connections: [{name: a, url: 'ldap://cn=admin:s3cret@h:badport'}]"))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("Load() error = %v, want an error without the password", err)
	}
}

func TestShared(t *testing.T) {
	const dn = "cn=svc,dc=example,dc=org"
	tests := []struct {
		name string
		p    Profile
		want bool
	}{
		{name: "passwordEnv", p: Profile{BindDN: dn, PasswordEnv: "PW"}, want: true},
		{name: "passwordFile", p: Profile{BindDN: dn, PasswordFile: "/pw"}, want: true},
		{name: "anonymous", p: Profile{}, want: true},
		{name: "bindDN without a configured password", p: Profile{BindDN: dn}, want: false},
		{name: "bindDNTemplate", p: Profile{BindDNTemplate: "uid={user},dc=example,dc=org"}, want: false},
	}
	for _, tc := range tests {
		if got := tc.p.Shared(); got != tc.want {
			t.Errorf("%s: Shared() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestConfiguredPassword(t *testing.T) {
	t.Setenv("LDAPLENS_TEST_PW", "from-env")
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		p       Profile
		want    string
		wantOK  bool
		wantErr bool
	}{
		{name: "env", p: Profile{PasswordEnv: "LDAPLENS_TEST_PW"}, want: "from-env", wantOK: true}, //nolint:gosec // env var name, not a credential
		{name: "file trims newline", p: Profile{PasswordFile: pwFile}, want: "from-file", wantOK: true},
		{name: "none", p: Profile{}, want: "", wantOK: false},
		{name: "env unset", p: Profile{PasswordEnv: "LDAPLENS_TEST_UNSET"}, wantErr: true}, //nolint:gosec // env var name, not a credential
	}
	for _, tc := range tests {
		got, ok, err := tc.p.ConfiguredPassword()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: ConfiguredPassword() error = %v, wantErr %v", tc.name, err, tc.wantErr)
			continue
		}
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: ConfiguredPassword() = %q, %v, want %q, %v", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestTLSConfig(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))

	cfg, err := Profile{Name: "x", URL: "ldaps://dc1.example:636", CAPEM: caPEM}.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if cfg.ServerName != "dc1.example" {
		t.Errorf("TLSConfig().ServerName = %q, want %q", cfg.ServerName, "dc1.example")
	}
	if cfg.RootCAs == nil {
		t.Error("TLSConfig().RootCAs = nil, want pool with the custom CA")
	}

	if _, err := (Profile{Name: "x", URL: "ldaps://h", CAPEM: "garbage"}).TLSConfig(); err == nil {
		t.Error("TLSConfig() with garbage CA error = nil, want error")
	}
}

func TestNormalizeTemplate(t *testing.T) {
	tests := []struct {
		name        string
		p           Profile
		wantInvalid bool
	}{
		{name: "dn", p: Profile{BindDNTemplate: "uid={user},ou=people,dc=example,dc=org"}},
		{name: "upn", p: Profile{BindDNTemplate: "{user}@corp.example"}},
		{name: "down-level", p: Profile{BindDNTemplate: `CORP\{user}`}},
		{name: "no placeholder", p: Profile{BindDNTemplate: "uid=x,dc=example,dc=org"}, wantInvalid: true},
		{name: "two placeholders", p: Profile{BindDNTemplate: "uid={user},ou={user},dc=org"}, wantInvalid: true},
		{name: "with bindDN", p: Profile{BindDNTemplate: "{user}@corp.example", BindDN: "cn=reader"}, wantInvalid: true},
		{name: "with passwordEnv", p: Profile{BindDNTemplate: "{user}@corp.example", PasswordEnv: "PW"}, wantInvalid: true},
		{name: "with passwordFile", p: Profile{BindDNTemplate: "{user}@corp.example", PasswordFile: "/pw"}, wantInvalid: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			p.Name, p.URL = "x", "ldap://h"
			err := p.normalize()
			if tc.wantInvalid && !errors.Is(err, ErrInvalid) {
				t.Errorf("normalize(%+v) = %v, want ErrInvalid", tc.p, err)
			}
			if !tc.wantInvalid && err != nil {
				t.Errorf("normalize(%+v) = %v, want nil", tc.p, err)
			}
		})
	}
}

func TestNormalizeLabel(t *testing.T) {
	tests := []struct {
		name        string
		label       string
		want        string
		wantInvalid bool
	}{
		{name: "none", label: "", want: ""},
		{name: "spaces and parentheses", label: "CICD Team (fictional organization catalog)", want: "CICD Team (fictional organization catalog)"},
		{name: "trimmed", label: "  Corp AD \t", want: "Corp AD"},
		{name: "only spaces", label: "   ", want: ""},
		{name: "non-ASCII", label: "Ádám's Директория", want: "Ádám's Директория"},
		{name: "80 characters", label: strings.Repeat("я", 80), want: strings.Repeat("я", 80)},
		{name: "81 characters", label: strings.Repeat("я", 81), wantInvalid: true},
		{name: "newline", label: "a\nb", wantInvalid: true},
		{name: "control character", label: "a\x1bb", wantInvalid: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Profile{Name: "x", URL: "ldap://h", Label: tc.label}
			err := p.normalize()
			if tc.wantInvalid {
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("normalize(label %q) = %v, want ErrInvalid", tc.label, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize(label %q) = %v, want nil", tc.label, err)
			}
			if p.Label != tc.want {
				t.Errorf("normalize(label %q): Label = %q, want %q", tc.label, p.Label, tc.want)
			}
		})
	}
}

func TestBindDNFor(t *testing.T) {
	for _, tc := range []struct {
		template, username, want string
	}{
		{template: "uid={user},ou=people,dc=example,dc=org", username: "user001", want: "uid=user001,ou=people,dc=example,dc=org"},
		{template: "{user}@corp.example", username: "user001", want: "user001@corp.example"},
		{template: "{user}@corp.example", username: strings.Repeat("a", 64), want: strings.Repeat("a", 64) + "@corp.example"},
	} {
		got, err := Profile{BindDNTemplate: tc.template}.BindDNFor(tc.username)
		if err != nil || got != tc.want {
			t.Errorf("BindDNFor(%q) with %q = %q, %v, want %q", tc.username, tc.template, got, err, tc.want)
		}
	}

	p := Profile{BindDNTemplate: "uid={user},ou=people,dc=example,dc=org"}
	for _, username := range []string{"", "a,ou=admins", "a+cn=x", " a", "#a", `a\`, "*", "a=b", strings.Repeat("a", 65), "user001\n"} {
		_, err := p.BindDNFor(username)
		if !errors.Is(err, ErrInvalidUsername) {
			t.Errorf("BindDNFor(%q) error = %v, want ErrInvalidUsername", username, err)
			continue
		}
		if username != "" && strings.Contains(err.Error(), username) {
			t.Errorf("BindDNFor(%q) error = %q, want it without the username", username, err)
		}
	}

	if _, err := (Profile{}).BindDNFor("user001"); err == nil {
		t.Error("BindDNFor() without a template error = nil, want error")
	}
}

func TestLoadTemplateFromYAML(t *testing.T) {
	ps, err := Load(writeFile(t, `
connections:
  - name: people
    url: ldap://h
    bindDNTemplate: uid={user},ou=people,dc=example,dc=org
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := ps[0]; got.BindDNTemplate != "uid={user},ou=people,dc=example,dc=org" || !got.UserLogin() {
		t.Errorf("Load() = %+v, want bindDNTemplate set", got)
	}
}
