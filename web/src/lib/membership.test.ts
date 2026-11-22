import { describe, expect, it } from 'vitest'
import type { Batch } from './changes'
import {
  filterRefs,
  groupFilter,
  inheritedHint,
  isGroup,
  memberOfView,
  membersEditable,
  membersView,
  membershipAttr,
  membershipEdit,
  sortRefs,
} from './membership'
import type { Entry } from './types'

const entry = (attrs: Record<string, string[]>): Entry => ({
  dn: 'cn=x,dc=org',
  attributes: Object.entries(attrs).map(([name, v]) => ({
    name,
    values: v.map((text) => ({ text })),
  })),
})

describe('isGroup', () => {
  it.each([
    [{ objectClass: ['top', 'groupOfNames'] }, true],
    [{ objectClass: ['top', 'group'] }, true],
    // The backend resolves member/uniqueMember only, not memberUid or dynamic groups.
    [{ objectClass: ['posixGroup'] }, false],
    [{ objectClass: ['posixGroup'], memberUid: ['jdoe'] }, false],
    [{ objectClass: ['groupOfURLs'] }, false],
    [{ objectClass: ['posixGroup', 'groupOfNames'] }, true],
    [{ objectClass: ['top', 'organizationalRole'], member: ['cn=a'] }, true],
    [{ objectClass: ['top'], uniqueMember: ['cn=a'] }, true],
    [{ objectClass: ['inetOrgPerson'] }, false],
  ])('%j', (attrs, want) => {
    expect(isGroup(entry(attrs))).toBe(want)
  })
})

describe('refs', () => {
  const refs = [
    { dn: 'cn=user10,ou=p,dc=org' },
    { dn: 'cn=User2,ou=p,dc=org', via: 'cn=devs,ou=g,dc=org' },
    { dn: 'cn=admins,ou=g,dc=org' },
  ]

  it('sorts by the first RDN naturally', () => {
    const direct = refs.map(({ dn }) => ({ dn }))
    expect(sortRefs(direct).map((r) => r.dn)).toEqual([
      'cn=admins,ou=g,dc=org',
      'cn=User2,ou=p,dc=org',
      'cn=user10,ou=p,dc=org',
    ])
  })

  it('puts direct groups first, then nested ones, then those only the server lists', () => {
    const mixed = [
      { dn: 'cn=access,ou=g,dc=org', source: 'isMemberOf' },
      { dn: 'cn=zeta,ou=g,dc=org' },
      { dn: 'cn=Admins,ou=g,dc=org', source: 'isMemberOf' },
      { dn: 'cn=beta,ou=g,dc=org', via: 'cn=zeta,ou=g,dc=org' },
    ]
    expect(sortRefs(mixed).map((r) => r.dn)).toEqual([
      'cn=zeta,ou=g,dc=org', // direct
      'cn=beta,ou=g,dc=org', // nested
      'cn=access,ou=g,dc=org', // the server's list
      'cn=Admins,ou=g,dc=org',
    ])
  })

  it('filters on DN and via', () => {
    expect(filterRefs(refs, 'DEVS').map((r) => r.dn)).toEqual(['cn=User2,ou=p,dc=org'])
    expect(filterRefs(refs, ' ')).toBe(refs)
  })
})

const OU = 'ou=e2e,dc=example,dc=org'
const named = (dn: string, attrs: Record<string, string[]>): Entry => ({ ...entry(attrs), dn })

describe('membershipAttr', () => {
  it.each([
    [['top', 'groupOfNames'], 'member'],
    [['top', 'group'], 'member'],
    [['groupOfUniqueNames'], 'uniqueMember'],
    [['posixGroup'], 'memberUid'],
    // The list's order decides, not the entry's.
    [['posixGroup', 'groupOfNames'], 'member'],
    [['GROUPOFNAMES'], 'member'],
    [['inetOrgPerson'], ''],
  ])('%j -> %s', (classes, want) => {
    expect(membershipAttr(entry({ objectClass: classes }))).toBe(want)
  })
})

describe('membersEditable', () => {
  it.each([
    [['top', 'groupOfNames'], true],
    [['group'], true],
    [['groupOfUniqueNames'], true],
    [['posixGroup', 'groupOfNames'], true],
    // Members lists DNs; a memberUid group is edited from the member's Member of.
    [['posixGroup'], false],
    [['groupOfMembers', 'posixGroup'], false],
    [['groupOfMembers'], false],
  ])('%j -> %s', (classes, want) => {
    expect(membersEditable(entry({ objectClass: classes }))).toBe(want)
  })
})

