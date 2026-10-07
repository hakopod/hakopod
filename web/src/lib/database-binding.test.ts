import assert from 'node:assert/strict'
import { mock, test } from 'node:test'
import { client } from './client'
import type { components } from './api.generated'
import { initialDatabaseSpec } from './database-create'
import {
  databaseBindingIssue,
  databaseBindingOptions,
  databaseBindingSSLOptions,
  databaseConnectionReviewIssue,
  emptyDatabaseBindingDraft,
  managedDatabaseName,
  saveDatabaseBindingPassword,
} from './database-binding'

test('managed defaults remain omitted and a custom user needs its own password reference', () => {
  assert.deepEqual(databaseBindingOptions(emptyDatabaseBindingDraft), {})
  assert.equal(databaseBindingIssue(initialDatabaseSpec, emptyDatabaseBindingDraft), undefined)
  const draft = { ...emptyDatabaseBindingDraft, username: 'reporter', database: 'analytics', sslMode: 'verify-full' as const }
  assert.match(databaseBindingIssue(initialDatabaseSpec, draft) || '', /existing password/)
  const saved = { ...draft, passwordSource: 'existing' as const, passwordRef: 'reporter-password', passwordValue: 'unused-private-value' }
  assert.equal(databaseBindingIssue(initialDatabaseSpec, saved), undefined)
  assert.deepEqual(databaseBindingOptions(saved), {
    username: 'reporter', database: 'analytics', password: { ref: 'reporter-password' }, ssl_mode: 'verify-full',
  })
  assert.ok(!JSON.stringify(databaseBindingOptions(saved)).includes('unused-private-value'))
  assert.throws(() => databaseBindingOptions({ ...draft, passwordSource: 'enter', passwordValue: 'private-password' }), /Save the password/)
  assert.equal(databaseBindingIssue(initialDatabaseSpec, { ...emptyDatabaseBindingDraft, username: 'app' }), undefined)
})

test('managed database placeholders describe the engine default without changing blank selections', () => {
  assert.equal(managedDatabaseName(initialDatabaseSpec), 'app')
  assert.equal(managedDatabaseName({ ...initialDatabaseSpec, engine: 'redis' }), '0')
  assert.equal(managedDatabaseName({ ...initialDatabaseSpec, engine: 'oracle', oracle: { edition: 'free' } }), 'FREEPDB1')
  assert.equal(managedDatabaseName({ ...initialDatabaseSpec, engine: 'oracle', oracle: { edition: 'enterprise' } }), 'APPDB')
  assert.deepEqual(databaseBindingOptions(emptyDatabaseBindingDraft), {})
})

test('SSL choices follow the database engine and cannot downgrade a required TLS policy', () => {
  const modes = (engine: typeof initialDatabaseSpec.engine, tls = true) => databaseBindingSSLOptions({ ...initialDatabaseSpec, engine, tls: tls ? { mode: 'required' } : undefined }).map((option) => option.value)
  assert.deepEqual(modes('postgresql'), ['', 'require', 'verify-ca', 'verify-full'])
  for (const engine of ['redis', 'mongodb', 'clickhouse', 'oracle'] as const) assert.deepEqual(modes(engine), ['', 'verify-full'])
  for (const engine of ['mysql', 'vitess', 'duckdb'] as const) assert.deepEqual(modes(engine), [''])
  for (const engine of ['postgresql', 'redis'] as const) assert.deepEqual(modes(engine, false), ['', 'disable'])
  assert.match(databaseBindingIssue(initialDatabaseSpec, { ...emptyDatabaseBindingDraft, sslMode: 'disable' }) || '', /TLS policy/)
})

