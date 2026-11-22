// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Andrei Taranik

//go:build integration

package ldapx

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/cicdteam/ldaplens/internal/profile"
)

// openDJImage is Open Identity Platform's OpenDJ, pinned by digest.
const openDJImage = "openidentityplatform/opendj:5.2.0@sha256:111f7270355c0163eff2d5d578d99b2ee026cf7134752ecc37b80e865a91904b"

// openDJSeed is the directory TestOpenDJMemberOf works on: alice is a
// member of dev (member), ops (uniqueMember) and posix (memberUid), and of
// all through dev. OpenDJ creates no entry for the base DN by itself.
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

dn: uid=alice,ou=people,dc=example,dc=org
changetype: add
objectClass: inetOrgPerson
uid: alice
cn: Alice
sn: A
userPassword: alicepassword

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

// TestOpenDJMemberOf runs Member of as a plain user of OpenDJ, as on a real
// directory: memberUid has no index there, and with more entries than the
// backend's index entry limit (lowered to 2 here, as a big directory is
// over the default 4000) OpenDJ refuses any search naming it with result
// 50 unless the user holds the unindexed-search privilege. Member of still
// lists the groups found by member and uniqueMember, says memberUid was
// skipped, and adds the nested group the server lists in isMemberOf.
func TestOpenDJMemberOf(t *testing.T) {
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
		"/opt/opendj/bin/dsconfig", "set-backend-prop", "--backend-name", "userRoot", "--set", "index-entry-limit:2",
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
		m, err := c.MemberOf(ctx, p.BindDN, tc.nested)
		if err != nil {
			t.Fatalf("MemberOf(alice, nested %v) error = %v", tc.nested, err)
		}
		if diff := cmp.Diff(tc.want, m); diff != "" {
			t.Errorf("MemberOf(alice, nested %v) mismatch (-want +got):\n%s", tc.nested, diff)
		}
	}
}
