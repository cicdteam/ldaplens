// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package ldapx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
	"github.com/google/go-cmp/cmp"

	"github.com/cicdteam/ldaplens/internal/profile"
	"github.com/cicdteam/ldaplens/internal/schema"
)

// fakeServer accepts TCP connections and hands each one to serve with its
// 1-based number, closing it when serve returns. It returns an ldap:// URL
// and the number of accepted connections.
func fakeServer(t *testing.T, serve func(n int32, conn net.Conn)) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var accepts atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n := accepts.Add(1)
			go func() {
				defer conn.Close()
				serve(n, conn)
			}()
		}
	}()
	return "ldap://" + ln.Addr().String(), &accepts
}

// readRequest reads one LDAP message and returns its message ID.
func readRequest(r io.Reader) (int64, error) {
	p, err := ber.ReadPacket(r)
	if err != nil {
		return 0, err
	}
	if len(p.Children) == 0 {
		return 0, errors.New("empty LDAP message")
	}
	id, _ := p.Children[0].Value.(int64)
	return id, nil
}

// search is what the fake servers read from a search request.
type search struct {
	id        int64
	base      string
	sizeLimit int
	filter    string
	paging    *ldap.ControlPaging // nil without the paging control
}

// readSearch reads one search request.
func readSearch(r io.Reader) (search, error) {
	p, err := ber.ReadPacket(r)
	if err != nil {
		return search{}, err
	}
	if len(p.Children) < 2 || len(p.Children[1].Children) < 7 {
		return search{}, errors.New("not a search request")
	}
	var s search
	s.id, _ = p.Children[0].Value.(int64)
	s.base, _ = p.Children[1].Children[0].Value.(string)
	limit, _ := p.Children[1].Children[3].Value.(int64)
	s.sizeLimit = int(limit)
	if s.filter, err = ldap.DecompileFilter(p.Children[1].Children[6]); err != nil {
		return search{}, err
	}
	if len(p.Children) > 2 {
		for _, cp := range p.Children[2].Children {
			if ctl, err := ldap.DecodeControl(cp); err == nil {
				if paging, ok := ctl.(*ldap.ControlPaging); ok {
					s.paging = paging
				}
			}
		}
	}
	return s, nil
}

// writeResult writes an LDAPResult-shaped response of type op with the
// result code code and the controls ctls.
func writeResult(w io.Writer, id int64, op ber.Tag, code int64, ctls ...ldap.Control) error {
	p := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, ""))
	res := ber.Encode(ber.ClassApplication, ber.TypeConstructed, op, nil, "")
	res.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, code, ""))
	res.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", ""))
	res.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", ""))
	p.AppendChild(res)
	if len(ctls) > 0 {
		cp := ber.Encode(ber.ClassContext, ber.TypeConstructed, 0, nil, "")
		for _, ctl := range ctls {
			cp.AppendChild(ctl.Encode())
		}
		p.AppendChild(cp)
	}
	_, err := w.Write(p.Bytes())
	return err
}

// writeEntry writes a search result entry for dn with attrs.
func writeEntry(w io.Writer, id int64, dn string, attrs ...*ldap.EntryAttribute) error {
	p := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, ""))
	e := ber.Encode(ber.ClassApplication, ber.TypeConstructed, ldap.ApplicationSearchResultEntry, nil, "")
	e.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, dn, ""))
	list := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "")
	for _, a := range attrs {
		pa := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "")
		pa.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, a.Name, ""))
		vals := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "")
		for _, v := range a.Values {
			vals.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, v, ""))
		}
		pa.AppendChild(vals)
		list.AppendChild(pa)
	}
	e.AppendChild(list)
	p.AppendChild(e)
	_, err := w.Write(p.Bytes())
	return err
}

// answerSearches replies to every request with an empty, successful search.
func answerSearches(conn net.Conn) {
	for {
		id, err := readRequest(conn)
		if err != nil || writeResult(conn, id, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess) != nil {
			return
		}
	}
}

// fakeProfile's search timeout leaves room for a large response under the
// race detector; a test about a timeout sets a short one itself.
func fakeProfile(url string) profile.Profile {
	return profile.Profile{Name: "fake", URL: url, ConnectTimeout: time.Second, SearchTimeout: 30 * time.Second}
}

