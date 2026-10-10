import test from 'node:test'
import assert from 'node:assert/strict'
import { explorerScope, proxyExplorer } from './database-explorer.ts'
import { sealSession } from './session.ts'
import { explorerEditionHeaders } from './dashboard-edition.ts'

let workspace = ''
try {
  explorerEditionHeaders(new Request('http://localhost'), workspace)
} catch {
  workspace = 'a'.repeat(32)
}
const scope = { project: 'demo', environment: 'development', workspace }
const encoded = Buffer.from(JSON.stringify(scope)).toString('base64url')
test('explorer scope never falls back or accepts hidden fields', () => {
  assert.deepEqual(explorerScope(encoded), scope)
  for (const input of [
    { ...scope, workspace: 'foreign' },
    { ...scope, project: '' },
    { ...scope, upstream: 'https://evil.test' },
  ])
    assert.throws(() => explorerScope(Buffer.from(JSON.stringify(input)).toString('base64url')))
})
test('an unauthenticated API failure preserves the login message', async () => {
  const response = await proxyExplorer({
    request: new Request(`http://localhost/synehq/s/${encoded}/api/session`),
  })
  assert.equal(response.status, 401)
  assert.match((await response.json()).error, /Sign in to Hakopod/)
})
test('an explorer tab sends its URL scope and strips browser control headers', async () => {
  const original = globalThis.fetch
  let forwarded: RequestInit | undefined
  globalThis.fetch = async (_input, init) => {
    forwarded = init
    return Response.json({
      status: 200,
      contentType: 'text/html',
      body: Buffer.from('<script src="/synehq/_next/chunk.js"></script>').toString('base64'),
    })
  }
  try {
    const response = await proxyExplorer({
      request: new Request(`http://localhost/synehq/s/${encoded}/connections/`, {
        headers: {
          cookie: `hakopod_session=${sealSession('human-token')}`,
          'X-Hakopod-Explorer-Ticket': 'forged',
          'X-Hakopod-Workspace': 'other',
        },
      }),
    })
    assert.equal(response.status, 200)
    assert.deepEqual(JSON.parse(forwarded!.body as string), {
      project: 'demo',
      environment: 'development',
      method: 'GET',
      path: '/connections/',
      body: '',
    })
    assert.equal(new Headers(forwarded!.headers).get('X-Hakopod-Explorer-Ticket'), null)
    assert.equal(new Headers(forwarded!.headers).get('X-Hakopod-Workspace'), workspace || null)
    assert.match(await response.text(), new RegExp(`/synehq/s/${encoded}/_next/`))
  } finally {
    globalThis.fetch = original
  }
})
