import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  api,
  apiURL,
  authStore,
  errorKind,
  errorMessage,
  passwordHint,
  retryBusy,
  routerBasename,
  toProfileInput,
} from './api'
import type { Profile } from './types'

function respond(status: number, body: unknown) {
  const text = typeof body === 'string' ? body : JSON.stringify(body)
  return vi.fn<typeof fetch>(async () => new Response(status === 204 ? null : text, { status }))
}

beforeEach(() => {
  vi.stubGlobal('document', { baseURI: 'http://localhost:8080/ldap/' })
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('apiURL', () => {
  it('resolves under the base path', () => {
    expect(apiURL('api/profiles', 'http://h/ldap/')).toBe('http://h/ldap/api/profiles')
    expect(apiURL('api/profiles', 'http://h/')).toBe('http://h/api/profiles')
  })
})

describe('routerBasename', () => {
  it.each([
    ['http://h/', '/'],
    ['http://h/ldap/', '/ldap'],
    ['http://h/a/b/', '/a/b'],
  ])('%s -> %s', (base, want) => {
    expect(routerBasename(base)).toBe(want)
  })
})

describe('request', () => {
  it('returns parsed JSON', async () => {
    vi.stubGlobal('fetch', respond(200, [{ name: 'demo' }]))
    await expect(api.profiles()).resolves.toEqual([{ name: 'demo' }])
  })

  it('maps an error body to ApiError', async () => {
    vi.stubGlobal(
      'fetch',
      respond(404, { code: 'no_such_object', message: 'nope', matchedDN: 'dc=x' }),
    )
    const err = await api.entry('demo', 'cn=a,dc=x').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(404)
    expect((err as ApiError).code).toBe('no_such_object')
    expect((err as ApiError).body.matchedDN).toBe('dc=x')
  })

  it('wraps a non-JSON error', async () => {
    vi.stubGlobal('fetch', respond(502, 'Bad Gateway\n'))
    const err = (await api.profiles().catch((e: unknown) => e)) as ApiError
    expect(err.code).toBe('http_502')
    expect(err.message).toBe('Bad Gateway')
  })

  it('does not show a proxy error page as the message', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>(
        async () =>
          new Response('<!DOCTYPE html><html><title>502: Bad gateway</title></html>', {
            status: 502,
            headers: { 'Content-Type': 'text/html; charset=UTF-8' },
          }),
      ),
    )
    const err = (await api.profiles().catch((e: unknown) => e)) as ApiError
    expect(err.code).toBe('http_502')
    expect(err.message).toBe('The server answered HTTP 502')
  })

  it('reports an unreachable server as offline', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn<typeof fetch>(async () => Promise.reject(new TypeError('Failed to fetch'))),
    )
    const err = (await api.profiles().catch((e: unknown) => e)) as ApiError
    expect(err.status).toBe(0)
    expect(err.code).toBe('offline')
  })

  it('reads the mode from api/server', async () => {
    const fetch = respond(200, { mode: 'server' })
    vi.stubGlobal('fetch', fetch)
    await expect(api.server()).resolves.toEqual({ mode: 'server' })
    expect(new URL(fetch.mock.calls[0][0] as string).pathname).toBe('/ldap/api/server')
  })

  it('handles 204', async () => {
    vi.stubGlobal('fetch', respond(204, ''))
    await expect(api.disconnect('demo')).resolves.toBeUndefined()
  })

  it('sends JSON bodies and DNs verbatim in the query', async () => {
    const fetch = respond(200, { dn: 'x' })
    vi.stubGlobal('fetch', fetch)
    const dn = 'cn=Doe\\, John+uid=jd,ou=Sales\\, EMEA,dc=example,dc=org'
    await api.entry('my conn', dn)
    const url = new URL(fetch.mock.calls[0][0] as string)
    expect(url.pathname).toBe('/ldap/api/c/my%20conn/entry')
    expect(url.searchParams.get('dn')).toBe(dn)

    const post = respond(200, {})
    vi.stubGlobal('fetch', post)
    await api.connect('demo', 'pw')
    const init = post.mock.calls[0][1] as RequestInit
    expect(init.method).toBe('POST')
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
    expect(JSON.parse(init.body as string)).toEqual({ profile: 'demo', password: 'pw' })
  })

  it('sends the username to connect only when given', async () => {
    const post = respond(200, {})
    vi.stubGlobal('fetch', post)
    await api.connect('people', 'pw', 'user001')
    await api.connect('demo', 'pw')
    const bodies = post.mock.calls.map(([, init]) =>
      JSON.parse((init as RequestInit).body as string),
    )
    expect(bodies).toStrictEqual([
      { profile: 'people', password: 'pw', username: 'user001' },
      { profile: 'demo', password: 'pw' },
    ])
  })

  it('posts entry writes to their endpoints', async () => {
    const post = respond(200, { dn: 'uid=a,dc=x' })
    vi.stubGlobal('fetch', post)
    await api.addEntry('demo', {
      dn: 'uid=a,dc=x',
      attributes: [{ name: 'objectClass', values: ['top'] }],
    })
    await api.deleteEntry('demo', { dn: 'uid=a,dc=x' })
    await expect(api.renameEntry('demo', { dn: 'uid=a,dc=x', newRDN: 'uid=b' })).resolves.toEqual({
      dn: 'uid=a,dc=x',
    })
    expect(post.mock.calls.map(([url]) => new URL(url as string).pathname)).toEqual([
      '/ldap/api/c/demo/entries/add',
      '/ldap/api/c/demo/entries/delete',
      '/ldap/api/c/demo/entries/rename',
    ])
    expect(JSON.parse((post.mock.calls[1][1] as RequestInit).body as string)).toEqual({
      dn: 'uid=a,dc=x',
    })
  })
})

