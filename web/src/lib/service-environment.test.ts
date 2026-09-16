import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  environmentChanges,
  mergeEnvironment,
  parseEnvironment,
  sameEnvironment,
  splitEnvironment,
  sensitiveEnvironment,
} from './service-environment'

test('environment edits merge unrelated concurrent variables and identify same-key conflicts', () => {
  const result = mergeEnvironment(
    { MODE: 'development', REMOVE_ME: '', UNTOUCHED: 'old' },
    { MODE: 'production', UNTOUCHED: 'old', ADDED: 'yes' },
    { MODE: 'staging', REMOVE_ME: '', UNTOUCHED: 'new', CONCURRENT: 'kept' },
  )
  assert.deepEqual(result.conflicts, ['MODE'])
  assert.deepEqual(
    { ...result.environment },
    { MODE: 'production', UNTOUCHED: 'new', CONCURRENT: 'kept', ADDED: 'yes' },
  )
  assert.deepEqual(mergeEnvironment({ A: 'old' }, { A: 'new' }, { A: 'new' }).conflicts, [])
})

test('users can explicitly protect ordinary names and multiline private values', () => {
  const row = { id: 'private', name: 'LICENSE', value: 'first\nsecond', secret: true }
  const result = splitEnvironment([row, { id: 'normal', name: 'MODE', value: 'production' }])
  assert.deepEqual({ ...result.env }, { MODE: 'production' })
  assert.deepEqual(result.secrets, [row])
  assert.throws(() => parseEnvironment([row], []), /application secrets/)
  assert.equal(splitEnvironment([{ ...row, value: 'x'.repeat(65536) }]).secrets.length, 1)
  assert.throws(() => splitEnvironment([{ ...row, value: 'x'.repeat(65537) }]), /64 KiB/)
  assert.throws(() => splitEnvironment([{ ...row, value: '' }]), /nonempty/)
})

test('secret detection catches common key families and credential-shaped values', () => {
  for (const [name, value] of [
    ['SECRET_KEY_BASE', 'fixture'],
    ['SESSION_SIGNING_KEY', 'fixture'],
    ['CONFIG', '-----BEGIN RSA PRIVATE KEY-----\nfixture\n-----END RSA PRIVATE KEY-----'],
    ['CONFIG', 'github_pat_' + 'a'.repeat(32)],
    ['CONFIG', 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature'],
    ['DATABASE_URL', 'postgres://name:fixture@db/app'],
  ])
    assert.equal(sensitiveEnvironment(name, value), true, name)
  for (const [name, value] of [
    ['APP_ENV', 'production'],
    ['PORT', '8000'],
    ['URL', 'https://example.test'],
    ['PUBLIC_KEY', 'public material'],
  ])
    assert.equal(sensitiveEnvironment(name, value), false, name)
})

test('empty values and prototype-like names remain plain environment entries', () => {
  const rows = ['EMPTY', '__proto__', 'constructor', 'toString'].map((name) => ({
    id: name,
    name,
    value: '',
  }))
  const environment = parseEnvironment(rows, [])
  const merged = mergeEnvironment({}, environment, {})
  assert.equal(Object.keys(merged.environment).length, 4)
  assert.equal(merged.environment.__proto__, '')
  assert.equal(sameEnvironment(merged.environment, environment), true)
  assert.equal(sameEnvironment({}, environment), false)
  assert.deepEqual(environmentChanges({ A: '' }, {}), [{ name: 'A', before: '', after: undefined }])
})

test('environment validation rejects ambiguous, secret, and oversized entries before review', () => {
  const row = (name: string, value = '') => ({ id: name, name, value })
  assert.throws(() => parseEnvironment([row('A'), row('A')], []), /more than once/)
  assert.throws(() => parseEnvironment([row('DB_URL')], ['DB_URL']), /secret reference/)
  assert.throws(() => parseEnvironment([row('ACCESS_TOKEN')], []), /application secrets/)
  assert.throws(() => parseEnvironment([row('BAD-NAME')], []), /Variable names/)
  assert.throws(() => parseEnvironment([row('A', 'é'.repeat(2049))], []), /4,096 bytes/)
  assert.throws(() => parseEnvironment([row('A', '\0')], []), /null character/)
  assert.throws(
    () =>
      parseEnvironment(
        Array.from({ length: 129 }, (_, index) => row(`A${index}`)),
        [],
      ),
    /128 plain variables/,
  )
  assert.equal(parseEnvironment([row('A', 'é'.repeat(2048))], []).A.length, 2048)
})

test('mixed environment rows separate secrets, reject collisions and preserve empty overrides', () => {
  const row = (name: string, value: string) => ({ id: name, name, value })
  const result = splitEnvironment([
    row('MODE', ''),
    row('API_TOKEN', 'fixture'),
    row('URL', 'postgres://user:fixture@db/app'),
  ])
  assert.deepEqual({ ...result.env }, { MODE: '' })
  assert.deepEqual(
    result.secrets.map((row) => row.name),
    ['API_TOKEN', 'URL'],
  )
  assert.throws(
    () => splitEnvironment([row('API_TOKEN', 'fixture')], ['API_TOKEN']),
    /already has a secret reference/,
  )
  assert.throws(() => splitEnvironment([row('MODE', ''), row('MODE', 'other')]), /more than once/)
  assert.throws(
    () =>
      splitEnvironment(Array.from({ length: 33 }, (_, i) => row(`SERVICE_${i}_TOKEN`, 'fixture'))),
    /32/,
  )
})
