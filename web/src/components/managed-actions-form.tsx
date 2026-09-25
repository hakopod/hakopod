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
import { DiffTable } from './deploy-dialog'

export function ManagedActionsForm({
  application,
  serviceName,
  onClose,
}: {
  application?: Application
  serviceName?: string
  onClose: () => void
}) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const original = serviceName ? application?.spec.services[serviceName] : undefined
  const project = application?.project || scope.project
  const environment = application?.environment || scope.environment
  const [name, setName] = useState(application?.name || '')
  const [runnerName, setRunnerName] = useState(serviceName || 'runner')
  const [repository, setRepository] = useState(original?.actions?.repository || '')
  const [credential, setCredential] = useState(
    original?.actions?.credential || 'github-runner-token',
  )
  const [labels, setLabels] = useState(original?.actions?.labels.join(', ') || 'hakopod')
  const [replicas, setReplicas] = useState(String(original?.replicas ?? 1))
  const [architecture, setArchitecture] = useState(original?.architecture || '')
  const [timeout, setTimeout] = useState(String(original?.actions?.timeout_minutes || 60))
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
  const canWrite = canAccess(scope.identity, project, 'deployments:write')
  const ready = canWrite && !!capabilities.data?.licensed && !!capabilities.data?.runtime_ready
  const reviewFocus = useRef<HTMLDivElement>(null)
  const nameFocus = useRef<HTMLInputElement>(null)
  const wasReview = useRef(false)
  useEffect(() => {
    if (plan) reviewFocus.current?.focus()
    else if (wasReview.current) nameFocus.current?.focus()
    wasReview.current = !!plan
  }, [!!plan])
  const wrongApplication =
    application && Object.values(application.spec.services).some((service) => !service.actions)
  async function review(event: FormEvent) {
    event.preventDefault()
    if (busy || !ready) return
    setBusy(true)
    setError('')
    try {
      if (application?.spec.services[runnerName] && !original)
        throw new Error('Choose a service name that is not already used.')
      const service: Service = {
        ...original,
        image: capabilities.data!.runner_image,
        public: false,
        size: original?.size || (dashboardEdition.cloud ? 'large' : 'compute'),
        resources:
          original?.resources ||
          (dashboardEdition.cloud
            ? {
                cpu_request: '500m',
                cpu_limit: '2500m',
                memory_request: '1Gi',
                memory_limit: '4Gi',
              }
            : undefined),
        replicas: Number(replicas),
        architecture: architecture ? (architecture as 'amd64' | 'arm64') : undefined,
        actions: {
          repository,
          credential,
          labels: labels.split(',').map((label) => label.trim()),
          timeout_minutes: Number(timeout),
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
        title="Managed Actions"
        description="Manage repository-scoped GitHub runner pools."
        breadcrumbs={[{ label: 'Catalog', to: '/templates' }]}
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
        title="Managed Actions"
        description="Manage repository-scoped GitHub runner pools."
        breadcrumbs={[{ label: 'Catalog', to: '/templates' }]}
      >
        <Empty
          title="Runner pool not found"
          description="Return to the application and select an existing Managed Actions service."
        />
      </FormPage>
    )
  return (
    <FormPage
      title={original ? 'Configure runner pool' : 'Managed Actions'}
      description="Run GitHub Actions in isolated, single-job workspaces. Each replica is one concurrent job slot."
      breadcrumbs={[{ label: 'Catalog', to: '/templates' }, { label: 'Managed Actions' }]}
    >
      {capabilities.isPending ? (
        <Loading />
      ) : capabilities.error ? (
        <ErrorState error={capabilities.error} retry={() => void capabilities.refetch()} />
      ) : (
        <>
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
              {capabilities.data.message}
              {!dashboardEdition.cloud && scope.identity.admin ? (
                <>
                  <p className="mt-2">
                    From this release’s verified installer kit, enable the sandbox during a
                    maintenance window. This restarts K3s.
                  </p>
                  <p className="break-all mt-2">
                    <code>sudo python3 ./installer/modules.py managed-actions</code>
                  </p>
                </>
              ) : (
                <p className="mt-2">
                  Ask your installation operator to enable the Managed Actions sandbox.
                </p>
              )}
            </Note>
          )}
          {wrongApplication ? (
            <Note>
              Runner pools need a dedicated application. Keep your existing services here and create
              a separate Managed Actions application.
            </Note>
          ) : plan ? (
            <>
              <div className="min-w-0 wrap-anywhere" ref={reviewFocus} tabIndex={-1} aria-label="Review runner pool">
                <FormSection title="Review runner pool">
                  <dl className="grid grid-cols-1 gap-2 text-sm min-w-0 wrap-anywhere">
                    <div>
                      Repository: <strong>{repository}</strong>
                    </div>
                    <div>
                      Application:{' '}
                      <strong>
                        {project} / {environment} / {name}
                      </strong>
                    </div>
                    <div>
                      Concurrent jobs: <strong>{replicas}</strong>
                    </div>
                    <div>
                      Labels: <strong>{labels}</strong>
                    </div>
                    <div>
                      Architecture: <strong>{architecture || 'Automatic'}</strong>
                    </div>
                    <div>
                      Resource profile:{' '}
                      <strong>
                        {original?.size ||
                          (dashboardEdition.cloud
                            ? 'large with runner resource limits'
                            : 'compute')}
                      </strong>{' '}
                      per job slot; multiplied by {replicas} concurrent slots. Sandbox overhead is
                      included.
                    </div>
                    <div>
                      Credential reference: <code>{credential}</code>
                    </div>
                  </dl>
                  <Note>
                    Each slot includes the runner, Docker and temporary storage in its resource
                    budget. Jobs have a {timeout}-minute maximum lifetime. Scaling and updates drain
                    busy runners. Removing the service cancels running work.
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
                  <div className="flex flex-wrap gap-2">
                    <Button disabled={busy} onClick={() => setPlan(null)}>
                      Back to configuration
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
            </>
          ) : (
            <form onSubmit={(event) => void review(event)} className="grid gap-4">
              <FormSection title="Application">
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="grid gap-2">
                    Application name
                    <Input
                      ref={nameFocus}
                      required
                      readOnly={!!application}
                      disabled={busy}
                      value={name}
                      pattern="[a-z][a-z0-9-]{0,39}"
                      maxLength={40}
                      onChange={(event) => setName(event.target.value)}
                    />
                  </label>
                  <label className="grid gap-2">
                    Service name
                    <Input
                      required
                      disabled={!!original || busy}
                      value={runnerName}
                      pattern="[a-z][a-z0-9-]{0,39}"
                      maxLength={40}
                      onChange={(event) => setRunnerName(event.target.value)}
                    />
                  </label>
                </div>
              </FormSection>
              <FormSection title="GitHub repository">
                <label className="grid gap-2">
                  Repository
                  <Input
                    required
                    value={repository}
                    placeholder="your-team/your-repository"
                    maxLength={201}
                    disabled={busy}
                    onChange={(event) => setRepository(event.target.value)}
                  />
                </label>
                <label className="grid gap-2">
                  Application secret name
                  <Input
                    required
                    value={credential}
                    maxLength={40}
                    disabled={busy}
                    onChange={(event) => setCredential(event.target.value)}
                  />
                </label>
                <p className="text-sm muted-text">
                  Save a fine-grained GitHub token with Administration: read and write for this
                  repository during review. The token stays in the control plane; jobs receive only
                  a single-job registration.
                </p>
              </FormSection>
              <FormSection title="Runner pool">
                <label className="grid gap-2">
                  Labels
                  <Input
                    required
                    value={labels}
                    maxLength={520}
                    disabled={busy}
                    onChange={(event) => setLabels(event.target.value)}
                  />
                  <span className="text-sm muted-text">
                    Comma-separated labels for your workflow's runs-on setting.
                  </span>
                </label>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="grid gap-2">
                    <span>Concurrent jobs</span>
                    <SelectField
                      label="Concurrent jobs"
                      value={replicas}
                      onValueChange={setReplicas}
                      disabled={busy}
                      options={Array.from(
                        { length: dashboardEdition.cloud ? 3 : 10 },
                        (_, index) => ({
                          value: String(index + 1),
                          label: String(index + 1),
                        }),
                      )}
                    />
                  </div>
                  <div className="grid gap-2">
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
                  <div className="grid gap-2">
                    <span>Maximum lifetime</span>
                    <SelectField
                      label="Maximum lifetime"
                      value={timeout}
                      onValueChange={setTimeout}
                      disabled={busy}
                      options={[...new Set([Number(timeout), 30, 60, 120, 360])]
                        .sort((a, b) => a - b)
                        .map((value) => ({ value: String(value), label: `${value} minutes` }))}
                    />
                  </div>
                </div>
                <p className="text-sm muted-text">
                  {dashboardEdition.cloud
                    ? 'New Cloud pools use a 2.5 CPU and 4 GiB memory limit per slot.'
                    : 'New pools use the Compute size: 4.8 CPU and about 4.8 GiB memory limit per slot.'}{' '}
                  Adjust resource limits in the service configuration. Shell, JavaScript and
                  Docker-based actions use fresh workspaces.
                </p>
              </FormSection>
              <div className="flex flex-wrap gap-2">
                <Button type="button" onClick={onClose}>
                  Cancel
                </Button>
                <Button type="submit" variant="primary" disabled={busy || !ready}>
                  {busy ? 'Checking…' : 'Review runner pool'}
                </Button>
              </div>
            </form>
          )}
        </>
      )}
      {error && <RequestError error={error} />}
    </FormPage>
  )
}