describe('setPassword', () => {
  it('posts the password as typed to entries/password, not in the URL', async () => {
    const post = respond(200, { dn: 'uid=a,dc=x' })
    vi.stubGlobal('fetch', post)
    await api.setPassword('demo', { dn: 'uid=a,dc=x', password: ' p w ', method: 'ssha' })
    const [url, init] = post.mock.calls[0]
    expect(new URL(url as string).pathname).toBe('/ldap/api/c/demo/entries/password')
    expect(new URL(url as string).search).toBe('')
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({
      dn: 'uid=a,dc=x',
      password: ' p w ',
      method: 'ssha',
    })
  })
})

describe('errorKind', () => {
  it('classifies 401s by code', () => {
    expect(errorKind(new ApiError(401, { code: 'unauthorized', message: '' }))).toBe('auth')
    expect(errorKind(new ApiError(401, { code: 'not_connected', message: '' }))).toBe('session')
    expect(errorKind(new ApiError(401, { code: 'invalid_credentials', message: '' }))).toBe('other')
    expect(errorKind(new ApiError(500, { code: 'internal', message: '' }))).toBe('other')
    expect(errorKind(new Error('x'))).toBe('other')
  })
})

describe('retryBusy', () => {
  const busy = new ApiError(503, { code: 'busy', message: '' })
  it('retries busy up to 3 times', () => {
    expect([0, 1, 2, 3].map((n) => retryBusy(n, busy))).toEqual([true, true, true, false])
  })
  it.each([
    ['timeout', new ApiError(504, { code: 'timeout', message: '' })],
    ['not_connected', new ApiError(401, { code: 'not_connected', message: '' })],
    ['offline', new ApiError(0, { code: 'offline', message: '' })],
    ['a plain error', new Error('busy')],
  ])('does not retry %s', (_, err) => {
    expect(retryBusy(0, err)).toBe(false)
  })
})

describe('authStore', () => {
  it('flips once the server says the token is missing', async () => {
    expect(authStore.get()).toBe(false)
    vi.stubGlobal('fetch', respond(401, { code: 'not_connected', message: '' }))
    await api.info('demo').catch(() => {})
    expect(authStore.get()).toBe(false)
    const seen = vi.fn()
    const off = authStore.subscribe(seen)
    vi.stubGlobal('fetch', respond(401, { code: 'unauthorized', message: '' }))
    await api.profiles().catch(() => {})
    expect(authStore.get()).toBe(true)
    expect(seen).toHaveBeenCalledTimes(1)
    off()
  })
})

describe('passwordHint', () => {
  it('asks for a password when the stored one failed', () => {
    const err = (code: string) => new ApiError(401, { code, message: '' })
    expect(passwordHint(err('invalid_credentials'))).toBe(
      'The stored password did not work; enter one to try.',
    )
    expect(passwordHint(err('password_unavailable'))).toBe(
      'The stored password is unavailable; enter one to try.',
    )
    expect(passwordHint(err('network'))).toBeUndefined()
    expect(passwordHint(new Error('invalid_credentials'))).toBeUndefined()
    expect(passwordHint(null)).toBeUndefined()
  })
})

describe('errorMessage', () => {
  it('adds the decoded reason', () => {
    const e = new ApiError(401, {
      code: 'invalid_credentials',
      message: 'Invalid Credentials',
      reason: 'account disabled',
    })
    expect(errorMessage(e)).toBe('Invalid Credentials (account disabled)')
    expect(errorMessage(new Error('boom'))).toBe('boom')
    expect(errorMessage('x')).toBe('x')
  })

  it.each([
    [400, 'username_required'],
    [400, 'invalid_username'],
    [429, 'too_many_attempts'],
  ])('has UI text for %i %s', (status, code) => {
    expect(errorMessage(new ApiError(status, { code, message: '' }))).not.toBe('')
  })
})

describe('toProfileInput', () => {
  it('drops the server-computed fields', () => {
    const p: Profile = {
      name: 'a',
      url: 'ldaps://h',
      startTLS: false,
      insecureSkipVerify: false,
      readOnly: true,
      editable: true,
      hasPassword: false,
      connected: true,
      userLogin: false,
      needsPassword: false,
      caPEM: 'pem',
    }
    expect(toProfileInput(p)).toEqual({
      name: 'a',
      url: 'ldaps://h',
      startTLS: false,
      insecureSkipVerify: false,
      readOnly: true,
      caPEM: 'pem',
      baseDN: undefined,
      bindDN: undefined,
      color: undefined,
    })
  })
})