// fakeConn connects anonymously to url without reading the root DSE, which
// the fake servers do not serve.
func fakeConn(t *testing.T, url string) *Conn {
	t.Helper()
	p := fakeProfile(url)
	cfg, err := p.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	lc, err := connect(context.Background(), p, cfg)
	if err != nil {
		t.Fatalf("connect(%s) error = %v", url, err)
	}
	c := &Conn{prof: p, lc: lc}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// selfSignedCert returns a certificate for host, an IP address or a DNS
// name, and its PEM encoding.
func selfSignedCert(t *testing.T, host string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ldaplens test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pemText
}

// TestDialUntrustedCertificate checks the certificate verification of
// ldaps:// and StartTLS: a certificate from an unknown CA, or from a trusted
// CA for another host name, fails with an UntrustedCertError holding the
// server's chain; a trusted CA, or insecureSkipVerify, connects.
func TestDialUntrustedCertificate(t *testing.T) {
	local, localPEM := selfSignedCert(t, "127.0.0.1")
	other, otherPEM := selfSignedCert(t, "ldap.example.org")
	// serve upgrades the connection to TLS with cert, after a StartTLS
	// request if startTLS is set, then answers every search with the root
	// DSE.
	serve := func(cert tls.Certificate, startTLS bool) func(int32, net.Conn) {
		serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		return func(_ int32, conn net.Conn) {
			if startTLS {
				id, err := readRequest(conn)
				if err != nil || writeResult(conn, id, ldap.ApplicationExtendedResponse, ldap.LDAPResultSuccess) != nil {
					return
				}
			}
			tc := tls.Server(conn, serverTLS)
			if tc.Handshake() != nil {
				return
			}
			for {
				id, err := readRequest(tc)
				if err != nil || writeEntry(tc, id, "") != nil ||
					writeResult(tc, id, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess) != nil {
					return
				}
			}
		}
	}
	tests := []struct {
		name          string
		cert          tls.Certificate
		caPEM         string
		insecure      bool
		wantUntrusted bool
	}{
		{name: "unknown CA", cert: local, wantUntrusted: true},
		{name: "trusted CA", cert: local, caPEM: localPEM},
		{name: "trusted CA, another host name", cert: other, caPEM: otherPEM, wantUntrusted: true},
		{name: "insecureSkipVerify", cert: local, insecure: true},
	}
	for _, startTLS := range []bool{false, true} {
		for _, tc := range tests {
			name := "ldaps: " + tc.name
			if startTLS {
				name = "StartTLS: " + tc.name
			}
			t.Run(name, func(t *testing.T) {
				url, accepts := fakeServer(t, serve(tc.cert, startTLS))
				p := fakeProfile(url)
				p.StartTLS, p.CAPEM, p.InsecureSkipVerify = startTLS, tc.caPEM, tc.insecure
				if !startTLS {
					p.URL = strings.Replace(url, "ldap://", "ldaps://", 1)
				}
				c, err := Dial(context.Background(), p, "")
				if err == nil {
					_ = c.Close()
				}
				if !tc.wantUntrusted {
					if err != nil {
						t.Errorf("Dial() error = %v, want a connection", err)
					}
					return
				}
				var untrusted *UntrustedCertError
				if !errors.As(err, &untrusted) {
					t.Fatalf("Dial() error = %v, want UntrustedCertError", err)
				}
				if len(untrusted.Certificates) != 1 || !untrusted.Certificates[0].Equal(&x509.Certificate{Raw: tc.cert.Certificate[0]}) {
					t.Errorf("UntrustedCertError.Certificates = %d certs, want the server's one", len(untrusted.Certificates))
				}
				if n := accepts.Load(); n != 1 {
					t.Errorf("Dial() opened %d connections, want 1", n)
				}
			})
		}
	}
}

// TestStartTLSRefusedSendsNothing checks that a StartTLS profile never falls
// back to cleartext: when the server refuses the StartTLS operation, Dial
// fails with the refusal and sends neither the bind nor a search, on that
// connection or on a new one.
func TestStartTLSRefusedSendsNothing(t *testing.T) {
	var after atomic.Int32 // requests the server got after the refusal
	done := make(chan struct{})
	url, accepts := fakeServer(t, func(n int32, conn net.Conn) {
		if n != 1 {
			return // a redial, hung up on at once and counted by accepts
		}
		defer close(done)
		id, err := readRequest(conn) // the StartTLS request
		if err != nil || writeResult(conn, id, ldap.ApplicationExtendedResponse, ldap.LDAPResultProtocolError) != nil {
			return
		}
		// A request that comes is counted and hung up on; Dial closing the
		// connection ends the read.
		if _, err := readRequest(conn); err == nil {
			after.Add(1)
		}
	})
	p := fakeProfile(url)
	p.StartTLS, p.BindDN = true, "cn=admin,dc=x"
	// go-ldap stops reading at the StartTLS response and reads again only
	// after a handshake, so a bind sent after the refusal can only time out:
	// a short timeout makes that regression fail fast.
	p.SearchTimeout = time.Second
	c, err := Dial(context.Background(), p, "secret")
	if err == nil {
		_ = c.Close()
	}
	if !ldap.IsErrorWithCode(err, ldap.LDAPResultProtocolError) {
		t.Errorf("Dial(StartTLS refused) error = %v, want the refusal, protocolError", err)
	}
	if n := accepts.Load(); n != 1 {
		t.Errorf("Dial(StartTLS refused) opened %d connections, want 1: no redial in cleartext", n)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection still open 5s after Dial(StartTLS refused), want it closed")
	}
	if n := after.Load(); n != 0 {
		t.Errorf("Dial(StartTLS refused) sent %d requests after the refusal, want none", n)
	}
}

// TestIdleConnectionSurvivesConnectTimeout checks that the connect-phase
// deadline does not outlive connect: an ldap://, ldaps:// or StartTLS
// connection idle for longer than the connect timeout still serves a search,
// without a redial.
func TestIdleConnectionSurvivesConnectTimeout(t *testing.T) {
	t.Parallel()
	cert, caPEM := selfSignedCert(t, "127.0.0.1")
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ldaps := func(_ int32, conn net.Conn) {
		tc := tls.Server(conn, serverTLS)
		if tc.Handshake() == nil {
			answerSearches(tc)
		}
	}
	startTLS := func(n int32, conn net.Conn) {
		id, err := readRequest(conn)
		if err != nil || writeResult(conn, id, ldap.ApplicationExtendedResponse, ldap.LDAPResultSuccess) != nil {
			return
		}
		ldaps(n, conn)
	}
	tests := []struct {
		name     string
		scheme   string
		startTLS bool
		serve    func(int32, net.Conn)
	}{
		{name: "ldap", scheme: "ldap", serve: func(_ int32, conn net.Conn) { answerSearches(conn) }},
		{name: "ldaps", scheme: "ldaps", serve: ldaps},
		{name: "StartTLS", scheme: "ldap", startTLS: true, serve: startTLS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			url, accepts := fakeServer(t, tc.serve)
			p := fakeProfile(strings.Replace(url, "ldap://", tc.scheme+"://", 1))
			p.StartTLS = tc.startTLS
			p.CAPEM = caPEM
			p.ConnectTimeout = 500 * time.Millisecond
			cfg, err := p.TLSConfig()
			if err != nil {
				t.Fatalf("TLSConfig() error = %v", err)
			}
			lc, err := connect(context.Background(), p, cfg)
			if err != nil {
				t.Fatalf("connect(%s) error = %v", p.URL, err)
			}
			c := &Conn{prof: p, lc: lc}
			defer c.Close()
			// A TLS handshake under -race on a busy CI runner can take over
			// 100 ms, so the budget is generous and the idle time well past it.
			time.Sleep(1500 * time.Millisecond)
			if lc.IsClosing() {
				t.Error("connection closed while idle, want it open past the connect timeout")
			}
			// The fake answers with no entries, which rawEntry reports as
			// noSuchObject.
			if _, err := c.rawEntry(context.Background(), "dc=x", nil); !ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
				t.Errorf("rawEntry() after idling error = %v, want noSuchObject", err)
			}
			if n := accepts.Load(); n != 1 {
				t.Errorf("rawEntry() after idling opened %d connections in total, want 1", n)
			}
		})
	}
}

