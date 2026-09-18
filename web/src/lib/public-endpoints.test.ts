import assert from 'node:assert/strict'
import test from 'node:test'
import { publicEndpoints, type DomainStatus } from './public-endpoints'
import type { Application } from './types'

const application = {
  spec: {
    services: { api: { image: 'app:1' }, worker: { image: 'app:1' } },
    domains: {
      'api.example.com': 'api',
      'pending.example.com': 'api',
      'unknown.example.com': 'worker',
    },
  },
  observed: {
    services: [
      {
        name: 'api',
        url: 'https://api.generated.example.com',
        endpoints: {
          duplicate: 'https://api.generated.example.com/',
          console: 'https://console.example.com',
          custom: 'https://api.example.com',
        },
      },
      { name: 'worker', url: 'https://worker.generated.example.com' },
      { name: 'removed', url: 'https://removed.example.com' },
    ],
  },
} as unknown as Application
const domains = [
  { hostname: 'api.example.com', service: 'api', active: true },
  { hostname: 'pending.example.com', service: 'api', active: false, verified: true },
  { hostname: 'verification-only.example.com', service: 'api', active: false },
] as DomainStatus[]

test('public endpoints combine configured custom mappings and every service URL without duplicates', () => {
  const result = publicEndpoints(application, domains)
  assert.equal(result.length, 6)
  assert.equal(result.filter((item) => item.url === 'https://api.generated.example.com/').length, 1)
  assert.equal(result.find((item) => item.url === 'https://api.example.com/')?.custom, true)
  assert.equal(result.find((item) => item.url === 'https://api.example.com/')?.status, 'Configured')
  assert.equal(
    result.find((item) => item.url === 'https://pending.example.com/')?.status,
    'Needs setup',
  )
  assert.equal(
    result.find((item) => item.url === 'https://unknown.example.com/')?.status,
    'Status unavailable',
  )
  assert.ok(
    !result.some((item) => item.url.includes('verification-only') || item.service === 'removed'),
  )
  assert.equal(result.filter((item) => item.service === 'worker').length, 2)
})

test('failed and loading domain observations never claim active routing', () => {
  for (const [state, label] of [
    ['error', 'Status unavailable'],
    ['loading', 'Checking status'],
  ] as const) {
    assert.ok(
      publicEndpoints(application, domains, state)
        .filter((item) => item.custom)
        .every((item) => item.status === label),
    )
  }
})

test('public endpoint links reject credentials, unsupported schemes and malformed custom hosts', () => {
  const invalid = structuredClone(application)
  invalid.spec.domains = {
    'example.com/path': 'api',
    'user@example.com': 'api',
    'example.com?token=secret': 'api',
  }
  invalid.observed!.services = [
    {
      name: 'api',
      status: 'running',
      ready: 1,
      desired: 1,
      url: 'javascript:alert(1)',
      endpoints: { secret: 'https://user:password@example.com', file: 'file:///tmp/a' },
    },
  ]
  assert.deepEqual(publicEndpoints(invalid), [])
})
