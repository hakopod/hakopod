import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { components, operations } from '../lib/api.generated'
import type { Application, Identity } from '../lib/types'
import { ScopeContext } from '../lib/scope'
import { workflowJobPresentation } from '../lib/actions-workflow'
import { WorkflowJob, WorkflowLogStatus, WorkflowRunHeader } from './managed-actions-workflows'

type Job = components['schemas']['ActionsJob']
type JobLogs = operations['getActionsJobLogs']['responses'][200]['content']['application/json']

export function renderedButton(html: string, label: string) {
  return [...html.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g)]
    .map(([button]) => button)
    .find((button) => button.replace(/<[^>]*>/g, '').trim() === label)
}

export function nativeJob(): Job {
  // Synthetic history deliberately carries conflicting legacy fields. Native
  // rendering must use its verified provider identity without numeric coercion.
  return {
    slot_id: 'fixture-slot',
    runner_id: 1,
    provider: 'gitlab',
    provider_runner_id: '9007199254740993',
    observation: {
      repository: 'wrong-owner/wrong-repository',
      workflow: 'wrong-workflow',
      run_id: 1,
      run_number: 1,
      attempt: 1,
      job_key: 'wrong-job',
      branch: 'wrong-branch',
      sha: 'abcdef0',
    },
    job: null,
    native_job: {
      identity: {
        runner_id: '9007199254740993',
        runner_name: 'hakopod-fixture-slot',
        repository: '9007199254740995',
        run_id: '9007199254740997',
        job_id: '9007199254740999',
      },
      name: 'Build native package',
      status: 'completed',
      conclusion: 'success',
      started_at: '2026-09-29T06:45:00Z',
      completed_at: '2026-09-29T06:46:00Z',
      steps: [],
      steps_truncated: false,
    },
    discovery_state: 'observed',
    created_at: '2026-09-29T06:45:00Z',
    updated_at: '2026-09-29T06:46:00Z',
  }
}

export function renderJob(item: Job, permitted = true, logs: Partial<JobLogs> = {}) {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const application = {
    id: 'fixture-application',
    name: 'Fixture runners',
    project: 'fixture-project',
    environment: 'development',
  } as Application
  const identity: Identity = {
    id: 'fixture-reader',
    name: 'Fixture reader',
    admin: false,
    owner: false,
    credential_type: 'browser',
    permissions: [],
    project: '',
    environment: '',
    project_roles: [{ project: application.project, role: permitted ? 'developer' : 'viewer' }],
  }
  cache.setQueryData(['actions-job-logs', application.id, 'runner', item.slot_id], {
    source: 'gitlab',
    state: 'complete',
    lines: [{ number: 1, text: 'Synthetic GitLab job output' }],
    truncated: false,
    message: '',
    observed_at: '2026-09-29T06:46:00Z',
    ...logs,
  })
  try {
    return renderToStaticMarkup(
      <QueryClientProvider client={cache}>
        <ScopeContext.Provider
          value={{
            project: 'different-project',
            environment: 'production',
            identity,
            can: () => !permitted,
            syncScope: () => {},
          }}
        >
          <WorkflowJob item={item} application={application} service="runner" canReadLogs />
        </ScopeContext.Provider>
      </QueryClientProvider>,
    )
  } finally {
    cache.clear()
  }
}

test('workflow log status names each observed source without mislabeling native logs', () => {
  const observedAt = '2026-09-29T06:46:28.035Z'
  for (const [source, label] of [
    ['github', 'GitHub logs'],
    ['gitlab', 'GitLab logs'],
    ['runner', 'Live runner output'],
  ]) {
    const html = renderToStaticMarkup(<WorkflowLogStatus source={source} observedAt={observedAt} />)
    assert.ok(html.includes(`${label} · Checked `))
    if (source !== 'github') assert.doesNotMatch(html, /GitHub/)
  }
  for (const source of ['none', 'unknown']) {
    const html = renderToStaticMarkup(<WorkflowLogStatus source={source} observedAt={observedAt} />)
    assert.match(html, />Checked /)
    assert.doesNotMatch(html, /·|GitHub logs|GitLab logs|Live runner output/)
  }
})