describe('membershipEdit', () => {
  const joe = named(`uid=joe,${OU}`, { uid: ['joe', 'jdoe'] })
  const stored = `UID=Joe, ${OU.toUpperCase()}`
  it('adds the DN, or the first uid to a posixGroup', () => {
    expect(
      membershipEdit(entry({ objectClass: ['groupOfNames'], member: ['cn=x'] }), joe, 'add'),
    ).toEqual({ kind: 'add', attr: 'member', value: joe.dn })
    expect(membershipEdit(entry({ objectClass: ['posixGroup'] }), joe, 'add')).toEqual({
      kind: 'add',
      attr: 'memberUid',
      value: 'joe',
    })
  })
  it('refuses what it cannot add', () => {
    expect(
      membershipEdit(entry({ objectClass: ['posixGroup'] }), named(`cn=x,${OU}`, {}), 'add'),
    ).toBe('Entry has no uid')
    expect(
      membershipEdit(entry({ objectClass: ['groupOfNames'], member: [stored] }), joe, 'add'),
    ).toBe('Already a member')
    expect(
      membershipEdit(
        entry({ objectClass: ['organizationalRole'], roleOccupant: [joe.dn] }),
        joe,
        'add',
      ),
    ).toBe('Not a group ldaplens can edit')
  })
  it('removes the value as the group stores it', () => {
    expect(
      membershipEdit(
        entry({ objectClass: ['groupOfNames'], member: ['cn=x', stored] }),
        joe,
        'delete',
      ),
    ).toEqual({ kind: 'delete', attr: 'member', value: stored })
    const withUID = `${joe.dn}#'0101'B`
    expect(
      membershipEdit(
        entry({ objectClass: ['groupOfUniqueNames'], uniqueMember: [withUID] }),
        joe,
        'delete',
      ),
    ).toEqual({ kind: 'delete', attr: 'uniqueMember', value: withUID })
    // The uid the group stores (joe's second), not joe's first.
    expect(
      membershipEdit(entry({ objectClass: ['posixGroup'], memberUid: ['jdoe'] }), joe, 'delete'),
    ).toEqual({ kind: 'delete', attr: 'memberUid', value: 'jdoe' })
    // memberUid compares exactly.
    expect(
      membershipEdit(entry({ objectClass: ['posixGroup'], memberUid: ['JOE'] }), joe, 'delete'),
    ).toBe('Not a direct member')
    // A member known only by its DN (its entry may be gone) still goes.
    expect(
      membershipEdit(
        entry({ objectClass: ['groupOfNames'], member: [joe.dn] }),
        { dn: joe.dn },
        'delete',
      ),
    ).toEqual({ kind: 'delete', attr: 'member', value: joe.dn })
  })
  // A Members row passes the DN the server returned, UID suffix and all.
  it.each([`uid=a,dc=x#'0101'B`, 'uid=a,dc=x'])('removes uniqueMember %s as stored', (dn) => {
    const group = entry({
      objectClass: ['groupOfUniqueNames'],
      uniqueMember: [`uid=a,dc=x#'0101'B`],
    })
    expect(membershipEdit(group, { dn }, 'delete')).toEqual({
      kind: 'delete',
      attr: 'uniqueMember',
      value: `uid=a,dc=x#'0101'B`,
    })
  })
})

