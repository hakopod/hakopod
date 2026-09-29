import assert from 'node:assert/strict'
import test from 'node:test'
import {
  runnerBinding,
  runnerResourcesMeetMinimum,
  withRunnerMinimum,
  type RunnerBinding,
} from './runner-bindings'

const binding: RunnerBinding = {
  application: 'runners',
  service: 'ci',
  architecture: 'arm64',
  image: 'registry.example/runner@sha256:' + 'a'.repeat(64),
  gitlab: { url: 'https://gitlab.example/team', project_id: 42, trust_policy: 'company' },
  cache: true,
  cross_architecture: false,
}
test('native image selection requires the exact pool, target, trust policy and architecture', () => {
  assert.equal(runnerBinding([binding], binding), binding)
  for (const changed of [
    { application: 'other' },
    { service: 'other' },
    { architecture: 'amd64' },
    { architecture: '' },
    { gitlab: { ...binding.gitlab!, project_id: 43 } },
    { gitlab: { ...binding.gitlab!, trust_policy: 'other' } },
    { gitlab: { ...binding.gitlab!, url: 'https://gitlab.example/other' } },
    { gitlab: { ...binding.gitlab!, project_id: 0, group_id: 42 } },
  ])
    assert.equal(runnerBinding([binding], { ...binding, ...changed }), undefined)
  assert.equal(runnerBinding([binding, binding], binding), undefined)
  assert.equal(
    runnerBinding([binding], {
      ...binding,
      gitlab: { ...binding.gitlab!, url: 'https://GITLAB.EXAMPLE/team/' },
    }),
    binding,
  )
  assert.equal(
    runnerBinding([binding], {
      ...binding,
      gitlab: { ...binding.gitlab!, url: 'https://user@gitlab.example/team' },
    }),
    undefined,
  )
})
test('Bitbucket bindings require both UUIDs and never match a GitLab target', () => {
  const bitbucket = {
    ...binding,
    gitlab: undefined,
    bitbucket: {
      workspace: '{aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa}',
      repository: '{bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb}',
    },
  }
  assert.equal(runnerBinding([bitbucket], bitbucket), bitbucket)
  assert.equal(
    runnerBinding([bitbucket], {
      ...bitbucket,
      bitbucket: { ...bitbucket.bitbucket, repository: undefined },
    }),
    undefined,
  )
  assert.equal(runnerBinding([bitbucket], binding), undefined)
})
test('resource choices meet provider minimums without lowering a larger saved value', () => {
  const minimum = { cpu_request: '1', cpu_limit: '1', memory_request: '8Gi', memory_limit: '8Gi' }
  const resources = {
    cpu_request: '500m',
    cpu_limit: '4',
    memory_request: '2Gi',
    memory_limit: '8Gi',
  }
  assert.equal(runnerResourcesMeetMinimum(resources, minimum), false)
  const adjusted = withRunnerMinimum(resources, minimum)
  assert.deepEqual(adjusted, {
    cpu_request: '1',
    cpu_limit: '4',
    memory_request: '8Gi',
    memory_limit: '8Gi',
  })
  assert.equal(runnerResourcesMeetMinimum(adjusted, minimum), true)
  assert.equal(runnerResourcesMeetMinimum({ ...adjusted, cpu_limit: '500m' }, minimum), false)
})