test('MyDuck bindings keep the managed login and expose protocol-specific endpoints', () => {
  const myduck = { ...initialDatabaseSpec, engine: 'duckdb' as const, version: '0.3.1-dev.20260919.3' }
  assert.equal(databaseBindingIssue(myduck, emptyDatabaseBindingDraft, 'mysql'), undefined)
  assert.equal(databaseBindingIssue(myduck, { ...emptyDatabaseBindingDraft, username: 'root', database: 'app' }, 'mysql'), undefined)
  assert.equal(databaseBindingIssue(myduck, { ...emptyDatabaseBindingDraft, username: 'postgres', database: 'app' }, 'postgresql'), undefined)
  assert.match(databaseBindingIssue(myduck, { ...emptyDatabaseBindingDraft, username: 'analyst', passwordSource: 'existing', passwordRef: 'analyst-password' }, 'mysql') || '', /managed root user/)
  assert.match(databaseBindingIssue(myduck, { ...emptyDatabaseBindingDraft, passwordSource: 'existing', passwordRef: 'other-password' }, 'postgresql') || '', /managed postgres user/)
  assert.deepEqual(databaseBindingSSLOptions(myduck, 'mysql').map((option) => option.value), [''])
  assert.deepEqual(databaseBindingSSLOptions(myduck, 'postgresql').map((option) => option.value), ['', 'verify-full'])
})

test('pooled PostgreSQL keeps the managed login and database while direct endpoints permit overrides', () => {
  const custom = { ...emptyDatabaseBindingDraft, username: 'reporter', database: 'analytics', passwordSource: 'existing' as const, passwordRef: 'reporter-password' }
  assert.equal(databaseBindingIssue(initialDatabaseSpec, custom, 'read_write'), undefined)
  for (const endpoint of ['pooled_read_write', 'pooled_read_only']) {
    assert.match(databaseBindingIssue(initialDatabaseSpec, custom, endpoint) || '', /direct endpoint/)
    assert.equal(databaseBindingIssue(initialDatabaseSpec, emptyDatabaseBindingDraft, endpoint), undefined)
    assert.equal(databaseBindingIssue(initialDatabaseSpec, { ...emptyDatabaseBindingDraft, username: 'app', database: 'app' }, endpoint), undefined)
  }
})

test('engine-specific database selections reject Redis cluster indexes and Vitess route suffixes', () => {
  const redis = { ...initialDatabaseSpec, engine: 'redis' as const }
  assert.equal(databaseBindingIssue(redis, { ...emptyDatabaseBindingDraft, database: '15', username: 'default' }), undefined)
  for (const database of ['16', '-1', '01', '1.5']) assert.match(databaseBindingIssue(redis, { ...emptyDatabaseBindingDraft, database }) || '', /index/)
  assert.match(databaseBindingIssue({ ...redis, mode: 'cluster' }, { ...emptyDatabaseBindingDraft, database: '1' }) || '', /only database 0/)
  assert.equal(databaseBindingIssue({ ...redis, mode: 'cluster' }, { ...emptyDatabaseBindingDraft, database: '0' }), undefined)
  assert.match(databaseBindingIssue({ ...initialDatabaseSpec, engine: 'vitess' }, { ...emptyDatabaseBindingDraft, database: 'app@replica' }) || '', /route suffix/)
  assert.match(databaseBindingIssue({ ...initialDatabaseSpec, engine: 'vitess' }, { ...emptyDatabaseBindingDraft, database: 'analytics' }) || '', /app login and keyspace/)
  assert.equal(databaseBindingIssue({ ...initialDatabaseSpec, engine: 'vitess' }, { ...emptyDatabaseBindingDraft, username: 'app', database: 'app' }), undefined)
  assert.match(databaseBindingIssue(initialDatabaseSpec, { ...emptyDatabaseBindingDraft, database: 'app?sslmode=disable' }) || '', /URL separators/)
  assert.match(databaseBindingIssue(initialDatabaseSpec, { ...emptyDatabaseBindingDraft, username: 'é'.repeat(33) }) || '', /64 bytes/)
})