describe('membersView', () => {
  const team = named(`cn=team,${OU}`, { objectClass: ['groupOfNames'] })
  const refs = [
    { dn: `uid=a,${OU}` },
    { dn: `uid=b,${OU}` },
    { dn: `uid=c,${OU}`, via: `cn=sub,${OU}` },
  ]
  it('marks staged deletes and adds rows for staged adds', () => {
    const ec = {
      dn: team.dn,
      changes: [
        { op: 'delete' as const, attr: 'member', values: [`UID=B,${OU}`] },
        { op: 'add' as const, attr: 'member', values: [`uid=d,${OU}`] },
      ],
    }
    expect(membersView(refs, team, ec)).toEqual([
      { dn: `uid=a,${OU}` },
      {
        dn: `uid=b,${OU}`,
        mark: 'deleted',
        undo: { kind: 'add', attr: 'member', value: `UID=B,${OU}` },
      },
      { dn: `uid=c,${OU}`, via: `cn=sub,${OU}` },
      {
        dn: `uid=d,${OU}`,
        mark: 'added',
        undo: { kind: 'delete', attr: 'member', value: `uid=d,${OU}` },
      },
    ])
  })
  it('leaves other attributes, and groups it cannot edit, alone', () => {
    const ec = {
      dn: team.dn,
      changes: [{ op: 'delete' as const, attr: 'description', values: ['x'] }],
    }
    expect(membersView(refs, team, ec)).toEqual(refs)
    expect(membersView(refs, named(team.dn, { objectClass: ['organizationalRole'] }), ec)).toBe(
      refs,
    )
  })
  it('never marks a nested row; a staged delete without a direct row gets its own', () => {
    const both = [{ dn: `uid=c,${OU}`, via: `cn=sub,${OU}` }, { dn: `uid=c,${OU}` }]
    const ec = {
      dn: team.dn,
      changes: [{ op: 'delete' as const, attr: 'member', values: [`uid=c,${OU}`, `uid=e,${OU}`] }],
    }
    expect(membersView(both, team, ec)).toEqual([
      { dn: `uid=c,${OU}`, via: `cn=sub,${OU}` },
      {
        dn: `uid=c,${OU}`,
        mark: 'deleted',
        undo: { kind: 'add', attr: 'member', value: `uid=c,${OU}` },
      },
      {
        dn: `uid=e,${OU}`,
        mark: 'deleted',
        undo: { kind: 'add', attr: 'member', value: `uid=e,${OU}` },
      },
    ])
  })
  it('leaves a memberUid group alone', () => {
    const px = named(`cn=px,${OU}`, { objectClass: ['posixGroup'] })
    const ec = {
      dn: px.dn,
      changes: [{ op: 'add' as const, attr: 'memberUid', values: ['joe'] }],
    }
    expect(membersView(refs, px, ec)).toBe(refs)
  })
})

describe('memberOfView', () => {
  it('marks the groups whose staged changes name the entry', () => {
    const joe = named(`uid=joe,${OU}`, { uid: ['joe'] })
    const refs = [{ dn: `cn=g1,${OU}` }, { dn: `cn=g2,${OU}`, via: `cn=g1,${OU}` }]
    const batch: Batch = [
      { dn: `CN=G1,${OU}`, changes: [{ op: 'delete', attr: 'member', values: [`UID=JOE,${OU}`] }] },
      {
        dn: `cn=g3,${OU}`,
        changes: [{ op: 'add', attr: 'uniqueMember', values: [`${joe.dn}#'01'B`] }],
      },
      { dn: `cn=px,${OU}`, changes: [{ op: 'add', attr: 'memberUid', values: ['joe'] }] },
      // memberUid compares exactly; other members and other attributes do not count.
      { dn: `cn=py,${OU}`, changes: [{ op: 'add', attr: 'memberUid', values: ['JOE'] }] },
      { dn: `cn=pz,${OU}`, changes: [{ op: 'add', attr: 'member', values: [`uid=ann,${OU}`] }] },
      { dn: joe.dn, changes: [{ op: 'add', attr: 'description', values: ['x'] }] },
    ]
    expect(memberOfView(refs, joe, batch)).toEqual([
      {
        dn: `cn=g1,${OU}`,
        mark: 'deleted',
        undo: { kind: 'add', attr: 'member', value: `UID=JOE,${OU}` },
      },
      { dn: `cn=g2,${OU}`, via: `cn=g1,${OU}` },
      {
        dn: `cn=g3,${OU}`,
        mark: 'added',
        undo: { kind: 'delete', attr: 'uniqueMember', value: `${joe.dn}#'01'B` },
      },
      {
        dn: `cn=px,${OU}`,
        mark: 'added',
        undo: { kind: 'delete', attr: 'memberUid', value: 'joe' },
      },
    ])
  })
})

describe('hints and filters', () => {
  it('says why a nested row stays', () => {
    expect(inheritedHint({ dn: `uid=a,${OU}`, via: `cn=sub,${OU}` })).toBe(
      `Inherited through cn=sub,${OU}`,
    )
    expect(inheritedHint({ dn: `uid=a,${OU}` })).toBe('')
    expect(inheritedHint({ dn: `cn=g,${OU}`, source: 'isMemberOf' })).toBe(
      "Listed in the entry's isMemberOf: open the group to change it",
    )
  })
  it('finds groups by cn, escaped', () => {
    expect(groupFilter(' a*(b) ')).toBe(
      '(&(|(objectClass=groupOfNames)(objectClass=groupOfUniqueNames)(objectClass=posixGroup)(objectClass=group))(cn=*a\\2a\\28b\\29*))',
    )
  })
})