// TestConnectTimeoutErrors pins how connect reports a server that stops
// answering. An ldaps handshake out of time is a context.DeadlineExceeded,
// the API's timeout, as tls.DialWithDialer reported it. go-ldap turns any
// StartTLS failure into ErrorNetwork text, and a StartTLS request timeout
// was ErrorNetwork before connect had a deadline.
func TestConnectTimeoutErrors(t *testing.T) {
	t.Parallel()
	silent := func(_ int32, conn net.Conn) { _, _ = io.Copy(io.Discard, conn) }
	stallHandshake := func(n int32, conn net.Conn) {
		id, err := readRequest(conn)
		if err != nil || writeResult(conn, id, ldap.ApplicationExtendedResponse, ldap.LDAPResultSuccess) != nil {
			return
		}
		silent(n, conn)
	}
	tests := []struct {
		name        string
		scheme      string
		startTLS    bool
		serve       func(int32, net.Conn)
		wantTimeout bool // errors.Is(err, context.DeadlineExceeded)
	}{
		{name: "ldaps handshake", scheme: "ldaps", serve: silent, wantTimeout: true},
		{name: "StartTLS response", scheme: "ldap", startTLS: true, serve: silent},
		{name: "StartTLS handshake", scheme: "ldap", startTLS: true, serve: stallHandshake},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url, _ := fakeServer(t, tc.serve)
			p := fakeProfile(strings.Replace(url, "ldap://", tc.scheme+"://", 1))
			p.StartTLS = tc.startTLS
			p.ConnectTimeout = 200 * time.Millisecond
			p.SearchTimeout = 10 * time.Second // the connect deadline must fire first
			cfg, err := p.TLSConfig()
			if err != nil {
				t.Fatalf("TLSConfig() error = %v", err)
			}
			// A socket deadline and a context timer that expire together can
			// fire in either order; enough attempts catch the losing order.
			// The bound catches a stall the connect timeout does not cover.
			errs := make([]error, 20)
			var wg sync.WaitGroup
			for i := range errs {
				wg.Go(func() {
					lc, err := connect(context.Background(), p, cfg)
					if err == nil {
						_ = lc.Close()
					}
					errs[i] = err
				})
			}
			if !returnsWithin(5*time.Second, wg.Wait) {
				t.Fatalf("connect(stalled %s) still blocked after 5s", tc.name)
			}
			for _, err := range errs {
				if !ldap.IsErrorWithCode(err, ldap.ErrorNetwork) || errors.Is(err, context.DeadlineExceeded) != tc.wantTimeout {
					t.Fatalf("connect(stalled %s) error = %v, want ErrorNetwork with context.DeadlineExceeded %v", tc.name, err, tc.wantTimeout)
				}
			}
		})
	}
}

// TestWriteToStalledServerTimesOut checks that a request the server does
// not read fails after the search timeout, and that the connection is then
// closed: a request cut off midway leaves the stream unusable.
func TestWriteToStalledServerTimesOut(t *testing.T) {
	t.Parallel()
	read, hungUp := make(chan struct{}), make(chan struct{})
	url, _ := fakeServer(t, func(_ int32, conn net.Conn) {
		<-read
		_, _ = io.Copy(io.Discard, conn)
		close(hungUp)
	})
	p := fakeProfile(url)
	p.SearchTimeout = 500 * time.Millisecond
	cfg, err := p.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	lc, err := connect(context.Background(), p, cfg)
	if err != nil {
		t.Fatalf("connect() error = %v", err)
	}
	defer lc.Close()
	// Larger than the socket buffers, so the write blocks on the server.
	req := ldap.NewModifyRequest("cn=x", nil)
	req.Replace("description", []string{strings.Repeat("x", 32<<20)})
	errc := make(chan error, 1)
	go func() { errc <- lc.Modify(req) }()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Modify(stalled server) error = nil, want a timeout")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Modify(stalled server) still blocked after 5s")
	}
	close(read)
	select {
	case <-hungUp:
	case <-time.After(5 * time.Second):
		t.Error("connection still open after a failed write, want it closed")
	}
}

func TestReadDoesNotRetryTimeout(t *testing.T) {
	t.Parallel()
	url, accepts := fakeServer(t, func(_ int32, conn net.Conn) { _, _ = io.Copy(io.Discard, conn) })
	c := fakeConn(t, url)
	c.prof.SearchTimeout = 200 * time.Millisecond
	c.lc.SetTimeout(c.prof.SearchTimeout)
	if _, err := c.rawEntry(context.Background(), "dc=x", nil); err == nil {
		t.Error("rawEntry(silent server) error = nil, want a timeout")
	}
	if n := accepts.Load(); n != 1 {
		t.Errorf("rawEntry(silent server) opened %d connections, want 1 (no redial on timeout)", n)
	}
}

func TestReadRetriesDroppedConnection(t *testing.T) {
	url, accepts := fakeServer(t, func(n int32, conn net.Conn) {
		if n == 1 {
			_, _ = readRequest(conn) // drop the connection mid-request
			return
		}
		answerSearches(conn)
	})
	c := fakeConn(t, url)
	// The retried search succeeds without entries, which rawEntry reports
	// as noSuchObject.
	if _, err := c.rawEntry(context.Background(), "dc=x", nil); !ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
		t.Errorf("rawEntry(after drop) error = %v, want noSuchObject from the retry", err)
	}
	if n := accepts.Load(); n != 2 {
		t.Errorf("rawEntry(after drop) opened %d connections, want 2", n)
	}
}

// TestRedialInvalidCredentialsCloses checks that a redial whose bind the
// directory refuses with invalid credentials, as after a password change,
// closes the Conn: the password is never sent again, so ldaplens cannot lock
// the account under a lockout policy. Any other failed bind leaves the Conn
// open, and the next read redials.
func TestRedialInvalidCredentialsCloses(t *testing.T) {
	tests := []struct {
		name       string
		bindCode   int64 // the redial's bind result; -1 hangs up instead
		wantClosed bool
	}{
		{name: "invalid credentials", bindCode: ldap.LDAPResultInvalidCredentials, wantClosed: true},
		{name: "unavailable", bindCode: ldap.LDAPResultUnavailable},
		{name: "network error", bindCode: -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var binds atomic.Int32
			url, _ := fakeServer(t, func(n int32, conn net.Conn) {
				if n == 1 {
					_, _ = readRequest(conn) // drop the connection mid-request
					return
				}
				id, err := readRequest(conn) // the bind
				if err != nil {
					return
				}
				binds.Add(1)
				if tc.bindCode >= 0 {
					_ = writeResult(conn, id, ldap.ApplicationBindResponse, tc.bindCode)
				}
			})
			c := fakeConn(t, url)
			c.prof.BindDN, c.password = "uid=user001,ou=people,dc=example,dc=org", "old-password"
			for i := range 4 {
				_, err := c.rawEntry(context.Background(), "dc=x", nil)
				switch {
				case !tc.wantClosed && (err == nil || errors.Is(err, net.ErrClosed)):
					t.Errorf("rawEntry() %d after the drop error = %v, want the bind's error", i+1, err)
				case tc.wantClosed && i == 0 && !ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials):
					t.Errorf("rawEntry() 1 after the drop error = %v, want invalid credentials", err)
				case tc.wantClosed && i > 0 && !errors.Is(err, net.ErrClosed):
					t.Errorf("rawEntry() %d after the drop error = %v, want net.ErrClosed", i+1, err)
				}
			}
			wantBinds := int32(4)
			if tc.wantClosed {
				wantBinds = 1
			}
			if n := binds.Load(); n != wantBinds {
				t.Errorf("4 reads after the drop made %d binds, want %d", n, wantBinds)
			}
			if got := c.Closed(); got != tc.wantClosed {
				t.Errorf("Closed() after the reads = %v, want %v", got, tc.wantClosed)
			}
		})
	}
}

