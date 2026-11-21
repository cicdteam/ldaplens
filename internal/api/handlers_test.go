// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package api

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/google/go-cmp/cmp"

	"github.com/cicdteam/ldaplens/internal/ldapx"
	"github.com/cicdteam/ldaplens/internal/profile"
	"github.com/cicdteam/ldaplens/internal/schema"
	"github.com/cicdteam/ldaplens/internal/session"
)

// The real connection must keep satisfying Directory.
var _ Directory = (*ldapx.Conn)(nil)

type directoryFake struct {
	info       ldapx.Info
	entry      ldapx.Entry
	err        error
	lastSearch ldapx.SearchRequest
	lastDN     string
	lastNested bool
	closed     bool
	modified   []string
	changes    map[string][]ldapx.Change
	modifyErr  map[string]error
	// onModify, when set, runs at the start of every Modify.
	onModify func(ctx context.Context, dn string)
	// writes records Add, Delete, Rename and SetPassword calls, one line each.
	writes   []string
	writeErr error
	// added is the attributes of the last Add.
	added []ldapx.TextAttribute
	// password is the last password SetPassword got; writes never holds it.
	password string
}

func (d *directoryFake) Close() error     { d.closed = true; return nil }
func (d *directoryFake) Closed() bool     { return d.closed }
func (d *directoryFake) Info() ldapx.Info { return d.info }
func (d *directoryFake) Schema(context.Context) (*schema.Schema, error) {
	return schema.Parse(nil, nil, nil), d.err
}

func (d *directoryFake) ReloadSchema(context.Context) (*schema.Schema, error) {
	return schema.Parse(nil, nil, nil), d.err
}

func (d *directoryFake) Search(_ context.Context, r ldapx.SearchRequest) (ldapx.Page, error) {
	d.lastSearch = r
	return ldapx.Page{}, d.err
}

func (d *directoryFake) Entry(_ context.Context, dn string) (ldapx.Entry, error) {
	d.lastDN = dn
	return d.entry, d.err
}

func (d *directoryFake) MemberOf(_ context.Context, dn string, nested bool) (ldapx.Membership, error) {
	d.lastDN, d.lastNested = dn, nested
	return ldapx.Membership{Refs: []ldapx.Ref{{DN: "cn=g"}}}, d.err
}

// Members reports a truncated empty result, as ldapx does when a server
// limit cut the group index short.
func (d *directoryFake) Members(_ context.Context, dn string, nested bool) (ldapx.Membership, error) {
	d.lastDN, d.lastNested = dn, nested
	return ldapx.Membership{Refs: []ldapx.Ref{}, Truncated: true}, d.err
}

func (d *directoryFake) Modify(ctx context.Context, dn string, changes []ldapx.Change) error {
	if d.onModify != nil {
		d.onModify(ctx, dn)
	}
	if err := d.modifyErr[dn]; err != nil {
		return err
	}
	d.modified = append(d.modified, dn)
	if d.changes == nil {
		d.changes = map[string][]ldapx.Change{}
	}
	d.changes[dn] = changes
	return nil
}

func (d *directoryFake) Add(_ context.Context, dn string, attrs []ldapx.TextAttribute) error {
	d.added = attrs
	names := make([]string, len(attrs))
	for i, a := range attrs {
		names[i] = a.Name
	}
	d.writes = append(d.writes, fmt.Sprintf("add %s %v", dn, names))
	return d.writeErr
}

func (d *directoryFake) Delete(_ context.Context, dn string) error {
	d.writes = append(d.writes, "delete "+dn)
	return d.writeErr
}

func (d *directoryFake) Rename(_ context.Context, dn, newRDN, newParent string, deleteOld bool) error {
	d.writes = append(d.writes, fmt.Sprintf("rename %s %s %s deleteOld=%v", dn, newRDN, newParent, deleteOld))
	return d.writeErr
}

func (d *directoryFake) SetPassword(_ context.Context, dn, password, method string) error {
	d.password = password
	d.writes = append(d.writes, fmt.Sprintf("password %s %s", dn, method))
	return d.writeErr
}

type testEnv struct {
	h        http.Handler
	dir      *directoryFake
	dialErr  error
	password string          // password of the last dial
	dialed   profile.Profile // profile of the last dial
	dials    int
	cookies  []*http.Cookie
	// header is added to every request; remote, when set, is its
	// RemoteAddr instead of httptest's 192.0.2.1:1234.
	header http.Header
	remote string
	// opts is what h was built from.
	opts Options
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWith(t, Options{})
}

// newTestEnvWith is newTestEnv with extra options. It always sets Catalog
// and Sessions, and Dial unless opts has one; the testEnv fields about the
// last dial are then unused.
func newTestEnvWith(t *testing.T, opts Options) *testEnv {
	t.Helper()
	t.Setenv("LDAPLENS_TEST_PW", "from-env")
	cat, err := profile.NewCatalog([]profile.Profile{
		{Name: "dev", URL: "ldap://dev:389", BindDN: "cn=admin,dc=example,dc=org", PasswordEnv: "LDAPLENS_TEST_PW"}, //nolint:gosec // env var name, not a credential
		{Name: "manual", URL: "ldap://manual:389", StartTLS: true, BindDN: "cn=admin,dc=example,dc=org", ReadOnly: true, InsecureSkipVerify: true, Color: "red"},
		{Name: "anon", URL: "ldap://anon:389"},
		{Name: "people", URL: "ldap://people:389", BindDNTemplate: "uid={user},ou=people,dc=example,dc=org"},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	env := &testEnv{dir: &directoryFake{info: ldapx.Info{BaseDN: "dc=example,dc=org"}}}
	opts.Catalog = cat
	opts.Sessions = session.New[Directory](time.Hour, 8*time.Hour)
	opts.Shared = session.New[Directory](time.Hour, 8*time.Hour)
	if opts.Dial == nil {
		opts.Dial = func(_ context.Context, p profile.Profile, password string) (Directory, error) {
			env.password, env.dialed = password, p
			env.dials++
			if env.dialErr != nil {
				return nil, env.dialErr
			}
			env.dir.info.BindDN = p.BindDN // as ldapx.Dial reports it
			return env.dir, nil
		}
	}
	env.h, env.opts = New(opts), opts
	return env
}

// do sends a same-origin request to localhost, replaying cookies set by
// earlier responses. A cookie replaces an earlier one of the same name, as
// in a browser.
func (e *testEnv) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.doContext(context.Background(), t, method, target, body)
}

// doContext is do with a request context.
func (e *testEnv) doContext(ctx context.Context, t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(ctx, method, target, r)
	req.Host = "localhost:8080"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range e.header {
		req.Header[k] = v
	}
	if e.remote != "" {
		req.RemoteAddr = e.remote
	}
	for _, c := range e.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		e.cookies = slices.DeleteFunc(e.cookies, func(old *http.Cookie) bool { return old.Name == c.Name })
		e.cookies = append(e.cookies, c)
	}
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return e.Code
}

