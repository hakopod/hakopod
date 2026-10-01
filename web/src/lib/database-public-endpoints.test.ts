import assert from 'node:assert/strict'
import test from 'node:test'
import type { DatabasePublicEndpointCapabilities, DatabasePublicEndpointRoute } from './databases'
import {
  databasePublicEndpointPublicationAvailable,
  databasePublicEndpointRevocationDisabled,
  databasePublicEndpointOperationTerminal,
  databasePublicEndpointRouteForPurpose,
  databasePublicEndpointRouteOptions,
  parseDatabasePublicEndpointCIDRs,
} from './database-public-endpoints'

test('public endpoint source networks accept lines and commas without empty entries', () => {
  assert.deepEqual(
    parseDatabasePublicEndpointCIDRs('192.0.2.0/24\n\n198.51.100.8/32, 203.0.113.0/24'),
    ['192.0.2.0/24', '198.51.100.8/32', '203.0.113.0/24'],
  )
})

test('public endpoint polling stops only on terminal operation states', () => {
  for (const status of ['succeeded', 'failed', 'cancelled'])
    assert.equal(databasePublicEndpointOperationTerminal(status), true)
  for (const status of [undefined, 'queued', 'running'])
    assert.equal(databasePublicEndpointOperationTerminal(status), false)
})

test('public endpoint routes retain the server-selected MySQL Router contract', () => {
  const routes: DatabasePublicEndpointRoute[] = [
    {
      purpose: 'read_write',
      protocol: 'mysql' as const,
      routing: 'mysql_router' as const,
      read_only: false,
      pooled: false,
    },
    {
      purpose: 'read_only',
      protocol: 'mysql' as const,
      routing: 'mysql_router' as const,
      read_only: true,
      pooled: false,
    },
  ]
  assert.deepEqual(databasePublicEndpointRouteForPurpose(routes, 'read_only'), routes[1])
  assert.deepEqual(databasePublicEndpointRouteOptions(routes), [
    { value: 'read_write', label: 'Direct read and write · MySQL via MySQL Router' },
    { value: 'read_only', label: 'Direct read only · MySQL via MySQL Router' },
  ])
})

test('unavailable publication capability preserves endpoint inventory, revoke tracking and revocation', () => {
  const unavailableMySQL: DatabasePublicEndpointCapabilities = {
    engine: 'mysql',
    available: false,
    unavailable_reason: 'MySQL Router has not passed native qualification.',
    routes: [
      {
        purpose: 'read_write',
        protocol: 'mysql' as const,
        routing: 'mysql_router' as const,
        read_only: false,
        pooled: false,
      },
    ],
  }

  assert.equal(databasePublicEndpointPublicationAvailable(unavailableMySQL), false)
  assert.equal(
    databasePublicEndpointRevocationDisabled(false, false, false),
    false,
    'an existing active endpoint can still be revoked while new publication is unavailable',
  )
})

test('failed capability refresh blocks publication even when a previous response allowed it', () => {
  const capabilities: DatabasePublicEndpointCapabilities = {
    engine: 'postgresql',
    available: true,
    unavailable_reason: '',
    routes: [
      {
        purpose: 'read_write',
        protocol: 'postgresql' as const,
        routing: 'direct' as const,
        read_only: false,
        pooled: false,
      },
    ],
  }

  assert.equal(databasePublicEndpointPublicationAvailable(capabilities), true)
  assert.equal(databasePublicEndpointPublicationAvailable(capabilities, true), false)
  assert.equal(databasePublicEndpointRevocationDisabled(false, false, false), false)
})