func TestCloseIsFinal(t *testing.T) {
	url, accepts := fakeServer(t, func(_ int32, conn net.Conn) { answerSearches(conn) })
	c := fakeConn(t, url)
	// A round trip first, so the server has counted the connection.
	if _, err := c.rawEntry(context.Background(), "dc=x", nil); !ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
		t.Fatalf("rawEntry() error = %v, want noSuchObject", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := c.rawEntry(context.Background(), "dc=x", nil); !errors.Is(err, net.ErrClosed) {
		t.Errorf("rawEntry(after Close) error = %v, want net.ErrClosed", err)
	}
	if n := accepts.Load(); n != 1 {
		t.Errorf("rawEntry(after Close) opened %d connections in total, want 1", n)
	}
}

// returnsWithin reports whether f returns within d. A call that does not
// is left running.
func returnsWithin(d time.Duration, f func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestRedialDoesNotHoldLock(t *testing.T) {
	url, _ := fakeServer(t, func(_ int32, conn net.Conn) { answerSearches(conn) })
	c := fakeConn(t, url)
	ctx := context.Background()
	_ = c.lc.Close() // the shared connection is lost
	started, unblock := make(chan struct{}), make(chan struct{})
	var dialed *ldap.Conn
	c.dialFn = func(ctx context.Context, password string) (*ldap.Conn, error) {
		close(started)
		<-unblock
		lc, err := c.dial(ctx, password)
		dialed = lc
		return lc, err
	}
	errc := make(chan error, 1)
	go func() {
		_, err := c.current(ctx)
		errc <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(unblock)
		t.Fatal("current(lost connection) did not redial")
	}

	const limit = 5 * time.Second
	if !returnsWithin(limit, func() { _ = c.Close() }) {
		t.Errorf("Close() during a redial did not return within %v", limit)
	}

	close(unblock)
	if err := <-errc; !errors.Is(err, net.ErrClosed) {
		t.Errorf("current(closed during redial) error = %v, want net.ErrClosed", err)
	}
	if dialed == nil || !dialed.IsClosing() {
		t.Error("connection dialed while closing is still open, want it closed")
	}
	c.mu.Lock()
	installed := c.lc
	c.mu.Unlock()
	if installed != nil {
		t.Error("Conn holds a shared connection after Close, want none")
	}
}

func TestConcurrentRedialInstallsOne(t *testing.T) {
	url, _ := fakeServer(t, func(_ int32, conn net.Conn) { answerSearches(conn) })
	c := fakeConn(t, url)
	_ = c.lc.Close() // the shared connection is lost
	arrived, gate := make(chan struct{}, 2), make(chan struct{})
	var mu sync.Mutex
	var dialed []*ldap.Conn
	c.dialFn = func(ctx context.Context, password string) (*ldap.Conn, error) {
		arrived <- struct{}{}
		<-gate
		lc, err := c.dial(ctx, password)
		if err == nil {
			mu.Lock()
			dialed = append(dialed, lc)
			mu.Unlock()
		}
		return lc, err
	}
	type result struct {
		lc  *ldap.Conn
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			lc, err := c.current(context.Background())
			results <- result{lc, err}
		}()
	}
	for i := range 2 {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Errorf("%d of 2 concurrent redials were in flight together, want 2", i)
		}
	}
	close(gate)
	var got [2]*ldap.Conn
	for i := range got {
		r := <-results
		if r.err != nil {
			t.Fatalf("current(lost connection) error = %v", r.err)
		}
		got[i] = r.lc
	}

	c.mu.Lock()
	installed := c.lc
	c.mu.Unlock()
	if got[0] != installed || got[1] != installed {
		t.Error("concurrent current() returned a connection other than the installed one")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 2 {
		t.Fatalf("concurrent redials dialed %d connections, want 2", len(dialed))
	}
	if dialed[0] != installed && dialed[1] != installed {
		t.Fatal("installed connection is neither of the two dialed, want one of them")
	}
	extra := dialed[0]
	if extra == installed {
		extra = dialed[1]
	}
	if installed.IsClosing() {
		t.Error("installed connection is closed, want it open")
	}
	if !extra.IsClosing() {
		t.Error("extra dialed connection is open, want it closed")
	}
}

func TestSearchTimeLimit(t *testing.T) {
	tests := []struct {
		timeout time.Duration
		want    int
	}{
		{timeout: 30 * time.Second, want: 28},
		{timeout: 2500 * time.Millisecond, want: 1},
		{timeout: 200 * time.Millisecond, want: 1},
	}
	for _, tc := range tests {
		if got := searchTimeLimit(tc.timeout); got != tc.want {
			t.Errorf("searchTimeLimit(%v) = %d, want %d", tc.timeout, got, tc.want)
		}
	}
}

// TestPagedSearchClosesConnectionOnFirstPageError checks that a first page
// that fails, here on a context timeout against a silent server, does not
// hand its connection back to the shared slot: go-ldap sends no Abandon, so
// the paged search may still be running on it.
func TestPagedSearchClosesConnectionOnFirstPageError(t *testing.T) {
	t.Parallel()
	url, accepts := fakeServer(t, func(_ int32, conn net.Conn) { _, _ = io.Copy(io.Discard, conn) })
	c := fakeConn(t, url)
	c.info.Paging = true
	c.mu.Lock()
	first := c.lc
	c.mu.Unlock()
	search := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := c.Search(ctx, SearchRequest{BaseDN: "dc=x"})
		return err
	}
	if err := search(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Search(silent server) error = %v, want context.DeadlineExceeded", err)
	}
	c.mu.Lock()
	shared := c.lc
	c.mu.Unlock()
	if shared == first {
		t.Error("Search(silent server) put its connection back in the shared slot, want it closed")
	}
	if !first.IsClosing() {
		t.Error("Search(silent server) left its connection open, want it closed")
	}
	if err := search(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Search(silent server) error = %v, want context.DeadlineExceeded", err)
	}
	if n := accepts.Load(); n != 2 {
		t.Errorf("second Search() opened %d connections in total, want 2 (a new dial)", n)
	}
}

