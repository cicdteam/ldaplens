import type { Batch, Edit, EntryChanges } from './changes'
import { rdnValue, sameDN, splitDN } from './dn'
import { escapeValue } from './filter'
import { attrKey, baseName } from './schema'
import type { Entry, Ref } from './types'

// Classes whose members live in member/uniqueMember, the only attributes the backend
// resolves for Members (not posixGroup memberUid, not groupOfURLs dynamic groups).
const GROUP_CLASSES = new Set(['group', 'groupofnames', 'groupofuniquenames', 'groupofmembers'])
const MEMBER_ATTRS = new Set(['member', 'uniquemember'])
// A group's membership attribute: the first pair whose class the group has.
const MEMBERSHIP: [string, string][] = [
  ['groupofnames', 'member'],
  ['group', 'member'],
  ['groupofuniquenames', 'uniqueMember'],
  ['posixgroup', 'memberUid'],
]
// Staged changes of these attributes name a member, for Member of.
const STAGED_MEMBER_ATTRS = new Set(['member', 'uniquemember', 'memberuid'])
// NameAndOptionalUID values (uniqueMember) may end in a bit string UID: "dn#'0101'B".
const OPTIONAL_UID = /#'[01]*'B$/
const GROUPS =
  '(|(objectClass=groupOfNames)(objectClass=groupOfUniqueNames)(objectClass=posixGroup)(objectClass=group))'

/** Whether the "Members" list (member/uniqueMember groups) applies to this entry. */
export function isGroup(e: Entry): boolean {
  return (e.attributes ?? []).some((a) => {
    const n = a.name.toLowerCase().split(';')[0]
    if (MEMBER_ATTRS.has(n)) return true
    return (
      n === 'objectclass' && (a.values ?? []).some((v) => GROUP_CLASSES.has(v.text.toLowerCase()))
    )
  })
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

/** Natural order of the first RDN value; labels are parsed once (10000 refs: ~400 ms -> ms). */
export function sortRefs<T extends Ref>(refs: T[]): T[] {
  return refs
    .map((r) => ({ r, label: rdnValue(splitDN(r.dn)[0] ?? r.dn) }))
    .sort((a, b) => collator.compare(a.label, b.label))
    .map((x) => x.r)
}

export function filterRefs<T extends Ref>(refs: T[], q: string): T[] {
  const needle = q.trim().toLowerCase()
  if (!needle) return refs
  return refs.filter(
    (r) => r.dn.toLowerCase().includes(needle) || (r.via ?? '').toLowerCase().includes(needle),
  )
}

/** Text values of the attribute named name (a baseName), base64 ones left out. */
function textValues(e: Entry, name: string): string[] {
  return (e.attributes ?? [])
    .filter((a) => baseName(a.name) === name)
    .flatMap((a) => a.values ?? [])
    .filter((v) => !v.base64)
    .map((v) => v.text)
}

/** The attribute that lists group's members, '' when no class tells (the group is not edited). */
export function membershipAttr(group: Entry): string {
  const classes = textValues(group, 'objectclass').map((c) => c.toLowerCase())
  return MEMBERSHIP.find(([cls]) => classes.includes(cls))?.[1] ?? ''
}

/**
 * Whether group's Members list is edited here: its members are DNs. A memberUid group (also one
 * that lists member DNs beside it) is edited from the member's Member of.
 */
export function membersEditable(group: Entry): boolean {
  const attr = membershipAttr(group)
  return attr !== '' && attr !== 'memberUid'
}

/**
 * Whether value of attr names member: by DN for member and uniqueMember, by uid for memberUid.
 * The UID suffix is ignored on both sides: a Members row carries the DN as the server stores it.
 */
function names(attr: string, value: string, member: Entry): boolean {
  if (baseName(attr) === 'memberuid') return textValues(member, 'uid').includes(value)
  return sameDN(value.replace(OPTIONAL_UID, ''), member.dn.replace(OPTIONAL_UID, ''))
}

/**
 * The staged edit that adds member to group or removes it, or why not. group is the entry with
 * its staged changes applied. A removal names the value as the group stores it (its spelling and
 * a uniqueMember UID suffix), so the server finds it; an addition takes the member's DN, or its
 * first uid for memberUid.
 */
export function membershipEdit(group: Entry, member: Entry, op: 'add' | 'delete'): Edit | string {
  const attr = membershipAttr(group)
  if (!attr) return 'Not a group ldaplens can edit'
  const stored = textValues(group, baseName(attr)).find((v) => names(attr, v, member))
  if (op === 'delete')
    return stored ? { kind: 'delete', attr, value: stored } : 'Not a direct member'
  if (stored) return 'Already a member'
  const value = attr === 'memberUid' ? textValues(member, 'uid')[0] : member.dn
  return value ? { kind: 'add', attr, value } : 'Entry has no uid'
}

/** A membership row with the staged change behind its mark. */
export interface MembershipRow extends Ref {
  mark?: 'added' | 'deleted'
  /** Stages the opposite change: restores a deleted row, drops an added one. */
  undo?: Edit
}

/** Shows a staged op of attr's value on rows: marks dn's direct row deleted, or adds a row for dn. */
function overlay(
  rows: MembershipRow[],
  dn: string,
  op: 'add' | 'delete',
  attr: string,
  value: string,
): void {
  const mark = op === 'add' ? 'added' : 'deleted'
  const undo: Edit = { kind: op === 'add' ? 'delete' : 'add', attr, value }
  const row = rows.find((r) => !r.via && sameDN(r.dn, dn))
  // A staged add of a member the list has already (a stale one) shows nothing new.
  if (!row) rows.push({ dn, mark, undo })
  else if (op === 'delete') Object.assign(row, { mark, undo })
}

/** group's Members with its staged changes (ec) of its membership attribute on top. */
export function membersView(
  refs: Ref[],
  group: Entry,
  ec: EntryChanges | undefined,
): MembershipRow[] {
  const attr = membershipAttr(group)
  // memberUid values are uids, not DNs: they name no row.
  if (!attr || attr === 'memberUid') return refs
  const rows: MembershipRow[] = refs.map((r) => ({ ...r }))
  for (const c of ec?.changes ?? [])
    if (attrKey(c.attr) === attrKey(attr))
      for (const value of c.values) overlay(rows, value, c.op, c.attr, value)
  return rows
}

/** entry's Member of with the batch's staged member, uniqueMember and memberUid changes on top. */
export function memberOfView(refs: Ref[], entry: Entry, batch: Batch): MembershipRow[] {
  const rows: MembershipRow[] = refs.map((r) => ({ ...r }))
  for (const ec of batch)
    for (const c of ec.changes)
      if (STAGED_MEMBER_ATTRS.has(baseName(c.attr)))
        for (const value of c.values)
          if (names(c.attr, value, entry)) overlay(rows, ec.dn, c.op, c.attr, value)
  return rows
}

/**
 * Why a row cannot be removed here: a nested row comes through another group, and one the server
 * lists in isMemberOf or memberOf may be nested too. '' if direct.
 */
export function inheritedHint(r: Ref): string {
  if (r.via) return `Inherited through ${r.via}`
  return r.source ? `Listed in the entry's ${r.source}: open the group to change it` : ''
}

/** Groups whose cn contains text, as "Add to group…" suggests them. */
export function groupFilter(text: string): string {
  return `(&${GROUPS}(cn=*${escapeValue(text.trim())}*))`
}
