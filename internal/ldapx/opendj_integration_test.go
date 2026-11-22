// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

//go:build integration

package ldapx

import (
	"context"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/google/go-cmp/cmp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/cicdteam/ldaplens/internal/profile"
)

// openDJImage is Open Identity Platform's OpenDJ, pinned by digest.
const openDJImage = "openidentityplatform/opendj:5.2.0@sha256:111f7270355c0163eff2d5d578d99b2ee026cf7134752ecc37b80e865a91904b"

// openDJSeed is the directory TestOpenDJ works on: alice is a member of dev
// (member), ops (uniqueMember) and posix (memberUid), and of all through
// dev. Like a user a directory team lets edit groups, alice may change
// member and uniqueMember under ou=groups and do anything under
// ou=sandbox. OpenDJ creates no entry for the base DN by itself.
const openDJSeed = `dn: dc=example,dc=org
changetype: add
objectClass: dcObject
objectClass: organization
dc: example
o: example

dn: ou=people,dc=example,dc=org
changetype: add
objectClass: organizationalUnit
ou: people

dn: ou=groups,dc=example,dc=org
changetype: add
objectClass: organizationalUnit
ou: groups
aci: (targetattr="member || uniqueMember")(version 3.0; acl "alice edits groups"; allow (write) userdn="ldap:///uid=alice,ou=people,dc=example,dc=org";)

dn: ou=sandbox,dc=example,dc=org
changetype: add
objectClass: organizationalUnit
ou: sandbox
aci: (targetattr="*")(version 3.0; acl "alice owns the sandbox"; allow (all, import, export) userdn="ldap:///uid=alice,ou=people,dc=example,dc=org";)

dn: uid=alice,ou=people,dc=example,dc=org
changetype: add
objectClass: inetOrgPerson
uid: alice
cn: Alice
sn: A
userPassword: alicepassword

dn: uid=p1,ou=people,dc=example,dc=org
changetype: add
objectClass: inetOrgPerson
uid: p1
cn: P1
sn: P

dn: uid=p2,ou=people,dc=example,dc=org
changetype: add
objectClass: inetOrgPerson
uid: p2
cn: P2
sn: P

dn: uid=p3,ou=people,dc=example,dc=org
changetype: add
objectClass: inetOrgPerson
uid: p3
cn: P3
sn: P

dn: uid=p4,ou=people,dc=example,dc=org
changetype: add
objectClass: inetOrgPerson
uid: p4
cn: P4
sn: P

dn: uid=p5,ou=people,dc=example,dc=org
changetype: add
objectClass: inetOrgPerson
uid: p5
cn: P5
sn: P

dn: cn=dev,ou=groups,dc=example,dc=org
changetype: add
objectClass: groupOfNames
cn: dev
member: uid=alice,ou=people,dc=example,dc=org

dn: cn=ops,ou=groups,dc=example,dc=org
changetype: add
objectClass: groupOfUniqueNames
cn: ops
uniqueMember: uid=alice,ou=people,dc=example,dc=org

dn: cn=posix,ou=groups,dc=example,dc=org
changetype: add
objectClass: groupOfEntries
objectClass: posixGroup
cn: posix
gidNumber: 5000
memberUid: alice

dn: cn=all,ou=groups,dc=example,dc=org
changetype: add
objectClass: groupOfNames
cn: all
member: cn=dev,ou=groups,dc=example,dc=org
`

