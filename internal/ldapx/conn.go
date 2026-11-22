// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package ldapx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/cicdteam/ldaplens/internal/ad"
	"github.com/cicdteam/ldaplens/internal/profile"
	"github.com/cicdteam/ldaplens/internal/schema"
)

// rfc3062OID names the Password Modify extended operation (RFC 3062)
// in a root DSE's supportedExtension.
const rfc3062OID = "1.3.6.1.4.1.4203.1.11.1"

// Info summarizes the server's root DSE.
type Info struct {
	// BaseDN is where browsing starts: the profile's base DN, else the
	// server's default naming context, else its first naming context.
	BaseDN               string   `json:"baseDN"`
	NamingContexts       []string `json:"namingContexts"`
	DefaultNamingContext string   `json:"defaultNamingContext,omitempty"`
	SubschemaSubentry    string   `json:"subschemaSubentry,omitempty"`
	Vendor               string   `json:"vendor,omitempty"`
	ActiveDirectory      bool     `json:"activeDirectory"`
	Paging               bool     `json:"paging"`
	// PasswordModify is set when the root DSE lists the Password Modify
	// extended operation, which hashes a new password by the server's policy.
	PasswordModify bool `json:"passwordModify"`
	// Encrypted is set for ldaps:// and StartTLS connections.
	Encrypted bool `json:"encrypted"`
	// BindDN is the DN the connection bound as, empty for an anonymous
	// bind. For a template profile it is the template filled in with the
	// username.
	BindDN string `json:"bindDN,omitempty"`
}

// UntrustedCertError reports a server certificate that failed verification.
// Certificates holds the chain the server presented (leaf first) so the
// user can inspect it and choose to trust the CA.
type UntrustedCertError struct {
	Certificates []*x509.Certificate
	Err          error
}

func (e *UntrustedCertError) Error() string { return "untrusted server certificate: " + e.Err.Error() }

func (e *UntrustedCertError) Unwrap() error { return e.Err }

// Conn is an authenticated LDAP connection. It is safe for concurrent use.
// Read operations share one server connection and redial once if it was
// lost. Servers such as OpenLDAP keep one paged-results state per
// connection, so a paged search takes the shared connection for itself
// while it runs (see take) and other reads dial another.
type Conn struct {
	prof     profile.Profile
	password string
	info     Info

	mu     sync.Mutex
	lc     *ldap.Conn // shared; nil until the next read dials one
	closed bool
	schema *schema.Schema
	// refused names the membership attributes the server refuses to
	// search on (see MemberOf), so that later scans leave them out.
	refused map[string]bool

	// dialFn replaces dial when set, so that tests can stall a redial.
	dialFn func(ctx context.Context, password string) (*ldap.Conn, error)
}

