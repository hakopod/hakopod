import assert from 'node:assert/strict'
import test from 'node:test'
import { templateReviewMatches, templateReviewSecretFields } from './template-review'
import type { components } from './api.generated'

test('edited template reviews show only retained references and allow new application secrets', () => {
  const fields = [
    { name: 'database-password', description: 'Database credential', format: 'password', generate: true, optional: false },
    { name: 'secret-key', description: 'Signing key', format: 'hex32', generate: true, optional: false },
  ]
  const required = templateReviewSecretFields(fields, ['secret-key', 'custom-db-password'])
  assert.deepEqual(required.map((field) => field.name), ['secret-key', 'custom-db-password'])
  assert.equal(required[0].format, 'hex32')
  assert.equal(required[1].generate, false)
  assert.equal(required[1].format, 'password')
  assert.deepEqual(templateReviewSecretFields(fields, []), [])
})

test('template review rejects a changed target scope, name or revision', () => {
  const expected = { name: 'fixture', project: 'review-fixture', environment: 'development', applicationId: 'fixture-id', revision: 4 }
  const plan = { expected_revision: 4, application_id: 'fixture-id', spec: { name: 'fixture' }, configuration: { project: expected.project, environment: expected.environment } } as components['schemas']['TemplatePlan']
  assert.equal(templateReviewMatches(plan, expected), true)
  assert.equal(templateReviewMatches({ ...plan, expected_revision: 5 }, expected), false)
  assert.equal(templateReviewMatches({ ...plan, application_id: 'another' }, expected), false)
  assert.equal(templateReviewMatches({ ...plan, spec: { ...plan.spec, name: 'another' } }, expected), false)
  assert.equal(templateReviewMatches({ ...plan, configuration: { ...plan.configuration, project: 'another' } }, expected), false)
})
