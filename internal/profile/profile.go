// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

// Package profile loads and stores LDAP connection profiles.
package profile

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-ldap/ldap/v3"
)

const (
	defaultConnectTimeout = 10 * time.Second
	defaultSearchTimeout  = 30 * time.Second
)

// ErrInvalid marks a profile that fails validation.
var ErrInvalid = errors.New("invalid profile")

// ErrInvalidUsername marks a username that cannot be put into a bind DN
// template. Its message never carries the username.
var ErrInvalidUsername = errors.New("invalid username")

// namePattern keeps profile names safe to use as a URL path segment.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// usernamePattern keeps a typed username from changing the structure of the
// bind DN, or of a UPN or down-level name, it is substituted into.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// userPlaceholder is replaced by the username in BindDNTemplate.
const userPlaceholder = "{user}"

// maxLabelLen is the longest Label, in characters.
const maxLabelLen = 80

// Profile describes how to connect to one LDAP server.
//
// Fields tagged json:"-" can only come from the config file: the HTTP API
// must never be able to point the server at a local file or environment
// variable.
type Profile struct {
	Name string `yaml:"name" json:"name"`
	// Label is what the UI shows for the profile, Name when empty; Name
	// stays the identifier in URLs and the API.
	Label              string        `yaml:"label" json:"label,omitempty"`
	URL                string        `yaml:"url" json:"url"`
	StartTLS           bool          `yaml:"startTLS" json:"startTLS"`
	BaseDN             string        `yaml:"baseDN" json:"baseDN,omitempty"`
	BindDN             string        `yaml:"bindDN" json:"bindDN,omitempty"`
	BindDNTemplate     string        `yaml:"bindDNTemplate" json:"bindDNTemplate,omitempty"`
	PasswordEnv        string        `yaml:"passwordEnv" json:"-"`
	PasswordFile       string        `yaml:"passwordFile" json:"-"`
	CAFile             string        `yaml:"caFile" json:"-"`
	CAPEM              string        `yaml:"caPEM" json:"caPEM,omitempty"`
	InsecureSkipVerify bool          `yaml:"insecureSkipVerify" json:"insecureSkipVerify"`
	Color              string        `yaml:"color" json:"color,omitempty"`
	ReadOnly           bool          `yaml:"readOnly" json:"readOnly"`
	ConnectTimeout     time.Duration `yaml:"connectTimeout" json:"-"`
	SearchTimeout      time.Duration `yaml:"searchTimeout" json:"-"`
}

// normalize validates p and fills in defaults. It is idempotent.
func (p *Profile) normalize() error {
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("%w: name %q must match %s", ErrInvalid, p.Name, namePattern)
	}
	p.Label = strings.TrimSpace(p.Label)
	if utf8.RuneCountInString(p.Label) > maxLabelLen || strings.ContainsFunc(p.Label, unicode.IsControl) {
		return fmt.Errorf("%w: %s: label must be at most %d characters, without control characters", ErrInvalid, p.Name, maxLabelLen)
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		// url.Error repeats the raw URL, which may hold a password; keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%w: %s: url: %w", ErrInvalid, p.Name, err)
	}
	switch u.Scheme {
	case "ldap":
	case "ldaps":
		if p.StartTLS {
			return fmt.Errorf("%w: %s: startTLS cannot be combined with ldaps://", ErrInvalid, p.Name)
		}
	default:
		return fmt.Errorf("%w: %s: url scheme must be ldap or ldaps, got %q", ErrInvalid, p.Name, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: %s: url has no host", ErrInvalid, p.Name)
	}
	if u.User != nil {
		return fmt.Errorf("%w: %s: url must not contain credentials", ErrInvalid, p.Name)
	}
	if p.BindDNTemplate != "" {
		if strings.Count(p.BindDNTemplate, userPlaceholder) != 1 {
			return fmt.Errorf("%w: %s: bindDNTemplate must contain %s exactly once", ErrInvalid, p.Name, userPlaceholder)
		}
		if p.BindDN != "" || p.PasswordEnv != "" || p.PasswordFile != "" {
			return fmt.Errorf("%w: %s: bindDNTemplate cannot be combined with bindDN, passwordEnv or passwordFile", ErrInvalid, p.Name)
		}
	}
	if p.ConnectTimeout <= 0 {
		p.ConnectTimeout = defaultConnectTimeout
	}
	if p.SearchTimeout <= 0 {
		p.SearchTimeout = defaultSearchTimeout
	}
	return nil
}

// UserLogin reports whether users sign in with their own username, which
// BindDNFor turns into the bind DN.
func (p Profile) UserLogin() bool {
	return p.BindDNTemplate != ""
}

// Shared reports whether everyone who connects binds the same way, with
// the configured password or anonymously, so that one connection can serve
// them all.
func (p Profile) Shared() bool {
	return !p.UserLogin() && (p.PasswordEnv != "" || p.PasswordFile != "" || p.BindDN == "")
}

// BindDNFor returns BindDNTemplate with the placeholder replaced by
// username. A username that does not match usernamePattern is refused with
// an error wrapping ErrInvalidUsername.
func (p Profile) BindDNFor(username string) (string, error) {
	if !p.UserLogin() {
		return "", fmt.Errorf("profile %s has no bindDNTemplate", p.Name)
	}
	if !usernamePattern.MatchString(username) {
		return "", fmt.Errorf("%w: must match %s", ErrInvalidUsername, usernamePattern)
	}
	if strings.Contains(p.BindDNTemplate, "=") {
		// The pattern leaves nothing to escape; escaping keeps the DN safe if it is widened.
		username = ldap.EscapeDN(username)
	}
	return strings.Replace(p.BindDNTemplate, userPlaceholder, username, 1), nil
}

// ConfiguredPassword returns the bind password from PasswordEnv or
// PasswordFile. ok is false when the profile configures neither, in which
// case the user has to type the password.
func (p Profile) ConfiguredPassword() (password string, ok bool, err error) {
	switch {
	case p.PasswordEnv != "":
		v, set := os.LookupEnv(p.PasswordEnv)
		if !set {
			return "", false, fmt.Errorf("profile %s: environment variable %s is not set", p.Name, p.PasswordEnv)
		}
		return v, true, nil
	case p.PasswordFile != "":
		b, err := os.ReadFile(p.PasswordFile) //nolint:gosec // path comes from the operator's config file
		if err != nil {
			return "", false, fmt.Errorf("profile %s: %w", p.Name, err)
		}
		return strings.TrimRight(string(b), "\r\n"), true, nil
	}
	return "", false, nil
}

// TLSConfig returns the TLS settings for ldaps:// and StartTLS. Custom CAs
// from CAFile and CAPEM are added to the system pool.
func (p Profile) TLSConfig() (*tls.Config, error) {
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, fmt.Errorf("profile %s: url: %w", p.Name, err)
	}
	cfg := &tls.Config{
		ServerName:         u.Hostname(),
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: p.InsecureSkipVerify, //nolint:gosec // explicit per-profile opt-in, flagged in the UI
	}
	if p.CAFile == "" && p.CAPEM == "" {
		return cfg, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	pemData := []byte(p.CAPEM)
	if p.CAFile != "" {
		b, err := os.ReadFile(p.CAFile) //nolint:gosec // path comes from the operator's config file
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", p.Name, err)
		}
		pemData = append(append(pemData, '\n'), b...)
	}
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("profile %s: no PEM certificates in CA data", p.Name)
	}
	cfg.RootCAs = pool
	return cfg, nil
}
