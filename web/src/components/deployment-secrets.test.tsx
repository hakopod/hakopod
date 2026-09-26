import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { DeploymentSecrets } from './deployment-secrets'
import type { Plan } from '../lib/types'

const ordinaryPlan: Plan = {
  application_id: 'fixture-app',
  expected_revision: 2,
  spec: {
    schema_version: 1,
    name: 'example',
    services: {
      web: {
        image: 'example.test/web:fixture',
        resources: {
          cpu_request: '100m',
          cpu_limit: '500m',
          memory_request: '128Mi',
          memory_limit: '256Mi',
        },
      },
    },
  },
  changes: [],
  warnings: [],
}

function render(plan: Plan) {
  return renderToStaticMarkup(
    <DeploymentSecrets
      plan={plan}
      project="demo"
      environment="production"
      busy={false}
      onBusy={() => {}}
      onChange={() => {}}
    />,
  )
}

test('ordinary service resize review renders without any secret requirements', () => {
  assert.equal(render(ordinaryPlan), '')
  assert.equal(render({ ...ordinaryPlan, required_secrets: [], missing_secrets: [] }), '')
})

test('ordinary service review survives saving the final missing secret', () => {
  const plan = { ...ordinaryPlan, required_secrets: ['database-password'] }
  const missing = render({ ...plan, missing_secrets: ['database-password'] })
  assert.match(missing, /Generate and save/)
  assert.doesNotMatch(missing, /Supply a GitHub token/)
  for (const missing_secrets of [[], undefined]) {
    const saved = render({ ...plan, missing_secrets })
    assert.match(saved, /All referenced application secrets are saved/)
    assert.doesNotMatch(saved, /Generate and save|Supply a GitHub token/)
  }
})

test('mixed runner and ordinary services retain scoped token instructions and prohibit generation', () => {
  const plan: Plan = {
    ...ordinaryPlan,
    spec: {
      ...ordinaryPlan.spec,
      services: {
        ...ordinaryPlan.spec.services,
        org: {
          image: 'example.test/runner:fixture',
          actions: { organization: 'example', credential: 'github-token', labels: ['fixture'] },
        },
        repo: {
          image: 'example.test/runner:fixture',
          actions: { repository: 'example/repo', credential: 'github-token', labels: ['fixture'] },
        },
      },
    },
    required_secrets: ['github-token'],
    missing_secrets: ['github-token'],
  }
  const missing = render(plan)
  assert.match(missing, /organization Self-hosted runners and repository Administration/)
  assert.doesNotMatch(missing, /Generate and save/)
  assert.match(
    render({ ...plan, missing_secrets: [] }),
    /All referenced application secrets are saved/,
  )
})
