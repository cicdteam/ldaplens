// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/cicdteam/ldaplens/internal/ldapx"
	"github.com/cicdteam/ldaplens/internal/profile"
	"github.com/cicdteam/ldaplens/internal/session"
)

const sessionCookie = "ldaplens_session"

type server struct {
	catalog  *profile.Catalog
	dial     Dialer
	sessions *session.Manager[Directory]
	shared   *sharedConns
	cookies  cookieOptions
	log      *slog.Logger
	// attempts counts wrong passwords per server and bind DN, keyed by
	// serverKey and limiterKey; clients counts them per client address,
	// keyed "client|" and the address.
	attempts *attemptLimiter
	clients  *attemptLimiter

	// trustedProxies are the reverse proxies whose X-Forwarded-For names
	// the client (see clientAddr).
	trustedProxies []netip.Prefix
	// serverMode is Options.Server.
	serverMode bool
	// notice is Options.Notice.
	notice string
	// noticeColor is Options.NoticeColor.
	noticeColor string
}

// mode tells the UI the mode, so that it hides what server mode does not
// offer, and the notice, if any.
func (s *server) mode(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]string{"mode": "local"}
	if s.serverMode {
		resp["mode"] = "server"
	}
	if s.notice != "" {
		resp["notice"] = s.notice
		if s.noticeColor != "" {
			resp["noticeColor"] = s.noticeColor
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// profileView is a profile as the UI sees it.
type profileView struct {
	profile.Profile
	Editable    bool `json:"editable"`
	HasPassword bool `json:"hasPassword"` // the server can bind without asking
	Connected   bool `json:"connected"`
	// UserLogin is set for a bindDNTemplate profile, which asks for a
	// username.
	UserLogin bool `json:"userLogin"`
	// NeedsPassword is set when connect asks for a password: a
	// bindDNTemplate profile, or a bindDN without a configured password.
	NeedsPassword bool `json:"needsPassword"`
}

// serverProfileView is a profile as the UI sees it in server mode: what the
// connect page needs, and nothing about where or how the profile binds.
type serverProfileView struct {
	Name          string `json:"name"`
	Label         string `json:"label,omitempty"`
	Color         string `json:"color,omitempty"`
	ReadOnly      bool   `json:"readOnly"`
	Connected     bool   `json:"connected"`
	UserLogin     bool   `json:"userLogin"`
	NeedsPassword bool   `json:"needsPassword"`
}

// view is p as the UI sees it.
func (s *server) view(p profile.Profile, editable bool, connected map[string]bool) profileView {
	return profileView{
		Profile:       p,
		Editable:      editable,
		HasPassword:   p.PasswordEnv != "" || p.PasswordFile != "",
		Connected:     connected[p.Name],
		UserLogin:     p.UserLogin(),
		NeedsPassword: !p.Shared(),
	}
}

func (s *server) listProfiles(w http.ResponseWriter, r *http.Request) {
	fixed, saved := s.catalog.All()
	connected := map[string]bool{}
	for n, d := range s.sessions.Conns(sessionID(r)) {
		connected[n] = !d.Closed()
	}
	for _, p := range slices.Concat(fixed, saved) {
		if p.Shared() && s.shared.connected(p.Name) {
			connected[p.Name] = true
		}
	}
	views := make([]profileView, 0, len(fixed)+len(saved))
	for _, p := range fixed {
		views = append(views, s.view(p, false, connected))
	}
	for _, p := range saved {
		views = append(views, s.view(p, true, connected))
	}
	if s.serverMode {
		short := make([]serverProfileView, len(views))
		for i, v := range views {
			short[i] = serverProfileView{
				Name: v.Name, Label: v.Label, Color: v.Color, ReadOnly: v.ReadOnly,
				Connected: v.Connected, UserLogin: v.UserLogin, NeedsPassword: v.NeedsPassword,
			}
		}
		writeJSON(w, http.StatusOK, short)
		return
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *server) saveProfile(w http.ResponseWriter, r *http.Request) {
	if s.serverMode {
		writeError(w, http.StatusForbidden, apiError{Code: "saving_disabled", Message: "this server keeps no saved connections"})
		return
	}
	var p profile.Profile
	if !decode(w, r, &p, maxBody) {
		return
	}
	// ponytail: read before Save, so two saves of one name at once may log a
	// stale oldURL; have Save return the replaced profile if that matters.
	old, _ := s.catalog.Find(p.Name) // the zero Profile, so oldURL "", for a new one
	saved, err := s.catalog.Save(p)
	switch {
	case errors.Is(err, profile.ErrInvalid):
		writeError(w, http.StatusBadRequest, apiError{Code: "invalid_profile", Message: err.Error()})
	case errors.Is(err, profile.ErrFixed):
		writeError(w, http.StatusConflict, apiError{Code: "profile_fixed", Message: err.Error()})
	case errors.Is(err, profile.ErrSavingDisabled):
		writeError(w, http.StatusForbidden, apiError{Code: "saving_disabled", Message: err.Error()})
	case errors.Is(err, profile.ErrLimit):
		writeError(w, http.StatusConflict, apiError{Code: "profile_limit", Message: err.Error()})
	case err != nil:
		s.fail(w, err)
	default:
		// A shared connection with the old settings goes, so that the next
		// request dials the saved ones.
		s.shared.drop(saved.Name)
		s.log.Info("profile saved", slices.Concat([]any{
			"name", saved.Name, "oldURL", old.URL, "url", saved.URL,
			"readOnly", saved.ReadOnly, "insecureSkipVerify", saved.InsecureSkipVerify,
		}, s.who(r))...)
		writeJSON(w, http.StatusOK, s.view(saved, true, nil))
	}
}

// infoView is a connection's Info plus the profile settings the UI shows as
// badges and in its status bar. They come with the info so the badges never
// wait on, or vanish with, a separate profiles request, and so that server
// mode, whose profile list leaves out the URL, shows it only after a
// successful bind.
type infoView struct {
	ldapx.Info
	URL                string `json:"url"`
	StartTLS           bool   `json:"startTLS"`
	ReadOnly           bool   `json:"readOnly"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	Color              string `json:"color,omitempty"`
}

func newInfoView(info ldapx.Info, p profile.Profile) infoView {
	return infoView{
		Info:               info,
		URL:                p.URL,
		StartTLS:           p.StartTLS,
		ReadOnly:           p.ReadOnly,
		InsecureSkipVerify: p.InsecureSkipVerify,
		Color:              p.Color,
	}
}

type connectRequest struct {
	Profile string `json:"profile"`
	// Username is for a bindDNTemplate profile; other profiles ignore it.
	Username string `json:"username,omitempty"`
	Password string `json:"password"`
}

func (s *server) connect(w http.ResponseWriter, r *http.Request) {
	var req connectRequest
	if !decode(w, r, &req, maxBody) {
		return
	}
	p, err := s.catalog.Find(req.Profile)
	if err != nil {
		writeError(w, http.StatusNotFound, apiError{Code: "profile_not_found", Message: err.Error()})
		return
	}
	// A shared profile's connection serves every browser and needs no
	// session. A typed password, as after the configured one failed, gets
	// a connection of this browser's own below.
	if p.Shared() && req.Password == "" {
		d, err := s.sharedDir(r.Context(), p)
		if err != nil {
			s.connectFailed(w, r, p, err)
			return
		}
		s.log.Info("connected", s.attributed(r, p, "profile", p.Name)...)
		writeJSON(w, http.StatusOK, newInfoView(d.Info(), p))
		return
	}
	if p.UserLogin() {
		bound, ok := s.userLogin(w, p, req)
		if !ok {
			return
		}
		p = bound
	} else if req.Password == "" {
		writeError(w, http.StatusBadRequest, apiError{Code: "password_required", Message: "this profile needs a password"})
		return
	}
	// Only a typed password is limited: a wrong configured one would lock
	// its DN for everyone. A login counts against its client and its
	// account, and both must admit it. endClient and endAccount end a
	// limited attempt, set from the dial: fail for a wrong password, else
	// done, but succeed for the account on a success. A success leaves the
	// client's count, so that one valid account cannot reset it between
	// guesses at others. The defer ends both on every path, a panic
	// included.
	endClient, endAccount := (*attemptLimiter).done, (*attemptLimiter).done
	if req.Password != "" && p.BindDN != "" {
		throttled := func(wait time.Duration, limit, whose string) {
			secs := int((wait + time.Second - 1) / time.Second) // rounded up
			s.log.Info("connect throttled", s.attributed(r, p, "profile", p.Name, "limit", limit)...)
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeError(w, http.StatusTooManyRequests, apiError{
				Code:    "too_many_attempts",
				Message: fmt.Sprintf("too many sign-in attempts %s; try again in %d s", whose, secs),
			})
		}
		// Whatever the profile, so that spraying many DNs from one client
		// is caught.
		client := "client|" + s.clientAddr(r).String()
		// Per server, so that a success elsewhere, as on a server a saved
		// profile points at, cannot clear this server's count.
		key := serverKey(p.URL) + "|" + limiterKey(p.BindDN)
		wait, ok := s.clients.begin(client)
		if !ok {
			throttled(wait, "client", "from this address")
			return
		}
		if wait, ok = s.attempts.begin(key); !ok {
			s.clients.done(client)
			throttled(wait, "account", "for this account")
			return
		}
		defer func() {
			endClient(s.clients, client)
			endAccount(s.attempts, key)
		}()
	}
	ctx, cancel := context.WithTimeout(r.Context(), p.ConnectTimeout+p.SearchTimeout)
	defer cancel()
	dir, err := s.dial(ctx, p, req.Password)
	if err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			endClient, endAccount = (*attemptLimiter).fail, (*attemptLimiter).fail
		}
		s.connectFailed(w, r, p, err)
		return
	}
	endAccount = (*attemptLimiter).succeed
	// Every connect moves the browser to a new session ID, so an ID that
	// leaked before it reaches no connection. The cookie is set before Put:
	// a refused Put leaves the session, under its new ID, as it was.
	id := s.sessions.Rotate(sessionID(r))
	http.SetCookie(w, s.cookies.cookie(sessionCookie, id, r))
	if err := s.sessions.Put(id, p.Name, dir); err != nil {
		_ = dir.Close()
		s.fail(w, err)
		return
	}
	// Logged under the new ID, the one the browser's next lines carry, with
	// the old one in previous to tie them to the lines before.
	lr := r.Clone(r.Context())
	lr.Header.Set("Cookie", sessionCookie+"="+id)
	s.log.Info("connected", s.attributed(lr, p, "profile", p.Name, "previous", fingerprint(sessionID(r)))...)
	writeJSON(w, http.StatusOK, newInfoView(dir.Info(), p))
}

// connectFailed logs and answers a failed connect to p, in server mode
// with signInError. The log keeps the full error.
func (s *server) connectFailed(w http.ResponseWriter, r *http.Request, p profile.Profile, err error) {
	s.log.Info("connect failed", s.attributed(r, p, "profile", p.Name, "error", err)...)
	if !s.serverMode {
		s.fail(w, err)
		return
	}
	status, body := signInError(errorResponse(err))
	if status == http.StatusInternalServerError {
		s.log.Error("request failed", "error", err)
	}
	writeError(w, status, body)
}

// userLogin checks a connect to the bindDNTemplate profile p, in this
// order: a username (400 username_required); a username the template
// accepts (400 invalid_username); a password (400 password_required). It
// returns p with BindDN set to the DN to bind as, or writes the refusal and
// returns false, so that a template profile never binds anonymously:
// RFC 4513 treats a bind with an empty password as unauthenticated, and
// many servers accept it.
func (s *server) userLogin(w http.ResponseWriter, p profile.Profile, req connectRequest) (profile.Profile, bool) {
	refuse := func(status int, code, msg string) (profile.Profile, bool) {
		writeError(w, status, apiError{Code: code, Message: msg})
		return profile.Profile{}, false
	}
	if req.Username == "" {
		return refuse(http.StatusBadRequest, "username_required", "this profile needs a username")
	}
	dn, err := p.BindDNFor(req.Username)
	if errors.Is(err, profile.ErrInvalidUsername) {
		return refuse(http.StatusBadRequest, "invalid_username", err.Error())
	}
	if err != nil {
		s.fail(w, err)
		return profile.Profile{}, false
	}
	if req.Password == "" {
		return refuse(http.StatusBadRequest, "password_required", "this profile needs a password")
	}
	p.BindDN = dn
	return p, true
}

func (s *server) disconnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string `json:"profile"`
	}
	if !decode(w, r, &req, maxBody) {
		return
	}
	// A shared profile's connection stays: other browsers use it. This drops
	// only the browser's own, from a typed password.
	p, _ := s.catalog.Find(req.Profile) // for the bind DN; "" for an unknown name
	// The connection's DN, as guard takes it.
	if d, err := s.sessions.Get(sessionID(r), req.Profile); err == nil {
		p.BindDN = d.Info().BindDN
	}
	s.sessions.Drop(sessionID(r), req.Profile)
	s.log.Info("disconnected", s.attributed(r, p, "profile", req.Profile)...)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) info(w http.ResponseWriter, r *http.Request) {
	d, ok := s.directory(w, r)
	if !ok {
		return
	}
	// The flags come from the current catalog profile, as the profiles list
	// the header used to read: an edit shows without reconnecting. Sessions
	// are keyed by profile name and the catalog never drops a profile, so
	// Find only fails on a bug.
	p, err := s.catalog.Find(r.PathValue("conn"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newInfoView(d.Info(), p))
}

func (s *server) schema(w http.ResponseWriter, r *http.Request) {
	d, ok := s.directory(w, r)
	if !ok {
		return
	}
	load := d.Schema
	if r.URL.Query().Get("reload") == "true" {
		load = d.ReloadSchema
	}
	sch, err := load(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sch)
}

func (s *server) search(w http.ResponseWriter, r *http.Request) {
	d, ok := s.directory(w, r)
	if !ok {
		return
	}
	var req ldapx.SearchRequest
	if !decode(w, r, &req, maxBody) {
		return
	}
	if req.BaseDN == "" {
		req.BaseDN = d.Info().BaseDN
	}
	page, err := d.Search(r.Context(), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) entry(w http.ResponseWriter, r *http.Request) {
	d, ok := s.directory(w, r)
	if !ok {
		return
	}
	dn, ok := requireDN(w, r)
	if !ok {
		return
	}
	e, err := d.Entry(r.Context(), dn)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) memberOf(w http.ResponseWriter, r *http.Request) { s.refs(w, r, Directory.MemberOf) }

func (s *server) members(w http.ResponseWriter, r *http.Request) { s.refs(w, r, Directory.Members) }

func (s *server) refs(w http.ResponseWriter, r *http.Request, list func(Directory, context.Context, string, bool) (ldapx.Membership, error)) {
	d, ok := s.directory(w, r)
	if !ok {
		return
	}
	dn, ok := requireDN(w, r)
	if !ok {
		return
	}
	m, err := list(d, r.Context(), dn, r.URL.Query().Get("nested") == "true")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// directory returns the connection named by the {conn} path segment, or
// writes an error and returns false. Without one in the browser's session,
// a shared profile gets this process's connection, dialed by the first
// request that needs it. One in the session that ldapx closed for good, as
// a typed password the server refused at a reconnect, is dropped and counts
// as none.
func (s *server) directory(w http.ResponseWriter, r *http.Request) (Directory, bool) {
	name := r.PathValue("conn")
	d, err := s.sessions.Get(sessionID(r), name)
	if err == nil && d.Closed() {
		s.sessions.DropConn(sessionID(r), name, d)
		err = session.ErrNotConnected
	}
	if err != nil {
		if p, ferr := s.catalog.Find(name); ferr == nil && p.Shared() {
			d, err = s.sharedDir(r.Context(), p)
		}
	}
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	return d, true
}

// sharedDir returns this process's connection of the shared profile p,
// dialing it with the configured password, or anonymously, when there is
// none.
func (s *server) sharedDir(ctx context.Context, p profile.Profile) (Directory, error) {
	return s.shared.get(ctx, p.Name, func(ctx context.Context) (Directory, error) {
		password, _, err := p.ConfiguredPassword()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errPasswordUnavailable, err)
		}
		ctx, cancel := context.WithTimeout(ctx, p.ConnectTimeout+p.SearchTimeout)
		defer cancel()
		return s.dial(ctx, p, password)
	})
}

// sharedConns holds the connections of shared profiles, each in a session
// of its own in m. Every replica dials its own.
type sharedConns struct {
	m *session.Manager[Directory]

	mu    sync.Mutex
	conns map[string]sharedConn    // profile name to its last dialed connection
	dials map[string]chan struct{} // profile name to a slot held across its dial
}

// sharedConn is a dialed connection and the session in m that holds it.
// Every dial gets a new session, so id names this connection only.
type sharedConn struct {
	id string
	d  Directory
}

func (c *sharedConns) last(name string) sharedConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conns[name]
}

// connected reports whether name has a live connection, without counting
// as a use.
func (c *sharedConns) connected(name string) bool {
	sc := c.last(name)
	return len(c.m.Names(sc.id)) > 0 && !sc.d.Closed()
}

// lookup returns name's connection. One closed for good, as by a redial
// whose bind the server refused, is dropped instead, so that the next dial
// reads the configured password again.
func (c *sharedConns) lookup(name string) (Directory, error) {
	sc := c.last(name)
	d, err := c.m.Get(sc.id, name)
	if err == nil && d.Closed() {
		c.m.Drop(sc.id, name)
		return nil, session.ErrNotConnected
	}
	return d, err
}

// get returns name's connection, dialing it with dial when there is none.
// One dial per profile runs at a time, outside the lock: a request that
// finds one under way waits for it and takes its connection.
//
// ponytail: when a dial fails, each request that waited dials again in
// turn, so a burst of k requests on a broken profile binds up to k times;
// hand the dial's error to the waiters if a lockout policy counts them.
func (c *sharedConns) get(ctx context.Context, name string, dial func(context.Context) (Directory, error)) (Directory, error) {
	if d, err := c.lookup(name); err == nil {
		return d, nil
	}
	c.mu.Lock()
	slot, ok := c.dials[name]
	if !ok {
		slot = make(chan struct{}, 1)
		c.dials[name] = slot
	}
	c.mu.Unlock()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-slot }()
	if d, err := c.lookup(name); err == nil {
		return d, nil // dialed while this request waited
	}
	d, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	id := c.m.Create()
	if err := c.m.Put(id, name, d); err != nil {
		_ = d.Close()
		return nil, err
	}
	c.mu.Lock()
	c.conns[name] = sharedConn{id: id, d: d}
	c.mu.Unlock()
	return d, nil
}

// drop closes name's connection, if there is one.
//
// ponytail: a dial under way when drop runs still stores its connection,
// made with the settings it started with, and under steady use that lives
// until --session-max-age; take the dial slot here if that matters.
func (c *sharedConns) drop(name string) {
	c.m.Drop(c.last(name).id, name)
}

func (s *server) fail(w http.ResponseWriter, err error) {
	status, body := errorResponse(err)
	if status == http.StatusInternalServerError {
		s.log.Error("request failed", "error", err)
	}
	writeError(w, status, body)
}

func sessionID(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func requireDN(w http.ResponseWriter, r *http.Request) (string, bool) {
	dn := r.URL.Query().Get("dn")
	if dn == "" {
		writeError(w, http.StatusBadRequest, apiError{Code: "bad_request", Message: "dn query parameter is required"})
		return "", false
	}
	return dn, true
}

// Request body limits: a batch or a new entry may carry binary values, up to
// ldapx.MaxValue each once decoded; every other body stays small.
const (
	maxBody      = 1 << 20
	maxWriteBody = 8 << 20
)

// decode reads a JSON body of at most limit bytes into v, rejecting unknown
// fields so the UI cannot smuggle in config-only settings such as
// passwordFile.
func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, apiError{Code: "bad_request", Message: "invalid JSON body: " + err.Error()})
		return false
	}
	return true
}
