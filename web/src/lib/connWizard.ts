// Pure helpers of the New connection wizard (app/connect/ProfileForm.tsx): the server step's
// host, port and security to and from a profile URL, and the sign-in step's bind DN templates.

export type Security = 'ldaps' | 'starttls' | 'none'
export type DirKind = 'openldap' | 'ad'

const defaultPort = (s: Security) => (s === 'ldaps' ? '636' : '389')

/**
 * The server field's value: a host, or a pasted ldap:// or ldaps:// URL, which also gives the
 * port and the security (an ldap:// URL: StartTLS, the safer reading).
 */
export function parseServer(v: string): { host: string; port?: string; security?: Security } {
  const m = /^(ldaps?):\/\/(\[[^\]]*\]|[^/:]*)(?::(\d+))?/i.exec(v.trim())
  if (!m) return { host: v.trim() }
  const security: Security = m[1].toLowerCase() === 'ldaps' ? 'ldaps' : 'starttls'
  return { host: m[2], port: m[3] ?? defaultPort(security), security }
}

/** The profile URL and StartTLS flag of the server step; a default port is left out. */
export function buildURL(host: string, port: string, security: Security) {
  const p = port.trim() === defaultPort(security) ? '' : `:${port.trim()}`
  return {
    url: `${security === 'ldaps' ? 'ldaps' : 'ldap'}://${host.trim()}${p}`,
    startTLS: security === 'starttls',
  }
}

/** The server step's fields for a saved profile's URL; an empty URL gives LDAPS on 636. */
export function splitURL(
  url: string,
  startTLS: boolean,
): { host: string; port: string; security: Security } {
  const p = parseServer(url)
  if (!p.security) return { host: p.host, port: '636', security: 'ldaps' }
  const security = p.security === 'ldaps' ? 'ldaps' : startTLS ? 'starttls' : 'none'
  return { host: p.host, port: p.port ?? defaultPort(security), security }
}

/** The DNS domain of a base DN's dc= parts: DC=corp,DC=example gives corp.example. */
export function adDomain(baseDN: string): string {
  return baseDN
    .split(',')
    .map((p) => p.trim())
    .filter((p) => /^dc=/i.test(p))
    .map((p) => p.slice(3).trim())
    .join('.')
}

/** The suggested template: a uid entry under ou=people, or an AD user principal name. */
export function defaultTemplate(kind: DirKind, baseDN: string): string {
  return kind === 'ad'
    ? `{user}@${adDomain(baseDN) || 'corp.example'}`
    : `uid={user},ou=people,${baseDN.trim() || 'dc=example,dc=org'}`
}

/** The server takes a template with {user} exactly once. */
export const templateOK = (t: string) => t.split('{user}').length === 2

/** A profile name from the host, within the server's pattern (letters, digits, . _ -, 64). */
export function defaultName(host: string): string {
  return host
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, '-')
    .replace(/^[^a-z0-9]+|[-.]+$/g, '')
    .slice(0, 64)
}
