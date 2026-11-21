// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package api

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/cicdteam/ldaplens/internal/ad"
	"github.com/cicdteam/ldaplens/internal/ldapx"
	"github.com/cicdteam/ldaplens/internal/session"
)

// apiError is the body of every error response. Code is stable and meant
// for the UI to branch on; Message is for humans.
type apiError struct {
	Code           string     `json:"code"`
	Message        string     `json:"message"`
	Attribute      string     `json:"attribute,omitempty"`
	LDAPResultCode int        `json:"ldapResultCode,omitempty"`
	Diagnostic     string     `json:"diagnostic,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	MatchedDN      string     `json:"matchedDN,omitempty"`
	Certificates   []certInfo `json:"certificates,omitempty"`
}

type certInfo struct {
	Subject  string    `json:"subject"`
	Issuer   string    `json:"issuer"`
	NotAfter time.Time `json:"notAfter"`
	SHA256   string    `json:"sha256"`
	PEM      string    `json:"pem"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, e apiError) {
	writeJSON(w, status, e)
}

// errPasswordUnavailable marks a configured password that cannot be read.
// The error it wraps names the env var or file, which only the server log
// may show.
var errPasswordUnavailable = errors.New("the configured password for this profile is unavailable; see the server log")

// errorResponse maps an error from the LDAP layer to an HTTP status and body.
// It never answers 502 or 504: a proxy or CDN in front, Cloudflare for one,
// replaces those with its own error page, and the UI would lose the body.
func errorResponse(err error) (int, apiError) {
	var certErr *ldapx.UntrustedCertError
	var le *ldap.Error
	switch {
	case errors.Is(err, errPasswordUnavailable):
		return http.StatusInternalServerError, apiError{Code: "password_unavailable", Message: errPasswordUnavailable.Error()}
	case errors.As(err, &certErr):
		return http.StatusUnprocessableEntity, apiError{Code: "tls_untrusted", Message: certErr.Error(), Certificates: certInfos(certErr.Certificates)}
	// net.ErrClosed means the session's connection was closed under the
	// request, for example by idle expiry.
	case errors.Is(err, session.ErrNoSession), errors.Is(err, session.ErrNotConnected), errors.Is(err, net.ErrClosed):
		return http.StatusUnauthorized, apiError{Code: "not_connected", Message: "connect to this profile first"}
	case errors.Is(err, session.ErrTooManyConns):
		return http.StatusConflict, apiError{Code: "too_many_connections", Message: "too many connections in this session; disconnect one first"}
	case errors.Is(err, ldapx.ErrInvalidRequest):
		return http.StatusBadRequest, apiError{Code: "bad_request", Message: err.Error()}
	case errors.Is(err, ldapx.ErrSecretFilter):
		return http.StatusBadRequest, apiError{Code: "secret_filter", Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, apiError{Code: "timeout", Message: "the LDAP server did not answer in time"}
	case errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable, apiError{Code: "canceled", Message: "request canceled"}
	case errors.As(err, &le):
		e := apiError{Message: err.Error(), LDAPResultCode: int(le.ResultCode), MatchedDN: le.MatchedDN}
		if le.Err != nil {
			e.Diagnostic = le.Err.Error()
		}
		// Only AD writes this code, when its policy refuses a new password.
		e.Reason = ad.PasswordReason(e.Diagnostic)
		switch le.ResultCode {
		case ldap.LDAPResultInvalidCredentials:
			e.Code, e.Reason = "invalid_credentials", ad.DiagnosticReason(e.Diagnostic)
			return http.StatusUnauthorized, e
		case ldap.LDAPResultInsufficientAccessRights:
			e.Code = "insufficient_access"
			return http.StatusForbidden, e
		case ldap.LDAPResultNoSuchObject:
			e.Code = "no_such_object"
			return http.StatusNotFound, e
		case ldap.LDAPResultNoSuchAttribute, ldap.LDAPResultAttributeOrValueExists,
			ldap.LDAPResultEntryAlreadyExists:
			// The entry changed since the UI read it, or the DN is taken.
			e.Code = "conflict"
			return http.StatusConflict, e
		case ldap.LDAPResultUndefinedAttributeType, ldap.LDAPResultConstraintViolation,
			ldap.LDAPResultInvalidAttributeSyntax, ldap.LDAPResultObjectClassViolation,
			ldap.LDAPResultNotAllowedOnRDN, ldap.LDAPResultObjectClassModsProhibited,
			ldap.LDAPResultNamingViolation, ldap.LDAPResultNotAllowedOnNonLeaf:
			e.Code = "rejected"
			return http.StatusUnprocessableEntity, e
		case ldap.ErrorFilterCompile:
			e.Code = "bad_filter"
			return http.StatusBadRequest, e
		case ldap.ErrorNetwork:
			e.Code = "network"
			return http.StatusServiceUnavailable, e
		}
		e.Code = "ldap_error"
		return http.StatusUnprocessableEntity, e
	}
	// Unexpected errors may name local files such as a CA file; the caller
	// logs err, the client only learns that something failed.
	return http.StatusInternalServerError, apiError{Code: "internal", Message: "internal error; see the server log"}
}

// signInError is errorResponse's answer to a failed connect, as server mode
// gives it to someone who has not signed in: nothing about the directory.
// Every LDAP result is a wrong username or password, since a diagnostic or
// a code such as AD's "account locked out" or 389-DS's 19 and 53 tells that
// the account exists. A network or TLS failure keeps its code, without the
// host name, the error text or the certificate chain.
func signInError(status int, e apiError) (int, apiError) {
	switch {
	case e.Code == "network":
		return status, apiError{Code: e.Code, Message: "cannot reach the directory server"}
	case e.Code == "tls_untrusted":
		return status, apiError{Code: e.Code, Message: "the directory server's certificate is not trusted"}
	case e.LDAPResultCode != 0: // every other LDAP result
		return http.StatusUnauthorized, apiError{Code: "invalid_credentials", Message: "invalid username or password"}
	}
	return status, e
}

func certInfos(certs []*x509.Certificate) []certInfo {
	var out []certInfo
	for _, c := range certs {
		sum := sha256.Sum256(c.Raw)
		out = append(out, certInfo{
			Subject:  c.Subject.String(),
			Issuer:   c.Issuer.String(),
			NotAfter: c.NotAfter,
			SHA256:   hex.EncodeToString(sum[:]),
			PEM:      string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})),
		})
	}
	return out
}
