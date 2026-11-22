// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProbes checks that a kubelet finds the probes at the root and under
// any base path, without a token and by pod IP, while the API under the
// same Host stays refused.
func TestProbes(t *testing.T) {
	const podHost = "10.0.0.5:8080"
	for _, base := range []string{"", "/ldap", "/ldap/"} {
		h := New(Options{BasePath: base, AuthToken: "abcXYZ0129._~-ok"}) //nolint:gosec // test value, not a credential
		prefix := strings.TrimSuffix(base, "/")
		paths := []string{"/healthz", "/readyz"}
		if prefix != "" {
			paths = append(paths, prefix+"/healthz", prefix+"/readyz")
		}
		for _, path := range paths {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Host = podHost
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if want := "ok\n"; rec.Code != http.StatusOK || rec.Body.String() != want {
				t.Errorf("base path %q: GET %s with Host %s = %d %q, want 200 %q", base, path, podHost, rec.Code, rec.Body, want)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("base path %q: GET %s X-Content-Type-Options = %q, want nosniff", base, path, got)
			}
		}

		req := httptest.NewRequest(http.MethodGet, prefix+"/api/profiles", nil)
		req.Host = podHost
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("base path %q: GET %s/api/profiles with Host %s = %d, want %d", base, prefix, podHost, rec.Code, http.StatusForbidden)
		}
	}
}

// TestServerModeAndNotice checks the mode, the version and the notice, the
// last two left out when empty.
func TestServerModeAndNotice(t *testing.T) {
	for _, tc := range []struct {
		server      bool
		notice      string
		noticeColor string
		version     string
		want        string
	}{
		{server: false, want: `{"mode":"local"}`},
		{server: true, want: `{"mode":"server"}`},
		{server: false, notice: "Local note", want: `{"mode":"local","notice":"Local note"}`},
		{server: true, notice: "Public demo <b>x</b>\nsecond", want: `{"mode":"server","notice":"Public demo \u003cb\u003ex\u003c/b\u003e\nsecond"}`},
		{server: true, notice: "Public demo", noticeColor: "#f59e0b", want: `{"mode":"server","notice":"Public demo","noticeColor":"#f59e0b"}`},
		// A color without a notice colors nothing.
		{server: true, noticeColor: "#f59e0b", want: `{"mode":"server"}`},
		{server: false, version: "v1.7.3", want: `{"mode":"local","version":"v1.7.3"}`},
	} {
		env := newTestEnvWith(t, Options{Server: tc.server, Notice: tc.notice, NoticeColor: tc.noticeColor, Version: tc.version})
		rec := env.do(t, http.MethodGet, "/api/server", "")
		if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != tc.want {
			t.Errorf("Server %v, Notice %q: GET /api/server = %d %s, want 200 %s", tc.server, tc.notice, rec.Code, got, tc.want)
		}
	}
}
