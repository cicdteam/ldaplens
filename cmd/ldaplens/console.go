// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/lmittmann/tint"

	"github.com/cicdteam/ldaplens/internal/profile"
)

// The --log-format values: console is for a person at a terminal, text is
// the structured log (logfmt) for collectors, auto picks by stdout.
var logFormats = []string{"auto", "console", "text"}

// consoleOutput reports whether format, with stdout a terminal or not, means
// the console format.
func consoleOutput(format string, tty bool) bool {
	return format == "console" || format == "auto" && tty
}

// isTerminal reports whether f is a character device, as a terminal is; a
// pipe, a file or /dev/null under a supervisor is not.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0 && fi.Mode()&os.ModeDevice != 0
}

// newLogger is the console format (short time, colored level) or the
// structured one.
func newLogger(w io.Writer, console, color bool) *slog.Logger {
	if console {
		return slog.New(tint.NewTextHandler(w, &tint.Options{TimeFormat: "15:04:05", NoColor: !color}))
	}
	return slog.New(slog.NewTextHandler(w, nil))
}

// browserCommand names the program that opens a URL in this desktop's
// browser, or "" where there is none to reach: over SSH (it would open on the
// remote screen, if any), on Linux without a display, or on other systems.
func browserCommand(goos string, getenv func(string) string, lookPath func(string) (string, error)) string {
	if getenv("SSH_CONNECTION") != "" {
		return ""
	}
	name := ""
	switch goos {
	case "darwin":
		name = "open"
	case "linux":
		if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
			return ""
		}
		name = "xdg-open"
	default:
		return ""
	}
	if _, err := lookPath(name); err != nil {
		return ""
	}
	return name
}

// openBrowser starts command on link and does not wait for the browser. The
// link, token included, is in its argv while it runs: on one person's
// desktop, the same user who can read the data dir's token file anyway.
func openBrowser(command, link string) bool {
	cmd := exec.Command(command, link) //nolint:gosec // command is open or xdg-open, from browserCommand
	if err := cmd.Start(); err != nil {
		return false
	}
	go func() { _ = cmd.Wait() }() // reap it
	return true
}

// startConsole opens the login link in a browser where it can and prints
// the banner, in place of logStartup's lines.
func startConsole(w io.Writer, cfg config, addr string, catalog *profile.Catalog, link bool, color bool) {
	fixed, saved := catalog.All()
	b := banner{
		version: version, mode: cfg.mode, addr: addr, basePath: cfg.basePath,
		ssh: os.Getenv("SSH_CONNECTION") != "", fixed: len(fixed), saved: len(saved), dataDir: cfg.dataDir,
	}
	b.home, _ = os.UserHomeDir()
	if link {
		b.link = loginURL(addr, cfg.basePath, cfg.authToken)
		if !cfg.noBrowser {
			if command := browserCommand(runtime.GOOS, os.Getenv, exec.LookPath); command != "" {
				b.opened = openBrowser(command, b.link)
			}
		}
	}
	fmt.Fprint(w, b.text(color))
}

// banner is what the console format prints at startup, before the log lines.
type banner struct {
	version  string
	mode     mode
	addr     string
	basePath string
	// link is the login link of local mode, "" when the token was set.
	link   string
	opened bool // the browser was started on link
	ssh    bool // over SSH: hint at a tunnel
	fixed  int  // profiles from --config
	saved  int
	// dataDir is "" when local mode keeps nothing; home shortens it to ~.
	dataDir string
	home    string
}

// text renders b, with ANSI colors when color is set.
func (b banner) text(color bool) string {
	paint := func(code, s string) string { return paintIf(color, code, s) }
	dim := func(s string) string { return paint("2", s) }
	var sb strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&sb, format+"\n", args...) }

	line("")
	line("  %s %s", paint("1", "ldaplens"), dim(b.version+" · "+b.mode.String()+" mode"))
	line("")
	line("  %s Listening on %s", paint("32", "✓"), paint("36", b.addr+b.basePath))
	if b.link != "" {
		if b.opened {
			line("  %s Opened in your browser. If it did not open, use this link:", paint("32", "✓"))
		} else {
			line("  %s Open this link in your browser:", paint("35", "→"))
		}
		line("    %s", paint("4;36", b.link))
		if b.ssh {
			_, port, _ := net.SplitHostPort(b.addr)
			line("    %s", dim(fmt.Sprintf("On another machine? ssh -L %s:%s this-host, then open the link there.", port, b.addr)))
		}
	}
	line("")
	line("  %s  %s", dim("Connections"), b.connections(dim))
	line("  %s", dim("Press Ctrl+C to stop."))
	line("")
	return sb.String()
}

// connections is the banner's count of profiles and where saved ones live.
func (b banner) connections(dim func(string) string) string {
	var parts []string
	if b.fixed > 0 {
		parts = append(parts, fmt.Sprintf("%d from the config", b.fixed))
	}
	if b.mode == modeServer {
		if len(parts) == 0 {
			return "none"
		}
		return parts[0]
	}
	if b.dataDir == "" {
		if len(parts) == 0 {
			parts = []string{"none"}
		}
		return parts[0] + "; saving is off without a data dir"
	}
	dir := b.dataDir
	if b.home != "" && strings.HasPrefix(dir, b.home+string(os.PathSeparator)) {
		dir = "~" + dir[len(b.home):]
	}
	saved := "none saved yet"
	if b.saved > 0 {
		saved = fmt.Sprintf("%d saved", b.saved)
	}
	return strings.Join(append(parts, saved), ", ") + " " + dim("in "+dir)
}

// paintIf wraps s in the ANSI SGR code when color is set.
func paintIf(color bool, code, s string) string {
	if !color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
