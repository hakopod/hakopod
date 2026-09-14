import assert from 'node:assert/strict'
import test from 'node:test'
import { withoutService } from './remove-service'
import { specToTOML } from './toml'
import type { Spec } from './types'

test('removal stages domain cleanup and retains dependencies, storage and original revision', () => {
  const original: Spec = {
    schema_version: 1,
    name: 'shop',
    services: { api: { image: 'python:3.13' }, web: { image: 'python:3.13', depends_on: ['api'] } },
    domains: { 'api.example.com': 'api', 'example.com': 'web' },
    volumes: { data: { size_gib: 1 } },
  }
  const next = withoutService(original, 'api')
  assert.deepEqual(Object.keys(next.services), ['web'])
  assert.deepEqual(next.domains, { 'example.com': 'web' })
  assert.deepEqual(next.services.web.depends_on, ['api'])
  assert.deepEqual(next.volumes, original.volumes)
  assert.ok(original.services.api)
  const empty = withoutService(next, 'web')
  assert.match(specToTOML(empty), /\[services\]/)
  assert.deepEqual(empty.domains, {})
})
