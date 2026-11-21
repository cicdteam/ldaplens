import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// The e2e LDIF loaded after deploy/demo-ldif/demo.ldif: a group with 5000
// members for the value cap and one person per editing scenario under
// ou=e2e, so specs never share an entry.
export const BASE = 'dc=example,dc=org'
export const E2E_OU = `ou=e2e,${BASE}`
export const HUGE_GROUP = `cn=huge,ou=groups,${BASE}`
export const e2eDN = (name: string) => `uid=${name},${E2E_OU}`
export const e2eOU = (name: string) => `ou=${name},${E2E_OU}`
export const e2eGroup = (name: string) => `cn=${name},${E2E_OU}`

/** A demo avatar, for uploads and the people seeded with a photo. */
export const PHOTO = fileURLToPath(
  new URL('../../deploy/demo-ldif/photos/avatar-1.jpg', import.meta.url),
)
/** The demo certificate, for uploads. */
export const CERT = fileURLToPath(new URL('../../deploy/demo-ldif/cert.der', import.meta.url))

/** People seeded with PHOTO as their jpegPhoto, one per binary scenario. */
export const E2E_PHOTO_PEOPLE: string[] = [
  'snapshot',
  'photodel',
  'bigfile',
  'clonephoto',
  'photoswap',
  'photoscroll',
]

/** OUs under ou=e2e, each with the people (uids) it holds. */
export const E2E_OUS: Record<string, string[]> = { moveto: [], full: ['inside'] }

/** One inetOrgPerson per scenario. */
export const E2E_PEOPLE: string[] = [
  'read',
  'edit',
  'markers',
  'apply1',
  'apply2',
  'partial1',
  'partial2',
  'conflict',
  'rejected',
  'keys',
  'smoke',
  'inflight',
  'focus',
  'aux',
  'staged',
  'clonesrc',
  'renamer',
  'deleteme',
  'grouped',
  'pwssha',
  'pwserver',
  'joiner',
  'keeper',
  'dropped',
  'soloist',
  'posixer',
  'leaver',
  'racer',
  'quota',
  'photoup',
  'langer',
  'certholder',
  'layout',
  'pending',
  'pendingdrop',
]

/** Groups under ou=e2e for the membership scenarios and the journey: their class and the uids in them. */
export const E2E_GROUPS: Record<string, { cls: 'groupOfNames' | 'posixGroup'; members: string[] }> =
  {
    'join-team': { cls: 'groupOfNames', members: ['keeper'] },
    'drop-team': { cls: 'groupOfNames', members: ['keeper', 'dropped'] },
    'solo-team': { cls: 'groupOfNames', members: ['soloist'] },
    'posix-team': { cls: 'posixGroup', members: [] },
    'leave-team': { cls: 'groupOfNames', members: ['keeper', 'leaver'] },
    'grouped-names': { cls: 'groupOfNames', members: ['grouped'] },
    'grouped-posix': { cls: 'posixGroup', members: ['grouped'] },
    'journey-names': { cls: 'groupOfNames', members: ['keeper'] },
    'journey-posix': { cls: 'posixGroup', members: [] },
  }

function group(name: string, { cls, members }: (typeof E2E_GROUPS)[string]): string {
  const values =
    cls === 'posixGroup'
      ? ['gidNumber: 20000', ...members.map((m) => `memberUid: ${m}`)]
      : members.map((m) => `member: ${e2eDN(m)}`)
  return [`dn: ${e2eGroup(name)}`, `objectClass: ${cls}`, `cn: ${name}`, ...values, ''].join('\n')
}

function person(name: string, parent = E2E_OU, extra: string[] = []): string {
  return [
    `dn: uid=${name},${parent}`,
    'objectClass: inetOrgPerson',
    `uid: ${name}`,
    `cn: E2E ${name}`,
    `sn: ${name}`,
    `mail: ${name}@example.org`,
    `description: ${name} description`,
    ...extra,
    '',
  ].join('\n')
}

export function fixtureLDIF(): string {
  const huge = [`dn: ${HUGE_GROUP}`, 'objectClass: groupOfNames', 'cn: huge']
  for (let i = 1; i <= 5000; i++) huge.push(`member: uid=user${i},ou=people,${BASE}`)
  return [
    [`dn: ${E2E_OU}`, 'objectClass: organizationalUnit', 'ou: e2e', ''].join('\n'),
    ...Object.entries(E2E_OUS).flatMap(([ou, people]) => [
      [`dn: ${e2eOU(ou)}`, 'objectClass: organizationalUnit', `ou: ${ou}`, ''].join('\n'),
      ...people.map((p) => person(p, e2eOU(ou))),
    ]),
    huge.join('\n') + '\n',
    ...E2E_PEOPLE.map((p) => person(p)),
    ...E2E_PHOTO_PEOPLE.map((p) =>
      person(p, E2E_OU, [`jpegPhoto:: ${readFileSync(PHOTO).toString('base64')}`]),
    ),
    ...Object.entries(E2E_GROUPS).map(([name, g]) => group(name, g)),
    // An auxiliary class with a MAY value: removing the class flags loginShell, under Optional.
    person('sections', E2E_OU, [
      'objectClass: posixAccount',
      'uidNumber: 7100',
      'gidNumber: 7100',
      'homeDirectory: /home/sections',
      'loginShell: /bin/sh',
    ]),
    // With the demo's "Doe, John", what a name search for "doe" finds.
    [
      `dn: uid=jane.doe,${E2E_OU}`,
      'objectClass: inetOrgPerson',
      'uid: jane.doe',
      'cn: Jane Doe',
      'sn: Doe',
      'mail: jane.doe@example.org',
      '',
    ].join('\n'),
    // The tree shows its displayName beside the RDN: uid=jdoe John Doe. The card pins a
    // person's cn, uid, mail, title, manager and telephoneNumber.
    [
      `dn: uid=jdoe,${E2E_OU}`,
      'objectClass: inetOrgPerson',
      'uid: jdoe',
      'cn: J. Doe',
      'displayName: John Doe',
      'sn: Doe',
      'mail: jdoe@example.org',
      'title: Site Reliability Engineer',
      `manager: uid=jane.doe,${E2E_OU}`,
      'telephoneNumber: +49 30 5550 0142',
      '',
    ].join('\n'),
  ].join('\n')
}
