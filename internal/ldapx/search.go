// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

package ldapx

import (
	"context"
	"errors"
	"fmt"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"

	"github.com/cicdteam/ldaplens/internal/schema"
)

// Caps on one search response, so that a server cannot make ldaplens hold an
// unbounded result in memory. A response past one ends early as truncated.
// maxResponseEntries is also the most entries a Search returns, and the
// value bytes of all the pages it walks share maxResponseBytes.
const (
	maxResponseEntries = 5000
	// maxResponseBytes counts attribute value bytes.
	maxResponseBytes = 64 << 20
)

func init() {
	// go-ldap reads each message whole; the default lets one claim 2 GiB.
	// One entry needs no more than a whole response may carry.
	ber.MaxPacketLengthBytes = maxResponseBytes
}

type result struct {
	entries   []*ldap.Entry
	referrals []string
	controls  []ldap.Control
	// size is the value bytes of entries.
	size      int
	truncated bool
	// capped is set when a response cap stopped the reading: the server
	// may still be sending the rest, which go-ldap drops.
	capped bool
	// more is set by walk when the server has entries past its limit.
	more bool
}

// searchOnce sends one search request and collects its results. Size and
// time limit errors are not failures: they set truncated and keep what
// arrived, and so does a response past maxResponseEntries entries or
// maxResponseBytes value bytes (which also sets capped). A connection that
// is closing yields an ErrorNetwork error so that read can redial, and so
// does a lost one (see networkError).
func searchOnce(ctx context.Context, lc *ldap.Conn, req *ldap.SearchRequest) (*result, error) {
	return searchWithin(ctx, lc, req, maxResponseBytes)
}

// searchWithin is searchOnce with maxBytes value bytes in place of
// maxResponseBytes.
func searchWithin(ctx context.Context, lc *ldap.Conn, req *ldap.SearchRequest, maxBytes int) (*result, error) {
	// Cancelling sctx on a cap stops go-ldap's reading for this search;
	// only ctx ending is an error.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A small buffer: go-ldap parses entries into it ahead of the cap check
	// below, and each one may be a packet of up to maxResponseBytes, so the
	// buffer bounds what is held beyond the cap.
	resp := lc.SearchAsync(sctx, req, 4)
	res := &result{}
	for resp.Next() {
		if e := resp.Entry(); e != nil {
			n := valueBytes(e)
			if len(res.entries) == maxResponseEntries || res.size+n > maxBytes {
				res.truncated, res.capped = true, true
				break
			}
			res.size += n
			res.entries = append(res.entries, e)
		}
		if r := resp.Referral(); r != "" {
			res.referrals = append(res.referrals, r)
		}
		if ctls := resp.Controls(); len(ctls) > 0 {
			res.controls = ctls
		}
	}
	if res.capped {
		// go-ldap's goroutine for this search returns on the cancel and
		// finishes the message before the results close, so once they do,
		// go-ldap no longer tracks it: it reads and drops the rest of the
		// response packet by packet and lc stays usable.
		// ponytail: go-ldap sends no Abandon, so the server still sends
		// the rest and later requests on lc queue behind it; send an
		// Abandon if go-ldap ever exposes the message ID.
		cancel()
		for resp.Next() {
		}
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	err := resp.Err()
	switch {
	case ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded), ldap.IsErrorWithCode(err, ldap.LDAPResultTimeLimitExceeded):
		res.truncated = true
		return res, nil
	case err != nil:
		return nil, networkError(err)
	case lc.IsClosing():
		return nil, ldap.NewError(ldap.ErrorNetwork, errors.New("connection closed"))
	}
	return res, nil
}

func valueBytes(e *ldap.Entry) int {
	n := 0
	for _, a := range e.Attributes {
		for _, v := range a.ByteValues {
			n += len(v)
		}
	}
	return n
}

// searchTimeLimit is the server-side time limit in seconds for a client
// timeout of d. It stays below d so that a slow search ends with what was
// found so far (timeLimitExceeded, reported as truncated) rather than with
// a client timeout and nothing.
func searchTimeLimit(d time.Duration) int {
	return max(1, int((d-2*time.Second)/time.Second))
}

