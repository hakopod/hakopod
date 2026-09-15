import { frameworkLabel } from '../components/framework-build-fields'
import { Input } from '../components/ui/input'
import { useEffect, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Pipeline, type PipelineStage } from '@hakopod/hatch-ui/blocks/pipeline'
import { createFileRoute, Link, useNavigate, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { APIError, activeDeployment, message, relative, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Icon } from '../components/icons'
import { HeadingHelp, Copy, Empty, ErrorState, Loading, Note, Status } from '../components/shared'
import { DiffTable } from '../components/deploy-dialog'

type Build = components['schemas']['BuildConfig']
type Run = components['schemas']['BuildRun']
function runStatus(run: Run) {
  if (run.conclusion === 'success') return 'succeeded'
  if (run.conclusion === 'failure' || run.conclusion === 'timed_out') return 'failed'
  if (run.conclusion === 'cancelled') return 'cancelled'
  if (run.status === 'in_progress') return 'building'
  return run.status
}
export const Route = createFileRoute('/builds/$buildId')({ component: BuildRoute })
function BuildRoute() {
  const { buildId } = Route.useParams()
  return useLocation().pathname === `/builds/${buildId}` ? <BuildDetail /> : <Outlet />
}
function BuildDetail() {
  const { buildId } = Route.useParams()
  const scope = useScope()
  const [start, setStart] = useState(false)
  const [tab, setTab] = useState('runs')
  const navigationRoot = useActiveSection(tab, '.tab-list')
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
    <div className="ops-page ops-build-page">
      <header className="application-heading">
        <div>
          <div className="sr-only">
            SOURCE BUILD / {build.project} / {build.environment}
          </div>
          <div className="title-row hako-page-heading-title">
            <Status
              value={
                build.installed_revision === build.revision ? 'installed' : 'installation required'
              }
            />
            <h1>
              {build.name} / {build.service}
            </h1>
          </div>
          <div className="application-metadata">
            <code>{build.id}</code>
            <Copy value={build.id} />
            <span>
              {providerLabel} · {build.repository} · {build.branch}
            </span>
          </div>
        </div>
        {scope.can('deployments:write') && (
          <div className="toolbar-actions">
            <Button asChild>
              <Link to="/builds/$buildId/edit" params={{ buildId }}>
                Edit build
              </Link>
            </Button>
            <Button
              variant={runId || tab === 'configuration' ? 'secondary' : 'primary'}
              disabled={build.installed_revision !== build.revision}
              onClick={() => setStart(true)}
            >
              Run build
            </Button>
          </div>
        )}
      </header>
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <Tabs.Root value={tab} onValueChange={setTab}>
        <Tabs.List ref={navigationRoot} className="tab-list" aria-label="Source build sections">
          <Tabs.Trigger className="tab-trigger" value="runs">
            Runs
          </Tabs.Trigger>
          <Tabs.Trigger className="tab-trigger" value="configuration">
            Configuration
          </Tabs.Trigger>
        </Tabs.List>
        <Tabs.Content value="configuration" className="tab-content">
          <div className="service-overview-grid">
            <section className="panel service-summary-panel">
              <div className="panel-heading">
                <h2>Build configuration</h2>
                <span className="label-chip">r{build.revision}</span>
              </div>
              <dl className="service-definition-list">
                <div>
                  <dt>Method</dt>
                  <dd>
                    {build.mode === 'framework'
                      ? `${frameworkLabel(build.framework?.framework || '')} · ${build.framework?.runtime}`
                      : build.mode === 'buildpacks'
                        ? `Buildpacks · ${build.preset}`
                        : 'Dockerfile'}
                  </dd>
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
                {build.mode === 'framework' &&
                  build.framework &&
                  Object.entries({
                    Framework: frameworkLabel(build.framework.framework),
                    'Package manager': build.framework.package_manager,
                    Install: build.framework.install_command,
                    Build: build.framework.build_command,
                    Start: build.framework.start_command,
                    Output: build.framework.output_directory,
                  })
                    .filter(([, value]) => value)
                    .map(([label, value]) => (
                      <div key={label}>
                        <dt>{label}</dt>
                        <dd>
                          <code className="break-all">{value}</code>
                        </dd>
                      </div>
                    ))}
                {Object.keys(build.build_secrets || {}).length > 0 && (
                  <div>
                    <dt>Build secret references</dt>
                    <dd>
                      {Object.entries(build.build_secrets || {}).map(([id, ref]) => (
                        <div key={id}>
                          <code className="break-all">
                            {id} → {ref}
                          </code>
                        </div>
                      ))}
                    </dd>
                  </div>
                )}
                <div>
                  <dt>Automatic build / deploy</dt>
                  <dd>
                    {build.auto_build ? 'Build on push' : 'Manual builds'} ·{' '}
                    {build.auto_deploy
                      ? 'Deploy verified successes'
                      : 'Review deployments manually'}
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
                    build.installed_revision === build.revision
                      ? 'installed'
                      : 'installation required'
                  }
                />
              </div>
              <p className="muted-text">
                Review the generated workflow before a repository manager commits it to the
                repository’s default branch.
              </p>
              {build.installed_commit && (
                <p className="field-help break-text">
                  Installed commit: <code>{build.installed_commit}</code>
                  <Copy value={build.installed_commit} />
                </p>
              )}
              {scope.can('deployments:write') && (
                <Button
                  variant="primary"
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
        </Tabs.Content>
        <Tabs.Content value="runs" className="tab-content">
          <div className="section-toolbar">
            <div>
              <div className="hako-section-heading-title">
                <h2>Recent build runs</h2>
                <HeadingHelp title="Recent build runs">
                  Up to 20 runs. Select a run to observe its current {providerLabel} status.
                </HeadingHelp>
              </div>
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
              <div className="ops-run-layout">
                <aside className="ops-run-list" aria-label="Build run history">
                  {runs.data.items.map((run) => (
                    <button
                      type="button"
                      key={run.id}
                      className={`ops-run-card interactive ${run.id === runId && !['completed', 'failed', 'cancelled'].includes(run.status) ? 'hatch' : ''}`}
                      data-selected={run.id === runId}
                      aria-pressed={run.id === runId}
                      onClick={() => setSelected(run.id)}
                    >
                      <Brackets />
                      <div className="ops-object">
                        <Status value={runStatus(run)} small />
                        <code>{run.commit_sha.slice(0, 9) || run.id.slice(0, 9)}</code>
                      </div>
                      <small>
                        {build.branch} / {run.automatic ? 'Git push' : 'Manual build'}
                      </small>
                      <time title={run.created_at} dateTime={run.created_at}>
                        {relative(run.created_at)}
                      </time>
                    </button>
                  ))}
                </aside>
                <div className="ops-run-detail">
                  {runId && <BuildRunDetail key={runId} build={build} runId={runId} />}
                </div>
              </div>
            </>
          )}
        </Tabs.Content>
      </Tabs.Root>
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
          {!(scope.identity.admin || scope.identity.can_manage_git) && (
            <Note>A repository manager must install this reviewed workflow.</Note>
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
          {(scope.identity.admin || scope.identity.can_manage_git) && (
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
    </div>
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
            <Input
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
  const [stage, setStage] = useState('build')
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
  const release = useQuery({
    queryKey: ['deployment', run.data?.deployment_id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/deployments/{id}', {
          signal,
          params: { path: { id: run.data!.deployment_id } },
        }),
      ),
    enabled: Boolean(run.data?.deployment_id),
    refetchInterval: (query) => (activeDeployment(query.state.data?.status) ? 10000 : false),
    refetchIntervalInBackground: false,
    gcTime: 0,
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
  const failed =
    current.status === 'failed' ||
    (current.status === 'completed' &&
      current.conclusion !== 'success' &&
      current.conclusion !== 'cancelled')
  const stages: PipelineStage[] = [
    {
      id: 'source',
      label: 'Source',
      state: current.commit_sha ? 'success' : 'neutral',
      description: current.commit_sha ? current.commit_sha.slice(0, 9) : 'No commit returned',
    },
    {
      id: 'build',
      label: 'Build',
      state: failed
        ? 'error'
        : current.conclusion === 'success'
          ? 'success'
          : ['queued', 'cancelled'].includes(current.status)
            ? 'neutral'
            : current.status === 'dispatch_unknown'
              ? 'warning'
              : active
                ? 'running'
                : 'neutral',
      description: current.conclusion || current.status,
    },
    {
      id: 'image',
      label: 'Image',
      state: current.image ? 'success' : 'neutral',
      description: current.image ? 'Digest verified' : 'No verified image',
    },
    {
      id: 'deploy',
      label: 'Deploy',
      state:
        release.data?.status === 'succeeded'
          ? 'success'
          : release.data?.status === 'failed'
            ? 'error'
            : activeDeployment(release.data?.status)
              ? 'running'
              : 'neutral',
      description:
        release.data?.status ||
        (release.error
          ? 'Observation unavailable'
          : current.deployment_id
            ? 'Loading observation'
            : current.automatic
              ? current.auto_status || 'Not requested'
              : 'Manual deployment'),
    },
  ]
  const descriptions: Record<string, string> = {
    source: 'The immutable source commit resolved for this run.',
    build: `The current status reported by ${current.provider === 'gitlab' ? 'GitLab CI' : 'GitHub Actions'}. Open provider logs for individual build steps.`,
    image:
      'Only verified image digests are eligible for deployment. An earlier configuration revision must be reviewed again.',
    deploy:
      'Deployment has its own recorded events and readiness result. Open it to inspect service health.',
  }
  return (
    <section className="ops-build-run">
      <div className="section-toolbar">
        <div>
          <h2>Build {current.id.slice(0, 8)}</h2>
          <div className="ops-object-id">
            <code>{current.id}</code>
            <Copy value={current.id} />
          </div>
          <p>{timestamp(current.created_at)}</p>
        </div>
        <Status value={runStatus(current)} />
      </div>
      <Pipeline stages={stages} active={stage} onStageChange={setStage} />
      <div className="ops-stage-context" role="status">
        {descriptions[stage]}
      </div>
      <dl className="service-definition-list">
        <div>
          <dt>Source commit</dt>
          <dd className="mono break-text">
            {current.commit_sha}
            <Copy value={current.commit_sha} />
          </dd>
        </div>
        <div>
          <dt>Result</dt>
          <dd>{current.conclusion || 'Not completed'}</dd>
        </div>
        <div>
          <dt>Verified image</dt>
          <dd className="mono break-text">
            {current.image || 'Not available yet'}
            {current.image && <Copy value={current.image} label="Copy digest" />}
          </dd>
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