// Dial connects to the profile's server, binds (anonymously when the
// profile has no bind DN) and reads the root DSE.
func Dial(ctx context.Context, p profile.Profile, password string) (*Conn, error) {
	c := &Conn{prof: p, password: password}
	lc, err := c.dial(ctx, password)
	if err != nil {
		return nil, err
	}
	c.lc = lc
	if err := c.loadInfo(ctx); err != nil {
		_ = lc.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the shared server connection and forgets the password. A
// paged search running meanwhile closes its connection when it ends. Later
// operations fail with net.ErrClosed.
func (c *Conn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.password = ""
	lc := c.lc
	c.lc = nil
	c.mu.Unlock()
	if lc == nil {
		return nil
	}
	return lc.Close()
}

// Closed reports whether c was closed for good, by Close or by a redial
// whose bind the server refused with invalid credentials. Every later
// operation fails with net.ErrClosed.
func (c *Conn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Info returns what the server announced at connect time.
func (c *Conn) Info() Info { return c.info }

// dial opens and binds a new server connection. It takes the password
// rather than reading it, because it runs outside mu and Close clears it.
func (c *Conn) dial(ctx context.Context, password string) (*ldap.Conn, error) {
	cfg, err := c.prof.TLSConfig()
	if err != nil {
		return nil, err
	}
	lc, err := connect(ctx, c.prof, cfg)
	if err != nil {
		return nil, err
	}
	if c.prof.BindDN != "" {
		if err := lc.Bind(c.prof.BindDN, password); err != nil {
			_ = lc.Close()
			return nil, networkError(err)
		}
	}
	return lc, nil
}

// deadlineConn bounds each write by timeout, once connect has set it, so
// that a server which stops reading cannot block go-ldap's writer forever.
// Reads get no deadline: go-ldap's reader goroutine reads all the time.
type deadlineConn struct {
	net.Conn
	// timeout is a time.Duration. It stays 0 while connect bounds every
	// read and write by its own deadline.
	timeout atomic.Int64
}

// Write closes the connection when it fails: the next request would
// otherwise continue the stream where this one was cut off.
func (c *deadlineConn) Write(b []byte) (int, error) {
	if d := time.Duration(c.timeout.Load()); d > 0 {
		if err := c.SetWriteDeadline(time.Now().Add(d)); err != nil {
			return 0, err
		}
	}
	n, err := c.Conn.Write(b)
	if err != nil {
		_ = c.Close()
	}
	return n, err
}

// connect opens the transport and upgrades it with StartTLS when the
// profile asks for it. A certificate that fails verification yields an
// UntrustedCertError. The certificate is verified in VerifyConnection
// rather than by crypto/tls because go-ldap flattens StartTLS handshake
// errors to text, losing both the error type and the chain.
//
// The dial and both kinds of TLS handshake must finish within the connect
// timeout. On the returned connection each write is bounded by the search
// timeout and reads have no deadline.
func connect(ctx context.Context, p profile.Profile, cfg *tls.Config) (*ldap.Conn, error) {
	var untrusted *UntrustedCertError
	if !cfg.InsecureSkipVerify {
		roots, name := cfg.RootCAs, cfg.ServerName
		cfg = cfg.Clone()
		cfg.InsecureSkipVerify = true //nolint:gosec // VerifyConnection below does the full verification
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			err := verifyChain(cs.PeerCertificates, roots, name)
			if err != nil {
				untrusted = &UntrustedCertError{Certificates: cs.PeerCertificates, Err: err}
			}
			return err
		}
	}
	fail := func(err error) error {
		if untrusted != nil {
			return untrusted
		}
		return networkError(err)
	}
	// Dial, handshake and errors as ldap.DialURL does them, but with
	// deadlines it does not set.
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, fail(ldap.NewError(ldap.ErrorNetwork, err))
	}
	var port string
	switch u.Scheme {
	case "ldap":
		port = ldap.DefaultLdapPort
	case "ldaps":
		port = ldap.DefaultLdapsPort
	default:
		return nil, fail(ldap.NewError(ldap.ErrorNetwork, fmt.Errorf("unknown scheme %q", u.Scheme)))
	}
	if u.Port() != "" {
		port = u.Port()
	}
	ctx, cancel := context.WithTimeout(ctx, p.ConnectTimeout)
	defer cancel()
	raw, err := new(net.Dialer).DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, fail(ldap.NewError(ldap.ErrorNetwork, err))
	}
	dc := &deadlineConn{Conn: raw}
	var conn net.Conn = dc
	isTLS := u.Scheme == "ldaps"
	if isTLS {
		// ctx alone bounds the handshake, so that running out of time
		// always yields ctx.Err(). A socket deadline at the same instant
		// could fire first and turn a timeout into a plain network error.
		tc := tls.Client(dc, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, fail(ldap.NewError(ldap.ErrorNetwork, err))
		}
		conn = tc
	}
	// go-ldap's StartTLS takes no context, so the socket deadline bounds it.
	deadline, _ := ctx.Deadline()
	if err := raw.SetDeadline(deadline); err != nil {
		_ = raw.Close()
		return nil, fail(ldap.NewError(ldap.ErrorNetwork, err))
	}
	lc := ldap.NewConn(conn, isTLS)
	lc.Start()
	lc.SetTimeout(p.SearchTimeout)
	if p.StartTLS {
		if err := lc.StartTLS(cfg); err != nil {
			_ = lc.Close()
			return nil, fail(err)
		}
	}
	// The connect deadline must not outlive connect: an idle connection
	// would lose its reader once it passed.
	if err := raw.SetDeadline(time.Time{}); err != nil {
		_ = lc.Close()
		return nil, fail(ldap.NewError(ldap.ErrorNetwork, err))
	}
	dc.timeout.Store(int64(p.SearchTimeout))
	return lc, nil
}

// networkError turns an error go-ldap reports as plain text, such as
// "unable to read LDAP response packet: EOF" after the server hung up, into
// an ErrorNetwork *ldap.Error so that callers can tell a lost connection
// from a local failure. Errors with a meaning of their own pass unchanged.
func networkError(err error) error {
	var le *ldap.Error
	var untrusted *UntrustedCertError
	if err == nil || errors.As(err, &le) || errors.As(err, &untrusted) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, ErrInvalidRequest) {
		return err
	}
	return ldap.NewError(ldap.ErrorNetwork, err)
}

// verifyChain checks the server's certificates (leaf first) against roots
// and name the way crypto/tls does. Nil roots means the system pool.
func verifyChain(certs []*x509.Certificate, roots *x509.CertPool, name string) error {
	if len(certs) == 0 {
		return errors.New("server sent no certificate")
	}
	opts := x509.VerifyOptions{Roots: roots, DNSName: name, Intermediates: x509.NewCertPool()}
	for _, cert := range certs[1:] {
		opts.Intermediates.AddCert(cert)
	}
	_, err := certs[0].Verify(opts)
	return err
}

// isEncrypted reports whether p's connection runs over TLS: ldaps:// or
// StartTLS.
func isEncrypted(p profile.Profile) bool {
	return p.StartTLS || strings.HasPrefix(strings.ToLower(p.URL), "ldaps://")
}

