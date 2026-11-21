// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/cicdteam/ldaplens/internal/profile"
)

func TestParseArgs(t *testing.T) {
	env := map[string]string{
		"LDAPLENS_LISTEN":        "0.0.0.0:9000",
		"LDAPLENS_ALLOWED_HOSTS": "ldap.example.com, ui.local,",
		"LDAPLENS_DATA_DIR":      "/data",
	}
	getenv := func(k string) string { return env[k] }

	got, err := parseArgs([]string{"--base-path", "/ldap/"}, getenv, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	want := config{
		listen: "0.0.0.0:9000", basePath: "/ldap", dataDir: "/data",
		allowedHosts: []string{"ldap.example.com", "ui.local"}, sessionIdle: 30 * time.Minute,
		sessionMaxAge: 8 * time.Hour,
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(config{})); diff != "" {
		t.Errorf("parseArgs() mismatch (-want +got):\n%s", diff)
	}

	got, err = parseArgs([]string{"--listen", "127.0.0.1:1"}, getenv, io.Discard)
	if err != nil || got.listen != "127.0.0.1:1" {
		t.Errorf("parseArgs(--listen) = %q, %v, want the flag to win over env", got.listen, err)
	}

	for _, bad := range []string{"ldap", "/a b", "/ldap/<x>", "/.", "/..", "/a/..", "/a/./b", "/a/../"} {
		if _, err := parseArgs([]string{"--base-path", bad}, getenv, io.Discard); err == nil {
			t.Errorf("parseArgs(--base-path %q) error = nil, want error", bad)
		}
	}
}

func TestParseArgsSecureCookies(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     string
		want    bool
		wantErr bool
	}{
		{name: "default", want: false},
		{name: "env", env: "true", want: true},
		{name: "flag", args: []string{"--secure-cookies"}, want: true},
		{name: "flag wins over env", args: []string{"--secure-cookies=false"}, env: "1", want: false},
		{name: "invalid env", env: "maybe", wantErr: true},
	}
	for _, tc := range tests {
		getenv := func(k string) string {
			if k == "LDAPLENS_SECURE_COOKIES" {
				return tc.env
			}
			return ""
		}
		got, err := parseArgs(tc.args, getenv, io.Discard)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: parseArgs(%q) error = nil, want error", tc.name, tc.args)
			}
			continue
		}
		if err != nil || got.secureCookies != tc.want {
			t.Errorf("%s: parseArgs(%q) secureCookies = %v, %v, want %v, nil", tc.name, tc.args, got.secureCookies, err, tc.want)
		}
	}
}

func TestParseArgsNotice(t *testing.T) {
	// Multibyte runes show that the limit counts characters, not bytes.
	full := strings.Repeat("é", 300)
	tests := []struct {
		name    string
		args    []string
		env     string
		want    string
		wantErr bool
	}{
		{name: "default", want: ""},
		{name: "flag", args: []string{"--notice", "Public demo"}, want: "Public demo"},
		{name: "env", env: "Public demo", want: "Public demo"},
		{name: "flag wins over env", args: []string{"--notice", "from flag"}, env: "from env", want: "from flag"},
		{name: "trimmed", args: []string{"--notice", " \t Public demo \n "}, want: "Public demo"},
		{name: "only spaces", env: "   ", want: ""},
		{name: "300 characters", args: []string{"--notice", " " + full + " "}, want: full},
		{name: "301 characters", env: full + "é", wantErr: true},
	}
	for _, tc := range tests {
		getenv := func(k string) string {
			if k == "LDAPLENS_NOTICE" {
				return tc.env
			}
			return ""
		}
		got, err := parseArgs(tc.args, getenv, io.Discard)
		if tc.wantErr {
			if err == nil || !strings.Contains(err.Error(), "--notice") || !strings.Contains(err.Error(), "300") {
				t.Errorf("%s: parseArgs(%q) error = %v, want one naming --notice and 300", tc.name, tc.args, err)
			}
			continue
		}
		if err != nil || got.notice != tc.want {
			t.Errorf("%s: parseArgs(%q) notice = %q, %v, want %q, nil", tc.name, tc.args, got.notice, err, tc.want)
		}
	}
}

