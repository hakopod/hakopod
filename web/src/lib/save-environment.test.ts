import { test, mock } from 'node:test'
import assert from 'node:assert/strict'
import { client } from './client'
import { importDotenv } from './dotenv'
import { prepareEnvironment, saveEnvironment } from './save-environment'
import { specToTOML } from './toml'

test('mixed imports send private values only to scoped secret storage and serialize references', async () => {
  const writes: { name: string; value: string; query: unknown }[] = []
  const put = mock.method(
    client,
    'PUT',
    async (
      path: string,
      options: { params: { path: { name: string }; query: unknown }; body: { value: string } },
    ) => {
      assert.equal(path, '/secrets/{name}')
      writes.push({
        name: options.params.path.name,
        value: options.body.value,
        query: options.params.query,
      })
      return { data: { saved: true }, response: new Response('{}', { status: 200 }) }
    },
  )
  try {
    const query = { project: 'demo', environment: 'development', application: 'shop' }
    const api = importDotenv('PORT=8000\nAPI_TOKEN=private-api-value', [], true)
    const worker = [
      { id: 'worker-private', name: 'LICENSE', value: 'private-worker-value', secret: true },
    ]
    const first = await saveEnvironment(api, query)
    const second = await saveEnvironment(worker, query)
    assert.deepEqual({ ...first.env }, { PORT: '8000' })
    assert.deepEqual({ ...second.env }, {})
    assert.equal(writes.length, 2)
    assert.ok(writes.every((write) => JSON.stringify(write.query) === JSON.stringify(query)))
    assert.notEqual(writes[0].name, writes[1].name)
    const spec = {
      schema_version: 1 as const,
      name: 'shop',
      services: {
        api: { image: 'nginx:alpine', ...first },
        worker: { image: 'busybox:stable', ...second },
      },
    }
    for (const serialized of [JSON.stringify(spec), specToTOML(spec)]) {
      assert.ok(!serialized.includes('private-api-value'))
      assert.ok(!serialized.includes('private-worker-value'))
      assert.ok(serialized.includes(writes[0].name))
      assert.ok(serialized.includes(writes[1].name))
    }
    await saveEnvironment(api, query, first.secrets)
    assert.equal(writes[2].name, writes[0].name, 'Retry keeps the draft reference')
    assert.throws(
      () => prepareEnvironment(api, query, { API_TOKEN: { ref: 'existing-user-secret' } }),
      /already has a secret reference/,
    )
    assert.equal(
      writes.length,
      3,
      'Validation never writes values or replaces unrelated references',
    )
  } finally {
    put.mock.restore()
  }
})

test('failed secret saves retain the draft and never produce a plain-value fallback', async () => {
  const rows = importDotenv('MODE=production\nTOKEN=private-retry-value', [], true)
  const put = mock.method(client, 'PUT', async () => ({
    error: { error: { message: 'Secret storage unavailable' } },
    response: new Response('{}', { status: 503 }),
  }))
  try {
    await assert.rejects(
      saveEnvironment(rows, { project: 'demo', environment: 'dev', application: 'shop' }),
      /Secret storage unavailable/,
    )
    assert.equal(rows[1].value, 'private-retry-value')
    assert.equal(rows[1].secret, true)
  } finally {
    put.mock.restore()
  }
})
