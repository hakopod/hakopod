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
  assert.doesNotMatch(missing, /How to create the GitHub token/)
  for (const missing_secrets of [[], undefined]) {
    const saved = render({ ...plan, missing_secrets })
    assert.match(saved, /All referenced application secrets are saved/)
    assert.doesNotMatch(saved, /Generate and save|How to create the GitHub token/)
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
  assert.match(missing, /Self-hosted runners: Read and write/)
  assert.match(missing, /Administration: Read and write/)
  assert.match(missing, /Actions: Read-only/)
  assert.doesNotMatch(missing, /Generate and save/)
  assert.match(
    render({ ...plan, missing_secrets: [] }),
    /All referenced application secrets are saved/,
  )
})

function runnerPlan(jobsCredential?: string): Plan {
  return {
    ...ordinaryPlan,
    spec: {
      ...ordinaryPlan.spec,
      services: {
        runner: {
          image: 'example.test/runner:fixture',
          actions: {
            organization: 'example',
            credential: 'runner-management',
            jobs_credential: jobsCredential,
            labels: ['fixture'],
          },
        },
      },
    },
    required_secrets: ['runner-management', ...(jobsCredential ? [jobsCredential] : [])],
  }
}

test('separate runner credentials ask only for their own GitHub permissions', () => {
  const plan = runnerPlan('job-observation')
  const management = render({ ...plan, missing_secrets: ['runner-management'] })
  assert.match(management, /Self-hosted runners: Read and write/)
  assert.doesNotMatch(management, /<strong>Actions: Read-only<\/strong>|Generate and save/)

  const jobs = render({ ...plan, missing_secrets: ['job-observation'] })
  assert.match(jobs, /Actions: Read-only/)
  assert.doesNotMatch(jobs, /Self-hosted runners: Read and write|Administration: Read and write/)
  assert.doesNotMatch(jobs, /Generate and save/)
})

test('omitted, empty and identical job references keep existing combined credential guidance', () => {
  for (const reference of [undefined, '', 'runner-management']) {
    const html = render({ ...runnerPlan(reference), missing_secrets: ['runner-management'] })
    assert.match(html, /Self-hosted runners: Read and write/)
    assert.match(html, /Actions: Read-only/)
    assert.doesNotMatch(html, /Generate and save/)
  }
})

test('a secret used for management and job observation across pools combines its permissions', () => {
  const plan = runnerPlan('shared-job-token')
  plan.spec.services.second = {
    image: 'example.test/runner:fixture',
    actions: {
      repository: 'example/second',
      credential: 'shared-job-token',
      jobs_credential: 'other-observer',
      labels: ['second'],
    },
  }
  const html = render({ ...plan, missing_secrets: ['shared-job-token'] })
  assert.match(html, /Administration: Read and write/)
  assert.match(html, /Actions: Read-only/)
  assert.doesNotMatch(html, /Self-hosted runners: Read and write|Generate and save/)
})

test('job credential guidance never leaks into an unrelated secret or a completed review', () => {
  const plan = runnerPlan('job-observation')
  const ordinary = render({
    ...plan,
    required_secrets: [...plan.required_secrets!, 'ordinary-password'],
    missing_secrets: ['ordinary-password'],
  })
  assert.match(ordinary, /Generate and save/)
  assert.doesNotMatch(ordinary, /How to create the GitHub token/)
  const complete = render({ ...plan, missing_secrets: [] })
  assert.match(complete, /All referenced application secrets are saved/)
  assert.doesNotMatch(complete, /How to create the GitHub token|Generate and save/)
})

test('native provider secrets get their own guidance and cannot generate random credentials', () => {
  for (const provider of ['gitlab', 'bitbucket'] as const) {
    const plan = runnerPlan('native-observer')
    plan.spec.services.runner.actions = {
      provider,
      credential: 'runner-management',
      jobs_credential: 'native-observer',
      labels: ['fixture'],
      ...(provider === 'gitlab'
        ? { gitlab: { url: 'https://gitlab.com', project_id: 123 } }
        : { bitbucket: { workspace: '{bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb}' } }),
    }
    for (const name of ['runner-management', 'native-observer']) {
      const html = render({ ...plan, missing_secrets: [name] })
      assert.match(
        html,
        new RegExp(`credential issued by ${provider === 'gitlab' ? 'GitLab' : 'Bitbucket'}`),
      )
      assert.match(html, /Managed execution is[^<]*not available for this provider/)
      assert.doesNotMatch(html, /GitHub|Actions: Read-only|Self-hosted runners|Generate and save/)
    }
  }
})

test('shared references across providers retain distinct credential guidance', () => {
  const plan = runnerPlan()
  plan.spec.services.native = {
    image: 'example.test/native:fixture',
    actions: {
      provider: 'gitlab',
      gitlab: { url: 'https://gitlab.com', project_id: 123 },
      credential: 'runner-management',
      labels: ['fixture'],
    },
  }
  const html = render({ ...plan, missing_secrets: ['runner-management'] })
  assert.match(html, /How to create the GitHub token/)
  assert.match(html, /credential issued by GitLab/)
  assert.doesNotMatch(html, /Generate and save/)
})
