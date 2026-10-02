import assert from 'node:assert/strict'
import test from 'node:test'
import { availableManagedPlatformEntries, type ManagedPlatformCatalog } from './managed-platforms'

test('platform creation requires both availability and native cluster qualification', () => {
  const items = [
    { kind: 'neon', capability: { available: false, cluster_qualified: false } },
    { kind: 'supabase', capability: { available: true, cluster_qualified: false } },
    { kind: 'neon', capability: { available: false, cluster_qualified: true } },
    { kind: 'supabase', capability: { available: true, cluster_qualified: true } },
  ]
  const catalog = { items } as ManagedPlatformCatalog
  assert.deepEqual(availableManagedPlatformEntries(catalog), [items[3]])
  assert.deepEqual(availableManagedPlatformEntries({ ...catalog, items: [] }), [])
})