// TestOpenDJ runs ldaplens as alice, a plain user of OpenDJ, as on a real
// directory: Member of and Members, paged listing, search, entry and schema
// reads, group edits, and adds, renames and deletes. Without the
// unindexed-search privilege OpenDJ refuses, with result 50, a search it
// cannot answer from an index once the scope holds more entries than the
// backend's index entry limit (lowered to 10 here; a big directory is over
// the default 4000). memberUid has no index, so Member of lists the groups
// found by member and uniqueMember, says memberUid was skipped, and adds the
// nested group the server lists in isMemberOf. The 19 entries are over the
// limit, ou=people's 6 are under it: a one-level listing of a container with
// more children than the limit is refused too.
func TestOpenDJ(t *testing.T) {
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        openDJImage,
			ExposedPorts: []string{"1389/tcp"},
			Env:          map[string]string{"BASE_DN": "dc=example,dc=org", "ROOT_PASSWORD": "adminpassword"},
			Files: []testcontainers.ContainerFile{
				{Reader: strings.NewReader(openDJSeed), ContainerFilePath: "/opt/opendj/bootstrap/data/seed.ldif", FileMode: 0o644},
			},
			// Healthy once the bootstrap, seed included, is complete.
			WaitingFor: wait.ForHealthCheck().WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	if ctr != nil {
		t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	}
	if err = withLogTail(ctx, ctr, err); err != nil {
		t.Fatalf("start opendj: %v", err)
	}
	code, out, err := ctr.Exec(ctx, []string{
		"/opt/opendj/bin/dsconfig", "set-backend-prop", "--backend-name", "userRoot", "--set", "index-entry-limit:10",
		"-h", "localhost", "-p", "4444", "-D", "cn=Directory Manager", "-w", "adminpassword", "-X", "-n",
	})
	if err != nil || code != 0 {
		b := new(strings.Builder)
		if out != nil {
			_, _ = io.Copy(b, out)
		}
		t.Fatalf("dsconfig index-entry-limit = %d, %v: %s", code, err, b)
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, "1389/tcp")
	if err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{
		Name: "opendj", URL: "ldap://" + net.JoinHostPort(host, port.Port()),
		BaseDN: "dc=example,dc=org", BindDN: "uid=alice,ou=people,dc=example,dc=org",
		ConnectTimeout: 10 * time.Second, SearchTimeout: 30 * time.Second,
	}
	c, err := Dial(ctx, p, "alicepassword")
	if err != nil {
		t.Fatalf("Dial(alice) error = %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	t.Run("MemberOf", func(t *testing.T) { testOpenDJMemberOf(t, c, p.BindDN) })
	t.Run("Members", func(t *testing.T) { testOpenDJMembers(t, c) })
	t.Run("Search", func(t *testing.T) { testOpenDJSearch(t, c) })
	t.Run("EntryAndSchema", func(t *testing.T) { testOpenDJEntryAndSchema(t, c) })
	t.Run("EditGroup", func(t *testing.T) { testOpenDJEditGroup(t, c) })
	t.Run("AddRenameDelete", func(t *testing.T) { testOpenDJAddRenameDelete(t, c) })
}

func testOpenDJMemberOf(t *testing.T, c *Conn, alice string) {
	ctx := context.Background()
	tests := []struct {
		nested bool
		want   Membership
	}{
		{nested: false, want: Membership{Refs: []Ref{
			{DN: "cn=dev,ou=groups,dc=example,dc=org"},
			{DN: "cn=ops,ou=groups,dc=example,dc=org"},
			{DN: "cn=all,ou=groups,dc=example,dc=org", Source: "isMemberOf"},
		}, Skipped: []string{"memberUid"}}},
		{nested: true, want: Membership{Refs: []Ref{
			{DN: "cn=dev,ou=groups,dc=example,dc=org"},
			{DN: "cn=ops,ou=groups,dc=example,dc=org"},
			{DN: "cn=all,ou=groups,dc=example,dc=org", Via: "cn=dev,ou=groups,dc=example,dc=org"},
		}, Skipped: []string{"memberUid"}}},
	}
	for _, tc := range tests {
		m, err := c.MemberOf(ctx, alice, tc.nested)
		if err != nil {
			t.Fatalf("MemberOf(alice, nested %v) error = %v", tc.nested, err)
		}
		if diff := cmp.Diff(tc.want, m); diff != "" {
			t.Errorf("MemberOf(alice, nested %v) mismatch (-want +got):\n%s", tc.nested, diff)
		}
	}
}

func testOpenDJMembers(t *testing.T, c *Conn) {
	want := Membership{Refs: []Ref{
		{DN: "cn=dev,ou=groups,dc=example,dc=org"},
		{DN: "uid=alice,ou=people,dc=example,dc=org", Via: "cn=dev,ou=groups,dc=example,dc=org"},
	}}
	m, err := c.Members(context.Background(), "cn=all,ou=groups,dc=example,dc=org", true)
	if err != nil {
		t.Fatalf("Members(all, nested) error = %v", err)
	}
	if diff := cmp.Diff(want, m); diff != "" {
		t.Errorf("Members(all, nested) mismatch (-want +got):\n%s", diff)
	}
}

// testOpenDJSearch lists the people two to a page, as the tree does, and
// searches the whole directory by an indexed attribute.
func testOpenDJSearch(t *testing.T, c *Conn) {
	ctx := context.Background()
	req := SearchRequest{BaseDN: "ou=people,dc=example,dc=org", Scope: ScopeOne, Filter: "(objectClass=*)", Attributes: []string{"uid"}, PageSize: 2, SizeLimit: 100}
	p, err := c.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search(people, page size 2) error = %v", err)
	}
	if len(p.Entries) != 6 || p.More || p.Truncated {
		t.Errorf("Search(people, page size 2) = %d entries, more %v, truncated %v; want 6, neither", len(p.Entries), p.More, p.Truncated)
	}
	req = SearchRequest{BaseDN: "dc=example,dc=org", Filter: "(uid=p3)", Attributes: []string{"uid"}, PageSize: 100, SizeLimit: 100}
	p, err = c.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search(uid=p3) error = %v", err)
	}
	if len(p.Entries) != 1 || p.Entries[0].DN != "uid=p3,ou=people,dc=example,dc=org" {
		t.Errorf("Search(uid=p3) = %v, want uid=p3 alone", p.Entries)
	}

	// The header's quick search: OpenDJ has no sAMAccountName and no index
	// on displayName, so it refuses the OR, and the parts it answers still
	// find p3 (uid=p3* is answered from uid's equality index). Only
	// displayName is named: sAMAccountName, not in the schema, matches
	// nothing anyway.
	req.Filter = "(|(cn=*p3*)(uid=p3*)(sAMAccountName=p3*)(mail=p3*)(displayName=*p3*))"
	p, err = c.Search(ctx, req)
	if err != nil {
		t.Fatalf("Search(%s) error = %v", req.Filter, err)
	}
	if len(p.Entries) != 1 || p.Entries[0].DN != "uid=p3,ou=people,dc=example,dc=org" {
		t.Errorf("Search(%s) = %v, want uid=p3 alone", req.Filter, p.Entries)
	}
	if diff := cmp.Diff([]string{"displayName"}, p.Skipped); diff != "" {
		t.Errorf("Search(%s) skipped (-want +got):\n%s", req.Filter, diff)
	}
}