// Search returns the entries of a search, at most r.SizeLimit of them (see
// SearchRequest), in one call. Page.More is set when the search matches
// more; no state outlives the call (see walk). A filter that matches on a
// secret attribute's value fails with ErrSecretFilter before the search is
// sent.
func (c *Conn) Search(ctx context.Context, r SearchRequest) (Page, error) {
	req, limit, err := buildRequest(r, c.info.Paging)
	if err != nil {
		return Page{}, err
	}
	s := c.schemaOrEmpty(ctx)
	// ponytail: compiles the filter a second time after buildRequest;
	// maxFilterLength bounds the cost.
	attr, err := secretFilterAttr(req.Filter, s)
	if err != nil {
		return Page{}, err
	}
	if attr != "" {
		return Page{}, fmt.Errorf("%w: %s", ErrSecretFilter, attr)
	}
	req.TimeLimit = searchTimeLimit(c.prof.SearchTimeout)
	res, err := c.walk(ctx, req, limit)
	var skipped []string
	if refusesSearch(err) {
		res, skipped, err = c.walkParts(ctx, r, req.Filter, limit, err)
	}
	if err != nil {
		return Page{}, err
	}
	page := Page{Entries: make([]Entry, 0, len(res.entries)), More: res.more, Truncated: res.truncated, Referrals: res.referrals, Skipped: skipped}
	for _, e := range res.entries {
		page.Entries = append(page.Entries, convertEntry(e, s, c.info.ActiveDirectory))
	}
	return page, nil
}

