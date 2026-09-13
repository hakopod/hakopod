import assert from 'node:assert/strict'
import test from 'node:test'
import { resolveProjectRouteScope } from './projects'
import type { Project } from './types'

const projects: Project[] = [
  { id: 'alpha', name: 'alpha', environments: [{ name: 'development' }, { name: 'production' }] },
  { id: 'beta', name: 'beta', environments: [{ name: 'staging' }] },
  { id: 'empty', name: 'empty', environments: [] },
]

test('project routes resolve only their named project and actual environment', () => {
  assert.deepEqual(resolveProjectRouteScope(projects, 'beta'), {
    project: projects[1],
    environment: 'staging',
    status: 'ready',
  })
  assert.deepEqual(resolveProjectRouteScope(projects, 'alpha', 'production'), {
    project: projects[0],
    environment: 'production',
    status: 'ready',
  })
  assert.deepEqual(resolveProjectRouteScope(projects, 'removed', 'development'), {
    project: undefined,
    environment: '',
    status: 'missing-project',
  })
  for (const invalid of ['', 'development', 'STAGING', ' staging ', true, 1, ['staging'], {}])
    assert.deepEqual(resolveProjectRouteScope(projects, 'beta', invalid), {
      project: projects[1],
      environment: '',
      status: 'missing-environment',
    })
})

test('loading, missing access and projects without environments never supply an application scope', () => {
  assert.deepEqual(resolveProjectRouteScope(undefined, 'alpha'), {
    project: undefined,
    environment: '',
    status: 'loading',
  })
  assert.deepEqual(resolveProjectRouteScope([], 'alpha'), {
    project: undefined,
    environment: '',
    status: 'missing-project',
  })
  assert.deepEqual(resolveProjectRouteScope(projects, 'empty'), {
    project: projects[2],
    environment: '',
    status: 'missing-environment',
  })
})