func TestParseArgsNoticeColor(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     string
		want    string
		wantErr bool
	}{
		{name: "default", want: ""},
		{name: "six digits", args: []string{"--notice-color", "#f59e0b"}, want: "#f59e0b"},
		{name: "three digits, from env", env: "#F90", want: "#F90"},
		{name: "trimmed", args: []string{"--notice-color", " #f59e0b "}, want: "#f59e0b"},
		{name: "no hash", args: []string{"--notice-color", "f59e0b"}, wantErr: true},
		{name: "a name", args: []string{"--notice-color", "orange"}, wantErr: true},
		{name: "four digits", args: []string{"--notice-color", "#f59e"}, wantErr: true},
		{name: "CSS after it", env: "#fff;background:url(x)", wantErr: true},
	}
	for _, tc := range tests {
		getenv := func(k string) string {
			if k == "LDAPLENS_NOTICE_COLOR" {
				return tc.env
			}
			return ""
		}
		got, err := parseArgs(tc.args, getenv, io.Discard)
		if tc.wantErr {
			if err == nil || !strings.Contains(err.Error(), "--notice-color") {
				t.Errorf("%s: parseArgs(%q) error = %v, want one naming --notice-color", tc.name, tc.args, err)
			}
			continue
		}
		if err != nil || got.noticeColor != tc.want {
			t.Errorf("%s: parseArgs(%q) noticeColor = %q, %v, want %q, nil", tc.name, tc.args, got.noticeColor, err, tc.want)
		}
	}
}

func TestParseArgsSessionIdle(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", want: 30 * time.Minute},
		{name: "env", env: "5m", want: 5 * time.Minute},
		{name: "flag", args: []string{"--session-idle", "5m"}, want: 5 * time.Minute},
		{name: "flag wins over env", args: []string{"--session-idle", "1m"}, env: "5m", want: time.Minute},
		{name: "zero flag", args: []string{"--session-idle", "0"}, wantErr: true},
		{name: "negative flag", args: []string{"--session-idle", "-1m"}, wantErr: true},
		{name: "invalid env", env: "soon", wantErr: true},
		{name: "invalid variable with the flag set", args: []string{"--session-idle", "1m"}, env: "soon", wantErr: true},
	}
	for _, tc := range tests {
		getenv := func(k string) string {
			if k == "LDAPLENS_SESSION_IDLE" {
				return tc.env
			}
			return ""
		}
		got, err := parseArgs(tc.args, getenv, io.Discard)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: parseArgs(%q) error = nil, want error", tc.name, tc.args)
			}
			continue
		}
		if err != nil || got.sessionIdle != tc.want {
			t.Errorf("%s: parseArgs(%q) sessionIdle = %v, %v, want %v, nil", tc.name, tc.args, got.sessionIdle, err, tc.want)
		}
	}
}

// clearEnv blanks every LDAPLENS_* variable for the rest of t, so a test of
// run does not read the developer's environment; empty counts as unset.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "LDAPLENS_") {
			t.Setenv(k, "")
		}
	}
}

// TestRunHelp checks that -h succeeds after printing usage, as it would
// with flag.ExitOnError, instead of failing with flag.ErrHelp.
func TestRunHelp(t *testing.T) {
	clearEnv(t)
	for _, args := range [][]string{{"-h"}, {"local", "-h"}, {"server", "-h"}} {
		if err := run(args); err != nil {
			t.Errorf("run(%q) = %v, want nil", args, err)
		}
	}
}

