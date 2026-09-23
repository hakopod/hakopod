import assert from 'node:assert/strict'
import test from 'node:test'
import { buildErrorStep } from './build-onboarding'
import { fieldError, fieldGroupError } from './form-errors'
import { hostedFreeIssues, hostedComputeIssues } from './compute-limits'
import type { Spec } from './types'

test('explicit backend paths survive the input wrapper and route back to the right step', () => {
  for (const [error, step] of [
    ['invalid input: repository: Enter owner/repository', 0],
    ['invalid input: context_path: Use a relative directory', 0],
    ['invalid input: framework.start_command: Provide a command', 1],
    ['build_args.PUBLIC_URL: Use a public URL', 1],
    ['services.web.port: Use a valid port', 2],
    ['size: Hosted Free supports small', 2],
  ] as const)
    assert.equal(buildErrorStep(error, 'web'), step)
  assert.equal(
    fieldError('invalid input: repository: Enter owner/repository', 'repository'),
    'Enter owner/repository',
  )
  assert.equal(
    fieldGroupError('invalid input: build_args.PUBLIC_URL: Use a public URL', 'build_args'),
    'build_args.PUBLIC_URL: Use a public URL',
  )
  for (const error of [
    'Registry is unavailable',
    'Cannot access repository: permission denied',
    'invalid input: service failed',
    'services.worker.port: Invalid port',
  ]) {
    assert.equal(buildErrorStep(error, 'web'), undefined)
    assert.equal(fieldError(error, 'repository', 'port'), undefined)
  }
})

test('hosted Free advice permits private workers and native secrets without modifying configuration', () => {
  const spec: Spec = {
    schema_version: 1,
    name: 'app',
    inject_env: true,
    secrets: { API_TOKEN: { ref: 'api-token' } },
    services: {
      web: {
        image: 'nginx:stable',
        size: 'small',
        replicas: 1,
        architecture: 'amd64',
        public: false,
        port: 0,
        secrets: { TOKEN: { ref: 'token' } },
      },
    },
  }
  assert.deepEqual(hostedFreeIssues(spec), [])
  const invalid = structuredClone(spec)
  Object.assign(invalid.services.web, {
    size: 'large',
    replicas: 2,
    architecture: 'arm64',
    job: { timeout_seconds: 60, retries: 0 },
  })
  invalid.secrets = { EXTERNAL_TOKEN: { provider: 'vault', path: 'app', key: 'token' } }
  const before = structuredClone(invalid)
  const issues = hostedFreeIssues(invalid)
  for (const field of ['size', 'replicas', 'architecture', 'job', 'secrets'])
    assert.ok(
      issues.some((issue) => issue.startsWith(`services.web.${field}:`)),
      field,
    )
  assert.deepEqual(invalid, before)
})


test('licensed hosted forms relax capacity without exposing private networking or placement', () => {
  const spec = {schema_version: 1, name: 'hosted', services: {web: {image:'nginx:stable',size:'large',replicas:3,resources:{memory_limit:'1Gi'}}}} as Spec
  assert.deepEqual(hostedComputeIssues(spec), [])
  assert.ok(hostedFreeIssues(spec).some(issue => issue.includes('.size:')))
  const invalid = structuredClone(spec)
  Object.assign(invalid.services.web, {node_name:'operator',private_egress:['database'],replicas:4})
  const issues=hostedComputeIssues(invalid)
  for (const field of ['node_name','private_egress','replicas']) assert.ok(issues.some(issue=>issue.startsWith(`services.web.${field}:`)))
  assert.equal(invalid.services.web.node_name,'operator')
})