// serveChildren answers searches under dc=x with its n children cn=<i>,dc=x.
// A search with the paging control gets them a page at a time, with the
// offset of the next page as the cookie, and open is set while the server
// holds paged state: from a page with a cookie until the search ends or is
// abandoned with page size 0. A search without paging gets at most its size
// limit, then sizeLimitExceeded. Other bases (the schema) find nothing.
func serveChildren(n int, open *atomic.Bool) func(int32, net.Conn) {
	return func(_ int32, conn net.Conn) {
		for {
			s, err := readSearch(conn)
			if err != nil {
				return
			}
			from, to, code := 0, 0, int64(ldap.LDAPResultSuccess)
			var ctls []ldap.Control
			switch {
			case s.base != "dc=x":
			case s.paging == nil:
				to = n
				if s.sizeLimit > 0 && n > s.sizeLimit {
					to, code = s.sizeLimit, ldap.LDAPResultSizeLimitExceeded
				}
			default:
				from, _ = strconv.Atoi(string(s.paging.Cookie))
				to = from
				next := ldap.NewControlPaging(0)
				if s.paging.PagingSize > 0 {
					to = min(n, from+int(s.paging.PagingSize))
					if to < n {
						next.SetCookie([]byte(strconv.Itoa(to)))
					}
				}
				open.Store(len(next.Cookie) > 0)
				ctls = []ldap.Control{next}
			}
			for i := from; i < to; i++ {
				if writeEntry(conn, s.id, fmt.Sprintf("cn=%d,dc=x", i)) != nil {
					return
				}
			}
			if writeResult(conn, s.id, ldap.ApplicationSearchResultDone, code, ctls...) != nil {
				return
			}
		}
	}
}

// childrenConn connects to serveChildren(n) and returns the Conn, the
// server's paged state and its accepted connections.
func childrenConn(t *testing.T, n int, paging bool) (*Conn, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	open := new(atomic.Bool)
	url, accepts := fakeServer(t, serveChildren(n, open))
	c := fakeConn(t, url)
	c.info.Paging = paging
	return c, open, accepts
}

// checkChildren checks that entries are the first n children of dc=x, in
// order.
func checkChildren(t *testing.T, entries []Entry, n int) {
	t.Helper()
	if len(entries) != n {
		t.Errorf("got %d entries, want %d", len(entries), n)
		return
	}
	for i, e := range entries {
		if want := fmt.Sprintf("cn=%d,dc=x", i); e.DN != want {
			t.Errorf("entry %d = %s, want %s", i, e.DN, want)
			return
		}
	}
}

// TestSearchWalksPages checks that a search with more matches than its
// size limit, here the largest one, walks the pages up to the limit,
// reports more and abandons the paged search: no paged state outlives the
// request, and the next search runs on the same connection.
func TestSearchWalksPages(t *testing.T) {
	const n, limit = maxResponseEntries + 200, maxResponseEntries
	c, open, accepts := childrenConn(t, n, true)
	for i := range 2 {
		page, err := c.Search(context.Background(), SearchRequest{BaseDN: "dc=x", PageSize: 1000, SizeLimit: limit})
		if err != nil {
			t.Fatalf("Search(%d children, sizeLimit %d) %d error = %v", n, limit, i+1, err)
		}
		checkChildren(t, page.Entries, limit)
		if !page.More || page.Truncated {
			t.Errorf("Search(%d children, sizeLimit %d) %d = more %v, truncated %v; want more, not truncated", n, limit, i+1, page.More, page.Truncated)
		}
		if open.Load() {
			t.Errorf("after Search() %d the server holds paged state, want it abandoned", i+1)
		}
	}
	if n := accepts.Load(); n != 1 {
		t.Errorf("two searches opened %d connections, want 1 (no redial)", n)
	}
}

// TestSearchWalksToEnd checks that a search with fewer matches than its
// size limit returns all of them without more.
func TestSearchWalksToEnd(t *testing.T) {
	c, open, _ := childrenConn(t, 1200, true)
	page, err := c.Search(context.Background(), SearchRequest{BaseDN: "dc=x", SizeLimit: 5000})
	if err != nil {
		t.Fatalf("Search(1200 children, sizeLimit 5000) error = %v", err)
	}
	checkChildren(t, page.Entries, 1200)
	if page.More || page.Truncated {
		t.Errorf("Search(1200 children, sizeLimit 5000) = more %v, truncated %v; want neither", page.More, page.Truncated)
	}
	if open.Load() {
		t.Error("after Search() the server holds paged state, want none")
	}
}

// TestSearchNoPagingLimit checks that without paging the size limit goes to
// the server, and that sizeLimitExceeded at the limit means more rather
// than a search that stopped early.
func TestSearchNoPagingLimit(t *testing.T) {
	c, _, _ := childrenConn(t, 600, false)
	page, err := c.Search(context.Background(), SearchRequest{BaseDN: "dc=x", SizeLimit: 500})
	if err != nil {
		t.Fatalf("Search(600 children, no paging, sizeLimit 500) error = %v", err)
	}
	checkChildren(t, page.Entries, 500)
	if !page.More || page.Truncated {
		t.Errorf("Search(600 children, no paging, sizeLimit 500) = more %v, truncated %v; want more, not truncated", page.More, page.Truncated)
	}
}

func TestSearchCanceled(t *testing.T) {
	url, _ := fakeServer(t, func(_ int32, conn net.Conn) { answerSearches(conn) })
	c := fakeConn(t, url)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Search(ctx, SearchRequest{BaseDN: "dc=x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Search(canceled) error = %v, want context.Canceled", err)
	}
}

// TestAllDNsPassesMaxRefs checks that allDNs at the group index limit
// returns every match, past maxRefs and over pages, without truncation:
// the group list of Members(nested) is not held to the membership cap.
func TestAllDNsPassesMaxRefs(t *testing.T) {
	n := maxRefs + 2000
	c, _, _ := childrenConn(t, n, true)
	c.info.BaseDN = "dc=x"
	dns, truncated, err := c.allDNs(context.Background(), "(objectClass=*)", maxGroupIndex)
	if err != nil || len(dns) != n || truncated {
		t.Errorf("allDNs(%d entries, limit %d) = %d DNs truncated %v, %v; want %d not truncated",
			n, maxGroupIndex, len(dns), truncated, err, n)
	}
}

