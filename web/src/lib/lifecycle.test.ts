import assert from 'node:assert/strict'
import test from 'node:test'
import { specToTOML } from './toml'
import { readinessLabel } from './readiness'
import type { Spec } from './types'

test('canonical TOML preserves jobs, literal and secret files, bindings, and named endpoints', () => {
  const spec: Spec = {
    schema_version: 1,
    name: 'lifecycle-fixture',
    services: {
      migrate: {
        image: 'example.invalid/migrate',
        job: { timeout_seconds: 240, retries: 0 },
        files: {
          empty: { mount_path: '/config/empty', content: '', mode: 292 },
          credentials: {
            mount_path: '/config/credentials',
            secret: { ref: 'fixture-file' },
            mode: 288,
          },
        },
        bindings: {
          DATABASE_URL: {
            service: 'db',
            protocol: 'postgres',
            database: 'fixture',
            username: 'fixture-user',
            password: { ref: 'fixture-password' },
          },
        },
      },
      api: {
        image: 'example.invalid/api',
        http: { admin: { port: 8081, domain: 'admin.example.invalid' } },
      },
    },
  }
  const before = structuredClone(spec)
  const output = specToTOML(spec)
  for (const text of [
    '[services.migrate.job]',
    'timeout_seconds = 240',
    'retries = 0',
    '[services.migrate.files.empty]',
    'content = ""',
    'mode = 292',
    '[services.migrate.files.credentials.secret]',
    'ref = "fixture-file"',
    'mode = 288',
    '[services.migrate.bindings.DATABASE_URL]',
    'protocol = "postgres"',
    'database = "fixture"',
    '[services.migrate.bindings.DATABASE_URL.password]',
    'ref = "fixture-password"',
    '[services.api.http.admin]',
    'port = 8081',
    'domain = "admin.example.invalid"',
  ])
    assert.ok(output.includes(text), text)
  assert.deepEqual(spec, before)
  assert.equal(readinessLabel(spec.services.migrate), 'Job completion')
})

import { formatBuildArgs, parseBuildArgs } from './build-args'
test('public build values preserve empty values, URL equals signs and whitespace', () => {
  const values = {
    NEXT_PUBLIC_API_URL: 'https://api.example.invalid/?a=b',
    EMPTY: '',
    LABEL: ' keep spaces ',
  }
  assert.deepEqual({ ...parseBuildArgs(formatBuildArgs(values)) }, values)
  assert.throws(() => parseBuildArgs('MISSING_EQUALS'), /NAME=value/)
  assert.throws(() => parseBuildArgs('A=first\nA=second'), /appears more than once/)
})