// TestRunVersion checks that --version prints the version and stops in every
// mode, even when the LDAPLENS_* environment would fail validation.
func TestRunVersion(t *testing.T) {
	tests := []struct {
		name, key, value string
	}{
		{name: "clean env"},
		{name: "bad listen", key: "LDAPLENS_LISTEN", value: "bad"},
		{name: "bad secure cookies", key: "LDAPLENS_SECURE_COOKIES", value: "maybe"},
		{name: "bad base path", key: "LDAPLENS_BASE_PATH", value: "ldap"},
		{name: "short auth token", key: "LDAPLENS_AUTH_TOKEN", value: "short"},
		{name: "missing auth token file", key: "LDAPLENS_AUTH_TOKEN_FILE", value: "/nonexistent/token"},
		{name: "bad session idle", key: "LDAPLENS_SESSION_IDLE", value: "soon"},
		{name: "bad session max age", key: "LDAPLENS_SESSION_MAX_AGE", value: "soon"},
		{name: "data dir", key: "LDAPLENS_DATA_DIR", value: "/nonexistent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			for _, args := range [][]string{{"--version"}, {"local", "--version"}, {"server", "--version"}} {
				var out bytes.Buffer
				if err := runTo(args, &out); err != nil {
					t.Errorf("run(%q) with %s=%q = %v, want nil", args, tc.key, tc.value, err)
					continue
				}
				if got, want := out.String(), "ldaplens dev\n"; got != want {
					t.Errorf("run(%q) with %s=%q printed %q, want %q", args, tc.key, tc.value, got, want)
				}
			}
		})
	}
}

