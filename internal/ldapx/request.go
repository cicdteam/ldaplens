// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package ldapx

import (
	"errors"
	"fmt"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"

	"github.com/cicdteam/ldaplens/internal/schema"
)

// Search scopes accepted in SearchRequest.Scope. Empty means ScopeSub.
const (
	ScopeBase = "base"
	ScopeOne  = "one"
	ScopeSub  = "sub"
)

const (
	// defaultSizeLimit is the number of entries a search returns when its
	// request names none.
	defaultSizeLimit = 500
	defaultPageSize  = 500

	// go-ldap compiles filters recursively and slows down sharply on deep
	// nesting, so filters are capped before they reach it.
	maxFilterLength = 16 << 10
	maxFilterDepth  = 64
)

// ErrInvalidRequest marks search parameters rejected before reaching the server.
var ErrInvalidRequest = errors.New("invalid request")

// ErrSecretFilter marks a filter that matches on the value of a secret
// attribute: its results would let a search guess the secret.
var ErrSecretFilter = errors.New("filter tests a secret attribute")

// SearchRequest describes a search. SizeLimit is the number of entries
// wanted: 0 means defaultSizeLimit, and more than maxResponseEntries means
// maxResponseEntries. PageSize is a hint for the LDAP page size on servers
// with paging.
type SearchRequest struct {
	BaseDN     string   `json:"base"`
	Scope      string   `json:"scope"`
	Filter     string   `json:"filter"`
	Attributes []string `json:"attributes,omitempty"`
	PageSize   int      `json:"pageSize,omitempty"`
	SizeLimit  int      `json:"sizeLimit,omitempty"`
}

// Page is the result of a search.
type Page struct {
	Entries []Entry `json:"entries"`
	// More is set when the search matches more entries than its size
	// limit; the same search with a bigger one returns them.
	More bool `json:"more,omitempty"`
	// Truncated is set when the server stopped early because of its own
	// size or time limit, or a server response passed maxResponseEntries
	// entries or maxResponseBytes value bytes; Entries holds what arrived
	// before that.
	Truncated bool     `json:"truncated,omitempty"`
	Referrals []string `json:"referrals,omitempty"`
	// Skipped names the attributes of the parts of an OR filter the server
	// refused to search on (see walkParts); entries that match only those
	// parts are missing.
	Skipped []string `json:"skipped,omitempty"`
}

var scopes = map[string]int{
	"":        ldap.ScopeWholeSubtree,
	ScopeBase: ldap.ScopeBaseObject,
	ScopeOne:  ldap.ScopeSingleLevel,
	ScopeSub:  ldap.ScopeWholeSubtree,
}

// buildRequest validates r and turns it into a go-ldap request for
// Conn.walk and the number of entries wanted. With paging, pages are no
// bigger than that number. Without, it becomes the size limit, so a huge
// container is cut off at it instead of hanging.
func buildRequest(r SearchRequest, paging bool) (*ldap.SearchRequest, int, error) {
	scope, ok := scopes[r.Scope]
	if !ok {
		return nil, 0, fmt.Errorf("%w: scope %q must be base, one or sub", ErrInvalidRequest, r.Scope)
	}
	if r.SizeLimit < 0 {
		return nil, 0, fmt.Errorf("%w: sizeLimit must not be negative", ErrInvalidRequest)
	}
	filter := r.Filter
	if filter == "" {
		filter = "(objectClass=*)"
	}
	if len(filter) > maxFilterLength {
		return nil, 0, fmt.Errorf("%w: filter is longer than %d bytes", ErrInvalidRequest, maxFilterLength)
	}
	if filterDepth(filter) > maxFilterDepth {
		return nil, 0, fmt.Errorf("%w: filter nests deeper than %d levels", ErrInvalidRequest, maxFilterDepth)
	}
	if _, err := ldap.CompileFilter(filter); err != nil {
		return nil, 0, err
	}
	limit := r.SizeLimit
	if limit == 0 {
		limit = defaultSizeLimit
	}
	limit = min(limit, maxResponseEntries)
	size := r.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	req := &ldap.SearchRequest{
		BaseDN:       r.BaseDN,
		Scope:        scope,
		DerefAliases: ldap.NeverDerefAliases,
		Filter:       filter,
		Attributes:   r.Attributes,
	}
	if !paging {
		req.SizeLimit = limit
		return req, limit, nil
	}
	req.Controls = []ldap.Control{ldap.NewControlPaging(uint32(min(size, limit)))} //nolint:gosec // at most maxResponseEntries
	return req, limit, nil
}

// filterDepth returns the deepest parenthesis nesting in f. RFC 4515
// requires parentheses inside values to be escaped as \28 and \29, so every
// raw parenthesis is structural.
func filterDepth(f string) int {
	depth, deepest := 0, 0
	for i := range len(f) {
		switch f[i] {
		case '(':
			depth++
			deepest = max(deepest, depth)
		case ')':
			depth--
		}
	}
	return deepest
}

// typelessMatch is what secretFilterAttr names for an extensible match
// without an attribute type, such as (:octetStringMatch:=guess): the server
// applies its rule to every attribute it fits, secret ones included.
const typelessMatch = "an extensible match without an attribute type"

// secretFilterAttr is the first secret attribute that filter matches on by
// value (equality, substrings, ordering, approximate or extensible), or "".
// Presence, as in (userPassword=*), is allowed: the inspector shows how many
// values are masked anyway. An extensible match without a type is named
// typelessMatch.
func secretFilterAttr(filter string, s *schema.Schema) (string, error) {
	p, err := ldap.CompileFilter(filter)
	if err != nil {
		return "", err
	}
	return secretPacketAttr(p, s), nil
}

// secretPacketAttr is secretFilterAttr on a compiled filter.
func secretPacketAttr(p *ber.Packet, s *schema.Schema) string {
	switch p.Tag {
	case ldap.FilterAnd, ldap.FilterOr, ldap.FilterNot:
		for _, c := range p.Children {
			if a := secretPacketAttr(c, s); a != "" {
				return a
			}
		}
	case ldap.FilterEqualityMatch, ldap.FilterSubstrings, ldap.FilterGreaterOrEqual,
		ldap.FilterLessOrEqual, ldap.FilterApproxMatch:
		if len(p.Children) == 0 {
			return ""
		}
		if a := p.Children[0].Data.String(); IsSecret(a, s) {
			return a
		}
	case ldap.FilterExtensibleMatch:
		for _, c := range p.Children {
			if c.Tag == ldap.MatchingRuleAssertionType {
				if IsSecret(c.Data.String(), s) {
					return c.Data.String()
				}
				return ""
			}
		}
		// ponytail: refuses every typeless match, also one whose rule fits
		// no secret attribute; compare the rule with the secret types'
		// rules in the schema if someone needs those.
		return typelessMatch
	}
	return ""
}