func TestConnectUsesConfiguredPassword(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"dev"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("connect = %d %s, want 200", rec.Code, rec.Body)
	}
	if env.password != "from-env" {
		t.Errorf("dial password = %q, want the configured one", env.password)
	}
	if rec := env.do(t, http.MethodGet, "/api/c/dev/info", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"baseDN":"dc=example,dc=org"`) {
		t.Errorf("info after connect = %d %s, want 200 with baseDN", rec.Code, rec.Body)
	}
}

func TestConnectErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		dialErr    error
		wantStatus int
		wantCode   string
	}{
		{name: "password required", body: `{"profile":"manual"}`, wantStatus: http.StatusBadRequest, wantCode: "password_required"},
		{name: "unknown profile", body: `{"profile":"nope"}`, wantStatus: http.StatusNotFound, wantCode: "profile_not_found"},
		{
			name: "bad credentials", body: `{"profile":"manual","password":"x"}`,
			dialErr: ldap.NewError(ldap.LDAPResultInvalidCredentials, errors.New("data 775")), wantStatus: http.StatusUnauthorized, wantCode: "invalid_credentials",
		},
		{
			name: "untrusted cert", body: `{"profile":"anon"}`,
			dialErr: &ldapx.UntrustedCertError{Err: x509.UnknownAuthorityError{}}, wantStatus: http.StatusUnprocessableEntity, wantCode: "tls_untrusted",
		},
		{name: "unknown field", body: `{"profile":"dev","extra":1}`, wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
	}
	for _, tc := range tests {
		env := newTestEnv(t)
		env.dialErr = tc.dialErr
		rec := env.do(t, http.MethodPost, "/api/connect", tc.body)
		if rec.Code != tc.wantStatus || errorCode(t, rec) != tc.wantCode {
			t.Errorf("%s: connect = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantCode)
		}
	}
}

// TestConnectHidesPasswordSource checks that a missing configured password
// is logged with its source but the client never sees the env var name.
func TestConnectHidesPasswordSource(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err := os.Unsetenv("LDAPLENS_TEST_PW"); err != nil { // t.Setenv restores it
		t.Fatal(err)
	}
	rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"dev"}`)
	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "password_unavailable" || strings.Contains(rec.Body.String(), "LDAPLENS_TEST_PW") {
		t.Errorf("connect without env password = %d %s, want 500 password_unavailable without the env var name", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), "LDAPLENS_TEST_PW") {
		t.Errorf("server log = %q, want the env var name", logs.String())
	}
}

// TestInternalErrorHidesDetails checks that an unexpected dial error, here
// a missing CA file, is logged but its path never reaches the client.
func TestInternalErrorHidesDetails(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	caFile := filepath.Join(t.TempDir(), "missing-ca.pem")
	if _, env.dialErr = (profile.Profile{Name: "dev", URL: "ldaps://dev:636", CAFile: caFile}).TLSConfig(); env.dialErr == nil {
		t.Fatal("TLSConfig() with a missing CA file succeeded")
	}
	rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"dev"}`)
	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal" || strings.Contains(rec.Body.String(), caFile) {
		t.Errorf("connect with missing CA file = %d %s, want 500 internal without the path", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), caFile) {
		t.Errorf("server log = %q, want the CA file path", logs.String())
	}
}

// TestInfoCarriesProfileFlags checks that connect and info both return the
// profile settings the workspace shows as badges and in its status bar.
func TestInfoCarriesProfileFlags(t *testing.T) {
	type flags struct {
		BaseDN             string `json:"baseDN"`
		URL                string `json:"url"`
		StartTLS           bool   `json:"startTLS"`
		ReadOnly           bool   `json:"readOnly"`
		InsecureSkipVerify bool   `json:"insecureSkipVerify"`
		Color              string `json:"color"`
	}
	want := flags{BaseDN: "dc=example,dc=org", URL: "ldap://manual:389", StartTLS: true, ReadOnly: true, InsecureSkipVerify: true, Color: "red"}
	check := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s, want 200", name, rec.Code, rec.Body)
		}
		var got flags
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: decode %q: %v", name, rec.Body, err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s response mismatch (-want +got):\n%s", name, diff)
		}
	}
	env := newTestEnv(t)
	check("connect", env.do(t, http.MethodPost, "/api/connect", `{"profile":"manual","password":"pw"}`))
	check("info", env.do(t, http.MethodGet, "/api/c/manual/info", ""))
}

func TestNotConnected(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodGet, "/api/c/manual/info", "")
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "not_connected" {
		t.Errorf("info without connect = %d %s, want 401 not_connected", rec.Code, rec.Body)
	}
}

func connected(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	if rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"dev"}`); rec.Code != http.StatusOK {
		t.Fatalf("connect = %d %s", rec.Code, rec.Body)
	}
	return env
}

func TestEntryPassesDNVerbatim(t *testing.T) {
	env := connected(t)
	dn := `cn=Smith\, John/Jr Ünal,ou=people,dc=example,dc=org`
	rec := env.do(t, http.MethodGet, "/api/c/dev/entry?dn="+url.QueryEscape(dn), "")
	if rec.Code != http.StatusOK || env.dir.lastDN != dn {
		t.Errorf("entry = %d, dn %q, want 200 and %q", rec.Code, env.dir.lastDN, dn)
	}
	if rec := env.do(t, http.MethodGet, "/api/c/dev/entry", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("entry without dn = %d, want 400", rec.Code)
	}
}

func TestSearchDefaultsBase(t *testing.T) {
	env := connected(t)
	if rec := env.do(t, http.MethodPost, "/api/c/dev/search", `{"filter":"(cn=x)"}`); rec.Code != http.StatusOK {
		t.Fatalf("search = %d %s", rec.Code, rec.Body)
	}
	if env.dir.lastSearch.BaseDN != "dc=example,dc=org" || env.dir.lastSearch.Filter != "(cn=x)" {
		t.Errorf("search request = %+v, want base from Info and the given filter", env.dir.lastSearch)
	}
}

