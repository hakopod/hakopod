import { test } from 'node:test'
import assert from 'node:assert/strict'
import { connectVirtualNetwork, networkDraft, networkTOML } from './virtual-networks'
import type { Spec } from './types'

const network = {
  schema_version: 1 as const,
  name: 'shared',
  description: 'Private applications',
  segments: { data: { applications: ['orders', 'database'] } },
}

test('connecting a service preserves other networks, workload fields, and peer restrictions', () => {
  const original: Spec = {
    schema_version: 1,
    name: 'orders',
    networks: { default: { internal: false }, private: { internal: true } },
    services: {
      api: {
        image: 'python:3.13-alpine',
        networks: ['default', 'private'],
        env: { MODE: 'production' },
        secrets: { DATABASE_URL: { ref: 'database-url' } },
        network_access: { from: [], from_applications: ['database'] },
      },
      worker: { image: 'python:3.13-alpine', networks: ['private'] },
    },
  }
  const result = connectVirtualNetwork(original, network, 'data', 'api', 'shared-data')
  assert.deepEqual(result.services.api.networks, ['default', 'private', 'shared-data'])
  assert.deepEqual(result.services.api.network_access, original.services.api.network_access)
  assert.deepEqual(result.services.api.env, original.services.api.env)
  assert.deepEqual(result.services.api.secrets, original.services.api.secrets)
  assert.deepEqual(result.services.worker, original.services.worker)
  assert.deepEqual(result.networks?.default, { internal: false })
  assert.deepEqual(result.networks?.['shared-data'], {
    internal: true,
    virtual_network: 'shared',
    segment: 'data',
  })
  assert.deepEqual(original.services.api.networks, ['default', 'private'])
  assert.equal(original.networks?.['shared-data'], undefined)
})

test('network connections preserve implicit default membership and reject grant or name conflicts', () => {
  const spec: Spec = {
    schema_version: 1,
    name: 'orders',
    services: { api: { image: 'python:3.13-alpine' } },
  }
  const connected = connectVirtualNetwork(spec, network, 'data', 'api', 'shared-data')
  assert.deepEqual(connected.services.api.networks, ['default', 'shared-data'])
  assert.throws(
    () => connectVirtualNetwork(spec, network, 'missing', 'api', 'shared-data'),
    /not allowed/,
  )
  assert.throws(
    () =>
      connectVirtualNetwork({ ...spec, name: 'unlisted' }, network, 'data', 'api', 'shared-data'),
    /not allowed/,
  )
  assert.throws(
    () => connectVirtualNetwork(spec, network, 'data', 'removed', 'shared-data'),
    /no longer/,
  )
  assert.throws(
    () =>
      connectVirtualNetwork(
        { ...spec, networks: { occupied: { internal: true } } },
        network,
        'data',
        'api',
        'occupied',
      ),
    /different application network/,
  )
  assert.throws(
    () => connectVirtualNetwork(connected, network, 'data', 'api', 'shared-data'),
    /already connected/,
  )
  assert.throws(
    () => connectVirtualNetwork(spec, network, 'data', 'api', 'default'),
    /already connected/,
  )
})

test('network grant inputs reject duplicate or wildcard names and retain exact application names in TOML', () => {
  const segment = { id: 'data', name: 'data', applications: 'orders, database\nreporting' }
  const value = networkDraft('shared', 'A "private" network', [segment])
  assert.deepEqual(value.segments.data.applications, ['orders', 'database', 'reporting'])
  assert.match(networkTOML(value), /applications = \["orders", "database", "reporting"\]/)
  assert.match(networkTOML(value), /description = "A \\"private\\" network"/)
  assert.throws(
    () => networkDraft('shared', '', [segment, { ...segment, id: 'duplicate' }]),
    /appears more than once/,
  )
  assert.throws(
    () => networkDraft('shared', '', [{ ...segment, applications: 'orders, orders' }]),
    /duplicate application/,
  )
  assert.throws(() => networkDraft('shared', '', [{ ...segment, applications: '*' }]), /Wildcards/)
  assert.throws(() => networkDraft('shared-', '', [segment]), /network name/)
  assert.throws(() => networkDraft('shared', '', []), /1 and 16/)
})
