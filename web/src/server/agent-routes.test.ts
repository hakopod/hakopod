import assert from 'node:assert/strict'
import test from 'node:test'
import { agentRoutes } from './agent-routes.generated.ts'
import { generatedAgentRoute } from './agent-routes.ts'
test('generated agent mappings preserve exact contract methods and segments', () => {
 assert.deepEqual(generatedAgentRoute('users')?.methods,['GET'])
 assert.deepEqual(generatedAgentRoute('users/user-1')?.methods,['PATCH'])
 assert.equal(generatedAgentRoute('users/../keys'),undefined)
 assert.equal(generatedAgentRoute('users/%2Fkeys'),undefined)
 assert.equal(generatedAgentRoute('users/user-1/extra'),undefined)
 assert.equal(generatedAgentRoute('auth/setup'),undefined)
 assert.equal(generatedAgentRoute('databases/db/query'),undefined)
})

test('every generated mapping has an exact resolvable route', () => {
 for (const row of agentRoutes) {
  const path = row.path.slice(1).replace(/\{[^}]+\}/g, 'resource-1')
  assert.ok(generatedAgentRoute(path)?.operations.some((match) => match.id === row.id && match.method === row.method))
 }
})