// TestDialDropReportsNetworkError checks that a server hanging up during
// Bind or a search yields an ErrorNetwork error rather than go-ldap's plain
// "unable to read LDAP response packet" text, which the API could not
// classify.
func TestDialDropReportsNetworkError(t *testing.T) {
	tests := []struct {
		name   string
		bindDN string // the request the server drops: Bind, else the root DSE search
	}{
		{name: "bind", bindDN: "cn=reader,dc=x"},
		{name: "search"},
	}
	for _, tc := range tests {
		url, _ := fakeServer(t, func(_ int32, conn net.Conn) { _, _ = readRequest(conn) })
		p := fakeProfile(url)
		p.BindDN = tc.bindDN
		_, err := Dial(context.Background(), p, "secret")
		if !ldap.IsErrorWithCode(err, ldap.ErrorNetwork) {
			t.Errorf("%s: Dial(server hangs up) error = %v (%T), want ErrorNetwork", tc.name, err, err)
		}
	}

	// A local configuration error is not a network error: the API keeps
	// it, and the file path it names, in the server log.
	p := fakeProfile("ldaps://127.0.0.1:1")
	p.CAFile = filepath.Join(t.TempDir(), "missing-ca.pem")
	var le *ldap.Error
	if _, err := Dial(context.Background(), p, ""); err == nil || errors.As(err, &le) {
		t.Errorf("Dial(missing CA file) error = %v, want a plain error, not *ldap.Error", err)
	}
}

// TestMembershipTruncated checks that a membership scan cut short by a
// server limit is reported as truncated, and that empty results are []
// rather than null in JSON. The fake server answers every search with the
// entry cn=g, which has no attributes, and the given result code.
func TestMembershipTruncated(t *testing.T) {
	tests := []struct {
		name         string
		code         int64
		wantMemberOf string
		wantMembers  string
	}{
		{name: "complete", code: ldap.LDAPResultSuccess, wantMemberOf: `{"refs":[{"dn":"cn=g"}]}`, wantMembers: `{"refs":[]}`},
		{
			name: "size limit", code: ldap.LDAPResultSizeLimitExceeded,
			wantMemberOf: `{"refs":[{"dn":"cn=g"}],"truncated":true}`, wantMembers: `{"refs":[],"truncated":true}`,
		},
	}
	for _, tc := range tests {
		url, _ := fakeServer(t, func(_ int32, conn net.Conn) {
			for {
				id, err := readRequest(conn)
				if err != nil || writeEntry(conn, id, "cn=g") != nil ||
					writeResult(conn, id, ldap.ApplicationSearchResultDone, tc.code) != nil {
					return
				}
			}
		})
		c := fakeConn(t, url)
		memberOf, err := c.MemberOf(context.Background(), "uid=a", false)
		if got, _ := json.Marshal(memberOf); err != nil || string(got) != tc.wantMemberOf {
			t.Errorf("%s: MemberOf() = %s, %v, want %s", tc.name, got, err, tc.wantMemberOf)
		}
		// Nested, so the group index scan runs and hits the limit.
		members, err := c.Members(context.Background(), "cn=g", true)
		if got, _ := json.Marshal(members); err != nil || string(got) != tc.wantMembers {
			t.Errorf("%s: Members(nested) = %s, %v, want %s", tc.name, got, err, tc.wantMembers)
		}
	}
}

// TestEmptyListsAreJSONArrays checks that the UI gets [] rather than null
// for an empty page, a masked attribute and an entry without attributes.
func TestEmptyListsAreJSONArrays(t *testing.T) {
	url, _ := fakeServer(t, func(_ int32, conn net.Conn) { answerSearches(conn) })
	page, err := fakeConn(t, url).Search(context.Background(), SearchRequest{BaseDN: "dc=x"})
	if err != nil {
		t.Fatalf("Search(empty) error = %v", err)
	}
	s := schema.Parse(nil, nil, nil)
	tests := []struct {
		name string
		v    any
		want string
	}{
		{name: "empty page", v: page, want: `"entries":[]`},
		{name: "masked attribute", v: convertAttribute("userPassword", [][]byte{[]byte("{SSHA}abc")}, s, false), want: `"values":[]`},
		{name: "entry without attributes", v: convertEntry(&ldap.Entry{DN: "cn=x"}, s, false), want: `"attributes":[]`},
	}
	for _, tc := range tests {
		got, err := json.Marshal(tc.v)
		if err != nil || !strings.Contains(string(got), tc.want) {
			t.Errorf("%s: json.Marshal() = %s, %v, want it to contain %s", tc.name, got, err, tc.want)
		}
	}
}

func TestIsEncrypted(t *testing.T) {
	tests := []struct {
		url      string
		startTLS bool
		want     bool
	}{
		{url: "ldap://dc:389", want: false},
		{url: "ldap://dc:389", startTLS: true, want: true},
		{url: "LDAPS://dc:636", want: true},
	}
	for _, tc := range tests {
		if got := isEncrypted(profile.Profile{URL: tc.url, StartTLS: tc.startTLS}); got != tc.want {
			t.Errorf("isEncrypted(%s, startTLS %v) = %v, want %v", tc.url, tc.startTLS, got, tc.want)
		}
	}
}