// walkParts runs each part of an OR filter as a search of its own, for a
// server that refused the whole: OpenDJ and 389 DS refuse a search that
// cannot use an index, and an OR cannot as soon as one part cannot (see
// groupsListing). It merges the entries of the parts the server answers,
// in order and without duplicates, and lists in skipped the parts it
// refused. refused is the error when filter is no OR or every part is
// refused.
// ponytail: only a top-level OR is split; an OR inside an AND fails as the
// server answered.
func (c *Conn) walkParts(ctx context.Context, r SearchRequest, filter string, limit int, refused error) (*result, []string, error) {
	p, err := ldap.CompileFilter(filter)
	if err != nil || p.Tag != ldap.FilterOr {
		return nil, nil, refused
	}
	merged := &result{}
	seen := map[string]bool{}
	var skipped []string
	for _, child := range p.Children {
		r.Filter, err = ldap.DecompileFilter(child)
		if err != nil {
			return nil, nil, refused
		}
		req, _, err := buildRequest(r, c.info.Paging)
		if err != nil {
			return nil, nil, err
		}
		req.TimeLimit = searchTimeLimit(c.prof.SearchTimeout)
		res, err := c.walk(ctx, req, limit)
		if refusesSearch(err) {
			skipped = append(skipped, r.Filter)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		for _, e := range res.entries {
			if !seen[normDN(e.DN)] {
				seen[normDN(e.DN)] = true
				merged.entries = append(merged.entries, e)
			}
		}
		merged.referrals = append(merged.referrals, res.referrals...)
		merged.more = merged.more || res.more
		merged.truncated = merged.truncated || res.truncated
		// The parts share maxResponseBytes like the pages of one walk.
		if merged.size += res.size; merged.size >= maxResponseBytes {
			merged.truncated = true
			break
		}
	}
	if len(skipped) == len(p.Children) {
		return nil, nil, refused
	}
	if len(merged.entries) > limit {
		merged.entries, merged.more = merged.entries[:limit], true
	}
	return merged, skipped, nil
}

// walk runs req and returns at most limit of its entries, with more set
// when the server has others. A request without the paging control is one
// search, whose size limit the caller set to limit: sizeLimitExceeded at
// limit entries means more. With the control, walk pages through the
// results on the shared connection taken out of its slot, until it has
// limit entries or the server has no more. A walk that stops early
// abandons the paged search before it hands the connection back, so no
// paged state outlives it. The value bytes of all pages share
// maxResponseBytes.
func (c *Conn) walk(ctx context.Context, req *ldap.SearchRequest, limit int) (*result, error) {
	paging, ok := ldap.FindControl(req.Controls, ldap.ControlTypePaging).(*ldap.ControlPaging)
	if !ok {
		var res *result
		err := c.read(ctx, func(lc *ldap.Conn) error {
			var err error
			res, err = searchOnce(ctx, lc, req)
			return err
		})
		if err != nil {
			return nil, err
		}
		// ponytail: a time limit hit at exactly limit entries also reads
		// as more; tell size from time limit in result if that matters.
		if res.truncated && !res.capped && len(res.entries) == limit {
			res.truncated, res.more = false, true
		}
		return res, nil
	}
	var res *result
	lc, err := retry(ctx, c.take, func(lc *ldap.Conn) error {
		res = &result{}
		paging.SetCookie(nil)
		// At most limit pages, so a server that keeps sending cookies
		// with empty pages cannot keep us looping.
		for range limit {
			page, err := searchWithin(ctx, lc, req, maxResponseBytes-res.size)
			if err != nil {
				return err
			}
			res.entries = append(res.entries, page.entries...)
			res.referrals = append(res.referrals, page.referrals...)
			res.size += page.size
			res.truncated = res.truncated || page.truncated
			res.capped = page.capped
			var cookie []byte
			if ctl, ok := ldap.FindControl(page.controls, ldap.ControlTypePaging).(*ldap.ControlPaging); ok {
				cookie = ctl.Cookie
			}
			paging.SetCookie(cookie)
			if len(cookie) == 0 || page.truncated || len(res.entries) >= limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		if lc != nil {
			// go-ldap sends no Abandon on cancel, so the paged search
			// may still run on lc; never share it again.
			_ = lc.Close()
		}
		return nil, err
	}
	switch {
	case res.capped:
		// The rest of a half-read page cannot be fetched, so the walk
		// ends here, truncated. The server is still sending that page
		// and keeps its paged state; lc is this walk's own, so close it
		// rather than share it.
		_ = lc.Close()
	case len(paging.Cookie) > 0:
		// Stopped early with the paged search still open on the server.
		// Page size 0 with the cookie abandons it (RFC 2696); if that
		// fails, lc is closed rather than shared while still paging.
		res.more = true
		paging.PagingSize = 0
		if _, err := searchOnce(ctx, lc, req); err != nil {
			_ = lc.Close()
		}
		c.release(lc) // closes lc instead when it is closing
	default:
		c.release(lc)
	}
	if len(res.entries) > limit {
		res.entries, res.more = res.entries[:limit], true
	}
	return res, nil
}

// Entry reads one entry with all user and operational attributes. Ranged
// attributes (large AD groups) are fetched completely.
func (c *Conn) Entry(ctx context.Context, dn string) (Entry, error) {
	e, err := c.rawEntry(ctx, dn, []string{"*", "+"})
	if err != nil {
		return Entry{}, err
	}
	if err := completeRanges(e, c.rangeFetcher(ctx, dn)); err != nil {
		return Entry{}, err
	}
	return convertEntry(e, c.schemaOrEmpty(ctx), c.info.ActiveDirectory), nil
}

func (c *Conn) rawEntry(ctx context.Context, dn string, attrs []string) (*ldap.Entry, error) {
	var res *result
	err := c.read(ctx, func(lc *ldap.Conn) error {
		var err error
		res, err = searchOnce(ctx, lc, &ldap.SearchRequest{
			BaseDN: dn, Scope: ldap.ScopeBaseObject, DerefAliases: ldap.NeverDerefAliases,
			TimeLimit: searchTimeLimit(c.prof.SearchTimeout), Filter: "(objectClass=*)", Attributes: attrs,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(res.entries) == 0 {
		return nil, ldap.NewError(ldap.LDAPResultNoSuchObject, fmt.Errorf("entry %s not found", dn))
	}
	return res.entries[0], nil
}

func (c *Conn) rangeFetcher(ctx context.Context, dn string) func(string) (*ldap.EntryAttribute, error) {
	return func(attr string) (*ldap.EntryAttribute, error) {
		e, err := c.rawEntry(ctx, dn, []string{attr})
		if err != nil || len(e.Attributes) == 0 {
			return nil, err
		}
		return e.Attributes[0], nil
	}
}

// Schema returns the server schema, reading it on first use.
func (c *Conn) Schema(ctx context.Context) (*schema.Schema, error) {
	c.mu.Lock()
	s := c.schema
	c.mu.Unlock()
	if s != nil {
		return s, nil
	}
	return c.ReloadSchema(ctx)
}

// ReloadSchema reads the schema from the server again.
func (c *Conn) ReloadSchema(ctx context.Context) (*schema.Schema, error) {
	dn := c.info.SubschemaSubentry
	if dn == "" {
		dn = "cn=Subschema"
	}
	e, err := c.rawEntry(ctx, dn, []string{"attributeTypes", "objectClasses", "dITContentRules"})
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", dn, err)
	}
	s := schema.Parse(e.GetAttributeValues("attributeTypes"), e.GetAttributeValues("objectClasses"), e.GetAttributeValues("dITContentRules"))
	c.mu.Lock()
	c.schema = s
	c.mu.Unlock()
	return s, nil
}

// schemaOrEmpty returns the schema, or an empty one when it cannot be read
// (for example because the bind DN may not read cn=Subschema). Values are
// then shown without schema hints instead of failing the request.
func (c *Conn) schemaOrEmpty(ctx context.Context) *schema.Schema {
	s, err := c.Schema(ctx)
	if err != nil {
		return schema.Parse(nil, nil, nil)
	}
	return s
}