func testOpenDJEntryAndSchema(t *testing.T, c *Conn) {
	if diff := cmp.Diff([]string{"Alice"}, valuesOf(t, c, "uid=alice,ou=people,dc=example,dc=org", "cn")); diff != "" {
		t.Errorf("cn of alice (-want +got):\n%s", diff)
	}
	s, err := c.Schema(context.Background())
	if err != nil {
		t.Fatalf("Schema() error = %v", err)
	}
	if oc := objectClass(s, "person"); oc == nil || !slices.Contains(oc.Must, "cn") || !slices.Contains(oc.Must, "sn") {
		t.Errorf("Schema() person = %+v, want MUST cn and sn", oc)
	}
	if got, want := s.Syntax("member"), "1.3.6.1.4.1.1466.115.121.1.12"; got != want {
		t.Errorf("Syntax(member) = %q, want the DN syntax %q", got, want)
	}
}

// testOpenDJEditGroup adds a person to a group and takes them out again.
func testOpenDJEditGroup(t *testing.T, c *Conn) {
	ctx := context.Background()
	const group, p1 = "cn=dev,ou=groups,dc=example,dc=org", "uid=p1,ou=people,dc=example,dc=org"
	add := []Change{{Op: "add", Attr: "member", Values: []string{p1}}}
	if err := c.Modify(ctx, group, add); err != nil {
		t.Fatalf("Modify(add p1 to dev) error = %v", err)
	}
	if got := valuesOf(t, c, group, "member"); !slices.Contains(got, p1) {
		t.Errorf("member of dev = %q, want p1 in it", got)
	}
	if err := c.Modify(ctx, group, add); !ldap.IsErrorWithCode(err, ldap.LDAPResultAttributeOrValueExists) {
		t.Errorf("Modify(add p1 again) error = %v, want attributeOrValueExists", err)
	}
	if err := c.Modify(ctx, group, []Change{{Op: "delete", Attr: "member", Values: []string{p1}}}); err != nil {
		t.Fatalf("Modify(delete p1 from dev) error = %v", err)
	}
	if got := valuesOf(t, c, group, "member"); slices.Contains(got, p1) {
		t.Errorf("member of dev = %q, want p1 gone", got)
	}
	// Outside what the ACI allows, the server refuses.
	err := c.Modify(ctx, group, []Change{{Op: "add", Attr: "description", Values: []string{"x"}}})
	if !ldap.IsErrorWithCode(err, ldap.LDAPResultInsufficientAccessRights) {
		t.Errorf("Modify(description of dev) error = %v, want insufficientAccessRights", err)
	}
}

