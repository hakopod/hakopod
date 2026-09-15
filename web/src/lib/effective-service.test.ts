import { test } from 'node:test'
import assert from 'node:assert/strict'
import { effectiveService } from './effective-service'
import type { Spec } from './types'

test('displayed environment matches explicit injection and service overrides', () => {
  const app: Spec = {
    schema_version: 1,
    name: 'app',
    inject_env: true,
    env: { A: 'shared', EMPTY: 'default' },
    secrets: { SHARED_TOKEN: { ref: 'shared' }, LOCAL: { ref: 'local' } },
    services: {
      api: {
        image: 'nginx',
        env: { EMPTY: '', LOCAL: 'plain' },
        secrets: { A: { ref: 'override' } },
      },
    },
  }
  const original = JSON.stringify(app)
  const effective = effectiveService(app, app.services.api)
  assert.deepEqual({ ...effective.env }, { EMPTY: '', LOCAL: 'plain' })
  assert.deepEqual(
    { ...effective.secrets },
    { SHARED_TOKEN: { ref: 'shared' }, A: { ref: 'override' } },
  )
  assert.equal(JSON.stringify(app), original)
  app.inject_env = false
  app.env!.ONLY_DEFAULT = 'hidden'
  assert.equal(effectiveService(app, app.services.api).env?.ONLY_DEFAULT, undefined)
})