func TestParseArgsMode(t *testing.T) {
	noEnv := func(string) string { return "" }
	tests := []struct {
		args    []string
		want    mode
		wantErr string
	}{
		{args: nil, want: modeLocal},
		{args: []string{"local"}, want: modeLocal},
		{args: []string{"server", "--config", "c.yaml"}, want: modeServer},
		{args: []string{"--listen", "x:1"}, want: modeLocal},
		{args: []string{"serve"}, wantErr: `unknown command "serve": want local or server`},
		// A mode after the flags would otherwise be ignored.
		{args: []string{"--listen", "x:1", "server"}, wantErr: `unexpected argument "server": the mode goes first`},
	}
	for _, tc := range tests {
		got, err := parseArgs(tc.args, noEnv, io.Discard)
		if tc.wantErr != "" {
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("parseArgs(%q) error = %v, want %q", tc.args, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got.mode != tc.want {
			t.Errorf("parseArgs(%q) mode = %v, %v, want %v, nil", tc.args, got.mode, err, tc.want)
		}
	}
}

func TestServerPreset(t *testing.T) {
	const noData = "server mode keeps no data; --data-dir is for local mode"
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string // empty wants no error
	}{
		{name: "no config", args: []string{"server"}, wantErr: "server mode needs --config"},
		{name: "config from env", args: []string{"server"}, env: map[string]string{"LDAPLENS_CONFIG": "c.yaml"}},
		{name: "data dir flag", args: []string{"server", "--config", "c.yaml", "--data-dir", "/d"}, wantErr: noData},
		{name: "data dir env", args: []string{"server", "--config", "c.yaml"}, env: map[string]string{"LDAPLENS_DATA_DIR": "/d"}, wantErr: noData},
		{name: "data dir in local mode", args: []string{"local", "--data-dir", "/d"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			_, err := parseArgs(tc.args, getenv, io.Discard)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("parseArgs(%q) error = %v, want nil", tc.args, err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("parseArgs(%q) error = %v, want %q", tc.args, err, tc.wantErr)
			}
		})
	}
}

func TestCheckServer(t *testing.T) {
	const token = "abcXYZ0129._~-ok" //nolint:gosec // test value, not a credential
	configured := profile.Profile{Name: "corp", URL: "ldap://ldap.example.com", BindDN: "cn=reader,dc=example,dc=com", PasswordEnv: "CORP_PW"}
	anonymous := profile.Profile{Name: "anon", URL: "ldap://ldap.example.com"}
	template := profile.Profile{Name: "people", URL: "ldap://ldap.example.com", BindDNTemplate: "uid={user},dc=example,dc=com"}
	typed := profile.Profile{Name: "admin", URL: "ldap://ldap.example.com", BindDN: "cn=admin,dc=example,dc=com"}
	const tail = " binds with a configured password or anonymously; server mode needs --auth-token or --auth-token-file"
	tests := []struct {
		name     string
		token    string
		profiles []profile.Profile
		wantErr  string // empty wants nil
	}{
		{name: "configured password without token", profiles: []profile.Profile{template, configured}, wantErr: "profile corp" + tail},
		{name: "anonymous without token", profiles: []profile.Profile{anonymous}, wantErr: "profile anon" + tail},
		{name: "configured password with token", token: token, profiles: []profile.Profile{configured}},
		{name: "template only", profiles: []profile.Profile{template}},
		{name: "typed bindDN only", profiles: []profile.Profile{typed}},
		{name: "template and typed bindDN", profiles: []profile.Profile{template, typed}},
	}
	for _, tc := range tests {
		err := checkServer(config{mode: modeServer, authToken: tc.token}, tc.profiles)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: checkServer() = %v, want nil", tc.name, err)
			}
			continue
		}
		if err == nil || err.Error() != tc.wantErr {
			t.Errorf("%s: checkServer() = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}

// TestRunPresets checks the presets runTo applies before it listens. The
// port is taken, so that runTo returns instead of serving: server mode
// refuses an anonymous profile without a token, and local mode keeps a
// token in its data dir.
func TestRunPresets(t *testing.T) {
	clearEnv(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer taken.Close()
	addr := taken.Addr().String()

	config := filepath.Join(t.TempDir(), "connections.yaml")
	if err := os.WriteFile(config, []byte("connections:\n  - name: anon\n    url: ldap://ldap.example.com\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", config, err)
	}
	args := []string{"server", "--config", config, "--listen", addr}
	want := "profile anon binds with a configured password or anonymously; server mode needs --auth-token or --auth-token-file"
	if err := runTo(args, io.Discard); err == nil || err.Error() != want {
		t.Errorf("runTo(%q) = %v, want %q", args, err, want)
	}

	dataDir := t.TempDir()
	args = []string{"local", "--data-dir", dataDir, "--listen", addr}
	if err := runTo(args, io.Discard); err == nil {
		t.Errorf("runTo(%q) = nil, want the error of listening on a taken port", args)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "token")); err != nil {
		t.Errorf("runTo(%q) kept no token: %v", args, err)
	}
}

func TestLocalToken(t *testing.T) {
	dir := t.TempDir()
	first, err := localToken(dir)
	if err != nil {
		t.Fatalf("localToken() error = %v", err)
	}
	if !authTokenPattern.MatchString(first) {
		t.Errorf("localToken() = %q, want a token matching %v", first, authTokenPattern)
	}
	path := filepath.Join(dir, "token")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("localToken() created %s with mode %v, want 0600", path, got)
	}
	if second, err := localToken(dir); err != nil || second != first {
		t.Errorf("localToken() second call = %q, %v, want %q, nil", second, err, first)
	}

	bad := t.TempDir()
	path = filepath.Join(bad, "token")
	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
	if _, err := localToken(bad); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("localToken() with a bad token file error = %v, want an error naming %s", err, path)
	}
}

func TestLocalDefaultDataDir(t *testing.T) {
	tmp := t.TempDir()
	got, err := defaultDataDir(func() (string, error) { return tmp, nil })
	want := filepath.Join(tmp, "ldaplens")
	if err != nil || got != want {
		t.Fatalf("defaultDataDir() = %q, %v, want %q, nil", got, err, want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", want, err)
	}
	if perm := info.Mode().Perm(); !info.IsDir() || perm != 0o700 {
		t.Errorf("defaultDataDir() made %s with mode %v, want a directory with 0700", want, info.Mode())
	}

	noDir := func() (string, error) { return "", errors.New("$HOME is not defined") }
	if got, err := defaultDataDir(noDir); got != "" || err != nil {
		t.Errorf("defaultDataDir() without a config dir = %q, %v, want \"\", nil", got, err)
	}
}

func TestParseArgsTrustedProxies(t *testing.T) {
	env := map[string]string{"LDAPLENS_TRUSTED_PROXIES": "10.0.0.0/8, 192.0.2.7,,2001:db8::1"}
	getenv := func(k string) string { return env[k] }
	got, err := parseArgs(nil, getenv, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.0.2.7/32"),
		netip.MustParsePrefix("2001:db8::1/128"),
	}
	if diff := cmp.Diff(want, got.trustedProxies, cmp.Comparer(func(a, b netip.Prefix) bool { return a == b })); diff != "" {
		t.Errorf("parseArgs() trustedProxies mismatch (-want +got):\n%s", diff)
	}

	noEnv := func(string) string { return "" }
	args := []string{"--trusted-proxies", "10.0.0.0/8,10.0.0.0/99"}
	if _, err := parseArgs(args, noEnv, io.Discard); err == nil || !strings.Contains(err.Error(), "10.0.0.0/99") {
		t.Errorf("parseArgs(%q) error = %v, want an error naming 10.0.0.0/99", args, err)
	}
}

func TestParseArgsAuthToken(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "unset"},
		{name: "valid", value: "abcXYZ0129._~-ok"},
		{name: "generated", value: rand.Text()},
		{name: "too short", value: "abcXYZ0129._~-o", wantErr: true},
		{name: "bad char", value: "abcXYZ0129._~-ok;", wantErr: true},
		{name: "space", value: "abcXYZ0129 ._~-ok", wantErr: true},
	}
	noEnv := func(string) string { return "" }
	for _, tc := range tests {
		got, err := parseArgs([]string{"--auth-token", tc.value}, noEnv, io.Discard)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: parseArgs(--auth-token %q) error = nil, want error", tc.name, tc.value)
			}
			continue
		}
		if err != nil || got.authToken != tc.value {
			t.Errorf("%s: parseArgs(--auth-token %q) = %q, %v, want %q, nil", tc.name, tc.value, got.authToken, err, tc.value)
		}
	}

	envShort := func(k string) string {
		if k == "LDAPLENS_AUTH_TOKEN" {
			return "short"
		}
		return ""
	}
	if _, err := parseArgs(nil, envShort, io.Discard); err == nil {
		t.Errorf("parseArgs() with LDAPLENS_AUTH_TOKEN=short error = nil, want error")
	}
}

