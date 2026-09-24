import { test } from 'node:test'
import assert from 'node:assert/strict'
import { serviceVolumeRemoval } from './remove-service'
import type { Spec } from './types'
test('service deletion retains shared volumes and removes only unused definitions', () => {
 const input = {schema_version:1,name:'fixture',volumes:{shared:{size_gib:1},private:{size_gib:1}},services:{db:{image:'postgres:17',volume:{size_gib:1,mount_path:'/data'},mounts:[{volume:'shared',mount_path:'/shared'},{volume:'private',mount_path:'/private'}]},web:{image:'nginx:alpine',mounts:[{volume:'shared',mount_path:'/shared'}]}}} as Spec
 const result = serviceVolumeRemoval(input,'db')
 assert.deepEqual(result.claims,['db-data','hakopod-volume-private'])
 assert.ok(result.spec.volumes?.shared)
 assert.equal(result.spec.volumes?.private,undefined)
 assert.ok(input.services.db)
 assert.ok(input.volumes?.private)
})
