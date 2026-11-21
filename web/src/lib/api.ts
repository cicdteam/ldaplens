import type {
  AddRequest,
  ApiErrorBody,
  ApplyRequest,
  ApplyResult,
  DeleteRequest,
  Entry,
  Info,
  Membership,
  Page,
  PasswordRequest,
  Profile,
  ProfileInput,
  RenameRequest,
  Schema,
  SearchRequest,
  ServerInfo,
  WriteResult,
} from './types'

export class ApiError extends Error {
  readonly status: number
  readonly body: ApiErrorBody

  constructor(status: number, body: ApiErrorBody) {
    super(body.message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }

  get code(): string {
    return this.body.code
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError
}

/** Resolves an API path against <base href>, so --base-path deployments work. */
export function apiURL(path: string, base: string = document.baseURI): string {
  return new URL(path, base).toString()
}

/** The router basename: the path of <base href> without the trailing slash. */
export function routerBasename(base: string = document.baseURI): string {
  const p = new URL(base).pathname.replace(/\/+$/, '')
  return p === '' ? '/' : p
}

export type ErrorKind = 'auth' | 'session' | 'other'

/**
 * auth: the access token is missing (open the login link again).
 * session: the LDAP session is gone (reconnect the profile).
 */
export function errorKind(e: unknown): ErrorKind {
  if (!isApiError(e) || e.status !== 401) return 'other'
  if (e.code === 'unauthorized') return 'auth'
  if (e.code === 'not_connected') return 'session'
  return 'other'
}

/**
 * The query retry predicate: a read refused with 503 busy (the server is
 * saturated) is retried up to 3 times; any other error is final.
 */
export function retryBusy(failureCount: number, e: unknown): boolean {
  return isApiError(e) && e.code === 'busy' && failureCount < 3
}

/** The UI's text for the per-user login refusals; other codes show the server's message. */
const messages = new Map([
  ['username_required', 'Enter your username.'],
  [
    'invalid_username',
    'A username has only letters, digits, ".", "_" and "-", starts with a letter or digit and is at most 64 characters long.',
  ],
  // ponytail: a fixed wait, not Retry-After; read the header into ApiError to show the seconds.
  ['too_many_attempts', 'Too many failed sign-ins. Wait a minute and try again.'],
])

export function errorMessage(e: unknown): string {
  if (isApiError(e)) {
    const m = messages.get(e.code) ?? e.body.message
    return e.body.reason ? `${m} (${e.body.reason})` : m
  }
  if (e instanceof Error) return e.message
  return String(e)
}

const passwordHints = new Map([
  ['invalid_credentials', 'The stored password did not work; enter one to try.'],
  ['password_unavailable', 'The stored password is unavailable; enter one to try.'],
])

/** Set when a connect with the stored password failed: why to ask for one. */
export function passwordHint(e: unknown): string | undefined {
  return isApiError(e) ? passwordHints.get(e.code) : undefined
}

let authLost = false
const authListeners = new Set<() => void>()

/** Flips to true once any request fails with 401 unauthorized. */
export const authStore = {
  subscribe(fn: () => void): () => void {
    authListeners.add(fn)
    return () => authListeners.delete(fn)
  },
  get(): boolean {
    return authLost
  },
}

function isErrorBody(v: unknown): v is ApiErrorBody {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  return typeof o.code === 'string' && typeof o.message === 'string'
}

async function request<T>(
  method: 'GET' | 'POST',
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  let res: Response
  try {
    res = await fetch(apiURL(path), {
      method,
      signal,
      credentials: 'same-origin',
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch (e) {
    if (signal?.aborted) throw e
    throw new ApiError(0, { code: 'offline', message: 'Cannot reach the ldaplens server' })
  }
  const text = await res.text()
  let data: unknown
  try {
    data = text ? JSON.parse(text) : undefined
  } catch {
    data = undefined
  }
  if (!res.ok) {
    // Only a plain text body is a message: a proxy in front may answer with
    // its own HTML error page.
    const plain = res.headers.get('Content-Type')?.startsWith('text/plain') && text.trim()
    const err = new ApiError(
      res.status,
      isErrorBody(data)
        ? data
        : {
            code: `http_${res.status}`,
            message: plain || unreadable(res.status),
          },
    )
    if (errorKind(err) === 'auth' && !authLost) {
      authLost = true
      authListeners.forEach((fn) => fn())
    }
    throw err
  }
  return data as T
}

const conn = (c: string) => `api/c/${encodeURIComponent(c)}`
const query = (params: Record<string, string>) => new URLSearchParams(params).toString()

export const api = {
  server: () => request<ServerInfo>('GET', 'api/server'),
  profiles: () => request<Profile[]>('GET', 'api/profiles'),
  saveProfile: (p: ProfileInput) => request<Profile>('POST', 'api/profiles', p),
  /** username is for a userLogin profile; without one the body has no username key. */
  connect: (profile: string, password = '', username?: string) =>
    request<Info>('POST', 'api/connect', { profile, password, username }),
  disconnect: (profile: string) => request<void>('POST', 'api/disconnect', { profile }),
  info: (c: string, signal?: AbortSignal) =>
    request<Info>('GET', `${conn(c)}/info`, undefined, signal),
  schema: (c: string, reload = false, signal?: AbortSignal) =>
    request<Schema>('GET', `${conn(c)}/schema${reload ? '?reload=true' : ''}`, undefined, signal),
  search: (c: string, req: SearchRequest, signal?: AbortSignal) =>
    request<Page>('POST', `${conn(c)}/search`, req, signal),
  entry: (c: string, dn: string, signal?: AbortSignal) =>
    request<Entry>('GET', `${conn(c)}/entry?${query({ dn })}`, undefined, signal),
  memberOf: (c: string, dn: string, nested: boolean, signal?: AbortSignal) =>
    request<Membership>(
      'GET',
      `${conn(c)}/memberof?${query({ dn, nested: String(nested) })}`,
      undefined,
      signal,
    ),
  members: (c: string, dn: string, nested: boolean, signal?: AbortSignal) =>
    request<Membership>(
      'GET',
      `${conn(c)}/members?${query({ dn, nested: String(nested) })}`,
      undefined,
      signal,
    ),
  apply: (c: string, req: ApplyRequest) =>
    request<ApplyResult>('POST', `${conn(c)}/changes/apply`, req),
  addEntry: (c: string, req: AddRequest) =>
    request<WriteResult>('POST', `${conn(c)}/entries/add`, req),
  deleteEntry: (c: string, req: DeleteRequest) =>
    request<WriteResult>('POST', `${conn(c)}/entries/delete`, req),
  renameEntry: (c: string, req: RenameRequest) =>
    request<WriteResult>('POST', `${conn(c)}/entries/rename`, req),
  setPassword: (c: string, req: PasswordRequest) =>
    request<WriteResult>('POST', `${conn(c)}/entries/password`, req),
}

/** The message for an error answer without a body ldaplens wrote, such as a proxy's page. */
function unreadable(status: number): string {
  return status >= 500
    ? `The ldaplens server did not answer properly (HTTP ${status}). Try again in a moment, or reload the page.`
    : `Unexpected answer from the ldaplens server (HTTP ${status}). Reload the page.`
}

/** What the UI shows for a profile: its label, else its name. */
export function profileLabel(p: Pick<Profile, 'name' | 'label'>): string {
  return p.label || p.name
}

/**
 * The fields POST /api/profiles accepts; it rejects the computed ones. Only
 * an editable profile gets here, and local mode sends every field of it.
 */
export function toProfileInput(p: Profile): ProfileInput {
  const { name, label, url, startTLS, baseDN, bindDN, caPEM, insecureSkipVerify, color, readOnly } =
    p
  return {
    name,
    label,
    url: url ?? '',
    startTLS: !!startTLS,
    baseDN,
    bindDN,
    caPEM,
    insecureSkipVerify: !!insecureSkipVerify,
    color,
    readOnly,
  }
}