test('native history renders verified opaque IDs and provider status without GitHub links', () => {
  const item = nativeJob()
  const view = workflowJobPresentation(item)
  assert.equal(view.details, item.native_job)
  assert.equal(view.status, 'success')
  assert.equal(view.name, 'Build native package')
  assert.equal(view.jobID, '9007199254740999')
  const header = renderToStaticMarkup(<WorkflowRunHeader item={item} />)
  assert.match(header, /Pipeline #9007199254740997/)
  assert.match(header, /GitLab · Project 9007199254740995/)
  assert.doesNotMatch(header, /href=|GitHub|wrong-|Attempt|#0/)
  const body = renderJob(item)
  assert.match(body, /Build native package/)
  assert.match(body, /Job: 9007199254740999/)
  assert.match(body, /success · 1m 0s/)
  assert.match(body, /GitLab logs · Checked/)
  assert.doesNotMatch(body, /GitHub|wrong-|Waiting for/)
})

test('native pending and unavailable assignments remain distinct and preserve uncertainty', () => {
  const first = { ...nativeJob(), native_job: undefined, discovery_state: 'pending' as const }
  const second = { ...first, slot_id: 'another-fixture-slot' }
  assert.notEqual(workflowJobPresentation(first).runKey, workflowJobPresentation(second).runKey)
  assert.equal(workflowJobPresentation(first).status, 'waiting')
  const pendingMessage = workflowJobPresentation(first).discoveryMessage
  const pending = renderJob(first, true, {
    state: 'pending',
    message: pendingMessage,
    lines: [],
  })
  assert.match(pending, /Waiting for GitLab to identify the job assigned to this runner/)
  assert.equal(pending.split(pendingMessage).length - 1, 1)
  const logError = 'The provider refused access to the retained job trace.'
  assert.ok(renderJob(first, true, { message: logError, lines: [] }).includes(logError))
  assert.doesNotMatch(pending, /GitHub|wrong-|Not started/)
  const unavailable = { ...first, discovery_state: 'unavailable' as const }
  assert.equal(workflowJobPresentation(unavailable).status, 'unavailable')
  assert.match(renderJob(unavailable), /This does not mean that no job ran/)
  const reused = { ...nativeJob(), discovery_state: 'reuse_detected' as const }
  assert.equal(workflowJobPresentation(reused).status, 'reuse_detected')
  assert.match(renderJob(reused), /The pool is held for operator inspection/)
})

test('native equal pipeline IDs do not merge historical jobs from unknown original instances', () => {
  const first = nativeJob()
  const second = { ...first, slot_id: 'different-instance-slot' }
  assert.notEqual(workflowJobPresentation(first).runKey, workflowJobPresentation(second).runKey)
})

test('GitHub history retains its existing run selection and original repository link', () => {
  const item = { ...nativeJob(), provider: 'github' as const, native_job: undefined }
  const view = workflowJobPresentation(item)
  assert.equal(view.runKey, 'wrong-owner/wrong-repository:1:1')
  assert.equal(view.name, 'wrong-job')
  assert.equal(view.details, null)
  const header = renderToStaticMarkup(<WorkflowRunHeader item={item} />)
  assert.match(header, /wrong-workflow #1/)
  assert.match(
    header,
    /href="https:\/\/github.com\/wrong-owner\/wrong-repository\/actions\/runs\/1\/attempts\/1"/,
  )
  assert.match(header, /View on GitHub/)
})

test('a failed step opens its own output without a second full-log panel', () => {
  const item = nativeJob()
  item.native_job = {
    ...item.native_job!,
    conclusion: 'failure',
    steps: [
      {
        number: 1,
        name: 'Checkout sources',
        status: 'completed',
        conclusion: 'success',
        started_at: '2026-09-29T06:45:00Z',
        completed_at: '2026-09-29T06:45:20Z',
      },
      {
        number: 2,
        name: 'Build package',
        status: 'completed',
        conclusion: 'failure',
        started_at: '2026-09-29T06:45:20Z',
        completed_at: '2026-09-29T06:46:00Z',
      },
    ],
  }
  const html = renderJob(item, true, {
    lines: [
      { number: 1, text: '2026-09-29T06:45:01Z Checkout succeeded' },
      { number: 2, text: '2026-09-29T06:45:21Z error: compiler failed' },
      { number: 3, text: 'Provider housekeeping without a timestamp' },
    ],
  })
  assert.equal((html.match(/scrollable log/g) || []).length, 1)
  assert.match(html, /aria-expanded="true" aria-controls="workflow-step-fixture-slot-2"/)
  assert.match(html, /error: compiler failed/)
  assert.match(html, /whitespace-pre-wrap wrap-anywhere/)
  assert.doesNotMatch(html, /min-w-full w-max/)
  const stepsButton = [...html.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g)]
    .map(([button]) => button)
    .find((button) => /Steps/.test(button))
  assert.match(stepsButton || '', /aria-pressed="true"/)
  assert.match(stepsButton || '', /text-\[var\(--navigation-active\)\]/)
  assert.doesNotMatch(
    html,
    /Checkout succeeded|Provider housekeeping without a timestamp|Full job log/,
  )
  assert.match(html, /1 line could not be matched to a step/)
  assert.ok(renderedButton(html, 'All output'))
})

test('a running step is the initial output view and missing steps use a single fallback', () => {
  const item = nativeJob()
  item.native_job = {
    ...item.native_job!,
    status: 'in_progress',
    conclusion: '',
    completed_at: null,
    steps: [
      {
        number: 1,
        name: 'Build package',
        status: 'in_progress',
        conclusion: '',
        started_at: '2026-09-29T06:45:00Z',
        completed_at: null,
      },
    ],
  }
  const html = renderJob(item, true, {
    state: 'live',
    lines: [{ number: 1, text: '2026-09-29T06:45:21Z Building' }],
  })
  assert.match(html, /aria-expanded="true" aria-controls="workflow-step-fixture-slot-1"/)
  assert.match(html, /Building/)
  assert.equal((renderJob(nativeJob()).match(/scrollable log/g) || []).length, 1)
})

test('the fallback full-job output wraps long unbroken text by default', () => {
  const message = 'registry.example.invalid/' + 'very-long-image-reference'.repeat(40)
  const html = renderJob(nativeJob(), true, { lines: [{ number: 1, text: message }] })
  assert.ok(html.includes(message), 'wrapping must not truncate or rewrite copied output')
  assert.match(html, /whitespace-pre-wrap wrap-anywhere/)
  assert.doesNotMatch(html, /min-w-full w-max/)
})
