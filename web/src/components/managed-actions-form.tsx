import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { dashboardEdition } from '../lib/dashboard-edition'
import { canAccess } from '../lib/scope'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application, Plan, Service } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { FormPage } from './form-page'
import { Empty, ErrorState, HeadingHelp, Loading, Note, RequestError } from './shared'
import { DeploymentSecrets } from './deployment-secrets'
import { ManagedActionsTokenHelp } from './managed-actions-token-help'
import { DeploymentForm, DiffTable } from './deploy-dialog'
import { actionsProvider } from '../lib/actions-provider'
import { serviceResources } from '../lib/service-resources'
import { runnerReservationLabel, type RunnerResources } from '../lib/runner-resources'
import {
  runnerBinding,
  runnerResourcesMeetMinimum,
  withRunnerMinimum,
} from '../lib/runner-bindings'
import {
  RunnerCapacity,
  RunnerChoices,
  RunnerLabels,
  RunnerFact,
  RunnerLabelChips,
  RunnerPlacement,
  RunnerResourceFields,
  RunnerSteps,
  RunnerWorkflowGuide,
  runnerResourcePresets,
  runnerCustomLabels,
  runnerLabelError,
  runnerWorkflowLabels,
} from './managed-actions-setup'

type ManagedActionsFormProps = {
  application?: Application
  serviceName?: string
  onClose: () => void
}

export function ManagedActionsForm(props: ManagedActionsFormProps) {
  const [application] = useState(props.application)
  const actions = props.serviceName
    ? application?.spec.services[props.serviceName]?.actions
    : undefined
  if (
    actions &&
    ((actions.gitlab && actions.bitbucket) ||
      (actions.gitlab && actions.provider !== 'gitlab') ||
      (actions.bitbucket && actions.provider !== 'bitbucket'))
  )
    return (
      <DeploymentForm
        application={application}
        serviceName={props.serviceName}
        initialMode="toml"
        onClose={props.onClose}
      />
    )
  return <RunnerPoolForm {...props} />
}