func (c *Conn) loadInfo(ctx context.Context) error {
	res, err := searchOnce(ctx, c.lc, &ldap.SearchRequest{
		Scope:  ldap.ScopeBaseObject,
		Filter: "(objectClass=*)",
		Attributes: []string{
			"namingContexts", "defaultNamingContext", "subschemaSubentry",
			"supportedControl", "supportedCapabilities", "supportedExtension", "vendorName",
		},
	})
	if err != nil {
		return fmt.Errorf("read root DSE: %w", err)
	}
	if len(res.entries) == 0 {
		return errors.New("read root DSE: server returned nothing")
	}
	e := res.entries[0]
	c.info = Info{
		NamingContexts:       e.GetAttributeValues("namingContexts"),
		DefaultNamingContext: e.GetAttributeValue("defaultNamingContext"),
		SubschemaSubentry:    e.GetAttributeValue("subschemaSubentry"),
		Vendor:               e.GetAttributeValue("vendorName"),
		ActiveDirectory:      ad.IsActiveDirectory(e.GetAttributeValues("supportedCapabilities")),
		Paging:               slices.Contains(e.GetAttributeValues("supportedControl"), ldap.ControlTypePaging),
		PasswordModify:       slices.Contains(e.GetAttributeValues("supportedExtension"), rfc3062OID),
		Encrypted:            isEncrypted(c.prof),
		BindDN:               c.prof.BindDN,
	}
	switch {
	case c.prof.BaseDN != "":
		c.info.BaseDN = c.prof.BaseDN
	case c.info.DefaultNamingContext != "":
		c.info.BaseDN = c.info.DefaultNamingContext
	case len(c.info.NamingContexts) > 0:
		c.info.BaseDN = c.info.NamingContexts[0]
	}
	return nil
}

// current returns the shared connection, dialing a new one when there is
// none or it was lost. The dial runs outside mu, so that a slow server
// does not hold up Close. Of two concurrent dials, the
// first to finish is installed and the other is closed. A dial whose bind
// fails with invalid credentials closes c: the password was changed or
// reset since connect, and sending it again at every request could lock
// the account under a lockout policy. Other failures, such as a server
// that cannot be reached, leave c open for the next request to redial.
func (c *Conn) current(ctx context.Context) (*ldap.Conn, error) {
	c.mu.Lock()
	lc, closed, password := c.lc, c.closed, c.password
	c.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	if lc != nil && !lc.IsClosing() {
		return lc, nil
	}
	dial := c.dial
	if c.dialFn != nil {
		dial = c.dialFn
	}
	lc, err := dial(ctx, password)
	if err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			_ = c.Close()
		}
		return nil, err
	}
	extra := lc
	c.mu.Lock()
	switch {
	case c.closed:
		lc, err = nil, net.ErrClosed
	case c.lc != nil && !c.lc.IsClosing():
		lc = c.lc
	default:
		c.lc, extra = lc, nil
	}
	c.mu.Unlock()
	if extra != nil {
		_ = extra.Close()
	}
	return lc, err
}

// take removes the shared connection from its slot so that nobody else
// starts a paged search on it; the next shared read dials a new one. The
// caller hands it back with release or closes it. A connection that left
// the slot meanwhile, taken or replaced, is not returned.
func (c *Conn) take(ctx context.Context) (*ldap.Conn, error) {
	for {
		lc, err := c.current(ctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		taken := c.lc == lc
		if taken {
			c.lc = nil
		}
		c.mu.Unlock()
		if taken {
			return lc, nil
		}
	}
}

// release makes lc the shared connection again, or closes it when another
// one took its place meanwhile.
func (c *Conn) release(lc *ldap.Conn) {
	c.mu.Lock()
	if !c.closed && !lc.IsClosing() && (c.lc == nil || c.lc.IsClosing()) {
		c.lc, lc = lc, nil
	}
	c.mu.Unlock()
	if lc != nil {
		_ = lc.Close()
	}
}

// read runs fn on the shared connection, and once more on a new one if the
// connection was lost. Only use it for operations that are safe to repeat;
// writes must never go through read.
func (c *Conn) read(ctx context.Context, fn func(*ldap.Conn) error) error {
	_, err := retry(ctx, c.current, fn)
	return err
}

// retry runs fn on a connection from get and, if that connection was lost
// meanwhile, once more on a new one. It returns the connection fn last ran
// on. A connection is only lost when go-ldap shut it down: its request
// timeout leaves the connection open and must not trigger a rerun.
func retry(ctx context.Context, get func(context.Context) (*ldap.Conn, error), fn func(*ldap.Conn) error) (*ldap.Conn, error) {
	lc, err := get(ctx)
	if err != nil {
		return nil, err
	}
	if err = fn(lc); err == nil || ctx.Err() != nil || !lc.IsClosing() {
		return lc, err
	}
	if lc, err = get(ctx); err != nil {
		return nil, err
	}
	return lc, fn(lc)
}
