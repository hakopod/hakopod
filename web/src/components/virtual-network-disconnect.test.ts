import assert from 'node:assert/strict'
import test from 'node:test'
import type { Spec } from '../lib/types'
import { disconnectVirtualNetwork } from './virtual-network-disconnect.ts'

const spec = (networks: string[][]) =>
  ({
    name: 'billing',
    networks: { shared: { internal: true, virtual_network: 'edge', segment: 'data' } },
    services: Object.fromEntries(networks.map((list, i) => [`s${i}`, { image: 'x', networks: list }])),
  }) as unknown as Spec

test('disconnect drops the membership and the unused local network only', () => {
  const one = disconnectVirtualNetwork(spec([['default', 'shared']]), 'edge', 's0', 'shared')
  assert.deepEqual(one.spec.services.s0.networks, ['default'])
  assert.equal(one.removedLocal, true)
  assert.equal(one.spec.networks?.shared, undefined)
  const shared = disconnectVirtualNetwork(
    spec([['default', 'shared'], ['shared', 'default']]),
    'edge',
    's0',
    'shared',
  )
  assert.equal(shared.removedLocal, false)
  assert.ok(shared.spec.networks?.shared)
  assert.throws(() => disconnectVirtualNetwork(spec([['shared']]), 'edge', 's0', 'shared'), /only network/)
  assert.throws(() => disconnectVirtualNetwork(spec([['default']]), 'edge', 's0', 'shared'), /no longer connected/)
})