test('connection reviews invalidate when their expiry or either resource revision changes', () => {
  const now = Date.parse('2026-10-06T12:00:00Z')
  const plan = { database_revision: 3, application_revision: 7, expires_at: '2026-10-06T12:05:00Z' } as components['schemas']['DatabaseConnectionPlan']
  assert.equal(databaseConnectionReviewIssue(plan, 3, 7, now), undefined)
  assert.match(databaseConnectionReviewIssue(plan, 4, 7, now) || '', /changed/)
  assert.match(databaseConnectionReviewIssue(plan, 3, 8, now) || '', /changed/)
  assert.match(databaseConnectionReviewIssue(plan, 3, undefined, now) || '', /changed/)
  assert.match(databaseConnectionReviewIssue(plan, 3, 7, now + 300000) || '', /expired/)
  assert.match(databaseConnectionReviewIssue({ ...plan, expires_at: 'invalid' }, 3, 7, now) || '', /expired/)
})

test('entered passwords respect the runtime byte limit without trimming or silently changing values', () => {
  const draft = { ...emptyDatabaseBindingDraft, passwordSource: 'enter' as const }
  assert.equal(databaseBindingIssue(initialDatabaseSpec, { ...draft, passwordValue: 'a'.repeat(4096) }), undefined)
  assert.match(databaseBindingIssue(initialDatabaseSpec, { ...draft, passwordValue: 'a'.repeat(4097) }) || '', /4096 bytes/)
  assert.match(databaseBindingIssue(initialDatabaseSpec, { ...draft, passwordValue: 'é'.repeat(2049) }) || '', /4096 bytes/)
  assert.equal(databaseBindingIssue(initialDatabaseSpec, { ...draft, passwordValue: ' leading and trailing spaces ' }), undefined)
  assert.match(databaseBindingIssue(initialDatabaseSpec, { ...draft, passwordValue: 'line\nbreak' }) || '', /line breaks/)
})

test('entered passwords use unique create-only application secrets and produce only a reference', async () => {
  const scope = { project: 'demo', environment: 'development', application: 'reports' }
  const writes: { name: string; query: unknown; value: string }[] = []
  const post = mock.method(client, 'POST', async (path: string, options: { params: { path: { name: string }; query: unknown }; body: { value: string } }) => {
    assert.equal(path, '/secrets/{name}')
    writes.push({ name: options.params.path.name, query: options.params.query, value: options.body.value })
    return { data: { name: options.params.path.name, saved: true }, response: new Response('{}', { status: 201 }) }
  })
  try {
    const first = await saveDatabaseBindingPassword('first-private-value', scope)
    const second = await saveDatabaseBindingPassword('second-private-value', scope)
    assert.notEqual(first, second)
    assert.match(first, /^db-[a-f0-9]{32}$/)
    assert.deepEqual(writes.map((write) => write.query), [scope, scope])
    assert.deepEqual(writes.map((write) => write.value), ['first-private-value', 'second-private-value'])
    const binding = databaseBindingOptions({ ...emptyDatabaseBindingDraft, passwordSource: 'existing', passwordRef: first })
    assert.deepEqual(binding.password, { ref: first })
    assert.ok(!JSON.stringify(binding).includes('first-private-value'))
    await assert.rejects(saveDatabaseBindingPassword('value', { ...scope, application: '' }), /Choose an application/)
    assert.equal(writes.length, 2)
  } finally { post.mock.restore() }
})

test('failed password saves reject without returning a reference or changing the draft', async () => {
  const draft = { ...emptyDatabaseBindingDraft, passwordSource: 'enter' as const, passwordValue: 'retained-private-value' }
  const post = mock.method(client, 'POST', async () => ({ error: { error: { message: 'Secret storage is unavailable.' } }, response: new Response('{}', { status: 503 }) }))
  try {
    await assert.rejects(saveDatabaseBindingPassword(draft.passwordValue, { project: 'demo', environment: 'development', application: 'reports' }), /unavailable/)
    assert.equal(draft.passwordValue, 'retained-private-value')
    assert.equal(draft.passwordSource, 'enter')
    assert.equal(draft.passwordRef, '')
  } finally { post.mock.restore() }
})