// testOpenDJAddRenameDelete creates entries as LDIF import does, one add
// each, then renames, moves and deletes them.
func testOpenDJAddRenameDelete(t *testing.T, c *Conn) {
	ctx := context.Background()
	const sandbox = "ou=sandbox,dc=example,dc=org"
	from, to := "ou=from,"+sandbox, "ou=to,"+sandbox
	for _, ou := range []string{from, to} {
		name := strings.TrimPrefix(strings.SplitN(ou, ",", 2)[0], "ou=")
		if err := c.Add(ctx, ou, []TextAttribute{{Name: "objectClass", Values: []string{"organizationalUnit"}}, {Name: "ou", Values: []string{name}}}); err != nil {
			t.Fatalf("Add(%s) error = %v", ou, err)
		}
	}
	if err := c.Add(ctx, "uid=old,"+from, personAttrs("old")); err != nil {
		t.Fatalf("Add(uid=old) error = %v", err)
	}
	if err := c.Add(ctx, "uid=old,"+from, personAttrs("old")); !ldap.IsErrorWithCode(err, ldap.LDAPResultEntryAlreadyExists) {
		t.Errorf("second Add(uid=old) error = %v, want entryAlreadyExists", err)
	}
	if err := c.Rename(ctx, "uid=old,"+from, "uid=new", "", true); err != nil {
		t.Fatalf("Rename(old to new) error = %v", err)
	}
	if err := c.Rename(ctx, "uid=new,"+from, "uid=new", to, true); err != nil {
		t.Fatalf("Rename(move to ou=to) error = %v", err)
	}
	if diff := cmp.Diff([]string{"new"}, valuesOf(t, c, "uid=new,"+to, "uid")); diff != "" {
		t.Errorf("uid after rename and move (-want +got):\n%s", diff)
	}
	if err := c.Delete(ctx, to); !ldap.IsErrorWithCode(err, ldap.LDAPResultNotAllowedOnNonLeaf) {
		t.Errorf("Delete(non-leaf) error = %v, want notAllowedOnNonLeaf", err)
	}
	for _, dn := range []string{"uid=new," + to, to, from} {
		if err := c.Delete(ctx, dn); err != nil {
			t.Errorf("Delete(%s) error = %v", dn, err)
		}
	}
	if _, err := c.Entry(ctx, from); !ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
		t.Errorf("Entry(deleted) error = %v, want noSuchObject", err)
	}
	// Outside the sandbox alice may not add.
	if err := c.Add(ctx, "uid=x,ou=people,dc=example,dc=org", personAttrs("x")); !ldap.IsErrorWithCode(err, ldap.LDAPResultInsufficientAccessRights) {
		t.Errorf("Add(in ou=people) error = %v, want insufficientAccessRights", err)
	}
}
