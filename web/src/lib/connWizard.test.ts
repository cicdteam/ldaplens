import { describe, expect, it } from 'vitest'
import {
  adDomain,
  buildURL,
  defaultName,
  defaultTemplate,
  parseServer,
  splitURL,
  templateOK,
} from './connWizard'

describe('parseServer', () => {
  it('takes a host as it is', () => {
    expect(parseServer(' ldap.example.org ')).toEqual({ host: 'ldap.example.org' })
  })
  it('splits a pasted URL into host, port and security', () => {
    expect(parseServer('ldaps://dc1.corp.example:3269')).toEqual({
      host: 'dc1.corp.example',
      port: '3269',
      security: 'ldaps',
    })
    expect(parseServer('LDAP://h/dc=x')).toEqual({ host: 'h', port: '389', security: 'starttls' })
    expect(parseServer('ldaps://h')).toEqual({ host: 'h', port: '636', security: 'ldaps' })
  })
  it('keeps an IPv6 literal in brackets', () => {
    expect(parseServer('ldap://[::1]:1390')).toEqual({
      host: '[::1]',
      port: '1390',
      security: 'starttls',
    })
  })
})

describe('buildURL and splitURL', () => {
  it('leaves the default port out', () => {
    expect(buildURL('h', '636', 'ldaps')).toEqual({ url: 'ldaps://h', startTLS: false })
    expect(buildURL('h', '389', 'starttls')).toEqual({ url: 'ldap://h', startTLS: true })
    expect(buildURL('h', '389', 'none')).toEqual({ url: 'ldap://h', startTLS: false })
    expect(buildURL(' h ', '1390', 'none')).toEqual({ url: 'ldap://h:1390', startTLS: false })
  })
  it('splits a saved URL back for editing', () => {
    expect(splitURL('ldaps://h', false)).toEqual({ host: 'h', port: '636', security: 'ldaps' })
    expect(splitURL('ldap://h:1390', true)).toEqual({
      host: 'h',
      port: '1390',
      security: 'starttls',
    })
    expect(splitURL('ldap://h', false)).toEqual({ host: 'h', port: '389', security: 'none' })
    expect(splitURL('', false)).toEqual({ host: '', port: '636', security: 'ldaps' })
  })
})

describe('templates', () => {
  it('reads the AD domain from the base DN', () => {
    expect(adDomain('DC=corp,DC=example,DC=test')).toBe('corp.example.test')
    expect(adDomain('ou=x, dc=a ,dc=b')).toBe('a.b')
    expect(adDomain('')).toBe('')
  })
  it('suggests a template for each kind of directory', () => {
    expect(defaultTemplate('openldap', 'dc=example,dc=org')).toBe(
      'uid={user},ou=people,dc=example,dc=org',
    )
    expect(defaultTemplate('openldap', '')).toBe('uid={user},ou=people,dc=example,dc=org')
    expect(defaultTemplate('ad', 'DC=corp,DC=example')).toBe('{user}@corp.example')
    expect(defaultTemplate('ad', '')).toBe('{user}@corp.example')
  })
  it('wants {user} exactly once', () => {
    expect(templateOK('uid={user},dc=x')).toBe(true)
    expect(templateOK('uid=jdoe,dc=x')).toBe(false)
    expect(templateOK('{user}{user}')).toBe(false)
  })
})

describe('defaultName', () => {
  it('makes a profile name from the host', () => {
    expect(defaultName('LDAP.Example.org')).toBe('ldap.example.org')
    expect(defaultName('[::1]')).toBe('1')
    expect(defaultName('')).toBe('')
    expect(defaultName('a'.repeat(80))).toHaveLength(64)
  })
})
