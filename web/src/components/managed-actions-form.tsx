import { useEffect, useRef, useState, type FormEvent } from 'react'
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
import { FormPage, FormSection } from './form-page'
import { Empty, ErrorState, Loading, Note, RequestError } from './shared'
import { DeploymentSecrets } from './deployment-secrets'
import { ManagedActionsTokenHelp } from './managed-actions-token-help'
import { DiffTable } from './deploy-dialog'
import { serviceResources } from '../lib/service-resources'
import { runnerReservationLabel, type RunnerResources } from '../lib/runner-resources'
import {
  RunnerFact,
  RunnerLabelChips,
  RunnerPlacement,
  RunnerResourceFields,
  RunnerSteps,
  RunnerWorkflowGuide,
  runnerResourcePresets,
} from './managed-actions-setup'

export function ManagedActionsForm({
  application: currentApplication,
  serviceName,
  onClose,
}: {
  application?: Application
  serviceName?: string
  onClose: () => void
}) {
  // A background refetch must not silently advance the revision behind a draft.
  const [application] = useState(currentApplication)
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const original = serviceName ? application?.spec.services[serviceName] : undefined
  const project = application?.project || scope.project
  const environment = application?.environment || scope.environment
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
  const [labels, setLabels] = useState(original?.actions?.labels.join(', ') || 'hakopod')
  const [replicas, setReplicas] = useState(String(original?.replicas ?? 1))
  const [architecture, setArchitecture] = useState(original?.architecture || '')
  const [nodeName, setNodeName] = useState(original?.node_name || '')
  const [step, setStep] = useState(0)
  const [visited, setVisited] = useState(0)
  const [resourcePreset, setResourcePreset] = useState(original ? 'saved' : 'defaults')
  const [timeout, setTimeout] = useState(String(original?.actions?.timeout_minutes || 60))
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
  const size = original?.size || (dashboardEdition.cloud ? 'large' : 'compute')
  const effective = serviceResources(
    { image: '', size, resources },
    capabilities.data?.resource_profiles,
  )
  const effectiveResources: RunnerResources = {
    cpu_request: resources.cpu_request ?? effective?.CPURequest,
    cpu_limit: resources.cpu_limit ?? effective?.CPULimit,
    memory_request: resources.memory_request ?? effective?.MemoryRequest,
    memory_limit: resources.memory_limit ?? effective?.MemoryLimit,
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
  const ready =
    canWrite && !!capabilities.data?.licensed && !!capabilities.data?.runtime_ready && !!effective
  const reviewFocus = useRef<HTMLDivElement>(null)
  const nameFocus = useRef<HTMLInputElement>(null)
  const stepFocus = useRef<HTMLDivElement>(null)
  const form = useRef<HTMLFormElement>(null)
  useEffect(() => {
    if (plan) reviewFocus.current?.focus()
    else stepFocus.current?.focus()
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
    if (step < 2) move(step + 1)
    else await review(event)
  }
  async function review(event: FormEvent) {
    event.preventDefault()
    if (busy || !ready) return
    setBusy(true)
    setError('')
    try {
      if (workflowLabels.length === 0) throw new Error('Add at least one workflow label.')
      if (application?.spec.services[runnerName] && !original)
        throw new Error('Choose a service name that is not already used.')
      const service: Service = {
        ...original,
        image: capabilities.data!.runner_image,
        public: false,
        size,
        resources: Object.keys(resources).length ? resources : undefined,
        replicas: Number(replicas),
        architecture: architecture ? (architecture as 'amd64' | 'arm64') : undefined,
        node_name: nodeName || undefined,
        actions: {
          ...(runnerScope === 'organization'
            ? { organization, runner_group_id: runnerGroup ? Number(runnerGroup) : undefined }
            : { repository }),
          credential,
          labels: workflowLabels,
          timeout_minutes: Number(timeout),
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
        description="Run GitHub Actions in isolated workspaces."
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
        description="Configure an existing Managed Actions service."
        breadcrumbs={[]}
      >
        <Empty
          title="Runner pool not found"
          description="Return to the application and choose an existing runner pool."
        />
      </FormPage>
    )
  return (
    <FormPage
      title={original ? 'Configure runner pool' : 'Create runner pool'}
      description="Connect GitHub, choose compute and review your pool. Each runner handles one job in an isolated workspace."
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
              Managed Actions requires{' '}
              {dashboardEdition.cloud
                ? 'Cloud Team access. Ask your workspace owner to enable it.'
                : 'Pro access.'}{' '}
              {!dashboardEdition.cloud && scope.identity.admin && (
                <Link to="/settings" search={{ tab: 'license' }}>
                  Open license settings
                </Link>
              )}
            </Note>
          )}
          {!capabilities.data.runtime_ready && (
            <Note>
              {capabilities.data.message || 'The Managed Actions sandbox is not ready.'}{' '}
              {!dashboardEdition.cloud && scope.identity.admin ? (
                <p className="mt-2 text-sm">
                  From the verified installer kit, run{' '}
                  <code className="break-all">
                    sudo python3 ./installer/modules.py managed-actions
                  </code>{' '}
                  during a maintenance window. This restarts K3s.
                </p>
              ) : (
                <p className="mt-2 text-sm">
                  Ask your installation operator to prepare a runner node.
                </p>
              )}
            </Note>
          )}
          {currentApplication && currentApplication.revision !== application?.revision && (
            <Note>
              The application changed while you were editing. Your draft is preserved. Review will
              check its original revision before allowing deployment.
            </Note>
          )}
          {wrongApplication ? (
            <Note>
              Runner pools need a dedicated application. Create a separate Managed Actions
              application for these jobs.
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
                <span>{name || 'New pool'}</span>
                <span>
                  {replicas} {replicas === '1' ? 'job at a time' : 'jobs at a time'}
                </span>
              </div>
              {plan ? (
                <div
                  ref={reviewFocus}
                  tabIndex={-1}
                  aria-label="Review runner pool"
                  className="grid min-w-0 gap-4 focus-visible:outline-2"
                >
                  <FormSection title="Review runner pool">
                    <div className="grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-4">
                      <RunnerFact
                        label={
                          runnerScope === 'organization'
                            ? 'GitHub organization'
                            : 'GitHub repository'
                        }
                      >
                        {runnerScope === 'organization' ? organization : repository}
                      </RunnerFact>
                      <RunnerFact label="Runner compute">
                        {replicas} × {architecture || 'Automatic architecture'}
                      </RunnerFact>
                      <RunnerFact label="Placement">
                        {nodeName || 'Any eligible runner node'}
                      </RunnerFact>
                      <RunnerFact label="Workspace & lifetime">
                        {workspaceSize} GiB · {timeout} minutes
                      </RunnerFact>
                    </div>
                    <dl className="grid min-w-0 gap-3 text-sm sm:grid-cols-2">
                      <div>
                        <dt className="muted-text">Application / service</dt>
                        <dd className="wrap-anywhere">
                          {name} / {runnerName}
                        </dd>
                      </div>
                      <div>
                        <dt className="muted-text">Credential reference</dt>
                        <dd className="wrap-anywhere">
                          <code>{credential}</code>
                        </dd>
                      </div>
                      <div>
                        <dt className="muted-text">Reserved per runner</dt>
                        <dd>
                          {effectiveResources.cpu_request} CPU · {effectiveResources.memory_request}{' '}
                          memory
                        </dd>
                      </div>
                      <div>
                        <dt className="muted-text">Limit per runner</dt>
                        <dd>
                          {effectiveResources.cpu_limit} CPU · {effectiveResources.memory_limit}{' '}
                          memory
                        </dd>
                      </div>
                      {runnerScope === 'organization' && (
                        <div>
                          <dt className="muted-text">Runner group</dt>
                          <dd>{runnerGroup || 'GitHub default group'}</dd>
                        </div>
                      )}
                    </dl>
                    <RunnerLabelChips labels={workflowLabels} />
                    <p className="text-sm" role="status">
                      {reservationLabel}
                    </p>
                    {nodeName && (
                      <p className="field-help">
                        The pool waits if {nodeName} is unavailable. It will not move to a different
                        node.
                      </p>
                    )}
                    <Note>
                      Updates drain busy runners. Each job gets a fresh workspace; workflow caches
                      are restored separately. Removing the service cancels running jobs.
                    </Note>
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
                        Back to jobs
                      </Button>
                      <Button
                        variant="primary"
                        disabled={busy || !!plan.missing_secrets?.length || !ready}
                        onClick={() => void deploy()}
                      >
                        {busy ? 'Submitting…' : 'Deploy runner pool'}
                      </Button>
                    </div>
                  </FormSection>
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
                    aria-label={`${['GitHub connection', 'Runner compute', 'Job settings'][step]} step`}
                    className="min-w-0 focus-visible:outline-2"
                  >
                    {step === 0 && (
                      <FormSection title="Connect your GitHub jobs">
                        <div className="grid min-w-0 gap-4 lg:grid-cols-2">
                          <div className="grid content-start gap-3">
                            <label className="grid gap-2 text-sm">
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
                              <span className="field-help">
                                The application that holds this runner pool.
                              </span>
                            </label>
                            <div className="grid gap-2 text-sm">
                              <span>Share runners with</span>
                              <SelectField
                                label="Runner scope"
                                value={runnerScope}
                                onValueChange={setRunnerScope}
                                disabled={busy}
                                options={[
                                  { value: 'organization', label: 'An organization' },
                                  { value: 'repository', label: 'One repository' },
                                ]}
                              />
                            </div>
                            {runnerScope === 'organization' ? (
                              <label className="grid gap-2 text-sm">
                                GitHub organization
                                <Input
                                  required
                                  value={organization}
                                  placeholder="your-team"
                                  maxLength={39}
                                  disabled={busy}
                                  onChange={(event) => setOrganization(event.target.value)}
                                  aria-describedby="actions-scope-help"
                                />
                                <span id="actions-scope-help" className="field-help">
                                  Repositories allowed by your GitHub runner group can use this
                                  pool.
                                </span>
                              </label>
                            ) : (
                              <label className="grid gap-2 text-sm">
                                GitHub repository
                                <Input
                                  required
                                  value={repository}
                                  placeholder="your-team/your-repository"
                                  maxLength={201}
                                  disabled={busy}
                                  onChange={(event) => setRepository(event.target.value)}
                                />
                              </label>
                            )}
                          </div>
                          <div className="grid content-start gap-3">
                            <RunnerFact label="GitHub access">
                              Hakopod keeps your token private. Runners receive a single-job
                              registration.
                            </RunnerFact>
                            <ManagedActionsTokenHelp
                              organization={runnerScope === 'organization'}
                              repository={runnerScope === 'repository'}
                            />
                            <p className="field-help">
                              Save the token securely during the review step.
                            </p>
                          </div>
                        </div>
                        <details className="rounded border border-[var(--hairline)]">
                          <summary className="min-h-11 cursor-pointer px-3 py-3 text-sm font-medium">
                            Service, runner group and credential name
                          </summary>
                          <div className="grid gap-3 px-3 pb-3 sm:grid-cols-2">
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
                              Application secret name
                              <Input
                                required
                                value={credential}
                                maxLength={40}
                                disabled={busy}
                                onChange={(event) => setCredential(event.target.value)}
                              />
                              <span className="field-help">
                                A reference to your token, not the token itself.
                              </span>
                            </label>
                            {runnerScope === 'organization' && (
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
                                <span className="field-help">
                                  Leave blank for the default group. Find another group's ID in its
                                  GitHub settings URL.
                                </span>
                              </label>
                            )}
                          </div>
                        </details>
                      </FormSection>
                    )}
                    {step === 1 && (
                      <FormSection title="Choose where jobs run">
                        <div className="grid min-w-0 gap-4 lg:grid-cols-2">
                          <div className="grid content-start gap-3">
                            <div className="grid gap-2 text-sm">
                              <span>Concurrent jobs</span>
                              <SelectField
                                label="Concurrent jobs"
                                value={replicas}
                                onValueChange={setReplicas}
                                disabled={busy}
                                options={[
                                  ...new Set([
                                    Number(replicas),
                                    ...Array.from(
                                      { length: dashboardEdition.cloud ? 3 : 10 },
                                      (_, index) => index + 1,
                                    ),
                                  ]),
                                ]
                                  .sort((a, b) => a - b)
                                  .map((value) => ({
                                    value: String(value),
                                    label: `${value} ${value === 1 ? 'job' : 'jobs'} at a time`,
                                  }))}
                              />
                            </div>
                            <div className="grid gap-2 text-sm">
                              <span>Architecture</span>
                              <SelectField
                                label="Architecture"
                                value={architecture}
                                onValueChange={setArchitecture}
                                disabled={busy}
                                options={[
                                  { value: '', label: 'Automatic' },
                                  { value: 'amd64', label: 'Linux AMD64' },
                                  { value: 'arm64', label: 'Linux ARM64' },
                                ]}
                              />
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
                          <div className="grid content-start gap-3">
                            <div className="grid gap-2 text-sm">
                              <span>Resources per runner</span>
                              <SelectField
                                label="Resource profile"
                                value={resourcePreset}
                                disabled={busy}
                                onValueChange={(value) => {
                                  setResourcePreset(value)
                                  if (value === 'saved') setResources(original?.resources || {})
                                  else if (value === 'defaults')
                                    setResources(
                                      !original && dashboardEdition.cloud
                                        ? {
                                            cpu_request: '500m',
                                            cpu_limit: '2500m',
                                            memory_request: '1Gi',
                                            memory_limit: '4Gi',
                                          }
                                        : {},
                                    )
                                  else if (runnerResourcePresets[value])
                                    setResources({ ...runnerResourcePresets[value] })
                                }}
                                options={[
                                  ...(original
                                    ? [{ value: 'saved', label: 'Keep current settings' }]
                                    : []),
                                  { value: 'defaults', label: 'Installation defaults' },
                                  { value: 'balanced', label: 'Balanced · up to 2 CPU / 4 GiB' },
                                  { value: 'builds', label: 'Larger builds · up to 4 CPU / 8 GiB' },
                                  { value: 'custom', label: 'Custom settings' },
                                ]}
                              />
                            </div>
                            <div className="grid grid-cols-2 gap-3">
                              <RunnerFact label="Reserved per runner">
                                {effectiveResources.cpu_request} CPU
                                <br />
                                {effectiveResources.memory_request} memory
                              </RunnerFact>
                              <RunnerFact label="Limit per runner">
                                {effectiveResources.cpu_limit} CPU
                                <br />
                                {effectiveResources.memory_limit} memory
                              </RunnerFact>
                            </div>
                            <p className="field-help" role="status">
                              {reservationLabel}
                            </p>
                            <RunnerResourceFields
                              effective={effectiveResources}
                              expanded={resourcePreset === 'custom'}
                              disabled={busy || !effective}
                              onChange={(key, value) => {
                                setResourcePreset('custom')
                                setResources((current) => ({ ...current, [key]: value }))
                              }}
                            />
                          </div>
                        </div>
                        <p className="field-help">
                          More CPU reservation reduces competition with other jobs. Review checks
                          available capacity on your selected nodes and any plan allowances.
                        </p>
                      </FormSection>
                    )}
                    {step === 2 && (
                      <FormSection title="Set up your workflows">
                        <div className="grid min-w-0 gap-4 lg:grid-cols-2">
                          <div className="grid content-start gap-3">
                            <label className="grid gap-2 text-sm">
                              Workflow labels
                              <Input
                                required
                                value={labels}
                                maxLength={520}
                                disabled={busy}
                                onChange={(event) => setLabels(event.target.value)}
                              />
                              <span className="field-help">
                                Separate labels with commas. Use the exact labels shown in your
                                workflow.
                              </span>
                            </label>
                            <div className="grid gap-2 text-sm">
                              <span>Maximum runner lifetime</span>
                              <SelectField
                                label="Maximum lifetime"
                                value={timeout}
                                onValueChange={setTimeout}
                                disabled={busy}
                                options={[...new Set([Number(timeout), 30, 60, 120, 360])]
                                  .sort((a, b) => a - b)
                                  .map((value) => ({
                                    value: String(value),
                                    label: `${value} minutes`,
                                  }))}
                              />
                              <span className="field-help">
                                Includes startup, waiting for a job and execution.
                              </span>
                            </div>
                            <label className="grid gap-2 text-sm">
                              Temporary workspace (GiB)
                              <Input
                                type="number"
                                min={2}
                                max={16}
                                step={1}
                                required
                                value={workspaceSize}
                                disabled={busy}
                                onChange={(event) => setWorkspaceSize(event.target.value)}
                              />
                              <span className="field-help">
                                2–16 GiB per runner, shared by source, tools and Docker images.
                                Start with 8 GiB for container builds; this disk is reserved and
                                deleted after each job.
                              </span>
                            </label>
                          </div>
                          <RunnerWorkflowGuide labels={workflowLabels} />
                        </div>
                      </FormSection>
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
                    <Button type="submit" variant="primary" disabled={busy || !ready}>
                      {busy
                        ? 'Checking capacity…'
                        : step === 0
                          ? 'Continue to compute'
                          : step === 1
                            ? 'Continue to jobs'
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
