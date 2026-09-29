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
import {
  RunnerWorkflowGuide,
  runnerCustomLabels,
  runnerLabelError,
  runnerWorkflowLabels,
} from './managed-actions-setup'

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

function render(
  saved: Application,
  generic = false,
  capabilityOverrides: Record<string, unknown> = {},
) {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  cache.setQueryData(['actions-capabilities', saved.project, saved.environment], {
    licensed: true,
    runtime_ready: true,
    message: '',
    runner_image: saved.spec.services.runner.image,
    resource_profiles: {
      compute: { CPURequest: '500m', CPULimit: '2', MemoryRequest: '1Gi', MemoryLimit: '4Gi' },
    },
    ...capabilityOverrides,
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

test('native pools preserve targets in guided setup while unavailable deployment stays gated', () => {
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
    const html = render(saved)
    assert.match(html, /Connect your jobs/)
    assert.match(
      html,
      new RegExp(`${provider === 'gitlab' ? 'GitLab' : 'Bitbucket'} execution is not available`),
    )
    assert.match(html, /value="native-management"/)
    assert.match(html, /id="runner-jobs-credential"[^>]*value="native-observer"/)
    assert.doesNotMatch(html, /<textarea[^>]*id="toml-import"|How to create the GitHub token/)
    if (provider === 'gitlab') {
      assert.match(html, /value="https:\/\/ci.example.test\/gitlab"/)
      assert.match(html, /value="123"/)
      assert.match(html, /value="fixture-policy"/)
    } else {
      assert.match(html, /bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb/)
      assert.match(html, /cccccccc-cccc-cccc-cccc-cccccccccccc/)
    }
    const generic = render(saved, true)
    assert.match(generic, /runner settings are preserved in TOML/)
    assert.match(generic, /Use the runner pool setup for guided provider configuration/)
    assert.match(generic, /<textarea[^>]*id="toml-import"/)
    assert.match(generic, new RegExp(saved.spec.services.runner.image))
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

test('Bitbucket removal does not promise unverified graceful draining', () => {
  const message = actionsRemovalMessage({
    provider: 'bitbucket',
    credential: 'token',
    labels: ['fixture'],
    bitbucket: {
      workspace: '{11111111-1111-4111-8111-111111111111}',
      repository: '{22222222-2222-4222-8222-222222222222}',
    },
  })
  assert.match(message, /dedicated runner/)
  assert.match(message, /Active pipeline steps may be interrupted/)
  assert.doesNotMatch(message, /lets running jobs finish|automatically/)
})

test('Bitbucket custom labels match provider syntax and reserve platform and ownership labels', () => {
  assert.equal(runnerLabelError(['build', 'team.123', 'a'.repeat(64)], 'bitbucket'), '')
  assert.equal(
    runnerLabelError(
      Array.from({ length: 9 }, (_, index) => `team.${index}`),
      'bitbucket',
    ),
    '',
  )
  assert.match(
    runnerLabelError(
      Array.from({ length: 10 }, (_, index) => `team.${index}`),
      'bitbucket',
    ),
    /nine custom/,
  )
  for (const label of [
    'Team',
    'team-build',
    'team_build',
    'a'.repeat(65),
    'self.hosted',
    'linux',
    'linux.arm64',
    'windows',
    'macos',
    'linux.shell',
    'hakopod.owner.fixture',
  ]) {
    assert.ok(
      runnerLabelError([label], 'bitbucket'),
      `${label} must not become a custom Bitbucket label`,
    )
  }
  assert.equal(runnerLabelError(['Team_Build-1'], 'github'), '')
  assert.match(runnerLabelError(['Team', 'team'], 'gitlab'), /distinct labels/)
})

test('Bitbucket workflow routing follows architecture and preserves a pool without custom labels', () => {
  assert.equal(runnerCustomLabels(undefined, 'bitbucket'), 'hakopod')
  assert.equal(runnerCustomLabels([], 'bitbucket'), '')
  assert.equal(runnerCustomLabels(['self.hosted', 'linux.arm64'], 'bitbucket'), '')
  assert.equal(
    runnerCustomLabels(['self.hosted', 'linux', 'team.build'], 'bitbucket'),
    'team.build',
  )
  assert.deepEqual(runnerWorkflowLabels([], 'bitbucket', 'amd64'), ['self.hosted', 'linux'])
  assert.deepEqual(
    runnerWorkflowLabels(['self.hosted', 'linux', 'team.build'], 'bitbucket', 'arm64'),
    ['self.hosted', 'linux.arm64', 'team.build'],
  )
  assert.deepEqual(runnerWorkflowLabels(['team-build'], 'github', 'arm64'), ['team-build'])
  const html = renderToStaticMarkup(
    <RunnerWorkflowGuide labels={[]} provider="bitbucket" architecture="arm64" />,
  )
  assert.match(html, /runs-on: \[&quot;self.hosted&quot;,&quot;linux.arm64&quot;\]/)
  assert.doesNotMatch(html, />hakopod</)
})

test('native setup exposes only approved choices for the loaded pool and never falls back to a global image', () => {
  const saved = application()
  saved.spec.services.runner.architecture = 'arm64'
  saved.spec.services.runner.actions = {
    provider: 'gitlab',
    credential: 'native-management',
    labels: ['fixture'],
    gitlab: { url: 'https://gitlab.com', project_id: 123 },
  }
  const binding = {
    application: saved.name,
    service: 'runner',
    architecture: 'arm64',
    image: 'registry.example/approved@sha256:' + 'b'.repeat(64),
    gitlab: { url: 'https://gitlab.com', project_id: 123 },
    cache: false,
    cross_architecture: false,
  }
  const provider = {
    provider: 'gitlab',
    lifecycle: 'ephemeral',
    available: true,
    image: 'registry.example/legacy-global@sha256:' + 'c'.repeat(64),
    minimum_resources: {
      cpu_request: '1',
      cpu_limit: '2',
      memory_request: '2Gi',
      memory_limit: '4Gi',
      workspace_gib: 8,
    },
    cache: { persistent: false },
    build: { native_architectures: ['arm64'], cross_architecture: false },
    isolation: { single_job: true, manager_credentials_isolated: true },
    cancellation_scope: 'job',
  }
  const before = structuredClone(saved)
  const unmatched = render(saved, false, { providers: [{ ...provider, bindings: [] }] })
  assert.match(unmatched, /No approved runner matches this pool name/)
  const matched = render(saved, false, {
    providers: [
      {
        ...provider,
        bindings: [
          binding,
          { ...binding, application: 'unrelated-application' },
          { ...binding, service: 'unrelated-service' },
        ],
      },
    ],
  })
  assert.match(matched, /Approved runner pools/)
  const choice = matched.match(/<input\b[^>]*value="0"[^>]*>/)?.[0]
  assert.ok(choice)
  assert.match(choice, /type="radio"/)
  assert.match(choice, /checked=""/)
  assert.doesNotMatch(matched, /No approved runner matches|unrelated-application|unrelated-service/)
  assert.deepEqual(saved, before)
})
