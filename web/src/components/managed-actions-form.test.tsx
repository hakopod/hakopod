import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  RouterContextProvider,
  createRootRoute,
  createRouter,
} from '@tanstack/react-router'
import { canAccess, ScopeContext } from '../lib/scope'
import type { Application, Identity } from '../lib/types'
import { specToTOML } from '../lib/toml'
import { ManagedActionsForm } from './managed-actions-form'
import { DeploymentForm } from './deploy-dialog'
import { actionsRemovalMessage } from '../lib/actions-provider'

function application(jobsCredential?: string): Application {
  // Synthetic saved application for form and TOML serialization checks.
  return {
    id: 'fixture-application',
    name: 'runners',
    project: 'pool-project',
    environment: 'staging',
    revision: 3,
    status: 'ready',
    observed: { status: 'ready', revision: 3 },
    created_at: '2026-09-29T00:00:00Z',
    updated_at: '2026-09-29T00:00:00Z',
    spec: {
      schema_version: 1,
      name: 'runners',
      services: {
        runner: {
          image: 'registry.example/runner@sha256:' + 'a'.repeat(64),
          size: 'compute',
          actions: {
            organization: 'example',
            credential: 'saved-runner-token',
            jobs_credential: jobsCredential,
            labels: ['fixture'],
          },
        },
      },
    },
  }
}

test('pool removal guidance distinguishes native draining from GitHub job cancellation', () => {
  const actions = application().spec.services.runner.actions!
  assert.match(actionsRemovalMessage(actions), /cancels running GitHub jobs/)
  const native = actionsRemovalMessage({
    ...actions,
    provider: 'gitlab',
    repository: '',
    organization: '',
    gitlab: { url: 'https://gitlab.com', project_id: 123 },
  })
  assert.match(native, /stops new jobs, lets running jobs finish/)
  assert.match(native, /if GitLab is temporarily unavailable/)
  assert.doesNotMatch(native, /GitHub|cancels running/)
})

function render(saved: Application, generic = false) {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  cache.setQueryData(['actions-capabilities', saved.project, saved.environment], {
    licensed: true,
    runtime_ready: true,
    message: '',
    runner_image: saved.spec.services.runner.image,
    resource_profiles: {
      compute: { CPURequest: '500m', CPULimit: '2', MemoryRequest: '1Gi', MemoryLimit: '4Gi' },
    },
  })
  const identity: Identity = {
    id: 'fixture-owner',
    name: 'Fixture owner',
    admin: true,
    owner: true,
    credential_type: 'browser',
    permissions: ['admin'],
    project: '',
    environment: '',
  }
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory({ initialEntries: ['/templates/managed-actions'] }),
  })
  try {
    return renderToStaticMarkup(
      <QueryClientProvider client={cache}>
        <RouterContextProvider router={router}>
          <ScopeContext.Provider
            value={{
              project: 'other-project',
              environment: 'development',
              identity,
              can: (permission) => canAccess(identity, saved.project, permission),
              syncScope: () => {},
            }}
          >
            {generic ? (
              <DeploymentForm application={saved} initialMode="form" onClose={() => {}} />
            ) : (
              <ManagedActionsForm application={saved} serviceName="runner" onClose={() => {}} />
            )}
          </ScopeContext.Provider>
        </RouterContextProvider>
        ,
      </QueryClientProvider>,
    )
  } finally {
    cache.clear()
  }
}

test('editing an existing runner preserves its separate job credential reference and loaded scope', () => {
  const html = render(application('saved-job-token'))
  assert.match(html, /id="runner-jobs-credential"[^>]*value="saved-job-token"/)
  assert.match(html, /value="saved-runner-token"/)
  assert.match(html, /pool-project \/ staging/)
  assert.doesNotMatch(html, /other-project \/ development/)
  assert.match(html, /Job details and logs use <code>saved-job-token<\/code>/)
  assert.doesNotMatch(html, /<strong>Actions: Read-only<\/strong>/)
})