func TestParseArgsKeepsEnvTokenOutOfUsage(t *testing.T) {
	const fromEnv = "SuperSecretToken1234"
	getenv := func(k string) string {
		if k == "LDAPLENS_AUTH_TOKEN" {
			return fromEnv
		}
		return ""
	}
	for _, args := range [][]string{{"--bogus"}, {"-h"}} {
		var usage bytes.Buffer
		if _, err := parseArgs(args, getenv, &usage); err == nil {
			t.Errorf("parseArgs(%q) error = nil, want error", args)
		}
		if strings.Contains(usage.String(), fromEnv) {
			t.Errorf("parseArgs(%q) usage output contains the env auth token:\n%s", args, usage.String())
		}
	}
	got, err := parseArgs(nil, getenv, io.Discard)
	if err != nil || got.authToken != fromEnv {
		t.Errorf("parseArgs() authToken = %q, %v, want %q from LDAPLENS_AUTH_TOKEN", got.authToken, err, fromEnv)
	}
}

func TestServeDrainsInFlightRequests(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var finished atomic.Bool
	srv := &http.Server{
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(entered)
			<-release
			finished.Store(true)
		}),
		ReadHeaderTimeout: time.Second,
	}
	shuttingDown := make(chan struct{})
	srv.RegisterOnShutdown(func() { close(shuttingDown) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, srv, ln, drainTimeout, slog.New(slog.DiscardHandler)) }()

	requested := make(chan struct{})
	go func() {
		defer close(requested)
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			t.Errorf("GET in-flight request error = %v", err)
			return
		}
		_ = resp.Body.Close()
	}()
	<-entered
	cancel()
	<-shuttingDown
	// The listener is closed now, so srv.Serve returns; serve must still wait
	// for the in-flight handler.
	time.AfterFunc(50*time.Millisecond, func() { close(release) })
	if err := <-served; err != nil {
		t.Errorf("serve() = %v, want nil", err)
	}
	if !finished.Load() {
		t.Errorf("serve() returned before the in-flight request finished")
	}
	<-requested
}