// TestSearchOnceCaps checks that a response with more than
// maxResponseEntries entries or maxResponseBytes value bytes ends early as
// truncated rather than as an error, and that the connection stays in use:
// the rest of the response is dropped and the next search runs on it.
func TestSearchOnceCaps(t *testing.T) {
	tests := []struct {
		name string
		// entries is what the server sends before the next request; one
		// more entry and the done message follow it.
		entries     int
		value       string
		wantEntries int
	}{
		{name: "entries", entries: maxResponseEntries + 1, value: "x", wantEntries: maxResponseEntries},
		// 2 MiB values: the 33rd would pass 64 MiB.
		{name: "value bytes", entries: 33, value: strings.Repeat("x", 2<<20), wantEntries: 32},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var firstID atomic.Int64
			url, accepts := fakeServer(t, func(n int32, conn net.Conn) {
				id, err := readRequest(conn)
				if err != nil || n > 1 {
					return
				}
				firstID.Store(id)
				for i := range tc.entries {
					if writeEntry(conn, id, fmt.Sprintf("cn=%d", i), attr("description", tc.value)) != nil {
						return
					}
				}
				// The rest of the first response arrives once the client
				// sent the next request, so after it let go of the first.
				next, err := readRequest(conn)
				if err != nil || writeEntry(conn, id, "cn=late", attr("description", tc.value)) != nil ||
					writeResult(conn, id, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess) != nil ||
					writeResult(conn, next, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess) != nil {
					return
				}
				answerSearches(conn)
			})
			c := fakeConn(t, url)
			search := func() (*result, error) {
				var res *result
				err := c.read(context.Background(), func(lc *ldap.Conn) error {
					var err error
					res, err = searchOnce(context.Background(), lc, &ldap.SearchRequest{BaseDN: "dc=x", Filter: "(objectClass=*)"})
					return err
				})
				return res, err
			}
			res, err := search()
			if err != nil {
				t.Fatalf("searchOnce(%d entries) error = %v, want none", tc.entries, err)
			}
			if len(res.entries) != tc.wantEntries || !res.truncated {
				t.Errorf("searchOnce(%d entries) = %d entries, truncated %v; want %d, truncated", tc.entries, len(res.entries), res.truncated, tc.wantEntries)
			}
			res, err = search()
			if err != nil || len(res.entries) != 0 || res.truncated {
				t.Errorf("next searchOnce() = %+v, %v; want an empty complete result", res, err)
			}
			if n := accepts.Load(); n != 1 {
				t.Errorf("searches opened %d connections, want 1 (no redial)", n)
			}
			// go-ldap notes a packet for a message it no longer tracks, and
			// drops it: the late entry found no response channel.
			c.mu.Lock()
			lc := c.lc
			c.mu.Unlock()
			want := fmt.Sprintf("unexpected message %d", firstID.Load())
			if err := lc.GetLastError(); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("GetLastError() = %v, want it to contain %q", err, want)
			}
		})
	}
}

// TestPagedSearchCapClosesConnection checks that a page cut by a response
// cap ends the walk, truncated and without more, and closes the connection
// it ran on, which no other request uses: the server is still sending that
// page and keeps its paged state.
func TestPagedSearchCapClosesConnection(t *testing.T) {
	url, _ := fakeServer(t, func(_ int32, conn net.Conn) {
		for {
			s, err := readSearch(conn)
			if err != nil {
				return
			}
			if s.base == "dc=x" {
				for i := range maxResponseEntries + 1 {
					if writeEntry(conn, s.id, fmt.Sprintf("cn=%d,dc=x", i)) != nil {
						return
					}
				}
			}
			// Other bases (the schema) find nothing.
			if writeResult(conn, s.id, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess) != nil {
				return
			}
		}
	})
	c := fakeConn(t, url)
	c.info.Paging = true
	c.mu.Lock()
	first := c.lc
	c.mu.Unlock()
	page, err := c.Search(context.Background(), SearchRequest{BaseDN: "dc=x", SizeLimit: maxResponseEntries})
	if err != nil {
		t.Fatalf("Search(%d entries) error = %v", maxResponseEntries+1, err)
	}
	if len(page.Entries) != maxResponseEntries || !page.Truncated || page.More {
		t.Errorf("Search(%d entries) = %d entries, truncated %v, more %v; want %d, truncated, no more",
			maxResponseEntries+1, len(page.Entries), page.Truncated, page.More, maxResponseEntries)
	}
	if !first.IsClosing() {
		t.Error("Search(capped page) left its connection open, want it closed")
	}
}

// TestMembersHardCap checks that a nested membership scan returns at most
// maxRefs refs, marked truncated, even when the groups of one level bring
// more than that together.
func TestMembersHardCap(t *testing.T) {
	// Members: cn=root holds cn=a and cn=b, which hold 6000 users each.
	members := map[string][]string{"cn=root": {"cn=a", "cn=b"}}
	for _, g := range members["cn=root"] {
		for i := range 6000 {
			members[g] = append(members[g], fmt.Sprintf("uid=%d,%s", i, g))
		}
	}
	serveMembers := func(_ int32, conn net.Conn) {
		for {
			s, err := readSearch(conn)
			if err != nil {
				return
			}
			if s.base == "" { // the group index under the empty base DN
				err = errors.Join(writeEntry(conn, s.id, "cn=a"), writeEntry(conn, s.id, "cn=b"))
			} else {
				err = writeEntry(conn, s.id, s.base, attr("member", members[s.base]...))
			}
			if err != nil || writeResult(conn, s.id, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess) != nil {
				return
			}
		}
	}
	// MemberOf: the entry is in 4 groups, and each of them in 3000 more.
	var scans atomic.Int32
	serveMemberOf := func(_ int32, conn net.Conn) {
		for {
			s, err := readSearch(conn)
			if err != nil {
				return
			}
			if s.base == "" { // a scan for the groups listing a member
				n := scans.Add(1)
				count := 3000
				if n == 1 {
					count = 4
				}
				for i := range count {
					if err = writeEntry(conn, s.id, fmt.Sprintf("cn=g%d-%d", n, i)); err != nil {
						break
					}
				}
			} else {
				err = writeEntry(conn, s.id, s.base)
			}
			if err != nil || writeResult(conn, s.id, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess) != nil {
				return
			}
		}
	}
	tests := []struct {
		name  string
		serve func(int32, net.Conn)
		scan  func(*Conn) (Membership, error)
	}{
		{name: "Members", serve: serveMembers, scan: func(c *Conn) (Membership, error) {
			return c.Members(context.Background(), "cn=root", true)
		}},
		{name: "MemberOf", serve: serveMemberOf, scan: func(c *Conn) (Membership, error) {
			return c.MemberOf(context.Background(), "uid=u", true)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url, _ := fakeServer(t, tc.serve)
			m, err := tc.scan(fakeConn(t, url))
			if err != nil {
				t.Fatalf("%s(nested) error = %v", tc.name, err)
			}
			if len(m.Refs) != maxRefs || !m.Truncated {
				t.Errorf("%s(nested) = %d refs, truncated %v; want %d, truncated", tc.name, len(m.Refs), m.Truncated, maxRefs)
			}
		})
	}
}

