// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestBannerText(t *testing.T) {
	const link = "http://localhost:8080/?token=T"
	tests := []struct {
		name string
		b    banner
		want string
	}{
		{
			name: "local, browser opened",
			b: banner{
				version: "v1.7.0", mode: modeLocal, addr: "127.0.0.1:8080", link: link, opened: true,
				saved: 2, dataDir: "/Users/me/Library/Application Support/ldaplens", home: "/Users/me",
			},
			want: `
  ldaplens v1.7.0 · local mode

  ✓ Listening on 127.0.0.1:8080
  ✓ Opened in your browser. If it did not open, use this link:
    http://localhost:8080/?token=T

  Connections  2 saved in ~/Library/Application Support/ldaplens
  Press Ctrl+C to stop.

`,
		},
		{
			name: "local over ssh, nothing saved, one fixed",
			b: banner{
				version: "v1.7.0", mode: modeLocal, addr: "127.0.0.1:8080", basePath: "/ldap", link: link,
				ssh: true, fixed: 1, dataDir: "/home/me/.config/ldaplens", home: "/home/me",
			},
			want: `
  ldaplens v1.7.0 · local mode

  ✓ Listening on 127.0.0.1:8080/ldap
  → Open this link in your browser:
    http://localhost:8080/?token=T
    On another machine? ssh -L 8080:127.0.0.1:8080 this-host, then open the link there.

  Connections  1 from the config, none saved yet in ~/.config/ldaplens
  Press Ctrl+C to stop.

`,
		},
		{
			name: "local, token set, no data dir",
			b:    banner{version: "dev", mode: modeLocal, addr: "127.0.0.1:8080"},
			want: `
  ldaplens dev · local mode

  ✓ Listening on 127.0.0.1:8080

  Connections  none; saving is off without a data dir
  Press Ctrl+C to stop.

`,
		},
		{
			name: "server",
			b:    banner{version: "v1.7.0", mode: modeServer, addr: "0.0.0.0:8080", fixed: 3},
			want: `
  ldaplens v1.7.0 · server mode

  ✓ Listening on 0.0.0.0:8080

  Connections  3 from the config
  Press Ctrl+C to stop.

`,
		},
	}
	for _, tc := range tests {
		if diff := cmp.Diff(tc.want, tc.b.text(false)); diff != "" {
			t.Errorf("%s: banner.text(false) mismatch (-want +got):\n%s", tc.name, diff)
		}
	}
	colored := tests[0].b.text(true)
	if colored == tests[0].want || !strings.Contains(colored, "\x1b[") || !strings.Contains(colored, link) {
		t.Errorf("banner.text(true) = %q, want ANSI colors around the same text", colored)
	}
}

func TestBrowserCommand(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/x", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	tests := []struct {
		name     string
		goos     string
		env      map[string]string
		lookPath func(string) (string, error)
		want     string
	}{
		{name: "macOS", goos: "darwin", lookPath: found, want: "open"},
		{name: "macOS over ssh", goos: "darwin", env: map[string]string{"SSH_CONNECTION": "1 2 3 4"}, lookPath: found},
		{name: "Linux desktop", goos: "linux", env: map[string]string{"DISPLAY": ":0"}, lookPath: found, want: "xdg-open"},
		{name: "Linux Wayland", goos: "linux", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, lookPath: found, want: "xdg-open"},
		{name: "Linux without a display", goos: "linux", lookPath: found},
		{name: "Linux without xdg-open", goos: "linux", env: map[string]string{"DISPLAY": ":0"}, lookPath: missing},
		{name: "Windows", goos: "windows", lookPath: found},
	}
	for _, tc := range tests {
		getenv := func(k string) string { return tc.env[k] }
		if got := browserCommand(tc.goos, getenv, tc.lookPath); got != tc.want {
			t.Errorf("%s: browserCommand() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseArgsLogFormat(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		env           map[string]string
		wantFormat    string
		wantNoBrowser bool
		wantErr       bool
	}{
		{name: "default", wantFormat: "auto"},
		{name: "flag", args: []string{"--log-format", "text"}, wantFormat: "text"},
		{name: "env", env: map[string]string{"LDAPLENS_LOG_FORMAT": "console"}, wantFormat: "console"},
		{name: "invalid", args: []string{"--log-format", "json"}, wantErr: true},
		{name: "no browser flag", args: []string{"--no-browser"}, wantFormat: "auto", wantNoBrowser: true},
		{name: "no browser env", env: map[string]string{"LDAPLENS_NO_BROWSER": "true"}, wantFormat: "auto", wantNoBrowser: true},
		{name: "invalid no browser env", env: map[string]string{"LDAPLENS_NO_BROWSER": "maybe"}, wantErr: true},
	}
	for _, tc := range tests {
		got, err := parseArgs(tc.args, func(k string) string { return tc.env[k] }, io.Discard)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: parseArgs(%q) error = nil, want error", tc.name, tc.args)
			}
			continue
		}
		if err != nil || got.logFormat != tc.wantFormat || got.noBrowser != tc.wantNoBrowser {
			t.Errorf("%s: parseArgs(%q) = logFormat %q, noBrowser %v, %v; want %q, %v, nil",
				tc.name, tc.args, got.logFormat, got.noBrowser, err, tc.wantFormat, tc.wantNoBrowser)
		}
	}
}

func TestConsoleOutput(t *testing.T) {
	tests := []struct {
		format string
		tty    bool
		want   bool
	}{
		{format: "auto", tty: true, want: true},
		{format: "auto", tty: false, want: false},
		{format: "console", tty: false, want: true},
		{format: "text", tty: true, want: false},
	}
	for _, tc := range tests {
		if got := consoleOutput(tc.format, tc.tty); got != tc.want {
			t.Errorf("consoleOutput(%q, %v) = %v, want %v", tc.format, tc.tty, got, tc.want)
		}
	}
}