func TestParseArgsTokenFile(t *testing.T) {
	const token = "abcXYZ0129._~-ok" //nolint:gosec // test value, not a credential
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", path, err)
		}
		return path
	}
	lf := write("token-lf", token+"\n")
	crlf := write("token-crlf", token+"\r\n")
	twoLF := write("token-two-lf", token+"\n\n")
	tiny := write("tiny", "abc123\n")
	missing := filepath.Join(dir, "missing")

	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    string
		wantErr string // a substring of the error; empty wants no error
	}{
		{name: "flag", args: []string{"--auth-token-file", lf}, want: token},
		{name: "env", env: map[string]string{"LDAPLENS_AUTH_TOKEN_FILE": crlf}, want: token},
		{name: "flag beats env token", args: []string{"--auth-token-file", lf}, env: map[string]string{"LDAPLENS_AUTH_TOKEN": "otherTokenFromEnv1"}, want: token},
		{name: "both flags", args: []string{"--auth-token", token, "--auth-token-file", lf}, wantErr: "--auth-token-file"},
		{name: "both env", env: map[string]string{"LDAPLENS_AUTH_TOKEN": token, "LDAPLENS_AUTH_TOKEN_FILE": lf}, wantErr: "LDAPLENS_AUTH_TOKEN_FILE"},
		{name: "missing file", args: []string{"--auth-token-file", missing}, wantErr: missing},
		{name: "short token", args: []string{"--auth-token-file", tiny}, wantErr: tiny},
		{name: "only one newline trimmed", args: []string{"--auth-token-file", twoLF}, wantErr: twoLF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			got, err := parseArgs(tc.args, getenv, io.Discard)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseArgs(%q) error = %v, want an error naming %q", tc.args, err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "abc123") {
					t.Errorf("parseArgs(%q) error = %v, want it not to echo the token", tc.args, err)
				}
				return
			}
			if err != nil || got.authToken != tc.want {
				t.Errorf("parseArgs(%q) authToken = %q, %v, want %q, nil", tc.args, got.authToken, err, tc.want)
			}
		})
	}
}

func TestParseArgsSessionMaxAge(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", want: 8 * time.Hour},
		{name: "env", env: "2h", want: 2 * time.Hour},
		{name: "flag", args: []string{"--session-max-age", "90m"}, want: 90 * time.Minute},
		{name: "flag wins over env", args: []string{"--session-max-age", "1h"}, env: "2h", want: time.Hour},
		{name: "zero flag", args: []string{"--session-max-age", "0"}, wantErr: true},
		{name: "zero env", env: "0", wantErr: true},
		{name: "negative flag", args: []string{"--session-max-age", "-1h"}, wantErr: true},
		{name: "invalid env with flag", args: []string{"--session-max-age", "1h"}, env: "soon", wantErr: true},
	}
	for _, tc := range tests {
		getenv := func(k string) string {
			if k == "LDAPLENS_SESSION_MAX_AGE" {
				return tc.env
			}
			return ""
		}
		got, err := parseArgs(tc.args, getenv, io.Discard)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: parseArgs(%q) error = nil, want error", tc.name, tc.args)
			}
			continue
		}
		if err != nil || got.sessionMaxAge != tc.want {
			t.Errorf("%s: parseArgs(%q) sessionMaxAge = %v, %v, want %v, nil", tc.name, tc.args, got.sessionMaxAge, err, tc.want)
		}
	}
}

func TestServeDrainTimeout(t *testing.T) {
	t.Run("blocked handler", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		defer close(release)
		srv := &http.Server{
			Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				close(entered)
				<-release
			}),
			ReadHeaderTimeout: time.Second,
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen() error = %v", err)
		}
		var logs bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		served := make(chan error, 1)
		go func() { served <- serve(ctx, srv, ln, 50*time.Millisecond, slog.New(slog.NewTextHandler(&logs, nil))) }()
		go func() {
			// The server cuts the connection, so the error is expected.
			if resp, err := http.Get("http://" + ln.Addr().String() + "/"); err == nil {
				_ = resp.Body.Close()
			}
		}()
		<-entered
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serve() = %v, want nil after the drain timed out", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("serve() did not return after a 50ms drain")
		}
		if got, want := logs.String(), `level=WARN msg="shutdown drain timed out" after=50ms`; !strings.Contains(got, want) {
			t.Errorf("serve() logged %q, want it to contain %q", got, want)
		}
	})

	t.Run("idle", func(t *testing.T) {
		srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen() error = %v", err)
		}
		var logs bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		served := make(chan error, 1)
		go func() { served <- serve(ctx, srv, ln, time.Hour, slog.New(slog.NewTextHandler(&logs, nil))) }()
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serve() = %v, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("serve() of an idle server waited for the drain timeout")
		}
		if logs.Len() != 0 {
			t.Errorf("serve() of an idle server logged %q, want nothing", logs.String())
		}
	})
}