// TestMembershipDepthLimit checks that nested membership follows groups
// maxNestingDepth levels deep and marks the result truncated when groups are
// left past that depth. The fake directory is a chain: uid=u is a member of
// cn=g1, and each cn=gK is a member of cn=gK+1.
func TestMembershipDepthLimit(t *testing.T) {
	// member returns the one member of cn=gk.
	member := func(k int) string {
		if k == 1 {
			return "uid=u"
		}
		return fmt.Sprintf("cn=g%d", k-1)
	}
	tests := []struct {
		groups        int
		wantRefs      int
		wantTruncated bool
	}{
		{groups: 9, wantRefs: 9},
		{groups: 12, wantRefs: maxNestingDepth, wantTruncated: true},
	}
	for _, tc := range tests {
		url, _ := fakeServer(t, func(_ int32, conn net.Conn) {
			for {
				s, err := readSearch(conn)
				if err != nil {
					return
				}
				switch {
				case s.base == "uid=u":
					err = writeEntry(conn, s.id, s.base)
				case s.base != "": // a group read by Members
					k, _ := strconv.Atoi(strings.TrimPrefix(s.base, "cn=g"))
					err = writeEntry(conn, s.id, s.base, attr("member", member(k)))
				default: // the group index, or the groups that list one member
					_, rest, byMember := strings.Cut(s.filter, "(member=")
					for k := 1; k <= tc.groups && err == nil; k++ {
						if !byMember || strings.HasPrefix(rest, member(k)+")") {
							err = writeEntry(conn, s.id, fmt.Sprintf("cn=g%d", k))
						}
					}
				}
				if err != nil || writeResult(conn, s.id, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess) != nil {
					return
				}
			}
		})
		c := fakeConn(t, url)
		scans := []struct {
			name string
			scan func() (Membership, error)
		}{
			{name: "MemberOf", scan: func() (Membership, error) {
				return c.MemberOf(context.Background(), "uid=u", true)
			}},
			{name: "Members", scan: func() (Membership, error) {
				return c.Members(context.Background(), fmt.Sprintf("cn=g%d", tc.groups), true)
			}},
		}
		for _, sc := range scans {
			m, err := sc.scan()
			if err != nil {
				t.Errorf("%s(nested) in a chain of %d groups error = %v", sc.name, tc.groups, err)
				continue
			}
			if len(m.Refs) != tc.wantRefs || m.Truncated != tc.wantTruncated {
				t.Errorf("%s(nested) in a chain of %d groups = %d refs, truncated %v; want %d, truncated %v",
					sc.name, tc.groups, len(m.Refs), m.Truncated, tc.wantRefs, tc.wantTruncated)
			}
		}
	}
}

func TestBERPacketCap(t *testing.T) {
	if got, want := ber.MaxPacketLengthBytes, int64(64<<20); got != want {
		t.Errorf("ber.MaxPacketLengthBytes = %d, want %d", got, want)
	}
}

func TestInfoBindDN(t *testing.T) {
	url, _ := fakeServer(t, func(_ int32, conn net.Conn) {
		id, err := readRequest(conn) // the bind
		if err != nil || writeResult(conn, id, ldap.ApplicationBindResponse, ldap.LDAPResultSuccess) != nil {
			return
		}
		id, err = readRequest(conn) // the root DSE
		if err != nil || writeEntry(conn, id, "", attr("namingContexts", "dc=example,dc=org")) != nil ||
			writeResult(conn, id, ldap.ApplicationSearchResultDone, ldap.LDAPResultSuccess) != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn)
	})
	const dn = "uid=user001,ou=people,dc=example,dc=org"
	p := fakeProfile(url)
	p.BindDN = dn
	c, err := Dial(context.Background(), p, "secret001")
	if err != nil {
		t.Fatalf("Dial(%s) error = %v", dn, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if got := c.Info().BindDN; got != dn {
		t.Errorf("Dial(%s).Info().BindDN = %q, want %q", dn, got, dn)
	}
}

// TestMemberOfUnindexedAttribute checks MemberOf against a server that, like
// OpenDJ or 389 DS without the right privilege, refuses a search on an
// unindexed attribute (memberUid here) with result 50: the groups found by
// member and uniqueMember still come back, memberUid is reported as
// skipped, the connection does not ask for it again, and a server that
// refuses every attribute still fails with its own error.
func TestMemberOfUnindexedAttribute(t *testing.T) {
	var memberUidAsked atomic.Int32
	serve := func(refuseAll bool) func(int32, net.Conn) {
		return func(_ int32, conn net.Conn) {
			for {
				s, err := readSearch(conn)
				if err != nil {
					return
				}
				code := int64(ldap.LDAPResultSuccess)
				switch {
				case s.base != "": // the entry read for its uid values
					err = writeEntry(conn, s.id, s.base, &ldap.EntryAttribute{Name: "uid", Values: []string{"a"}})
				case strings.Contains(s.filter, "memberUid"):
					memberUidAsked.Add(1)
					code = ldap.LDAPResultInsufficientAccessRights
				case refuseAll:
					code = ldap.LDAPResultInsufficientAccessRights
				default: // one filter or an OR of them
					if strings.Contains(s.filter, "(member=uid=a)") {
						err = writeEntry(conn, s.id, "cn=g1")
					}
					if err == nil && strings.Contains(s.filter, "(uniqueMember=uid=a)") {
						err = writeEntry(conn, s.id, "cn=g2")
					}
				}
				if err != nil || writeResult(conn, s.id, ldap.ApplicationSearchResultDone, code) != nil {
					return
				}
			}
		}
	}

	url, _ := fakeServer(t, serve(false))
	c := fakeConn(t, url)
	for i := range 2 {
		m, err := c.MemberOf(context.Background(), "uid=a", false)
		if err != nil {
			t.Fatalf("MemberOf() call %d error = %v", i+1, err)
		}
		want := Membership{Refs: []Ref{{DN: "cn=g1"}, {DN: "cn=g2"}}, Skipped: []string{"memberUid"}}
		if diff := cmp.Diff(want, m); diff != "" {
			t.Errorf("MemberOf() call %d mismatch (-want +got):\n%s", i+1, diff)
		}
	}
	// The first call asks for memberUid in the combined filter, then alone;
	// the second call leaves it out.
	if got := memberUidAsked.Load(); got != 2 {
		t.Errorf("searches naming memberUid = %d, want 2", got)
	}

	url, _ = fakeServer(t, serve(true))
	_, err := fakeConn(t, url).MemberOf(context.Background(), "uid=a", false)
	if !ldap.IsErrorWithCode(err, ldap.LDAPResultInsufficientAccessRights) {
		t.Errorf("MemberOf() with every attribute refused error = %v, want result 50", err)
	}
}
