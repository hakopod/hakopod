import test from 'node:test'
import assert from 'node:assert/strict'
import { toggleGrant } from './agent-grants.ts'
test('credential selection requires write without making write reveal credentials', () => {
  assert.deepEqual(toggleGrant([], 'agent:credentials', true), [
    'agent:credentials',
    'deployments:read',
    'deployments:write',
  ])
  assert.equal(toggleGrant([], 'deployments:write', true).includes('agent:credentials'), false)
  assert.equal(
    toggleGrant(
      ['deployments:read', 'deployments:write', 'agent:credentials'],
      'deployments:write',
      false,
    ).includes('agent:credentials'),
    false,
  )
})
test('SQL write selection and removal preserve query dependencies', () => {
  assert.equal(toggleGrant([], 'databases:write-query', true).includes('databases:query'), true)
  assert.equal(
    toggleGrant(
      ['deployments:read', 'databases:query', 'databases:write-query'],
      'databases:query',
      false,
    ).includes('databases:write-query'),
    false,
  )
})