// TestReadRouteErrors checks that the entry and search routes answer a
// directory error or a refused body with its own status and code.
func TestReadRouteErrors(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name: "entry: no such object", method: http.MethodGet, path: "/api/c/dev/entry?dn=cn%3Dx",
			err:        ldap.NewError(ldap.LDAPResultNoSuchObject, errors.New("")),
			wantStatus: http.StatusNotFound, wantCode: "no_such_object",
		},
		{
			name: "search: bad filter", method: http.MethodPost, path: "/api/c/dev/search", body: `{"filter":"(cn=x"}`,
			err:        ldap.NewError(ldap.ErrorFilterCompile, errors.New("unexpected end of filter")),
			wantStatus: http.StatusBadRequest, wantCode: "bad_filter",
		},
		{
			name: "search: secret filter", method: http.MethodPost, path: "/api/c/dev/search", body: `{"filter":"(userPassword=x*)"}`,
			err:        fmt.Errorf("%w: userPassword", ldapx.ErrSecretFilter),
			wantStatus: http.StatusBadRequest, wantCode: "secret_filter",
		},
		// A search answers in one request: a paging cursor from a client is
		// an unknown field.
		{
			name: "search: unknown field", method: http.MethodPost, path: "/api/c/dev/search", body: `{"filter":"(cn=x)","cursor":"SOMEID"}`,
			wantStatus: http.StatusBadRequest, wantCode: "bad_request",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := connected(t)
			env.dir.err = tc.err
			rec := env.do(t, tc.method, tc.path, tc.body)
			if rec.Code != tc.wantStatus || errorCode(t, rec) != tc.wantCode {
				t.Errorf("%s %s = %d %s, want %d %s", tc.method, tc.path, rec.Code, rec.Body, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

func TestMembership(t *testing.T) {
	env := connected(t)
	rec := env.do(t, http.MethodGet, "/api/c/dev/memberof?dn=uid%3Da&nested=true", "")
	want := `{"refs":[{"dn":"cn=g"}]}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want || env.dir.lastDN != "uid=a" || !env.dir.lastNested {
		t.Errorf("memberof = %d %s dn %q nested %v, want 200 %s uid=a true", rec.Code, rec.Body, env.dir.lastDN, env.dir.lastNested, want)
	}
	rec = env.do(t, http.MethodGet, "/api/c/dev/members?dn=cn%3Dg", "")
	want = `{"refs":[],"truncated":true}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want || env.dir.lastNested {
		t.Errorf("members = %d %s nested %v, want 200 %s false", rec.Code, rec.Body, env.dir.lastNested, want)
	}
}

func TestDisconnectClosesConnection(t *testing.T) {
	env := newTestEnv(t)
	connectTo(t, env, `{"profile":"manual","password":"pw"}`)
	if rec := env.do(t, http.MethodPost, "/api/disconnect", `{"profile":"manual"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("disconnect = %d", rec.Code)
	}
	if !env.dir.closed {
		t.Error("disconnect did not close the directory")
	}
	if rec := env.do(t, http.MethodGet, "/api/c/manual/info", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("info after disconnect = %d, want 401", rec.Code)
	}
}

func TestProfilesNeverExposeSecrets(t *testing.T) {
	env := connected(t)
	rec := env.do(t, http.MethodGet, "/api/profiles", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "from-env") || strings.Contains(body, "LDAPLENS_TEST_PW") {
		t.Fatalf("profiles = %d %s, want 200 without password or env name", rec.Code, body)
	}
	type flags struct {
		Name          string `json:"name"`
		ReadOnly      bool   `json:"readOnly"`
		Editable      bool   `json:"editable"`
		HasPassword   bool   `json:"hasPassword"`
		Connected     bool   `json:"connected"`
		UserLogin     bool   `json:"userLogin"`
		NeedsPassword bool   `json:"needsPassword"`
	}
	var got []flags
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []flags{
		{Name: "dev", HasPassword: true, Connected: true},
		{Name: "manual", ReadOnly: true, NeedsPassword: true},
		{Name: "anon"},
		{Name: "people", UserLogin: true, NeedsPassword: true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("profiles flags mismatch (-want +got):\n%s", diff)
	}
}

func TestSaveProfile(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodPost, "/api/profiles", `{"name":"lab","url":"ldap://lab:389"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"editable":true`) {
		t.Errorf("save = %d %s, want 200 editable", rec.Code, rec.Body)
	}
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "file reference", body: `{"name":"x","url":"ldap://h","passwordFile":"/etc/passwd"}`, wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "fixed name", body: `{"name":"dev","url":"ldap://h"}`, wantStatus: http.StatusConflict, wantCode: "profile_fixed"},
		{name: "invalid", body: `{"name":"x","url":"http://h"}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_profile"},
		{name: "too large", body: `{"name":"big","url":"ldap://h","baseDN":"dc=` + strings.Repeat("a", profile.MaxProfileBytes) + `"}`, wantStatus: http.StatusConflict, wantCode: "profile_limit"},
	}
	for _, tc := range tests {
		rec := env.do(t, http.MethodPost, "/api/profiles", tc.body)
		if rec.Code != tc.wantStatus || errorCode(t, rec) != tc.wantCode {
			t.Errorf("%s: save = %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantCode)
		}
	}
}

func TestServerModeRefusesSaving(t *testing.T) {
	env := newTestEnvWith(t, Options{Server: true})
	rec := env.do(t, http.MethodPost, "/api/profiles", `{"name":"lab","url":"ldap://lab:389"}`)
	var got apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	want := apiError{Code: "saving_disabled", Message: "this server keeps no saved connections"}
	if rec.Code != http.StatusForbidden {
		t.Errorf("save in server mode = %d, want 403", rec.Code)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("save in server mode body mismatch (-want +got):\n%s", diff)
	}
}

// TestSaveWithoutDataDir checks that local mode without a data dir refuses a
// save with the status server mode gives it.
func TestSaveWithoutDataDir(t *testing.T) {
	cat, err := profile.NewCatalog(nil, "")
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	o := newTestEnv(t).opts
	o.Catalog = cat
	env := &testEnv{h: New(o)}
	rec := env.do(t, http.MethodPost, "/api/profiles", `{"name":"lab","url":"ldap://lab:389"}`)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "saving_disabled" {
		t.Errorf("save without a data dir = %d %s, want 403 saving_disabled", rec.Code, rec.Body)
	}
}

// TestServerModeProfileView checks that server mode tells the UI only what
// its connect page needs, nothing about where or how a profile binds.
func TestServerModeProfileView(t *testing.T) {
	env := newTestEnvWith(t, Options{Server: true})
	rec := env.do(t, http.MethodGet, "/api/profiles", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("profiles = %d %s, want 200", rec.Code, rec.Body)
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	want := []map[string]any{
		{"name": "dev", "readOnly": false, "connected": false, "userLogin": false, "needsPassword": false},
		{"name": "manual", "color": "red", "readOnly": true, "connected": false, "userLogin": false, "needsPassword": true},
		{"name": "anon", "readOnly": false, "connected": false, "userLogin": false, "needsPassword": false},
		{"name": "people", "readOnly": false, "connected": false, "userLogin": true, "needsPassword": true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("profiles mismatch (-want +got):\n%s", diff)
	}
}

// TestServerModeConnectErrors checks that in server mode a failed connect
// tells the browser nothing about the directory: any LDAP result is a
// generic invalid_credentials, and a network or TLS failure keeps its code
// with a generic message. The log keeps the full error, and local mode
// answers as errorResponse does, AD's reason included.
func TestServerModeConnectErrors(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	const typed = `{"profile":"manual","password":"wrong"}`
	invalid := map[string]any{"code": "invalid_credentials", "message": "invalid username or password"}
	tests := []struct {
		name string
		body string // the connect request
		err  error  // the dial's
		// logged is in the log in both modes.
		logged string
		// wantStatus and want are server mode's answer.
		wantStatus int
		want       map[string]any
		// localReason is the reason local mode decodes.
		localReason string
	}{
		{
			name: "AD account locked out", body: typed,
			err:    ldap.NewError(ldap.LDAPResultInvalidCredentials, errors.New("80090308: LdapErr: DSID-0C09044E, comment: AcceptSecurityContext error, data 775, v4563")),
			logged: "data 775", wantStatus: http.StatusUnauthorized, want: invalid, localReason: "account locked out",
		},
		{
			name: "shared profile, configured password refused", body: `{"profile":"dev"}`,
			err:    ldap.NewError(ldap.LDAPResultInvalidCredentials, errors.New("80090308: LdapErr: DSID-0C09044E, comment: AcceptSecurityContext error, data 52e, v4563")),
			logged: "data 52e", wantStatus: http.StatusUnauthorized, want: invalid, localReason: "invalid credentials",
		},
		{
			name: "389-DS retry limit", body: typed,
			err:    ldap.NewError(ldap.LDAPResultConstraintViolation, errors.New("Exceed password retry limit. Please try later.")),
			logged: "Exceed password retry limit", wantStatus: http.StatusUnauthorized, want: invalid,
		},
		{
			name: "389-DS inactivated account", body: typed,
			err:    ldap.NewError(ldap.LDAPResultUnwillingToPerform, errors.New("Account inactivated. Contact system administrator.")),
			logged: "Account inactivated", wantStatus: http.StatusUnauthorized, want: invalid,
		},
		{
			name: "network", body: typed,
			err:    ldap.NewError(ldap.ErrorNetwork, errors.New("dial tcp: lookup ldap.corp.internal: no such host")),
			logged: "ldap.corp.internal", wantStatus: http.StatusServiceUnavailable,
			want: map[string]any{"code": "network", "message": "cannot reach the directory server. Try again in a moment."},
		},
		{
			name: "untrusted certificate", body: typed,
			err:    &ldapx.UntrustedCertError{Certificates: []*x509.Certificate{srv.Certificate()}, Err: x509.UnknownAuthorityError{}},
			logged: "untrusted server certificate", wantStatus: http.StatusUnprocessableEntity,
			want: map[string]any{"code": "tls_untrusted", "message": "the directory server's certificate is not trusted"},
		},
	}
	connect := func(t *testing.T, server bool, body string, dialErr error) (int, map[string]any, string) {
		t.Helper()
		var logs bytes.Buffer
		env := newTestEnvWith(t, Options{Server: server, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
		env.dialErr = dialErr
		rec := env.do(t, http.MethodPost, "/api/connect", body)
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", rec.Body, err)
		}
		return rec.Code, got, logs.String()
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, got, logs := connect(t, true, tc.body, tc.err)
			if status != tc.wantStatus {
				t.Errorf("server mode: connect = %d, want %d", status, tc.wantStatus)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("server mode: connect body mismatch (-want +got):\n%s", diff)
			}
			if !strings.Contains(logs, tc.logged) {
				t.Errorf("server mode: log = %q, want %q in it", logs, tc.logged)
			}

			wantStatus, e := errorResponse(tc.err)
			b, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			status, got, logs = connect(t, false, tc.body, tc.err)
			if status != wantStatus {
				t.Errorf("local mode: connect = %d, want %d", status, wantStatus)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("local mode: connect body mismatch (-want +got):\n%s", diff)
			}
			if reason, _ := got["reason"].(string); reason != tc.localReason {
				t.Errorf("local mode: reason = %q, want %q", reason, tc.localReason)
			}
			if !strings.Contains(logs, tc.logged) {
				t.Errorf("local mode: log = %q, want %q in it", logs, tc.logged)
			}
		})
	}
}

// TestCookieScope checks that the token and session cookies share the
// base path and the SecureCookies setting.
func TestCookieScope(t *testing.T) {
	tests := []struct {
		name       string
		opts       Options
		wantPath   string
		wantSecure bool
	}{
		{name: "default", wantPath: "/"},
		{name: "base path", opts: Options{BasePath: "/ldap"}, wantPath: "/ldap/"},
		{name: "secure cookies", opts: Options{SecureCookies: true}, wantPath: "/", wantSecure: true},
	}
	for _, tc := range tests {
		tc.opts.AuthToken = "tok"
		env := newTestEnvWith(t, tc.opts)
		prefix := strings.TrimSuffix(tc.wantPath, "/")
		env.do(t, http.MethodGet, prefix+"/?token=tok", "")
		if rec := env.do(t, http.MethodPost, prefix+"/api/connect", `{"profile":"manual","password":"pw"}`); rec.Code != http.StatusOK {
			t.Fatalf("%s: connect = %d %s, want 200", tc.name, rec.Code, rec.Body)
		}
		for _, name := range []string{tokenCookie, sessionCookie} {
			var c *http.Cookie
			for _, ec := range env.cookies {
				if ec.Name == name {
					c = ec
				}
			}
			if c == nil {
				t.Errorf("%s: no %s cookie set", tc.name, name)
				continue
			}
			if c.Path != tc.wantPath || c.Secure != tc.wantSecure {
				t.Errorf("%s: cookie %s Path, Secure = %q, %v, want %q, %v", tc.name, name, c.Path, c.Secure, tc.wantPath, tc.wantSecure)
			}
		}
	}
}

func TestWiring(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "10.0.0.5:8080" // kubelet probes use the pod IP
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("healthz from pod IP = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/profiles", nil)
	req.Host = "10.0.0.5:8080"
	rec = httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("api from unknown host = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader(`{"profile":"dev"}`))
	req.Host = "localhost:8080"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec = httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader("profile=dev"))
	req.Host = "localhost:8080"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form POST = %d, want 415", rec.Code)
	}
}

// jsonLogs decodes the lines a slog.JSONHandler wrote.
func jsonLogs(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.Lines(logs.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines
}

// withMsg is the lines whose msg is msg.
func withMsg(lines []map[string]any, msg string) []map[string]any {
	var found []map[string]any
	for _, l := range lines {
		if l["msg"] == msg {
			found = append(found, l)
		}
	}
	return found
}

func TestWriteLogsAttribution(t *testing.T) {
	const (
		token    = "attribution-token-not-logged" //nolint:gosec // test value, not a credential
		typedPW  = "typed-password-not-logged"
		bindDN   = "cn=admin,dc=example,dc=org"
		remote   = "192.0.2.1" // httptest.NewRequest's RemoteAddr, without the port
		applyOne = `{"entries":[{"dn":"uid=a,dc=example,dc=org","changes":[{"op":"add","attr":"description","values":["secret-value"]}]}]}`
	)
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{AuthToken: token, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	env.do(t, http.MethodGet, "/?token="+token, "")

	env.dialErr = ldap.NewError(ldap.LDAPResultInvalidCredentials, errors.New("data 52e"))
	if rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"manual","password":"`+typedPW+`"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("connect with bad credentials = %d %s, want 401", rec.Code, rec.Body)
	}
	env.dialErr = nil
	connectTo(t, env, `{"profile":"dev","password":"`+typedPW+`"}`)
	for _, w := range []struct{ path, body string }{
		{path: "/api/c/dev/changes/apply", body: applyOne},
		{path: "/api/c/dev/entries/add", body: addBody},
		{path: "/api/c/dev/entries/rename", body: renameBody},
		{path: "/api/c/dev/entries/password", body: setBody},
		{path: "/api/c/dev/entries/delete", body: deleteBody},
	} {
		if rec := env.do(t, http.MethodPost, w.path, w.body); rec.Code != http.StatusOK {
			t.Fatalf("POST %s = %d %s, want 200", w.path, rec.Code, rec.Body)
		}
	}
	env.dir.writeErr = ldap.NewError(ldap.LDAPResultNotAllowedOnNonLeaf, errors.New("subordinate objects must be deleted first"))
	if rec := env.do(t, http.MethodPost, "/api/c/dev/entries/delete", deleteBody); rec.Code == http.StatusOK {
		t.Fatalf("failing delete = %d %s, want an error", rec.Code, rec.Body)
	}
	env.dir.modifyErr = map[string]error{"uid=a,dc=example,dc=org": ldap.NewError(ldap.LDAPResultConstraintViolation, errors.New("no"))}
	if rec := env.do(t, http.MethodPost, "/api/c/dev/changes/apply", applyOne); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"failed"`) {
		t.Fatalf("failing apply = %d %s, want 200 with a failed entry", rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodPost, "/api/disconnect", `{"profile":"dev"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("disconnect = %d %s, want 204", rec.Code, rec.Body)
	}

	lines := jsonLogs(t, &logs)
	for _, msg := range []string{"connect failed", "connected", "modified", "added", "renamed", "password set", "deleted", "delete rejected", "modify rejected", "disconnected"} {
		found := withMsg(lines, msg)
		if len(found) == 0 {
			t.Errorf("no %q log line in:\n%s", msg, logs.String())
			continue
		}
		for _, l := range found {
			if _, ok := l["session"].(string); !ok || l["remote"] != remote || l["bindDN"] != bindDN {
				t.Errorf("%q line = %v, want session, remote %q and bindDN %q", msg, l, remote, bindDN)
			}
		}
	}
	// A connect starts a new session; its line carries the new ID's
	// fingerprint, as the writes after it do.
	if c, m := withMsg(lines, "connected"), withMsg(lines, "modified"); len(c) > 0 && len(m) > 0 && (c[0]["session"] == "" || c[0]["session"] != m[0]["session"]) {
		t.Errorf("connected session = %q, want the modified line's %q", c[0]["session"], m[0]["session"])
	}
	for _, msg := range []string{"modified", "modify rejected"} {
		for _, l := range withMsg(lines, msg) {
			if diff := cmp.Diff([]any{"description"}, l["attrs"]); diff != "" {
				t.Errorf("%q attrs mismatch (-want +got):\n%s", msg, diff)
			}
		}
	}

	var sessionID string
	for _, c := range env.cookies {
		if c.Name == sessionCookie {
			sessionID = c.Value
		}
	}
	if sessionID == "" {
		t.Fatal("no session cookie set")
	}
	for _, secret := range []string{sessionID, typedPW, "from-env", "secret-value", token} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log contains %q:\n%s", secret, logs.String())
		}
	}
}

func TestSaveProfileLogged(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	for _, body := range []string{
		`{"name":"lab","url":"ldap://lab:389","readOnly":true}`,
		`{"name":"lab","url":"ldap://evil.example:389","insecureSkipVerify":true}`,
	} {
		if rec := env.do(t, http.MethodPost, "/api/profiles", body); rec.Code != http.StatusOK {
			t.Fatalf("save %s = %d %s, want 200", body, rec.Code, rec.Body)
		}
	}
	got := withMsg(jsonLogs(t, &logs), "profile saved")
	for _, l := range got {
		for _, k := range []string{"time", "level", "msg", "session", "remote"} {
			delete(l, k)
		}
	}
	want := []map[string]any{
		{"name": "lab", "oldURL": "", "url": "ldap://lab:389", "readOnly": true, "insecureSkipVerify": false},
		{"name": "lab", "oldURL": "ldap://lab:389", "url": "ldap://evil.example:389", "readOnly": false, "insecureSkipVerify": true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("profile saved lines mismatch (-want +got):\n%s", diff)
	}
}

func TestBadTokenLogged(t *testing.T) {
	const token = "bad-token-test-real-token"
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{AuthToken: token, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	req := httptest.NewRequest(http.MethodGet, "/api/profiles", nil)
	req.Host = "localhost:8080"
	req.Header.Set("Authorization", "Bearer wrong-token-not-logged")
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthorized" {
		t.Fatalf("bad token = %d %s, want 401 unauthorized", rec.Code, rec.Body)
	}
	got := withMsg(jsonLogs(t, &logs), "auth failed")
	if len(got) != 1 || got[0]["remote"] != "192.0.2.1" {
		t.Errorf("auth failed lines = %v, want one with remote 192.0.2.1, httptest's client", got)
	}
	for _, secret := range []string{token, "wrong-token-not-logged"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log contains %q:\n%s", secret, logs.String())
		}
	}
}

// cookie is the value of the named cookie env replays, or "".
func (e *testEnv) cookie(name string) string {
	for _, c := range e.cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// connectedNames is the names /api/profiles reports as connected.
func connectedNames(t *testing.T, env *testEnv) []string {
	t.Helper()
	rec := env.do(t, http.MethodGet, "/api/profiles", "")
	var views []struct {
		Name      string `json:"name"`
		Connected bool   `json:"connected"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("decode profiles %q: %v", rec.Body, err)
	}
	var names []string
	for _, v := range views {
		if v.Connected {
			names = append(names, v.Name)
		}
	}
	slices.Sort(names)
	return names
}

func TestConnectRotatesSession(t *testing.T) {
	env := newTestEnv(t)
	connectTo(t, env, `{"profile":"manual","password":"pw"}`)
	first, firstCookies := env.cookie(sessionCookie), slices.Clone(env.cookies)
	connectTo(t, env, `{"profile":"people","username":"user001","password":"pw"}`)
	second := env.cookie(sessionCookie)
	if first == "" || second == "" || second == first {
		t.Fatalf("session cookie after two connects = %q, then %q, want two different IDs", first, second)
	}
	if diff := cmp.Diff([]string{"manual", "people"}, connectedNames(t, env)); diff != "" {
		t.Errorf("connected profiles with the new cookie mismatch (-want +got):\n%s", diff)
	}

	env.cookies = firstCookies
	if rec := env.do(t, http.MethodGet, "/api/c/manual/info", ""); rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "not_connected" {
		t.Errorf("info with the first cookie = %d %s, want 401 not_connected", rec.Code, rec.Body)
	}
}

// TestConnectLogsPrevious checks that a connected line ties the new session
// to the one the request came with.
func TestConnectLogsPrevious(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	connectTo(t, env, `{"profile":"manual","password":"pw"}`)
	connectTo(t, env, `{"profile":"people","username":"user001","password":"pw"}`)
	var got []map[string]any
	for _, l := range withMsg(jsonLogs(t, &logs), "connected") {
		got = append(got, map[string]any{"session": l["session"], "previous": l["previous"]})
	}
	if len(got) != 2 {
		t.Fatalf("connected lines = %v, want 2", got)
	}
	first, second := got[0]["session"], got[1]["session"]
	want := []map[string]any{
		{"session": first, "previous": ""},
		{"session": second, "previous": first},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("connected lines mismatch (-want +got):\n%s", diff)
	}
	if first == "" || first == second {
		t.Errorf("connected sessions = %q, then %q, want two different fingerprints", first, second)
	}
}

func TestConnectFailureCreatesNoSession(t *testing.T) {
	env := newTestEnv(t)
	env.dialErr = ldap.NewError(ldap.ErrorNetwork, errors.New("refused"))
	if rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"manual","password":"pw"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("connect with a failing dial = %d %s, want 503", rec.Code, rec.Body)
	}
	if id := env.cookie(sessionCookie); id != "" {
		t.Errorf("connect with a failing dial set session cookie %q, want none", id)
	}
}

func TestConnectTooManyConnections(t *testing.T) {
	env := newTestEnv(t)
	var want []string
	for i := range session.MaxConns {
		name := fmt.Sprintf("p%d", i)
		if rec := env.do(t, http.MethodPost, "/api/profiles", `{"name":"`+name+`","url":"ldap://`+name+`:389","bindDN":"cn=admin"}`); rec.Code != http.StatusOK {
			t.Fatalf("save %s = %d %s", name, rec.Code, rec.Body)
		}
		connectTo(t, env, `{"profile":"`+name+`","password":"pw"}`)
		want = append(want, name)
	}

	env.dir = &directoryFake{}
	rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"manual","password":"pw"}`)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "too_many_connections" {
		t.Errorf("connect %d = %d %s, want 409 too_many_connections", session.MaxConns+1, rec.Code, rec.Body)
	}
	if !env.dir.closed {
		t.Error("refused connect did not close the dialed directory")
	}
	if diff := cmp.Diff(want, connectedNames(t, env)); diff != "" {
		t.Errorf("connected profiles after the refused connect mismatch (-want +got):\n%s", diff)
	}
}

// userDN is what the test catalog's template profiles bind as for user001.
const userDN = "uid=user001,ou=people,dc=example,dc=org"

func TestConnectTemplate(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"people","username":"user001","password":"secret001"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("connect = %d %s, want 200", rec.Code, rec.Body)
	}
	if env.dialed.BindDN != userDN || env.password != "secret001" {
		t.Errorf("dial bindDN, password = %q, %q, want %q, the typed password", env.dialed.BindDN, env.password, userDN)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"connect": rec,
		"info":    env.do(t, http.MethodGet, "/api/c/people/info", ""),
	} {
		var got struct {
			BindDN string `json:"bindDN"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.BindDN != userDN {
			t.Errorf("%s = %d %s, want bindDN %q", name, rec.Code, rec.Body, userDN)
		}
	}
}

// TestConnectTemplateRefusals checks that a template profile never binds,
// anonymously or as a half-filled DN, without a valid username and a
// password.
func TestConnectTemplateRefusals(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "no username", body: `{"profile":"people","password":"pw"}`, wantStatus: http.StatusBadRequest, wantCode: "username_required"},
		{name: "invalid username", body: `{"profile":"people","username":"a,ou=x","password":"pw"}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_username"},
		{name: "no password", body: `{"profile":"people","username":"user001"}`, wantStatus: http.StatusBadRequest, wantCode: "password_required"},
		{name: "empty password", body: `{"profile":"people","username":"user001","password":""}`, wantStatus: http.StatusBadRequest, wantCode: "password_required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			rec := env.do(t, http.MethodPost, "/api/connect", tc.body)
			if rec.Code != tc.wantStatus || errorCode(t, rec) != tc.wantCode {
				t.Errorf("connect %s = %d %s, want %d %s", tc.body, rec.Code, rec.Body, tc.wantStatus, tc.wantCode)
			}
			if env.dials != 0 {
				t.Errorf("connect %s dialed %d times as %q, want no dial", tc.body, env.dials, env.dialed.BindDN)
			}
			if strings.Contains(rec.Body.String(), "a,ou=x") {
				t.Errorf("connect %s = %s, want no username in the error", tc.body, rec.Body)
			}
		})
	}
}

// TestWriteLogsResolvedBindDN checks that a template connection's lines
// carry the DN it bound as, not the template's empty bindDN.
func TestWriteLogsResolvedBindDN(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	connectTo(t, env, `{"profile":"people","username":"user001","password":"secret001"}`)
	body := `{"entries":[{"dn":"uid=a,dc=example,dc=org","changes":[{"op":"add","attr":"description","values":["x"]}]}]}`
	if rec := env.do(t, http.MethodPost, "/api/c/people/changes/apply", body); rec.Code != http.StatusOK {
		t.Fatalf("apply = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodPost, "/api/disconnect", `{"profile":"people"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("disconnect = %d %s, want 204", rec.Code, rec.Body)
	}
	lines := jsonLogs(t, &logs)
	for _, msg := range []string{"connected", "modified", "disconnected"} {
		found := withMsg(lines, msg)
		if len(found) != 1 || found[0]["bindDN"] != userDN {
			t.Errorf("%q lines = %v, want one with bindDN %q", msg, found, userDN)
		}
	}
	if strings.Contains(logs.String(), "secret001") {
		t.Errorf("log contains the password:\n%s", logs.String())
	}
}

// TestWriteLogsBoundDN checks that the lines of a connection carry the DN
// it bound as even after its profile was edited to an anonymous one: they
// name the DN the connection bound as, not the catalog's.
func TestWriteLogsBoundDN(t *testing.T) {
	const dn = "cn=admin,dc=example,dc=org"
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if rec := env.do(t, http.MethodPost, "/api/profiles", `{"name":"lab","url":"ldap://lab:389","bindDN":"`+dn+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("save lab = %d %s, want 200", rec.Code, rec.Body)
	}
	connectTo(t, env, `{"profile":"lab","password":"pw"}`)
	if rec := env.do(t, http.MethodPost, "/api/profiles", `{"name":"lab","url":"ldap://lab:389"}`); rec.Code != http.StatusOK {
		t.Fatalf("edit lab = %d %s, want 200", rec.Code, rec.Body)
	}
	body := `{"entries":[{"dn":"uid=a,dc=example,dc=org","changes":[{"op":"add","attr":"description","values":["x"]}]}]}`
	if rec := env.do(t, http.MethodPost, "/api/c/lab/changes/apply", body); rec.Code != http.StatusOK {
		t.Fatalf("apply = %d %s, want 200", rec.Code, rec.Body)
	}
	env.dir.modifyErr = map[string]error{"uid=a,dc=example,dc=org": ldap.NewError(ldap.LDAPResultInsufficientAccessRights, errors.New("no"))}
	if rec := env.do(t, http.MethodPost, "/api/c/lab/changes/apply", body); rec.Code != http.StatusOK {
		t.Fatalf("failing apply = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodPost, "/api/disconnect", `{"profile":"lab"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("disconnect = %d %s, want 204", rec.Code, rec.Body)
	}
	lines := jsonLogs(t, &logs)
	for _, msg := range []string{"modified", "modify rejected", "disconnected"} {
		found := withMsg(lines, msg)
		if len(found) != 1 || found[0]["bindDN"] != dn {
			t.Errorf("%q lines = %v, want one with bindDN %s", msg, found, dn)
		}
	}
}

// invalidCredentials is the error ldapx.Dial returns for a wrong password:
// go-ldap's *ldap.Error as the server sent it, here Active Directory's.
var invalidCredentials = ldap.NewError(ldap.LDAPResultInvalidCredentials,
	errors.New("80090308: LdapErr: DSID-0C09044E, comment: AcceptSecurityContext error, data 52e, v4563"))

// retryAfter is rec's Retry-After header in seconds, or -1 if it is not a
// whole number.
func retryAfter(rec *httptest.ResponseRecorder) int {
	n, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil {
		return -1
	}
	return n
}

// TestConnectThrottled checks that 5 wrong passwords for one bind DN refuse
// the 6th connect without dialing, for every connect with a typed password
// and only for those.
func TestConnectThrottled(t *testing.T) {
	const people = `{"profile":"people","username":"user001","password":"wrong"}`
	tests := []struct {
		name          string
		body          string
		dialErr       error
		wantThrottled bool
	}{
		{name: "template", body: people, dialErr: invalidCredentials, wantThrottled: true},
		{name: "wrapped error", body: people, dialErr: fmt.Errorf("connect: %w", invalidCredentials), wantThrottled: true},
		{name: "fixed bindDN, typed password", body: `{"profile":"manual","password":"wrong"}`, dialErr: invalidCredentials, wantThrottled: true},
		{name: "network error", body: people, dialErr: ldap.NewError(ldap.ErrorNetwork, errors.New("connection refused"))},
		{name: "configured password", body: `{"profile":"dev"}`, dialErr: invalidCredentials},
		{name: "anonymous", body: `{"profile":"anon","password":"wrong"}`, dialErr: invalidCredentials},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.dialErr = tc.dialErr
			for i := range 5 {
				if rec := env.do(t, http.MethodPost, "/api/connect", tc.body); rec.Code == http.StatusTooManyRequests {
					t.Fatalf("connect %d of %s = %d %s, want a dial", i+1, tc.body, rec.Code, rec.Body)
				}
			}
			rec := env.do(t, http.MethodPost, "/api/connect", tc.body)
			if !tc.wantThrottled {
				if rec.Code == http.StatusTooManyRequests || env.dials != 6 {
					t.Errorf("6th connect of %s = %d %s after %d dials, want 6 dials and no 429", tc.body, rec.Code, rec.Body, env.dials)
				}
				return
			}
			if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
				t.Errorf("6th connect of %s = %d %s, want 429 too_many_attempts", tc.body, rec.Code, rec.Body)
			}
			if got := retryAfter(rec); got < 1 || got > 60 {
				t.Errorf("6th connect of %s Retry-After = %q, want whole seconds in 1..60", tc.body, rec.Header().Get("Retry-After"))
			}
			if env.dials != 5 {
				t.Errorf("6th connect of %s: %d dials, want 5", tc.body, env.dials)
			}
		})
	}
}

// TestConnectThrottlePerAccount checks that the limit is per bind DN,
// ignoring case, and that a refused connect is logged.
func TestConnectThrottlePerAccount(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	env.dialErr = invalidCredentials
	for range 5 {
		env.do(t, http.MethodPost, "/api/connect", `{"profile":"people","username":"user001","password":"secret-not-logged"}`)
	}
	for _, username := range []string{"user001", "USER001"} {
		if rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"people","username":"`+username+`","password":"pw"}`); rec.Code != http.StatusTooManyRequests {
			t.Errorf("connect as %s after 5 failures of user001 = %d %s, want 429", username, rec.Code, rec.Body)
		}
	}
	// The same account as a saved profile spells it: another spelling of
	// the DN gets no fresh tries.
	if rec := env.do(t, http.MethodPost, "/api/profiles", `{"name":"alias","url":"ldap://people:389","bindDN":"UID=User001, ou=People,dc=example,dc=org"}`); rec.Code != http.StatusOK {
		t.Fatalf("save alias = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"alias","password":"pw"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("connect to alias of user001 after 5 failures of user001 = %d %s, want 429", rec.Code, rec.Body)
	}
	env.do(t, http.MethodPost, "/api/connect", `{"profile":"people","username":"user002","password":"pw"}`)
	if env.dials != 6 || env.dialed.BindDN != "uid=user002,ou=people,dc=example,dc=org" {
		t.Errorf("connect as user002: %d dials, last as %q, want 6, the last as user002", env.dials, env.dialed.BindDN)
	}
	found := withMsg(jsonLogs(t, &logs), "connect throttled")
	if len(found) != 3 || found[0]["bindDN"] != userDN || found[0]["remote"] != "192.0.2.1" || found[0]["limit"] != "account" {
		t.Errorf(`"connect throttled" lines = %v, want 3, the first with bindDN %q, remote 192.0.2.1 and limit account`, found, userDN)
	}
	if strings.Contains(logs.String(), "secret-not-logged") {
		t.Errorf("log contains the password:\n%s", logs.String())
	}
}

// TestConnectThrottlePerServer checks that the limit counts one bind DN per
// server, however the profile's URL spells it, and that a success on another
// server, such as one a saved profile points at, does not clear the count.
func TestConnectThrottlePerServer(t *testing.T) {
	env := newTestEnv(t)
	env.dialErr = invalidCredentials
	for range 5 {
		env.do(t, http.MethodPost, "/api/connect", `{"profile":"people","username":"user001","password":"wrong"}`)
	}
	for _, p := range []string{
		`{"name":"same","url":"ldap://PEOPLE","bindDN":"` + userDN + `"}`,
		`{"name":"other","url":"ldap://other:389","bindDN":"` + userDN + `"}`,
	} {
		if rec := env.do(t, http.MethodPost, "/api/profiles", p); rec.Code != http.StatusOK {
			t.Fatalf("save %s = %d %s, want 200", p, rec.Code, rec.Body)
		}
	}
	if rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"same","password":"pw"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("connect as %s to ldap://PEOPLE after 5 failures on ldap://people:389 = %d %s, want 429", userDN, rec.Code, rec.Body)
	}
	env.dialErr = nil
	if rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"other","password":"pw"}`); rec.Code != http.StatusOK {
		t.Errorf("connect as %s to ldap://other:389 after 5 failures on ldap://people:389 = %d %s, want 200", userDN, rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"people","username":"user001","password":"pw"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("connect as user001 to ldap://people:389 after a success on ldap://other:389 = %d %s, want 429", rec.Code, rec.Body)
	}
}

// connectAs connects env to the people profile as username with a wrong
// password and returns the response.
func connectAs(t *testing.T, env *testEnv, username string) *httptest.ResponseRecorder {
	t.Helper()
	return env.do(t, http.MethodPost, "/api/connect", `{"profile":"people","username":"`+username+`","password":"wrong"}`)
}

// TestConnectThrottledPerClient checks that 20 wrong passwords from one
// client, each for another account, refuse its 21st connect without
// dialing, that the refusal is logged, and that another client is still
// admitted.
func TestConnectThrottledPerClient(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnvWith(t, Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	env.dialErr = invalidCredentials
	for i := range 20 {
		if rec := connectAs(t, env, fmt.Sprintf("user%03d", i+1)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("connect %d as another account = %d %s, want 401", i+1, rec.Code, rec.Body)
		}
	}
	rec := connectAs(t, env, "user021")
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
		t.Errorf("21st connect from one client = %d %s, want 429 too_many_attempts", rec.Code, rec.Body)
	}
	if got := retryAfter(rec); got < 1 || got > 60 {
		t.Errorf("21st connect from one client Retry-After = %q, want whole seconds in 1..60", rec.Header().Get("Retry-After"))
	}
	if env.dials != 20 {
		t.Errorf("21 connects from one client: %d dials, want 20", env.dials)
	}
	found := withMsg(jsonLogs(t, &logs), "connect throttled")
	if len(found) != 1 || found[0]["remote"] != "192.0.2.1" || found[0]["limit"] != "client" {
		t.Errorf(`"connect throttled" lines = %v, want one with remote 192.0.2.1 and limit client`, found)
	}
	env.remote = "198.51.100.1:1234"
	if rec := connectAs(t, env, "user022"); rec.Code != http.StatusUnauthorized {
		t.Errorf("connect from another client = %d %s, want 401", rec.Code, rec.Body)
	}
}

// TestConnectSuccessKeepsClientCount checks that a successful connect does
// not clear its client's failures, so that one valid account cannot reset
// the count between guesses at others.
func TestConnectSuccessKeepsClientCount(t *testing.T) {
	env := newTestEnv(t)
	env.dialErr = invalidCredentials
	for i := range 19 {
		connectAs(t, env, fmt.Sprintf("user%03d", i+1))
	}
	env.dialErr = nil
	if rec := env.do(t, http.MethodPost, "/api/connect", `{"profile":"people","username":"user020","password":"right"}`); rec.Code != http.StatusOK {
		t.Fatalf("connect as user020 after 19 failures = %d %s, want 200", rec.Code, rec.Body)
	}
	env.dialErr = invalidCredentials
	if rec := connectAs(t, env, "user021"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("connect after 19 failures and a success = %d %s, want 401", rec.Code, rec.Body)
	}
	if rec := connectAs(t, env, "user022"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("connect after 20 failures and a success = %d %s, want 429", rec.Code, rec.Body)
	}
}

// TestConnectUnreachableNotCounted checks that a connect failing for
// another reason than a wrong password does not count against its client.
func TestConnectUnreachableNotCounted(t *testing.T) {
	env := newTestEnv(t)
	env.dialErr = ldap.NewError(ldap.ErrorNetwork, errors.New("connection refused"))
	for i := range 25 {
		if rec := connectAs(t, env, fmt.Sprintf("user%03d", i+1)); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("connect %d to an unreachable server = %d %s, want a dial", i+1, rec.Code, rec.Body)
		}
	}
	env.dialErr = invalidCredentials
	for i := range 20 {
		if rec := connectAs(t, env, fmt.Sprintf("user%03d", i+26)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d after 25 unreachable = %d %s, want 401", i+1, rec.Code, rec.Body)
		}
	}
	if rec := connectAs(t, env, "user046"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("connect after 20 wrong passwords = %d %s, want 429", rec.Code, rec.Body)
	}
}

// TestConnectClientsBehindProxy checks that two clients behind one trusted
// proxy, told apart by X-Forwarded-For, are counted apart.
func TestConnectClientsBehindProxy(t *testing.T) {
	// httptest's remote, 192.0.2.1, is the proxy.
	env := newTestEnvWith(t, Options{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}})
	env.dialErr = invalidCredentials
	env.header = http.Header{"X-Forwarded-For": {"203.0.113.7"}}
	for i := range 20 {
		connectAs(t, env, fmt.Sprintf("user%03d", i+1))
	}
	if rec := connectAs(t, env, "user021"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("21st connect from 203.0.113.7 = %d %s, want 429", rec.Code, rec.Body)
	}
	env.header = http.Header{"X-Forwarded-For": {"203.0.113.8"}}
	if rec := connectAs(t, env, "user022"); rec.Code != http.StatusUnauthorized {
		t.Errorf("connect from 203.0.113.8 through the same proxy = %d %s, want 401", rec.Code, rec.Body)
	}
}

// TestConnectPerAccountRefusalNotCounted checks that a connect the
// per-account limit refuses does not count against its client.
func TestConnectPerAccountRefusalNotCounted(t *testing.T) {
	env := newTestEnv(t)
	env.dialErr = invalidCredentials
	for range 5 {
		connectAs(t, env, "user001")
	}
	for range 30 {
		if rec := connectAs(t, env, "user001"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("connect as user001 after 5 failures = %d %s, want 429", rec.Code, rec.Body)
		}
	}
	for i := range 15 {
		if rec := connectAs(t, env, fmt.Sprintf("user%03d", i+2)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("connect %d as another account after 5 failures and 30 refusals = %d %s, want 401", i+1, rec.Code, rec.Body)
		}
	}
	if rec := connectAs(t, env, "user017"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("connect after 20 failures from one client = %d %s, want 429", rec.Code, rec.Body)
	}
}

// TestConnectSuccessResetsThrottle checks that a successful connect clears
// the failures before it.
func TestConnectSuccessResetsThrottle(t *testing.T) {
	const body = `{"profile":"people","username":"user001","password":"pw"}`
	env := newTestEnv(t)
	env.dialErr = invalidCredentials
	for range 4 {
		env.do(t, http.MethodPost, "/api/connect", body)
	}
	env.dialErr = nil
	if rec := env.do(t, http.MethodPost, "/api/connect", body); rec.Code != http.StatusOK {
		t.Fatalf("5th connect = %d %s, want 200", rec.Code, rec.Body)
	}
	env.dialErr = invalidCredentials
	for range 4 {
		env.do(t, http.MethodPost, "/api/connect", body)
	}
	if rec := env.do(t, http.MethodPost, "/api/connect", body); rec.Code == http.StatusTooManyRequests {
		t.Errorf("connect after 4 failures, a success and 4 failures = %d %s, want a dial", rec.Code, rec.Body)
	}
}

// TestConnectConcurrentBurst checks that connects in flight count against
// the limit: of 10 connects at once as one DN, whose binds all wait and
// then fail, at most 5 reach the server and the rest get 429.
func TestConnectConcurrentBurst(t *testing.T) {
	const body = `{"profile":"people","username":"user001","password":"wrong"}`
	release := make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	defer releaseAll() // unblocks the dials if the test fails early
	var dials atomic.Int32
	dialed := make(chan struct{}, 10)
	env := newTestEnvWith(t, Options{Dial: func(context.Context, profile.Profile, string) (Directory, error) {
		dials.Add(1)
		dialed <- struct{}{}
		<-release
		return nil, invalidCredentials
	}})
	// post is env.do without cookies, which is not safe for concurrent use.
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/connect", strings.NewReader(body))
		req.Host = "localhost:8080"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		env.h.ServeHTTP(rec, req)
		return rec
	}
	recs := make(chan *httptest.ResponseRecorder, 10)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { recs <- post() })
	}
	// Every connect either waits in a dial or has been refused before any
	// dial ends.
	throttled := 0
	for range 10 {
		select {
		case <-dialed:
		case rec := <-recs:
			if rec.Code == http.StatusTooManyRequests {
				throttled++
			}
			// Refused only for connects in flight, which end soon.
			if ra := rec.Header().Get("Retry-After"); rec.Code == http.StatusTooManyRequests && ra != "1" {
				t.Errorf("connect refused during the burst: Retry-After %q, want 1", ra)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("10 connects at once: %d dials and %d 429s after 10 s, want all 10 settled", dials.Load(), throttled)
		}
	}
	releaseAll()
	wg.Wait()
	if got := dials.Load(); got > 5 || int(got)+throttled != 10 {
		t.Errorf("10 connects at once: %d dials, %d 429s before any dial ended, want at most 5 dials and 429 for the rest", got, throttled)
	}
	if rec := post(); rec.Code != http.StatusTooManyRequests {
		t.Errorf("connect after the burst failed = %d %s, want 429", rec.Code, rec.Body)
	}
}
