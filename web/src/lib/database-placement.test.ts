import assert from 'node:assert/strict'
import test from 'node:test'
import { databaseEligibleNodes, databasePlacementIssue } from './database-placement'
import { initialDatabaseSpec } from './database-create'
import type { DatabasePlacementNode } from './databases'

const nodes: DatabasePlacementNode[] = [
  { name: 'worker-a', architecture: 'amd64', available: true, reason: '', zone: 'zone-a' },
  { name: 'worker-b', architecture: 'amd64', available: true, reason: '', zone: 'zone-a' },
  { name: 'worker-c', architecture: 'amd64', available: true, reason: '', zone: 'zone-b' },
  { name: 'worker-offline', architecture: 'amd64', available: false, reason: 'Node is not ready', zone: 'zone-c' },
]

test('explicit database node selection never broadens an empty or stale selection', () => {
  assert.match(databasePlacementIssue(initialDatabaseSpec, nodes, true) || '', /Choose at least one/)
  for (const selected of ['worker-missing', 'worker-offline']) {
    assert.match(databasePlacementIssue({ ...initialDatabaseSpec, placement: { node_names: [selected] } }, nodes, true) || '', /unavailable or no longer approved/)
  }
  assert.equal(databasePlacementIssue({ ...initialDatabaseSpec, placement: { node_names: ['worker-c'] } }, nodes, true), undefined)
  assert.match(databasePlacementIssue(initialDatabaseSpec, [], false) || '', /No approved/)
})

test('database placement counts available distinct failure domains within the selection', () => {
  const cluster = { ...initialDatabaseSpec, mode: 'cluster' as const, replicas: 1 }
  assert.equal(databasePlacementIssue({ ...cluster, placement: { spread: 'nodes', node_names: ['worker-a', 'worker-b'] } }, nodes, true), undefined)
  assert.match(databasePlacementIssue({ ...cluster, placement: { spread: 'zones', node_names: ['worker-a', 'worker-b'] } }, nodes, true) || '', /Only 1/)
  assert.equal(databasePlacementIssue({ ...cluster, placement: { spread: 'zones', node_names: ['worker-a', 'worker-c'] } }, nodes, true), undefined)
  assert.match(databasePlacementIssue({ ...cluster, replicas: 2, placement: { spread: 'zones' } }, nodes, false) || '', /Only 2/)
  assert.match(databasePlacementIssue({ ...cluster, placement: { spread: 'zones' } }, nodes.map((node) => ({ ...node, zone: undefined })), false) || '', /Only 0/)
})

test('architecture restrictions preserve selections and exclude incompatible nodes from quorum', () => {
  const mixed = nodes.map((node) => node.name === 'worker-c' ? { ...node, architecture: 'arm64' } : node)
  for (const engine of ['mysql', 'mongodb', 'vitess'] as const) {
    const spec = { ...initialDatabaseSpec, engine, mode: 'cluster' as const, replicas: 2, placement: { spread: 'nodes' as const } }
    assert.match(databasePlacementIssue(spec, mixed, false) || '', /Only 2/)
    assert.match(databasePlacementIssue({ ...spec, placement: { node_names: ['worker-c'] } }, mixed, true) || '', /unavailable/)
    const display = databaseEligibleNodes(engine, mixed)
    assert.equal(display.find((node) => node.name === 'worker-c')?.available, false)
    assert.match(display.find((node) => node.name === 'worker-c')?.reason || '', /requires an amd64/)
  }
  assert.equal(databaseEligibleNodes('postgresql', mixed), mixed)
  assert.equal(mixed.find((node) => node.name === 'worker-c')?.available, true)
})
