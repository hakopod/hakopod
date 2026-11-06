import assert from 'node:assert/strict'
import test from 'node:test'
import { policyTemplates, summarizePolicy, validatePolicy } from './agent-policy-form.ts'
import type { AgentPolicyDocument } from './agent-policies'

const doc = (): AgentPolicyDocument => ({
  schema_version: 1,
  description: '',
  when: [
    { field: 'operation', operator: 'matches', values: ['deploy*'] },
    { field: 'environment', operator: 'is_any_of', values: ['production'] },
  ],
  then: {
    effect: 'require_approval',
    approvers: [{ kind: 'project_admins' }],
    approval_ttl_seconds: 900,
  },
})

test('valid document has no errors', () => {
  assert.deepEqual(validatePolicy('Deploys', doc()), {})
})

test('rules mirror the server', () => {
  const d = doc()
  d.when[1] = { field: 'operation', operator: 'is_any_of', values: ['a*'] }
  d.then = {
    effect: 'block',
    approvers: [{ kind: 'project_admins' }],
    max_credential_ttl_seconds: 60,
  }
  const errors = validatePolicy(' ', d)
  assert.ok(errors.name)
  assert.equal(errors['when.1'], 'Each field can be used once.')
  assert.ok(errors.approvers)
  assert.ok(errors.credential_ttl)
  assert.equal(errors.approval_ttl, undefined)
  const wildcard = doc()
  wildcard.when[1].values = ['prod*']
  assert.ok(validatePolicy('x', wildcard)['when.1'])
  const empty = doc()
  empty.when[0].values = []
  assert.ok(validatePolicy('x', empty)['when.0'])
})

test('summary reads as plain English', () => {
  assert.equal(
    summarizePolicy(doc()),
    'When the operation matches deploy* and the environment is production, hold it for approval by project administrators. Unapproved requests expire after 15 min.',
  )
  const d = doc()
  d.when = [{ field: 'category', operator: 'is_any_of', values: ['deploy', 'credentials'] }]
  d.then = { effect: 'block' }
  d.except = { active_incident: true }
  assert.equal(
    summarizePolicy(d),
    'When the request is a deployment or credential access, refuse it. While an alarm is active, matching requests run and a notice is sent.',
  )
})

test('templates are valid documents', () => {
  for (const template of policyTemplates)
    assert.deepEqual(validatePolicy(template.name, template.document), {}, template.id)
})