function RunnerPoolForm({
  application: currentApplication,
  serviceName,
  onClose,
}: ManagedActionsFormProps) {
  // A background refetch must not silently advance the revision behind a draft.
  const [application] = useState(currentApplication)
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const original = serviceName ? application?.spec.services[serviceName] : undefined
  const project = application?.project || scope.project
  const environment = application?.environment || scope.environment
  const [provider, setProvider] = useState<string>(
    original?.actions ? actionsProvider(original.actions) : 'github',
  )
  const providerName =
    provider === 'gitlab' ? 'GitLab' : provider === 'bitbucket' ? 'Bitbucket' : 'GitHub'
  const [gitlabHost, setGitlabHost] = useState(
    original?.actions?.gitlab?.url && original.actions.gitlab.url !== 'https://gitlab.com'
      ? 'self-managed'
      : 'cloud',
  )
  const [gitlabURL, setGitlabURL] = useState(original?.actions?.gitlab?.url || '')
  const [gitlabScope, setGitlabScope] = useState(
    original?.actions?.gitlab?.group_id ? 'group' : 'project',
  )
  const [gitlabID, setGitlabID] = useState(
    String(original?.actions?.gitlab?.project_id || original?.actions?.gitlab?.group_id || ''),
  )
  const [trustPolicy, setTrustPolicy] = useState(original?.actions?.gitlab?.trust_policy || '')
  const [bitbucketWorkspace, setBitbucketWorkspace] = useState(
    original?.actions?.bitbucket?.workspace || '',
  )
  const [bitbucketRepository, setBitbucketRepository] = useState(
    original?.actions?.bitbucket?.repository || '',
  )
  const [name, setName] = useState(application?.name || '')
  const [runnerName, setRunnerName] = useState(serviceName || 'runner')
  const [runnerScope, setRunnerScope] = useState(
    original?.actions?.repository ? 'repository' : 'organization',
  )
  const [organization, setOrganization] = useState(original?.actions?.organization || '')
  const [runnerGroup, setRunnerGroup] = useState(
    original?.actions?.runner_group_id ? String(original.actions.runner_group_id) : '',
  )
  const [repository, setRepository] = useState(original?.actions?.repository || '')
  const [credential, setCredential] = useState(
    original?.actions?.credential || 'github-runner-token',
  )
  const [jobsCredential, setJobsCredential] = useState(original?.actions?.jobs_credential || '')
  const [labels, setLabels] = useState(() =>
    runnerCustomLabels(original?.actions?.labels, provider),
  )
  const [replicas, setReplicas] = useState(String(original?.replicas ?? 1))
  const [architecture, setArchitecture] = useState(original?.architecture || '')
  const [nodeName, setNodeName] = useState(original?.node_name || '')
  const [step, setStep] = useState(0)
  const [visited, setVisited] = useState(0)
  const [resourcePreset, setResourcePreset] = useState(original ? 'saved' : 'defaults')
  const [timeout, setTimeout] = useState(String(original?.actions?.timeout_minutes || 60))
  const [cacheEnabled, setCacheEnabled] = useState(!!original?.actions?.cache)
  const [cacheCredential, setCacheCredential] = useState(
    original?.actions?.cache?.credential || 'gitlab-cache',
  )
  const [workspaceSize, setWorkspaceSize] = useState(
    String(original?.actions?.workspace_size_gib || 2),
  )
  const [resources, setResources] = useState<RunnerResources>(
    original?.resources ||
      (!original && dashboardEdition.cloud
        ? { cpu_request: '500m', cpu_limit: '2500m', memory_request: '1Gi', memory_limit: '4Gi' }
        : {}),
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [plan, setPlan] = useState<Plan | null>(null)
  const key = useRef('')
  const capabilities = useQuery({
    queryKey: ['actions-capabilities', project, environment],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/actions/capabilities', {
          signal,
          params: { query: { project, environment } },
        }),
      ),
    enabled: !!project && !!environment,
  })
  const providerCapability = capabilities.data?.providers?.find(
    (item) => item.provider === provider,
  )
  const minimum = providerCapability?.minimum_resources
  const size = original?.size || (dashboardEdition.cloud ? 'large' : 'compute')
  const effective = serviceResources(
    { image: '', size, resources },
    capabilities.data?.resource_profiles,
  )
  const rawResources: RunnerResources = {
    cpu_request: resources.cpu_request ?? effective?.CPURequest,
    cpu_limit: resources.cpu_limit ?? effective?.CPULimit,
    memory_request: resources.memory_request ?? effective?.MemoryRequest,
    memory_limit: resources.memory_limit ?? effective?.MemoryLimit,
  }
  const effectiveResources =
    !original && resourcePreset === 'defaults'
      ? withRunnerMinimum(rawResources, minimum)
      : rawResources
  const profile = serviceResources({ image: '', size }, capabilities.data?.resource_profiles)
  const installationResources: RunnerResources = {
    cpu_request: profile?.CPURequest,
    cpu_limit: profile?.CPULimit,
    memory_request: profile?.MemoryRequest,
    memory_limit: profile?.MemoryLimit,
  }
  const reservationLabel = runnerReservationLabel(effectiveResources, Number(replicas))
  const canWrite = canAccess(scope.identity, project, 'deployments:write')
  const nodes = useQuery({
    queryKey: ['placement-nodes', project, environment, name, 'actions'],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/placement/nodes', {
          signal,
          params: { query: { project, environment, application: name, runtime: 'actions' } },
        }),
      ),
    enabled: !!project && !!environment && !!name && canWrite && step >= 1,
    staleTime: 30_000,
  })
  const selectedNode = nodes.data?.items.find((node) => node.name === nodeName)
  const invalidNode = Boolean(
    nodeName &&
    nodes.data &&
    (!selectedNode ||
      !selectedNode.available ||
      (architecture && architecture !== selectedNode.architecture)),
  )
  const workflowLabels = [
    ...new Set(
      labels
        .split(',')
        .map((label) => label.trim())
        .filter(Boolean),
    ),
  ]
  const lifecycle =
    providerCapability?.lifecycle || (provider === 'bitbucket' ? 'dedicated' : 'ephemeral')
  const target = {
    gitlab:
      provider === 'gitlab'
        ? {
            url: gitlabHost === 'cloud' ? 'https://gitlab.com' : gitlabURL,
            project_id: gitlabScope === 'project' ? Number(gitlabID) : undefined,
            group_id: gitlabScope === 'group' ? Number(gitlabID) : undefined,
            trust_policy: gitlabHost === 'self-managed' ? trustPolicy : undefined,
          }
        : undefined,
    bitbucket:
      provider === 'bitbucket'
        ? { workspace: bitbucketWorkspace, repository: bitbucketRepository }
        : undefined,
  }
  const approvedBindings = (providerCapability?.bindings || []).filter(
    (item) =>
      (!application || item.application === application.name) &&
      (!original || item.service === runnerName),
  )
  const binding = runnerBinding(providerCapability?.bindings, {
    application: name,
    service: runnerName,
    architecture,
    ...target,
  })
  const cacheAvailable =
    provider === 'gitlab' ? !!binding?.cache : !!providerCapability?.cache.persistent
  const resourcesValid = runnerResourcesMeetMinimum(effectiveResources, minimum)
  const providerAvailable = providerCapability
    ? providerCapability.available
    : provider === 'github'
  const runnerImage =
    provider === 'github'
      ? providerCapability?.image || capabilities.data?.runner_image
      : binding?.image
  const bindingReason =
    provider === 'github' || binding
      ? ''
      : !providerAvailable
        ? providerCapability?.reason ||
          `${providerName} execution is not available on this installation yet.`
        : !architecture
          ? 'Choose the architecture approved for this pool.'
          : 'No approved runner matches this pool name, service, provider target and architecture. Choose an approved pool or ask your operator to approve this exact configuration.'
  const workspaceMinimum = minimum?.workspace_gib || 2
  const ready =
    canWrite &&
    !!capabilities.data?.licensed &&
    !!capabilities.data?.runtime_ready &&
    !!effective &&
    providerAvailable &&
    !!runnerImage &&
    resourcesValid &&
    Number(workspaceSize) >= workspaceMinimum &&
    (!cacheEnabled || provider !== 'gitlab' || cacheAvailable)
  const canDraft = canWrite && !!capabilities.data?.licensed
  const targetLabel =
    provider === 'github'
      ? runnerScope === 'organization'
        ? organization
        : repository
      : provider === 'gitlab'
        ? `${gitlabHost === 'cloud' ? 'gitlab.com' : gitlabURL} / ${gitlabScope} ${gitlabID}`
        : [bitbucketWorkspace, bitbucketRepository].filter(Boolean).join(' / ')
  function chooseProvider(next: string) {
    setProvider(next)
    if (!original && resourcePreset !== 'custom') {
      const nextMinimum = capabilities.data?.providers?.find(
        (item) => item.provider === next,
      )?.minimum_resources
      setResources(withRunnerMinimum(resources, nextMinimum))
      if (nextMinimum)
        setWorkspaceSize(String(Math.max(Number(workspaceSize), nextMinimum.workspace_gib)))
    }
    if (!original && credential === `${provider}-runner-token`)
      setCredential(`${next}-runner-token`)
  }
  function chooseBinding(value: string) {
    if (value === '') return
    const approved = approvedBindings[Number(value)]
    if (!approved) return
    if (!application) setName(approved.application)
    if (!original) setRunnerName(approved.service)
    setArchitecture(approved.architecture as 'amd64' | 'arm64')
    if (approved.gitlab) {
      setGitlabHost(
        approved.gitlab.url.replace(/\/+$/, '') === 'https://gitlab.com' ? 'cloud' : 'self-managed',
      )
      setGitlabURL(approved.gitlab.url)
      setGitlabScope(approved.gitlab.group_id ? 'group' : 'project')
      setGitlabID(String(approved.gitlab.project_id || approved.gitlab.group_id || ''))
      setTrustPolicy(approved.gitlab.trust_policy || '')
    }
    if (approved.bitbucket) {
      setBitbucketWorkspace(approved.bitbucket.workspace)
      setBitbucketRepository(approved.bitbucket.repository || '')
    }
    if (!original && resourcePreset !== 'custom') {
      setResources(
        withRunnerMinimum(
          resourcePreset === 'defaults'
            ? installationResources
            : runnerResourcePresets[resourcePreset] || resources,
          minimum,
        ),
      )
      setWorkspaceSize(String(Math.max(Number(workspaceSize), workspaceMinimum)))
    }
  }
  const bindingOptions = approvedBindings.map((item, index) => ({
    value: String(index),
    label: `${item.application} / ${item.service}`,
    detail: `${item.architecture.toUpperCase()} · ${item.gitlab ? `${item.gitlab.url} / ${item.gitlab.project_id ? `project ${item.gitlab.project_id}` : `group ${item.gitlab.group_id}`}` : item.bitbucket?.repository || 'Repository unavailable'}`,
  }))
  const reviewFocus = useRef<HTMLDivElement>(null)
  const nameFocus = useRef<HTMLInputElement>(null)
  const stepFocus = useRef<HTMLDivElement>(null)
  const form = useRef<HTMLFormElement>(null)
  useEffect(() => {
    const target = plan ? reviewFocus.current : stepFocus.current
    target?.focus({ preventScroll: true })
    target?.scrollIntoView({ block: 'start' })
  }, [step, !!plan])
  const wrongApplication =
    application && Object.values(application.spec.services).some((service) => !service.actions)
  function move(next: number) {
    if (busy || next === step) return
    if (next > step && !form.current?.reportValidity()) return
    setPlan(null)
    setError('')
    setStep(next)
    setVisited((current) => Math.max(current, next))
  }
  async function advance(event: FormEvent) {
    event.preventDefault()
    if (step === 1 && invalidNode) {
      setError('Choose an eligible node for this architecture, or use Automatic placement.')
      return
    }
    if (step === 1 && provider !== 'github' && !architecture) {
      setError('Choose AMD64 or ARM64 for this runner image.')
      return
    }
    if (step === 1 && !resourcesValid) {
      setError('Choose a resource preset or increase the values to meet this provider’s minimums.')
      return
    }
    if (step < 2) move(step + 1)
    else await review(event)
  }
  async function review(event: FormEvent) {
    event.preventDefault()
    if (busy || !ready) return
    setBusy(true)
    setError('')
    try {
      if (workflowLabels.length === 0 && provider !== 'bitbucket')
        throw new Error('Add at least one workflow label.')
      const labelError = runnerLabelError(workflowLabels, provider)
      if (labelError) throw new Error(labelError)
      if (application?.spec.services[runnerName] && !original)
        throw new Error('Choose a service name that is not already used.')
      const service: Service = {
        ...original,
        image: runnerImage!,
        public: false,
        size,
        resources:
          !original && resourcePreset === 'defaults'
            ? effectiveResources
            : Object.keys(resources).length
              ? resources
              : undefined,
        replicas: Number(replicas),
        architecture: architecture ? (architecture as 'amd64' | 'arm64') : undefined,
        node_name: nodeName || undefined,
        actions: {
          ...original?.actions,
          provider: provider as 'github' | 'gitlab' | 'bitbucket',
          gitlab:
            provider === 'gitlab'
              ? {
                  url: gitlabHost === 'cloud' ? 'https://gitlab.com' : gitlabURL,
                  project_id: gitlabScope === 'project' ? Number(gitlabID) : undefined,
                  group_id: gitlabScope === 'group' ? Number(gitlabID) : undefined,
                  trust_policy: gitlabHost === 'self-managed' ? trustPolicy : undefined,
                }
              : undefined,
          bitbucket:
            provider === 'bitbucket'
              ? { workspace: bitbucketWorkspace, repository: bitbucketRepository || undefined }
              : undefined,
          organization:
            provider === 'github' && runnerScope === 'organization' ? organization : undefined,
          repository:
            provider === 'github' && runnerScope === 'repository' ? repository : undefined,
          runner_group_id:
            provider === 'github' && runnerScope === 'organization' && runnerGroup
              ? Number(runnerGroup)
              : undefined,
          credential,
          jobs_credential: jobsCredential || undefined,
          cache:
            provider === 'gitlab' && cacheEnabled ? { credential: cacheCredential } : undefined,
          labels: runnerWorkflowLabels(workflowLabels, provider, architecture),
          timeout_minutes:
            lifecycle === 'dedicated' ? original?.actions?.timeout_minutes : Number(timeout),
          workspace_size_gib: Number(workspaceSize),
        },
      }
      const result = await unwrap(
        client.POST('/plan', {
          body: {
            project,
            environment,
            spec: {
              ...application?.spec,
              schema_version: 1,
              name,
              services: { ...application?.spec.services, [runnerName]: service },
            },
          },
        }),
      )
      if (
        result.expected_revision !== (application?.revision || 0) ||
        (application && result.application_id !== application.id)
      )
        throw new Error(
          'The application changed. Reload its latest revision before reviewing again.',
        )
      setPlan(result)
      setStep(3)
      setVisited(3)
      key.current = crypto.randomUUID()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  async function deploy() {
    if (!plan || busy || !ready || plan.missing_secrets?.length) return
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.POST('/deployments', {
          body: {
            project,
            environment,
            spec: plan.spec,
            expected_revision: plan.expected_revision,
          },
          params: { header: { 'Idempotency-Key': key.current } },
        }),
      )
      void cache.invalidateQueries({ queryKey: ['applications'] })
      if (application) void cache.invalidateQueries({ queryKey: ['application', application.id] })
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: result.id } })
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  if (!project || !environment)
    return (
      <FormPage
        title="Create runner pool"
        description="Connect a CI provider and choose where its jobs run."
        breadcrumbs={[]}
      >
        <Empty
          title="Choose a project"
          description="Select a project and environment before creating a runner pool."
        />
      </FormPage>
    )
  if (serviceName && !original?.actions)
    return (
      <FormPage
        title="Configure runner pool"
        description="Configure an existing runner service."
        breadcrumbs={[]}
      >
        <Empty
          title="Runner pool not found"
          description="Return to the application and choose an existing runner pool."
        />
      </FormPage>
    )
  function resourceChoice(value: string) {
    setResourcePreset(value)
    if (value === 'saved') setResources(original?.resources || {})
    else if (value === 'defaults') setResources(withRunnerMinimum(installationResources, minimum))
    else if (runnerResourcePresets[value])
      setResources(withRunnerMinimum(runnerResourcePresets[value], minimum))
    else if (value === 'custom') setResources({ ...effectiveResources })
  }
  function presetSummary(preset: RunnerResources) {
    const value = withRunnerMinimum(preset, minimum)
    return `Up to ${value.cpu_limit} CPU · ${value.memory_limit} memory`
  }
  return (
    <FormPage
      title={original ? 'Configure runner pool' : 'Create runner pool'}
      description="Connect your CI provider, choose compute and review the changes."
      breadcrumbs={[]}
    >
      {capabilities.isPending ? (
        <Loading />
      ) : capabilities.error ? (
        <ErrorState error={capabilities.error} retry={() => void capabilities.refetch()} />
      ) : (
        <div className="grid min-w-0 gap-4">
          {!canWrite && (
            <Note>You need deployment write access to create or update runner pools.</Note>
          )}
          {!capabilities.data.licensed && (
            <Note>
              Managed runners require {dashboardEdition.cloud ? 'Cloud Team' : 'Pro'} access.{' '}
              {!dashboardEdition.cloud && scope.identity.admin && (
                <Link to="/settings" search={{ tab: 'license' }}>
                  Open license settings
                </Link>
              )}
            </Note>
          )}
          {!capabilities.data.runtime_ready && (
            <Note>
              {capabilities.data.message || 'The runner sandbox is not ready.'} Ask your operator to
              prepare a runner node.
            </Note>
          )}
          {currentApplication && currentApplication.revision !== application?.revision && (
            <Note>
              The application changed while you were editing. Your draft is preserved; review checks
              the original revision.
            </Note>
          )}
          {wrongApplication ? (
            <Note>
              Runner pools need a dedicated application. Create a separate application for these
              jobs.
            </Note>
          ) : (
            <>
              <RunnerSteps step={step} visited={visited} busy={busy} onStep={move} />
              <div
                className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 text-xs muted-text"
                aria-label="Pool scope"
              >
                <span>
                  {project} / {environment}
                </span>
                <span>
                  {name || 'New pool'}
                  {original ? ` · ${runnerName}` : ''}
                </span>
                <span>
                  {replicas} {replicas === '1' ? 'job slot' : 'job slots'}
                </span>
              </div>
              {plan ? (
                <div
                  ref={reviewFocus}
                  tabIndex={-1}
                  aria-label="Review runner pool"
                  className="grid min-w-0 gap-4 focus-visible:outline-2"
                >
                  <RunnerSection title="Ready to deploy">
                    <div className="grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-4">
                      <RunnerFact label={providerName}>{targetLabel}</RunnerFact>
                      <RunnerFact label="Compute">
                        {replicas} × {architecture || 'Automatic architecture'}
                      </RunnerFact>
                      <RunnerFact label="Placement">
                        {nodeName || 'Any eligible runner node'}
                      </RunnerFact>
                      <RunnerFact
                        label={
                          lifecycle === 'dedicated' ? 'Runner workspace' : 'Workspace & lifetime'
                        }
                      >
                        {workspaceSize} GiB{lifecycle !== 'dedicated' && ` · ${timeout} minutes`}
                      </RunnerFact>
                    </div>
                    <RunnerLabelChips
                      labels={runnerWorkflowLabels(workflowLabels, provider, architecture)}
                    />
                    <p className="text-sm" role="status">
                      {reservationLabel}
                    </p>
                    {nodeName && (
                      <p className="text-sm">
                        Jobs wait if {nodeName} is unavailable; they will not move to another node.
                      </p>
                    )}
                    <p className="text-sm">
                      {lifecycle === 'dedicated'
                        ? 'This runner stays assigned to one repository. Updating or stopping it may interrupt an active pipeline step.'
                        : 'Busy runners finish their jobs before updates apply. Each new job receives a fresh workspace.'}
                    </p>
                    <details className="border-y border-[var(--hairline)]">
                      <summary className="min-h-11 cursor-pointer py-3 text-sm">
                        Configuration details
                      </summary>
                      <dl className="grid min-w-0 gap-3 pb-3 text-sm sm:grid-cols-2">
                        <div>
                          <dt className="muted-text">Application / service</dt>
                          <dd className="wrap-anywhere">
                            {name} / {runnerName}
                          </dd>
                        </div>
                        <div>
                          <dt className="muted-text">Runner credential</dt>
                          <dd className="wrap-anywhere">
                            <code>{credential}</code>
                          </dd>
                        </div>
                        <div>
                          <dt className="muted-text">Job logs credential</dt>
                          <dd className="wrap-anywhere">
                            <code>{jobsCredential || credential}</code>
                            {(!jobsCredential || jobsCredential === credential) &&
                              ' · Same as runner'}
                          </dd>
                        </div>
                        <div>
                          <dt className="muted-text">Reserved per runner</dt>
                          <dd>
                            {effectiveResources.cpu_request} CPU ·{' '}
                            {effectiveResources.memory_request} memory
                          </dd>
                        </div>
                        <div>
                          <dt className="muted-text">Limit per runner</dt>
                          <dd>
                            {effectiveResources.cpu_limit} CPU · {effectiveResources.memory_limit}{' '}
                            memory
                          </dd>
                        </div>
                        {provider === 'github' && runnerScope === 'organization' && (
                          <div>
                            <dt className="muted-text">Runner group</dt>
                            <dd>{runnerGroup || 'GitHub default group'}</dd>
                          </div>
                        )}
                      </dl>
                    </details>
                    <DiffTable changes={plan.changes} />
                    <DeploymentSecrets
                      plan={plan}
                      project={project}
                      environment={environment}
                      busy={busy}
                      onBusy={setBusy}
                      onChange={(missing) =>
                        setPlan((current) =>
                          current ? { ...current, missing_secrets: missing } : current,
                        )
                      }
                    />
                    <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--hairline)] pt-3">
                      <Button disabled={busy} onClick={() => move(2)}>
                        Back to workflow
                      </Button>
                      <Button
                        variant="primary"
                        disabled={busy || !!plan.missing_secrets?.length || !ready}
                        onClick={() => void deploy()}
                      >
                        {busy ? 'Submitting…' : 'Deploy runner pool'}
                      </Button>
                    </div>
                  </RunnerSection>
                </div>
              ) : (
                <form
                  ref={form}
                  onSubmit={(event) => void advance(event)}
                  className="grid min-w-0 gap-4"
                  onInvalidCapture={(event) => {
                    const details = (event.target as HTMLElement).closest('details')
                    if (details) details.open = true
                  }}
                >
                  <div
                    ref={stepFocus}
                    tabIndex={-1}
                    aria-label={`${['Provider connection', 'Runner compute', 'Workflow settings'][step]} step`}
                    className="min-w-0 focus-visible:outline-2"
                  >
                    {step === 0 && (
                      <RunnerSection title="Connect your jobs">
                        <RunnerChoices
                          label="CI provider"
                          value={provider}
                          onChange={chooseProvider}
                          disabled={busy || !!original}
                          options={[
                            { value: 'github', label: 'GitHub', detail: 'GitHub Actions' },
                            {
                              value: 'gitlab',
                              label: 'GitLab',
                              detail: 'GitLab.com or self-managed',
                            },
                            {
                              value: 'bitbucket',
                              label: 'Bitbucket',
                              detail: 'Bitbucket Cloud Pipelines',
                            },
                          ]}
                        />
                        {!providerAvailable && (
                          <Note>
                            {providerCapability?.reason ||
                              `${providerName} execution is not available on this installation yet. You can explore the setup; deployment stays disabled.`}
                          </Note>
                        )}
                        {provider !== 'github' &&
                          bindingOptions.length > 0 &&
                          (bindingOptions.length <= 6 ? (
                            <RunnerChoices
                              label="Approved runner pools"
                              value={binding ? String(approvedBindings.indexOf(binding)) : ''}
                              options={bindingOptions}
                              disabled={busy}
                              onChange={chooseBinding}
                            />
                          ) : (
                            <SelectField
                              label="Approved runner pool"
                              value={binding ? String(approvedBindings.indexOf(binding)) : ''}
                              options={[
                                { value: '', label: 'Choose an approved pool' },
                                ...bindingOptions.map((item) => ({
                                  value: item.value,
                                  label: `${item.label} · ${item.detail}`,
                                })),
                              ]}
                              disabled={busy}
                              onValueChange={chooseBinding}
                            />
                          ))}
                        {provider !== 'github' && providerAvailable && bindingReason && (
                          <p className="text-sm muted-text" role="status">
                            {bindingReason}
                          </p>
                        )}
                        <div className="grid min-w-0 gap-4 lg:grid-cols-2">
                          <div className="grid content-start gap-4">
                            <label className="grid gap-2 text-sm font-medium">
                              Pool name
                              <Input
                                ref={nameFocus}
                                required
                                readOnly={!!application}
                                disabled={busy}
                                value={name}
                                pattern={'[a-z][a-z0-9\\-]{0,39}'}
                                maxLength={40}
                                placeholder="team-runners"
                                onChange={(event) => setName(event.target.value)}
                              />
                            </label>
                            {provider === 'github' && (
                              <>
                                <RunnerChoices
                                  compact
                                  label="Share runners with"
                                  value={runnerScope}
                                  onChange={setRunnerScope}
                                  disabled={busy}
                                  options={[
                                    { value: 'organization', label: 'Organization' },
                                    { value: 'repository', label: 'Repository' },
                                  ]}
                                />
                                <label className="grid gap-2 text-sm font-medium">
                                  {runnerScope === 'organization'
                                    ? 'GitHub organization'
                                    : 'GitHub repository'}
                                  <Input
                                    required
                                    value={
                                      runnerScope === 'organization' ? organization : repository
                                    }
                                    placeholder={
                                      runnerScope === 'organization'
                                        ? 'your-team'
                                        : 'your-team/your-repository'
                                    }
                                    maxLength={201}
                                    disabled={busy}
                                    onChange={(event) =>
                                      runnerScope === 'organization'
                                        ? setOrganization(event.target.value)
                                        : setRepository(event.target.value)
                                    }
                                  />
                                </label>
                              </>
                            )}
                            {provider === 'gitlab' && (
                              <>
                                <RunnerChoices
                                  compact
                                  label="GitLab installation"
                                  value={gitlabHost}
                                  onChange={setGitlabHost}
                                  disabled={busy}
                                  options={[
                                    { value: 'cloud', label: 'GitLab.com' },
                                    { value: 'self-managed', label: 'Self-managed' },
                                  ]}
                                />
                                <RunnerChoices
                                  compact
                                  label="Share runners with"
                                  value={gitlabScope}
                                  onChange={setGitlabScope}
                                  disabled={busy}
                                  options={[
                                    { value: 'project', label: 'Project' },
                                    { value: 'group', label: 'Group' },
                                  ]}
                                />
                                <label className="grid gap-2 text-sm font-medium">
                                  GitLab {gitlabScope} ID
                                  <Input
                                    required
                                    type="number"
                                    min={1}
                                    max={9007199254740991}
                                    value={gitlabID}
                                    disabled={busy}
                                    placeholder="12345"
                                    onChange={(event) => setGitlabID(event.target.value)}
                                  />
                                  <span className="field-help">
                                    Find this numeric ID in your {gitlabScope}'s GitLab settings.
                                  </span>
                                </label>
                                {gitlabHost === 'self-managed' && (
                                  <details open className="border-t border-[var(--hairline)]">
                                    <summary className="min-h-11 cursor-pointer py-3 text-sm">
                                      Instance connection
                                    </summary>
                                    <div className="grid gap-3 pb-3">
                                      <label className="grid gap-2 text-sm">
                                        GitLab URL
                                        <Input
                                          required
                                          type="url"
                                          value={gitlabURL}
                                          placeholder="https://gitlab.example.com"
                                          disabled={busy}
                                          onChange={(event) => setGitlabURL(event.target.value)}
                                        />
                                      </label>
                                      <label className="grid gap-2 text-sm">
                                        Trust policy
                                        <Input
                                          required
                                          value={trustPolicy}
                                          disabled={busy}
                                          placeholder="Your operator's policy name"
                                          onChange={(event) => setTrustPolicy(event.target.value)}
                                        />
                                        <span className="field-help">
                                          Ask your operator for the policy allowing this instance.
                                        </span>
                                      </label>
                                    </div>
                                  </details>
                                )}
                              </>
                            )}
                            {provider === 'bitbucket' && (
                              <>
                                <label className="grid gap-2 text-sm font-medium">
                                  Workspace UUID
                                  <Input
                                    required
                                    maxLength={38}
                                    value={bitbucketWorkspace}
                                    placeholder="{workspace-uuid}"
                                    disabled={busy}
                                    onChange={(event) => setBitbucketWorkspace(event.target.value)}
                                  />
                                </label>
                                <label className="grid gap-2 text-sm font-medium">
                                  Repository UUID
                                  <Input
                                    required
                                    maxLength={38}
                                    value={bitbucketRepository}
                                    placeholder="{repository-uuid}"
                                    disabled={busy}
                                    onChange={(event) => setBitbucketRepository(event.target.value)}
                                  />
                                  <span className="field-help">
                                    Copy UUIDs from Bitbucket's runner setup. This runner stays
                                    dedicated to the selected repository.
                                  </span>
                                </label>
                              </>
                            )}
                          </div>
                          <div className="grid content-start gap-3">
                            {lifecycle === 'dedicated' && (
                              <p className="text-sm">
                                This runner stays assigned to one repository and reuses its sandbox
                                between pipeline steps.
                              </p>
                            )}
                            <div className="rounded-lg bg-[var(--surface-2)] p-4">
                              <div className="flex items-center gap-2 text-sm font-medium">
                                Connect securely
                                <HeadingHelp title="Runner credentials">
                                  Hakopod stores credentials as application secrets. Do not place
                                  credentials in workflow labels or repository names.
                                </HeadingHelp>
                              </div>
                              <p className="mt-2 text-sm muted-text">
                                You will save the {providerName} credential at review.
                              </p>
                            </div>
                            <details className="border-b border-[var(--hairline)]">
                              <summary className="min-h-11 cursor-pointer py-3 text-sm font-medium">
                                What access is needed?
                              </summary>
                              <div className="pb-3">
                                {provider === 'github' ? (
                                  <ManagedActionsTokenHelp
                                    organization={runnerScope === 'organization'}
                                    repository={runnerScope === 'repository'}
                                    jobs={!jobsCredential || jobsCredential === credential}
                                  />
                                ) : (
                                  <p className="text-sm">
                                    {provider === 'gitlab'
                                      ? 'Use a scoped GitLab token allowed to create and remove runners for the selected project or group. A separate read-only token can supply job status and logs.'
                                      : 'Use a Bitbucket OAuth access token or JSON with email and api_token. Hakopod generates runner registration credentials separately; you do not enter them here.'}
                                  </p>
                                )}
                              </div>
                            </details>
                            {jobsCredential && jobsCredential !== credential && (
                              <p className="field-help">
                                Job details and logs use <code>{jobsCredential}</code>
                                {provider === 'github'
                                  ? ' with repository Actions: Read-only access.'
                                  : '.'}
                              </p>
                            )}
                          </div>
                        </div>
                        <details className="border-t border-[var(--hairline)]">
                          <summary className="min-h-11 cursor-pointer py-3 text-sm">
                            Advanced connection settings
                          </summary>
                          <div className="grid gap-3 pb-3 sm:grid-cols-2">
                            <label className="grid gap-2 text-sm">
                              Service name
                              <Input
                                required
                                disabled={!!original || busy}
                                value={runnerName}
                                pattern={'[a-z][a-z0-9\\-]{0,39}'}
                                maxLength={40}
                                onChange={(event) => setRunnerName(event.target.value)}
                              />
                            </label>
                            <label className="grid gap-2 text-sm">
                              Runner credential
                              <Input
                                required
                                value={credential}
                                maxLength={40}
                                disabled={busy}
                                onChange={(event) => setCredential(event.target.value)}
                              />
                              <span className="field-help">
                                Application secret name; save the secret itself at review.
                              </span>
                            </label>
                            <label className="grid gap-2 text-sm">
                              Job logs credential (optional)
                              <Input
                                id="runner-jobs-credential"
                                value={jobsCredential}
                                maxLength={40}
                                pattern={'[a-z]([a-z0-9\\-]{0,38}[a-z0-9])?'}
                                placeholder="Use runner credential"
                                disabled={busy}
                                aria-describedby="runner-jobs-credential-help"
                                onChange={(event) => setJobsCredential(event.target.value)}
                              />
                              <span id="runner-jobs-credential-help" className="field-help">
                                Leave blank to use the runner credential.
                              </span>
                            </label>
                            {provider === 'github' && runnerScope === 'organization' && (
                              <label className="grid gap-2 text-sm">
                                Runner group ID (optional)
                                <Input
                                  type="number"
                                  min={1}
                                  max={9007199254740991}
                                  step={1}
                                  value={runnerGroup}
                                  placeholder="GitHub default group"
                                  disabled={busy}
                                  onChange={(event) => setRunnerGroup(event.target.value)}
                                />
                              </label>
                            )}
                          </div>
                        </details>
                      </RunnerSection>
                    )}
                    {step === 1 && (
                      <RunnerSection title="Choose your compute">
                        <RunnerChoices
                          compact
                          label="Architecture"
                          value={architecture}
                          onChange={setArchitecture}
                          disabled={busy}
                          options={[
                            ...(provider === 'github' ? [{ value: '', label: 'Automatic' }] : []),
                            { value: 'amd64', label: 'AMD64' },
                            { value: 'arm64', label: 'ARM64' },
                          ]}
                        />
                        <RunnerChoices
                          label="Resources per job"
                          value={resourcePreset}
                          onChange={resourceChoice}
                          disabled={busy}
                          options={[
                            ...(original
                              ? [
                                  {
                                    value: 'saved',
                                    label: 'Current settings',
                                    detail: 'Keep current reservations and limits',
                                  },
                                ]
                              : []),
                            {
                              value: 'defaults',
                              label: 'Installation defaults',
                              detail: presetSummary(installationResources),
                            },
                            {
                              value: 'balanced',
                              label: 'Balanced',
                              detail: presetSummary(runnerResourcePresets.balanced),
                            },
                            {
                              value: 'builds',
                              label: 'Larger builds',
                              detail: presetSummary(runnerResourcePresets.builds),
                            },
                            {
                              value: 'custom',
                              label: 'Custom',
                              detail: 'Set reservations and limits',
                            },
                          ]}
                        />
                        {minimum && (
                          <p className="text-xs muted-text">
                            {providerName} requires at least {minimum.cpu_request} CPU and{' '}
                            {minimum.memory_request} memory reserved per runner, with limits of{' '}
                            {minimum.cpu_limit} CPU and {minimum.memory_limit} memory or more.
                            Minimum workspace: {workspaceMinimum} GiB.
                          </p>
                        )}
                        {!resourcesValid && (
                          <Note>
                            Current values are preserved. Choose a preset or increase custom
                            resources to meet these minimums.
                          </Note>
                        )}
                        {provider !== 'github' && bindingReason && (
                          <p className="text-sm muted-text" role="status">
                            {bindingReason}
                          </p>
                        )}
                        <div className="grid min-w-0 content-start gap-4 lg:grid-cols-2">
                          <div className="grid content-start gap-3">
                            <RunnerCapacity
                              value={replicas}
                              maximum={Math.max(
                                Number(original?.replicas || 0),
                                dashboardEdition.cloud ? 3 : 10,
                              )}
                              disabled={busy}
                              onChange={setReplicas}
                            />
                            <p className="text-sm muted-text" role="status">
                              {reservationLabel}
                            </p>
                          </div>
                          <RunnerPlacement
                            value={nodeName}
                            architecture={architecture}
                            nodes={nodes.data?.items}
                            loading={nodes.isPending}
                            failed={!!nodes.error}
                            refreshing={nodes.isFetching}
                            disabled={busy}
                            onChange={setNodeName}
                            onRetry={() => void nodes.refetch()}
                          />
                        </div>
                        {resourcePreset === 'custom' && (
                          <RunnerResourceFields
                            effective={effectiveResources}
                            minimum={minimum}
                            expanded
                            disabled={busy || !effective}
                            onChange={(key, value) =>
                              setResources((current) => ({ ...current, [key]: value }))
                            }
                          />
                        )}
                      </RunnerSection>
                    )}
                    {step === 2 && (
                      <RunnerSection title="Prepare your workflow">
                        <div className="grid min-w-0 gap-4 lg:grid-cols-2">
                          <div className="grid content-start gap-4">
                            <RunnerLabels
                              value={labels}
                              onChange={setLabels}
                              disabled={busy}
                              label={
                                provider === 'gitlab'
                                  ? 'Pipeline tags'
                                  : provider === 'bitbucket'
                                    ? 'Custom labels'
                                    : 'Workflow labels'
                              }
                              provider={provider}
                            />
                            <RunnerChoices
                              compact
                              label={
                                lifecycle === 'dedicated'
                                  ? 'Runner workspace'
                                  : 'Temporary workspace'
                              }
                              value={workspaceSize}
                              onChange={setWorkspaceSize}
                              disabled={busy}
                              options={[
                                ...new Set([
                                  workspaceSize,
                                  String(workspaceMinimum),
                                  '2',
                                  '8',
                                  '16',
                                ]),
                              ]
                                .sort((a, b) => Number(a) - Number(b))
                                .map((value) => ({
                                  value,
                                  label: `${value} GiB`,
                                  disabled: Number(value) < workspaceMinimum,
                                }))}
                            />
                            <span className="text-xs muted-text">
                              {lifecycle === 'dedicated'
                                ? 'Source, tools and Docker images share the runner disk. The sandbox is reused for this repository; local files are not a durable cache.'
                                : 'Source, tools and Docker images share this disk. It is deleted after each job.'}
                            </span>
                          </div>
                          <RunnerWorkflowGuide
                            labels={workflowLabels}
                            provider={provider}
                            architecture={architecture}
                          />
                        </div>
                        {provider === 'gitlab' && (
                          <div className="grid gap-3">
                            <RunnerChoices
                              label="Dependency cache"
                              value={cacheEnabled ? 'shared' : 'none'}
                              onChange={(value) => setCacheEnabled(value === 'shared')}
                              disabled={busy}
                              options={[
                                {
                                  value: 'none',
                                  label: 'No shared cache',
                                  detail: 'Do not configure shared dependency storage',
                                },
                                {
                                  value: 'shared',
                                  label: 'Shared cache',
                                  detail:
                                    providerCapability?.cache.backend ||
                                    'Installation-approved storage',
                                  disabled: !cacheAvailable,
                                },
                              ]}
                            />
                            {!cacheAvailable && (
                              <p className="text-xs muted-text">
                                {binding
                                  ? 'Shared caching is not approved for this pool.'
                                  : 'Choose an approved pool to see its cache support.'}{' '}
                                {cacheEnabled &&
                                  'Your saved cache choice is preserved; disable it or select a matching approval before deploying.'}
                              </p>
                            )}
                            {providerCapability?.cache.reason && (
                              <p className="text-xs muted-text">
                                {providerCapability.cache.reason}
                              </p>
                            )}
                            {cacheEnabled && (
                              <label className="grid gap-2 text-sm">
                                Cache credential
                                <Input
                                  required
                                  value={cacheCredential}
                                  maxLength={40}
                                  disabled={busy}
                                  onChange={(event) => setCacheCredential(event.target.value)}
                                />
                                <span className="field-help">
                                  Application secret for the storage credentials. Save it securely
                                  at review; the runner manager keeps the secret.
                                </span>
                              </label>
                            )}
                          </div>
                        )}
                        {lifecycle !== 'dedicated' && (
                          <details className="border-t border-[var(--hairline)]">
                            <summary className="min-h-11 cursor-pointer py-3 text-sm">
                              Job lifetime
                            </summary>
                            <div className="pb-3">
                              <RunnerChoices
                                compact
                                label="Maximum runner lifetime"
                                value={timeout}
                                onChange={setTimeout}
                                disabled={busy}
                                options={[...new Set([timeout, '30', '60', '120', '360'])]
                                  .sort((a, b) => Number(a) - Number(b))
                                  .map((value) => ({ value, label: `${value} minutes` }))}
                              />
                              <p className="mt-2 text-xs muted-text">
                                Includes startup, waiting for a job and execution.
                              </p>
                            </div>
                          </details>
                        )}
                        {!ready && (
                          <Note>
                            {bindingReason ||
                              (!resourcesValid
                                ? 'Increase the resource reservations and limits to meet the provider minimums.'
                                : Number(workspaceSize) < workspaceMinimum
                                  ? `Choose at least ${workspaceMinimum} GiB of workspace.`
                                  : cacheEnabled && provider === 'gitlab' && !cacheAvailable
                                    ? 'Shared caching is not approved for this pool.'
                                    : providerCapability?.reason ||
                                      'This pool cannot deploy until its provider and runner environment are ready.')}
                          </Note>
                        )}
                      </RunnerSection>
                    )}
                  </div>
                  {error && <RequestError error={error} />}
                  <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--hairline)] pt-3">
                    <Button
                      type="button"
                      disabled={busy}
                      onClick={() => (step ? move(step - 1) : onClose())}
                    >
                      {step ? 'Back' : 'Cancel'}
                    </Button>
                    <Button
                      type="submit"
                      variant="primary"
                      disabled={busy || !canDraft || (step === 2 && !ready)}
                    >
                      {busy
                        ? 'Checking capacity…'
                        : step === 0
                          ? 'Choose compute'
                          : step === 1
                            ? 'Set up workflow'
                            : 'Review runner pool'}
                    </Button>
                  </div>
                </form>
              )}
            </>
          )}
        </div>
      )}
      {error && plan && <RequestError error={error} />}
    </FormPage>
  )
}

function RunnerSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid min-w-0 gap-4" aria-label={title}>
      <h2 className="text-base font-semibold">{title}</h2>
      {children}
    </section>
  )
}