func TestStartupLoginLink(t *testing.T) {
	const token = "abcXYZ0129._~-ok" //nolint:gosec // test value, not a credential
	catalog, err := profile.NewCatalog(nil, "")
	if err != nil {
		t.Fatalf("profile.NewCatalog() error = %v", err)
	}
	const link = "http://localhost:8080/?token=" + token
	const noData = `msg="saving profiles disabled (no data dir)"`
	tests := []struct {
		name    string
		cfg     config
		link    bool
		want    []string
		wantNot []string
	}{
		{name: "stored token", cfg: config{dataDir: "/d", authToken: token}, link: true, want: []string{link}, wantNot: []string{noData}},
		{name: "token set", cfg: config{dataDir: "/d", authToken: token}, wantNot: []string{"token=", noData}},
		{name: "no data dir", cfg: config{authToken: token}, link: true, want: []string{link, noData}},
		{name: "server", cfg: config{mode: modeServer, authToken: token}, wantNot: []string{"token=", noData}},
	}
	for _, tc := range tests {
		var logs bytes.Buffer
		logStartup(slog.New(slog.NewTextHandler(&logs, nil)), tc.cfg, "[::]:8080", catalog, tc.link)
		for _, s := range tc.want {
			if !strings.Contains(logs.String(), s) {
				t.Errorf("%s: logStartup() logs lack %q:\n%s", tc.name, s, logs.String())
			}
		}
		for _, s := range tc.wantNot {
			if strings.Contains(logs.String(), s) {
				t.Errorf("%s: logStartup() logs contain %q:\n%s", tc.name, s, logs.String())
			}
		}
	}
}

func TestListeningLogFields(t *testing.T) {
	dir := t.TempDir()
	fixed := []profile.Profile{
		{Name: "fixed1", URL: "ldap://ldap1.example.com"},
		{Name: "fixed2", URL: "ldap://ldap2.example.com"},
	}
	catalog, err := profile.NewCatalog(fixed, dir)
	if err != nil {
		t.Fatalf("profile.NewCatalog() error = %v", err)
	}
	defer catalog.Close()
	if _, err := catalog.Save(profile.Profile{Name: "saved1", URL: "ldap://ldap3.example.com"}); err != nil {
		t.Fatalf("catalog.Save() error = %v", err)
	}

	var logs bytes.Buffer
	cfg := config{basePath: "/ldap", dataDir: dir}
	logStartup(slog.New(slog.NewJSONHandler(&logs, nil)), cfg, "127.0.0.1:1234", catalog, false)
	var got map[string]any
	if err := json.NewDecoder(&logs).Decode(&got); err != nil {
		t.Fatalf("decoding the first log line: %v", err)
	}
	delete(got, "time")
	want := map[string]any{
		"level": "INFO", "msg": "listening", "version": "dev", "mode": "local", "addr": "127.0.0.1:1234",
		"basePath": "/ldap", "profiles": 2.0, "saved": 1.0, "dataDir": dir,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("logStartup() listening line mismatch (-want +got):\n%s", diff)
	}
}

func TestLoginURL(t *testing.T) {
	if got, want := loginURL("[::]:8080", "/ldap", "a b"), "http://localhost:8080/ldap/?token=a+b"; got != want {
		t.Errorf("loginURL() = %q, want %q", got, want)
	}
}

func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler())
	tests := []struct {
		name      string
		got, want time.Duration
	}{
		{name: "ReadHeaderTimeout", got: srv.ReadHeaderTimeout, want: 10 * time.Second},
		{name: "ReadTimeout", got: srv.ReadTimeout, want: 60 * time.Second},
		{name: "WriteTimeout", got: srv.WriteTimeout, want: 5 * time.Minute},
		{name: "IdleTimeout", got: srv.IdleTimeout, want: 2 * time.Minute},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("newHTTPServer().%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	if got, want := srv.MaxHeaderBytes, 64<<10; got != want {
		t.Errorf("newHTTPServer().MaxHeaderBytes = %d, want %d", got, want)
	}
	if srv.Handler == nil {
		t.Errorf("newHTTPServer().Handler = nil, want the given handler")
	}
}