test('existing pools keep the optional empty field and combined permission fallback', () => {
  for (const reference of [undefined, '']) {
    const html = render(application(reference))
    const input = html.match(/<input\b[^>]*id="runner-jobs-credential"[^>]*>/)?.[0]
    assert.ok(input)
    assert.match(input, /value=""/)
    assert.doesNotMatch(input, /\brequired=/)
    assert.match(html, /Leave blank to use the runner credential/)
    assert.match(html, /<strong>Actions: Read-only<\/strong>/)
    assert.match(html, /Self-hosted runners: Read and write/)
  }
})

test('an explicit shared reference keeps combined permission guidance', () => {
  const html = render(application('saved-runner-token'))
  assert.match(html, /id="runner-jobs-credential"[^>]*value="saved-runner-token"/)
  assert.match(html, /<strong>Actions: Read-only<\/strong>/)
  assert.doesNotMatch(html, /Job details and logs use <code>/)
})

test('runner TOML preserves reference names without creating a second credential by default', () => {
  const separate = specToTOML(application('saved-job-token').spec)
  assert.match(separate, /credential = "saved-runner-token"/)
  assert.match(separate, /jobs_credential = "saved-job-token"/)
  const existing = specToTOML(application().spec)
  assert.match(existing, /credential = "saved-runner-token"/)
  assert.doesNotMatch(existing, /jobs_credential/)
})

test('native pools retain provider targets and pinned images in the TOML editor from either entry point', () => {
  for (const provider of ['gitlab', 'bitbucket'] as const) {
    const saved = application('native-observer')
    saved.spec.services.runner.actions = {
      provider,
      credential: 'native-management',
      jobs_credential: 'native-observer',
      labels: ['fixture-native'],
      ...(provider === 'gitlab'
        ? {
            gitlab: {
              url: 'https://ci.example.test/gitlab',
              project_id: 123,
              trust_policy: 'fixture-policy',
            },
          }
        : {
            bitbucket: {
              workspace: '{bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb}',
              repository: '{cccccccc-cccc-cccc-cccc-cccccccccccc}',
            },
          }),
    }
    saved.spec.services.runner.image = 'registry.example/native@sha256:' + 'b'.repeat(64)
    const before = structuredClone(saved)
    for (const generic of [false, true]) {
      const html = render(saved, generic)
      assert.match(html, /runner settings are preserved in TOML/)
      assert.match(html, /Managed execution is not available for this provider/)
      assert.match(html, /<textarea[^>]*id="toml-import"/)
      assert.match(html, new RegExp(`provider = &quot;${provider}&quot;`))
      assert.match(html, /native-management|native-observer/)
      assert.match(html, new RegExp(saved.spec.services.runner.image))
      assert.doesNotMatch(
        html,
        /id="runner-jobs-credential"|How to create the GitHub token|Container images|Switching to the form|when switching to TOML/,
      )
      if (provider === 'gitlab') {
        assert.match(html, /\[services.runner.actions.gitlab\]/)
        assert.match(html, /project_id = 123/)
        assert.match(html, /trust_policy = &quot;fixture-policy&quot;/)
      } else {
        assert.match(html, /\[services.runner.actions.bitbucket\]/)
        assert.match(html, /bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb/)
        assert.match(html, /cccccccc-cccc-cccc-cccc-cccccccccccc/)
      }
    }
    assert.deepEqual(saved, before)
  }
})

test('inconsistent imported native targets cannot fall into the GitHub form', () => {
  for (const provider of [undefined, 'github'] as const) {
    const saved = application()
    saved.spec.services.runner.actions = {
      provider,
      credential: 'native-management',
      labels: ['fixture'],
      gitlab: { url: 'https://gitlab.com', group_id: 456 },
    }
    const html = render(saved)
    assert.match(html, /<textarea[^>]*id="toml-import"/)
    assert.match(html, /group_id = 456/)
    assert.doesNotMatch(html, /id="runner-jobs-credential"|How to create the GitHub token/)
  }
})
