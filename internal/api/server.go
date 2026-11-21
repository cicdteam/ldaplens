// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

// Package api serves the ldaplens HTTP API and the embedded web UI.
package api

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/cicdteam/ldaplens/internal/ldapx"
	"github.com/cicdteam/ldaplens/internal/profile"
	"github.com/cicdteam/ldaplens/internal/schema"
	"github.com/cicdteam/ldaplens/internal/session"
)

// Directory is an open LDAP connection. *ldapx.Conn implements it.
type Directory interface {
	io.Closer
	// Closed reports whether the connection was closed for good, by Close
	// or by itself; every later call then fails with net.ErrClosed.
	Closed() bool
	Info() ldapx.Info
	Schema(ctx context.Context) (*schema.Schema, error)
	ReloadSchema(ctx context.Context) (*schema.Schema, error)
	Search(ctx context.Context, r ldapx.SearchRequest) (ldapx.Page, error)
	Entry(ctx context.Context, dn string) (ldapx.Entry, error)
	MemberOf(ctx context.Context, dn string, nested bool) (ldapx.Membership, error)
	Members(ctx context.Context, dn string, nested bool) (ldapx.Membership, error)
	// Modify applies changes to one entry atomically. It is never retried.
	Modify(ctx context.Context, dn string, changes []ldapx.Change) error
	// Add creates an entry. It is never retried.
	Add(ctx context.Context, dn string, attrs []ldapx.TextAttribute) error
	// Delete removes a leaf entry. It is never retried.
	Delete(ctx context.Context, dn string) error
	// Rename moves dn to newRDN under newParent ("" keeps the parent);
	// deleteOld drops the old RDN value. It is never retried.
	Rename(ctx context.Context, dn, newRDN, newParent string, deleteOld bool) error
	// SetPassword sets dn's password, by method "server" (Password Modify)
	// or "ssha"; Active Directory ignores method. It is never retried.
	SetPassword(ctx context.Context, dn, password, method string) error
}

// Dialer opens a Directory for a profile. It must return a nil interface,
// not a typed nil, on error.
type Dialer func(ctx context.Context, p profile.Profile, password string) (Directory, error)

// Options configures the handler returned by New.
type Options struct {
	Catalog  *profile.Catalog
	Dial     Dialer
	Sessions *session.Manager[Directory]
	// Shared holds this process's connections of shared profiles (see
	// profile.Profile.Shared), one session per profile, so that its idle
	// and age limits apply to each.
	Shared *session.Manager[Directory]
	// AllowedHosts are Host header values accepted besides localhost.
	AllowedHosts []string
	// AuthToken, when set, is required on every request except probes.
	AuthToken string
	// BasePath serves everything under a prefix such as "/ldap".
	BasePath string
	// SecureCookies forces Secure cookies. A request over TLS, or with
	// X-Forwarded-Proto https, gets them anyway, so it is only for a TLS
	// proxy that sends no X-Forwarded-Proto.
	SecureCookies bool
	// TrustedProxies are the addresses of reverse proxies whose
	// X-Forwarded-For is trusted for the client address, which logs carry
	// and the failed-login limit counts per.
	TrustedProxies []netip.Prefix
	// Notice is an operator message the UI shows on every page that can
	// reach the API, so not before the access token; empty shows none.
	Notice string
	// NoticeColor is the notice strip's background, #rgb or #rrggbb, as
	// checked by the caller; empty keeps the default look.
	NoticeColor string
	// Server is set in server mode, which keeps no data on disk.
	Server bool
	// UI is the built web UI; nil serves a "not built" page.
	UI     fs.FS
	Logger *slog.Logger // nil discards logs
}

// New returns the root HTTP handler.
func New(opts Options) http.Handler {
	base := strings.TrimRight(opts.BasePath, "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	cookies := cookieOptions{Path: base + "/", Secure: opts.SecureCookies}
	s := &server{
		catalog: opts.Catalog, dial: opts.Dial, sessions: opts.Sessions, cookies: cookies, log: logger,
		shared:         &sharedConns{m: opts.Shared, conns: map[string]sharedConn{}, dials: map[string]chan struct{}{}},
		trustedProxies: opts.TrustedProxies,
		serverMode:     opts.Server,
		notice:         opts.Notice,
		noticeColor:    opts.NoticeColor,
		attempts:       newAttemptLimiter(5, time.Minute, 10000, time.Now),
		clients:        newAttemptLimiter(20, time.Minute, 10000, time.Now),
	}

	app := http.NewServeMux()
	app.HandleFunc("GET /api/server", s.mode)
	app.HandleFunc("GET /api/profiles", s.listProfiles)
	app.HandleFunc("POST /api/profiles", s.saveProfile)
	app.HandleFunc("POST /api/connect", s.connect)
	app.HandleFunc("POST /api/disconnect", s.disconnect)
	app.HandleFunc("GET /api/c/{conn}/info", s.info)
	app.HandleFunc("GET /api/c/{conn}/schema", s.schema)
	app.HandleFunc("POST /api/c/{conn}/search", s.search)
	app.HandleFunc("GET /api/c/{conn}/entry", s.entry)
	app.HandleFunc("GET /api/c/{conn}/memberof", s.memberOf)
	app.HandleFunc("GET /api/c/{conn}/members", s.members)
	app.HandleFunc("POST /api/c/{conn}/changes/apply", s.apply)
	app.HandleFunc("POST /api/c/{conn}/entries/add", s.addEntry)
	app.HandleFunc("POST /api/c/{conn}/entries/delete", s.deleteEntry)
	app.HandleFunc("POST /api/c/{conn}/entries/rename", s.renameEntry)
	app.HandleFunc("POST /api/c/{conn}/entries/password", s.setPassword)
	app.Handle("/", spaHandler(opts.UI, base))

	// Inside the token check, so only authenticated requests take a slot.
	h := admit(64, 4, 10*time.Second, withDeadline(2*time.Minute, 4*time.Minute, app))
	h = requireJSON(h)
	h = http.NewCrossOriginProtection().Handler(h)
	h = requireToken(opts.AuthToken, cookies, logger, s.origin, h)
	h = hostCheck(opts.AllowedHosts, h)

	// Probes answer at the root as well as under the base path: a kubelet
	// probing /healthz must not depend on --base-path.
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", health)
	root.HandleFunc("GET /readyz", health)
	if base != "" {
		root.HandleFunc("GET "+base+"/healthz", health)
		root.HandleFunc("GET "+base+"/readyz", health)
	}
	root.Handle("/", withBasePath(base, h))
	return securityHeaders(base, root)
}

// health answers liveness and readiness probes. Probes bypass the host and
// token checks because kubelets call them by pod IP. The process has no
// dependency that can be unready, so both probes share it.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
