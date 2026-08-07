import { useEffect, useState } from 'react'
import { createFileRoute, Link, useNavigate, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { APIError, message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Icon } from '../components/icons'
import { Copy, Empty, ErrorState, Loading, Note, Status } from '../components/shared'
import { DiffTable } from '../components/deploy-dialog'

type Build = components['schemas']['BuildConfig']
type Run = components['schemas']['BuildRun']
export const Route = createFileRoute('/builds/$buildId')({ component: BuildRoute })
function BuildRoute() {
  const { buildId } = Route.useParams()
  return useLocation().pathname === `/builds/${buildId}` ? <BuildDetail /> : <Outlet />
}
function BuildDetail() {
  const { buildId } = Route.useParams()
  const scope = useScope()
  const [start, setStart] = useState(false)
  const [preview, setPreview] = useState<components['schemas']['BuildPreview'] | null>(null)
  const [selected, setSelected] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const config = useQuery({
    queryKey: ['build', buildId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/builds/{id}', { signal, params: { path: { id: buildId } } })),
    gcTime: 0,
  })
  const runs = useQuery({
    queryKey: ['build-runs', buildId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/builds/{id}/runs', { signal, params: { path: { id: buildId } } })),
    gcTime: 0,
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
  })
  useEffect(() => {
    if (config.data) scope.syncScope(config.data.project, config.data.environment)
  }, [config.data?.project, config.data?.environment, scope.syncScope])
  if (config.isPending) return <Loading />
  if (config.error || !config.data) return <ErrorState error={config.error} />
  const build = config.data
  const providerLabel = build.provider === 'gitlab' ? 'GitLab' : 'GitHub'
  const runId = selected || runs.data?.items[0]?.id || ''
  return (
    <>
      <Link to="/builds" className="back-link">
        <Icon name="back" size={14} />
        All source builds
      </Link>
      <div className="section-toolbar">
        <div>
          <div className="eyebrow">
            SOURCE BUILD / {build.project} / {build.environment}
          </div>
          <h1>
            {build.name} / {build.service}
          </h1>
          <p>
            {providerLabel} · {build.repository} · {build.branch}
          </p>
        </div>
        {scope.can('deployments:write') && (
          <div className="toolbar-actions">
            <Link className="button" to="/builds/$buildId/edit" params={{ buildId }}>
              Edit build
            </Link>
            <Button
              variant="primary"
              disabled={build.installed_revision !== build.revision}
              onClick={() => setStart(true)}
            >
              Run build
            </Button>
          </div>
        )}
      </div>
      <div className="service-overview-grid">
        <section className="panel service-summary-panel">
          <div className="panel-heading">
            <h2>Build configuration</h2>
            <span className="label-chip">r{build.revision}</span>
          </div>
          <dl className="service-definition-list">
            <div>
              <dt>Method</dt>
              <dd>{build.mode === 'buildpacks' ? `Buildpacks · ${build.preset}` : 'Dockerfile'}</dd>
            </div>
            <div>
              <dt>Architecture</dt>
              <dd>{build.architecture}</dd>
            </div>
            <div>
              <dt>Context</dt>
              <dd>
                <code>{build.context_path}</code>
              </dd>
            </div>
            {build.mode === 'dockerfile' && (
              <div>
                <dt>Dockerfile</dt>
                <dd>
                  <code>{build.dockerfile}</code>
                </dd>
              </div>
            )}
            <div>
              <dt>Automatic build / deploy</dt>
              <dd>
                {build.auto_build ? 'Build on push' : 'Manual builds'} ·{' '}
                {build.auto_deploy ? 'Deploy verified successes' : 'Review deployments manually'}
              </dd>
            </div>
            <div>
              <dt>Runtime registry credential</dt>
              <dd>{build.registry_credential || 'None'}</dd>
            </div>
            {build.application_id && (
              <div>
                <dt>Application</dt>
                <dd>
                  <Link
                    to="/applications/$applicationId"
                    params={{ applicationId: build.application_id }}
                    search={{ service: build.service }}
                  >
                    Open service
                    <Icon name="arrow" size={13} />
                  </Link>
                </dd>
              </div>
            )}
          </dl>
        </section>
        <section className="panel service-summary-panel">
          <div className="panel-heading">
            <h2>{providerLabel} workflow</h2>
            <Status
              value={
                build.installed_revision === build.revision ? 'installed' : 'installation required'
              }
            />
          </div>
          <p className="muted-text">
            Review the generated workflow before an administrator commits it to the repository’s
            default branch.
          </p>
          {build.installed_commit && (
            <p className="field-help break-text">
              Installed commit: <code>{build.installed_commit}</code>
            </p>
          )}
          {scope.can('deployments:write') && (
            <Button
              disabled={busy}
              onClick={async () => {
                setBusy(true)
                setError('')
                try {
                  setPreview(
                    await unwrap(
                      client.POST('/builds/{id}/preview', {
                        params: { path: { id: build.id } },
                        body: {},
                      }),
                    ),
                  )
                } catch (err) {
                  setError(message(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              {busy ? 'Loading…' : 'Review workflow'}
            </Button>
          )}
        </section>
      </div>
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <div className="section-toolbar">
        <div>
          <h2>Recent build runs</h2>
          <p>Up to 20 runs. Select a run to observe its current {providerLabel} status.</p>
        </div>
        <Button size="sm" onClick={() => void runs.refetch()}>
          Refresh history
        </Button>
      </div>
      {runs.isPending ? (
        <Loading rows={2} />
      ) : runs.error ? (
        <ErrorState error={runs.error} />
      ) : !runs.data?.items.length ? (
        <Empty
          icon="branch"
          title="No builds started"
          description="Install the reviewed workflow, then run a build of this repository."
        />
      ) : (
        <>
          <div className="build-run-selector">
            {runs.data.items.map((run) => (
              <button
                key={run.id}
                className={`build-run-choice ${run.id === runId ? 'selected' : ''}`}
                onClick={() => setSelected(run.id)}
              >
                <code>{run.commit_sha.slice(0, 9)}</code>
                <Status value={run.status} small />
                <span>{timestamp(run.created_at)}</span>
              </button>
            ))}
          </div>
          {runId && <BuildRunDetail key={runId} build={build} runId={runId} />}
        </>
      )}
      {start && (
        <RunDialog
          build={build}
          onClose={() => setStart(false)}
          onCreated={(run) => {
            setSelected(run.id)
            setStart(false)
            void runs.refetch()
          }}
        />
      )}
      <Dialog
        open={Boolean(preview)}
        wide
        onOpenChange={(open) => {
          if (!busy && !open) setPreview(null)
        }}
        title="Review generated workflow"
        description={`Installing commits this file to ${preview?.config.repository || build.repository} on its default branch.`}
      >
        <div className="dialog-body">
          {preview && (
            <>
              <Note>
                <code>{preview.workflow_path}</code> · Image repository{' '}
                <code>{preview.image_repository}</code>
              </Note>
              {preview.requirements.map((requirement) => (
                <p className="field-help" key={requirement}>
                  {requirement}
                </p>
              ))}
              <div className="code-panel">
                <div>
                  <span>
                    Generated{' '}
                    {build.provider === 'gitlab' ? 'GitLab CI pipeline' : 'GitHub Actions workflow'}
                  </span>
                  <Copy value={preview.workflow} />
                </div>
                <pre>{preview.workflow}</pre>
              </div>
              {preview.config.auto_build && (
                <Note>
                  The workflow includes a push trigger. Installing it enables builds for future
                  pushes to {preview.config.branch}.
                </Note>
              )}
            </>
          )}
          {!scope.identity.admin && (
            <Note>An installation administrator must install this reviewed workflow.</Note>
          )}
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setPreview(null)}>
            Close preview
          </Button>
          {scope.identity.admin && (
            <Button
              variant="primary"
              disabled={busy || !preview}
              onClick={async () => {
                if (!preview || busy) return
                setBusy(true)
                setError('')
                try {
                  await unwrap(
                    client.POST('/builds/{id}/install', {
                      params: { path: { id: build.id } },
                      body: { expected_config_revision: preview.config.revision },
                    }),
                  )
                  setPreview(null)
                  void config.refetch()
                } catch (err) {
                  setError(message(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              {busy ? 'Committing workflow…' : `Commit reviewed workflow to ${providerLabel}`}
            </Button>
          )}
        </div>
      </Dialog>
    </>
  )
}

function RunDialog({
  build,
  onClose,
  onCreated,
}: {
  build: Build
  onClose: () => void
  onCreated: (run: Run) => void
}) {
  const [commit, setCommit] = useState('')
  const [key, setKey] = useState(() => crypto.randomUUID())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title="Run source build"
      description={`Build ${build.repository} using configuration r${build.revision}.`}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy) return
          setBusy(true)
          setError('')
          try {
            onCreated(
              await unwrap(
                client.POST('/builds/{id}/run', {
                  params: { path: { id: build.id }, header: { 'Idempotency-Key': key } },
                  body: { expected_config_revision: build.revision, commit },
                }),
              ),
            )
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body auth-form">
          <label>
            Exact commit (optional)
            <input
              value={commit}
              onChange={(e) => {
                setCommit(e.target.value)
                setKey(crypto.randomUUID())
              }}
              placeholder={`Resolve the current ${build.branch} commit`}
              pattern="[0-9a-f]{40,64}"
              maxLength={64}
            />
          </label>
          <Note>
            Hakopod resolves a commit, dispatches the reviewed{' '}
            {build.provider === 'gitlab' ? 'GitLab CI pipeline' : 'GitHub Actions workflow'}, then
            verifies the produced image digest. This action uses{' '}
            {build.provider === 'gitlab' ? 'GitLab' : 'GitHub'} runner capacity.
          </Note>
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Dispatching…' : 'Run build'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function BuildRunDetail({ build, runId }: { build: Build; runId: string }) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [deploy, setDeploy] = useState(false)
  const [cancel, setCancel] = useState(false)
  const [deploymentPlan, setDeploymentPlan] = useState<
    components['schemas']['BuildDeployPlan'] | null
  >(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const run = useQuery({
    queryKey: ['build-run', build.id, runId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/builds/{id}/runs/{run}', {
          signal,
          params: { path: { id: build.id, run: runId } },
        }),
      ),
    gcTime: 0,
    refetchInterval: (query) => {
      const value = query.state.data
      return value &&
        (['failed', 'cancelled'].includes(value.status) ||
          (value.status === 'completed' && value.image))
        ? false
        : 10000
    },
    refetchIntervalInBackground: false,
  })
  if (run.isPending) return <Loading />
  if (run.error || !run.data)
    return <ErrorState error={run.error} retry={() => void run.refetch()} />
  const current = run.data
  const ready =
    current.status === 'completed' &&
    current.conclusion === 'success' &&
    Boolean(current.image) &&
    current.config_revision === build.revision
  const active = !['completed', 'failed', 'cancelled'].includes(current.status)
  return (
    <section className="panel service-summary-panel">
      <div className="section-toolbar">
        <div>
          <h2>Build {current.id.slice(0, 8)}</h2>
          <p>{timestamp(current.created_at)}</p>
        </div>
        <Status value={current.status} />
      </div>
      <dl className="service-definition-list">
        <div>
          <dt>Source commit</dt>
          <dd className="mono break-text">{current.commit_sha}</dd>
        </div>
        <div>
          <dt>Result</dt>
          <dd>{current.conclusion || 'Not completed'}</dd>
        </div>
        <div>
          <dt>Verified image</dt>
          <dd className="mono break-text">{current.image || 'Not available yet'}</dd>
        </div>
        {current.automatic && (
          <div>
            <dt>Automatic deployment</dt>
            <dd>{current.auto_status || 'Waiting for observation'}</dd>
          </div>
        )}
      </dl>
      {current.message && <Note>{current.message}</Note>}
      <div className="toolbar-actions">
        {/^https:\/\/(?:github|gitlab)\.com\//.test(current.run_url) && (
          <a
            href={current.run_url}
            target="_blank"
            rel="noreferrer"
            className="button button-secondary"
          >
            Build logs in {current.provider === 'gitlab' ? 'GitLab' : 'GitHub'}
            <Icon name="external" size={13} />
          </a>
        )}
        <Button size="sm" onClick={() => void run.refetch()}>
          Refresh observation
        </Button>
        {current.deployment_id && (
          <Link
            to="/deployments/$deploymentId"
            params={{ deploymentId: current.deployment_id }}
            className="button button-secondary"
          >
            Open deployment
          </Link>
        )}
        {scope.can('deployments:write') && ready && (
          <Button
            variant="primary"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                setDeploymentPlan(
                  await unwrap(
                    client.POST('/builds/{id}/runs/{run}/plan', {
                      params: { path: { id: build.id, run: runId } },
                      body: {},
                    }),
                  ),
                )
                setDeploy(true)
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Review image deployment
          </Button>
        )}
        {scope.can('deployments:write') &&
          active &&
          (current.remote_run_id || current.github_run_id) > 0 && (
            <Button size="sm" onClick={() => setCancel(true)}>
              Cancel build
            </Button>
          )}
      </div>
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <Dialog
        open={deploy}
        onOpenChange={(open) => {
          if (!busy) {
            setDeploy(open)
            if (!open) setDeploymentPlan(null)
          }
        }}
        wide
        title="Review built image deployment"
        description={`Deploy the verified image to ${build.project} / ${build.environment} / ${build.name}.`}
      >
        <div className="dialog-body">
          <dl className="service-definition-list">
            <div>
              <dt>Service</dt>
              <dd>{build.service}</dd>
            </div>
            <div>
              <dt>Application revision</dt>
              <dd>
                {deploymentPlan?.expected_revision === 0
                  ? 'New application'
                  : `r${deploymentPlan?.expected_revision}`}{' '}
                → r{(deploymentPlan?.expected_revision || 0) + 1}
              </dd>
            </div>
            <div>
              <dt>Build configuration</dt>
              <dd>r{deploymentPlan?.expected_config_revision}</dd>
            </div>
            <div>
              <dt>Image</dt>
              <dd className="mono break-text">{current.image}</dd>
            </div>
          </dl>
          {deploymentPlan && (
            <>
              {deploymentPlan.warnings.map((warning) => (
                <Note key={warning}>{warning}</Note>
              ))}
              <DiffTable changes={deploymentPlan.changes} />
            </>
          )}
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button
            disabled={busy}
            onClick={() => {
              setDeploy(false)
              setDeploymentPlan(null)
            }}
          >
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy || !deploymentPlan}
            onClick={async () => {
              if (!deploymentPlan) return
              setBusy(true)
              setError('')
              try {
                const result = await unwrap(
                  client.POST('/builds/{id}/runs/{run}/deploy', {
                    params: { path: { id: build.id, run: runId } },
                    body: {
                      expected_revision: deploymentPlan.expected_revision,
                      expected_config_revision: deploymentPlan.expected_config_revision,
                    },
                  }),
                )
                void cache.invalidateQueries({ queryKey: ['applications'] })
                void navigate({
                  to: '/deployments/$deploymentId',
                  params: { deploymentId: result.id },
                })
              } catch (err) {
                setError(message(err))
                if (err instanceof APIError && err.status === 409) {
                  setDeploy(false)
                  setDeploymentPlan(null)
                }
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Deploying…' : 'Deploy reviewed image'}
          </Button>
        </div>
      </Dialog>
      <Dialog
        open={cancel}
        onOpenChange={(open) => {
          if (!busy) setCancel(open)
        }}
        title="Cancel this build?"
        description={`Request cancellation from ${build.provider === 'gitlab' ? 'GitLab CI' : 'GitHub Actions'}. A completed image or accepted deployment is not removed.`}
      >
        <div className="dialog-body">
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setCancel(false)}>
            Keep building
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.POST('/builds/{id}/runs/{run}/cancel', {
                    params: { path: { id: build.id, run: runId } },
                    body: {},
                  }),
                )
                setCancel(false)
                void run.refetch()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Cancel build
          </Button>
        </div>
      </Dialog>
    </section>
  )
}
