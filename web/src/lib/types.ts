// JSON shapes of the Go API (internal/api, internal/ldapx, internal/schema,
// internal/profile). Arrays the server may send as null are typed optional.

import type { Op } from './changes'

export type Scope = 'base' | 'one' | 'sub'

/** GET /api/server: server mode serves only the profiles in its config. */
export interface ServerInfo {
  mode: 'local' | 'server'
  /** The operator's plain-text message (--notice); absent when none. */
  notice?: string
}

/**
 * A profile as GET /api/profiles returns it. Server mode sends only name,
 * label, color, readOnly, connected, userLogin and needsPassword.
 */
export interface Profile {
  name: string
  /** What the UI shows for the profile; name when absent. */
  label?: string
  url?: string
  startTLS?: boolean
  baseDN?: string
  bindDN?: string
  caPEM?: string
  insecureSkipVerify?: boolean
  color?: string
  readOnly: boolean
  /** Saved from the UI (true) or fixed in connections.yaml (false). */
  editable?: boolean
  /** The server can bind without asking for a password. */
  hasPassword?: boolean
  connected: boolean
  /** A bindDNTemplate profile: each person signs in with a username. */
  userLogin: boolean
  /** Connect asks for a password (and, for userLogin, a username). */
  needsPassword: boolean
}

/** Body of POST /api/profiles; the server rejects unknown fields. */
export interface ProfileInput {
  name: string
  label?: string
  url: string
  startTLS: boolean
  baseDN?: string
  bindDN?: string
  caPEM?: string
  insecureSkipVerify: boolean
  color?: string
  readOnly: boolean
}

/** A connection's info plus the profile settings its badges and status bar show. */
export interface Info {
  baseDN: string
  /** The profile's URL; server mode shows it only here, after sign-in. */
  url: string
  startTLS: boolean
  /** The DN the connection bound as (a UPN on an AD UPN template); absent for anonymous. */
  bindDN?: string
  namingContexts?: string[]
  defaultNamingContext?: string
  subschemaSubentry?: string
  vendor?: string
  activeDirectory: boolean
  paging: boolean
  /** The server offers Password Modify (RFC 3062), which hashes by its own policy. */
  passwordModify: boolean
  /** ldaps:// or StartTLS. */
  encrypted: boolean
  readOnly: boolean
  insecureSkipVerify: boolean
  color?: string
}

export interface Value {
  /** UTF-8 text, or standard base64 when base64 is set. */
  text: string
  base64?: boolean
  /** Human-readable rendering (SID, GUID, date, UAC flag names). */
  display?: string
}

export interface Attribute {
  name: string
  values?: Value[]
  /** Number of secret values the server withheld. */
  masked?: number
  /** The attribute has more values than ldaplens reads: values holds the first 10,000. */
  truncated?: boolean
}

export interface Entry {
  dn: string
  attributes?: Attribute[]
}

export interface Page {
  entries?: Entry[]
  /** The search matches more entries than its sizeLimit; a bigger one returns them. */
  more?: boolean
  truncated?: boolean
  referrals?: string[]
}

export interface SearchRequest {
  base: string
  scope: Scope
  filter: string
  attributes?: string[]
  /** Entries wanted: 0 means 500, at most 5000. */
  sizeLimit?: number
}

export interface Ref {
  dn: string
  /** For nested results, the group through which the membership comes. */
  via?: string
}

export interface Membership {
  refs?: Ref[]
  truncated?: boolean
}

export interface AttributeType {
  oid: string
  names?: string[]
  desc?: string
  sup?: string
  syntax?: string
  singleValue?: boolean
  noUserModification?: boolean
  usage?: string
}

export interface ObjectClass {
  oid: string
  names?: string[]
  desc?: string
  sup?: string[]
  kind: 'STRUCTURAL' | 'AUXILIARY' | 'ABSTRACT'
  must?: string[]
  may?: string[]
}

export interface ContentRule {
  oid: string
  names?: string[]
  aux?: string[]
  must?: string[]
  may?: string[]
  not?: string[]
}

export interface Schema {
  attributeTypes?: AttributeType[]
  objectClasses?: ObjectClass[]
  contentRules?: ContentRule[]
  skipped?: string[]
}

export interface CertInfo {
  subject: string
  issuer: string
  notAfter: string
  sha256: string
  pem: string
}

export interface ApiErrorBody {
  code: string
  message: string
  ldapResultCode?: number
  diagnostic?: string
  /** Decoded AD reason for invalid_credentials ("account disabled", ...). */
  reason?: string
  matchedDN?: string
  certificates?: CertInfo[]
  /** The attribute a rejected change was about, when the server can tell. */
  attribute?: string
}

/** Body of POST /api/c/{conn}/changes/apply. */
export interface ApplyRequest {
  entries: { dn: string; changes: Op[] }[]
}

/** The entries applied in order, then the first failure that stopped the run. */
export interface ApplyResult {
  applied: string[]
  failed?: { dn: string; error: ApiErrorBody }
}

/** One attribute of a new entry (POST entries/add); base64 when every value is standard base64. */
export interface TextAttribute {
  name: string
  values: string[]
  base64?: boolean
}

/** Body of POST /api/c/{conn}/entries/add. */
export interface AddRequest {
  dn: string
  attributes: TextAttribute[]
}

/** Body of POST /api/c/{conn}/entries/delete. */
export interface DeleteRequest {
  dn: string
}

/** Body of POST /api/c/{conn}/entries/rename; newParent only for a move. */
export interface RenameRequest {
  dn: string
  newRDN: string
  newParent?: string
  keepOldRDN?: boolean
}

/** The DN an add, delete or rename wrote. */
export interface WriteResult {
  dn: string
}

/** How OpenLDAP gets a new password: hashed by its policy (Password Modify), or SSHA from ldaplens. */
export type PasswordMethod = 'server' | 'ssha'

/** Body of POST /api/c/{conn}/entries/password; Active Directory ignores method. */
export interface PasswordRequest {
  dn: string
  password: string
  method?: PasswordMethod
}
