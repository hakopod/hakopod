import { TOMLEditor } from '../components/toml-editor'
import { Input } from '../components/ui/input'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { createFileRoute, Link, useNavigate, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message, relative, timestamp } from '../lib/api'
import { canAccess, useScope } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Icon } from '../components/icons'
import { Copy, Loading, Note, RequestError } from '../components/shared'
import { PendingButton, plural } from '../components/create-flow'
import { ResourceEmpty, ResourceError, StaleNotice } from '../components/resource-card'
import {
  ProviderMark,
  RunStatus,
  WorkflowStatus,
  buildMethodLabel,
  providerName,
  providerRunner,
  runState,
  workflowState,
} from '../components/build-summary'
import { BuildRunDetail } from '../components/build-run'

type Build = components['schemas']['BuildConfig']
type Run = components['schemas']['BuildRun']

export const Route = createFileRoute('/builds/$buildId')({
  validateSearch: (search: Record<string, unknown>): { review?: 'workflow' } => ({
    review: search.review === 'workflow' ? 'workflow' : undefined,
  }),
  component: BuildRoute,
})
function BuildRoute() {
  const { buildId } = Route.useParams()
  return useLocation().pathname === `/builds/${buildId}` ? <BuildDetail /> : <Outlet />
}
function BuildDetail() {
  const { buildId } = Route.useParams()
  const scope = useScope()
  const { review } = Route.useSearch()
  const navigate = useNavigate()
  const reviewed = useRef('')
  // The control that opened the workflow review gets focus back when it closes.
  const reviewTrigger = useRef<HTMLElement | null>(null)
  const [start, setStart] = useState(false)
  const [tab, setTab] = useState(review === 'workflow' ? 'configuration' : 'runs')
  const navigationRoot = useActiveSection(tab, '.tab-list')
  const [preview, setPreview] = useState<components['schemas']['BuildPreview'] | null>(null)
  const [selected, setSelected] = useState('')
  const [previewing, setPreviewing] = useState(false)
  const [installing, setInstalling] = useState(false)
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
  const reviewWorkflow = async () => {
    if (document.activeElement instanceof HTMLElement)
      reviewTrigger.current = document.activeElement
    setPreviewing(true)
    setError('')
    try {
      setPreview(
        await unwrap(
          client.POST('/builds/{id}/preview', { params: { path: { id: buildId } }, body: {} }),
        ),
      )
    } catch (err) {
      setError(message(err))
    } finally {
      setPreviewing(false)
    }
  }
  useEffect(() => {
    if (config.data) scope.syncScope(config.data.project, config.data.environment)
  }, [config.data?.project, config.data?.environment, scope.syncScope])
  useEffect(() => {
    if (review !== 'workflow' || !config.data || reviewed.current === buildId) return
    reviewed.current = buildId
    setTab('configuration')
    void navigate({ to: '/builds/$buildId', params: { buildId }, search: {}, replace: true })
    if (!canAccess(scope.identity, config.data.project, 'deployments:write')) return
    void reviewWorkflow()
  }, [review, config.data, buildId])
  if (config.isPending) return <Loading />
  if (!config.data)
    return (
      <div className="ops-page builds-page">
        <ResourceError
          error={config.error}
          retry={() => void config.refetch()}
          subject="this source build"
        />
      </div>
    )
  const build = config.data
  const canWrite = scope.can('deployments:write')
  const canInstall = scope.identity.admin || scope.identity.can_manage_git
  const workflow = workflowState(build)
  const runner = providerRunner(build.provider)
  const items = runs.data?.items || []
  const runId = selected || items[0]?.id || ''
  const latest = items[0]
  const latestFailed = latest && runState(latest)[1] === 'error'
  const showRun = (id: string) => {
    setSelected(id)
    setTab('runs')
  }
  return (
    <div className="ops-page builds-page build-detail">
      <header className="app-workspace-head">
        <div className="app-workspace-identity">
          <nav className="app-workspace-scope" aria-label="Build scope">
            <Link to="/builds">Source builds</Link>
            <span aria-hidden="true">/</span>
            <span>
              {build.project} / {build.environment}
            </span>
          </nav>
          <div className="app-workspace-title">
            <h1>
              {build.name} / {build.service}
            </h1>
            <Copy value={build.id} label="Copy build ID" iconOnly />
          </div>
          <div className="app-workspace-meta">
            <WorkflowStatus build={build} />
            <span className="build-meta-item">
              <ProviderMark provider={build.provider} />
              <code>{build.repository}</code>
            </span>
            <span className="build-meta-item">
              <Icon name="branch" size={13} />
              <code>{build.branch}</code>
            </span>
            <span>Configuration r{build.revision}</span>
            <time
              dateTime={new Date(config.dataUpdatedAt).toISOString()}
              title={timestamp(new Date(config.dataUpdatedAt).toISOString())}
            >
              Observed {relative(new Date(config.dataUpdatedAt).toISOString()).toLowerCase()}
            </time>
          </div>
        </div>
        <div className="app-workspace-actions">
          {canWrite && (
            <Button asChild>
              <Link to="/builds/$buildId/edit" params={{ buildId }}>
                Edit build
              </Link>
            </Button>
          )}
          {canWrite &&
            (workflow.installed ? (
              <Button variant="primary" onClick={() => setStart(true)}>
                <Icon name="play" size={15} />
                Run build
              </Button>
            ) : (
              <PendingButton
                variant="primary"
                pending={previewing}
                pendingLabel="Preparing workflow…"
                onClick={() => void reviewWorkflow()}
              >
                Review workflow
              </PendingButton>
            ))}
        </div>
      </header>
      {config.error && (
        <StaleNotice updatedAt={config.dataUpdatedAt} retry={() => void config.refetch()} />
      )}
      {error && !preview && <RequestError error={error} />}
      {!workflow.installed ? (
        <div className="build-attention" data-tone="warning" role="status">
          <Icon name="alert" size={18} />
          <div className="build-attention-body">
            <h2>
              {build.installed_revision > 0
                ? 'Commit the updated workflow before the next build'
                : 'Install the workflow before building'}
            </h2>
            <p>
              {build.installed_revision > 0
                ? `${build.repository} still runs the workflow for configuration r${build.installed_revision}. Configuration r${build.revision} builds only after its workflow is committed.`
                : `Configuration r${build.revision} is saved, but ${build.repository} does not have its ${runner} workflow yet.`}{' '}
              {canInstall ? '' : 'A repository manager must commit it.'}
            </p>
          </div>
        </div>
      ) : (
        latestFailed && (
          <div className="build-attention" data-tone="error" role="status">
            <Icon name="alert" size={18} />
            <div className="build-attention-body">
              <h2>Latest build failed</h2>
              <p>
                Commit <code>{latest.commit_sha.slice(0, 9) || 'not resolved'}</code> ·{' '}
                {relative(latest.created_at)}
              </p>
              {latest.message && <code className="build-attention-log">{latest.message}</code>}
              {(tab !== 'runs' || runId !== latest.id) && (
                <div className="build-attention-actions">
                  <Button size="sm" onClick={() => showRun(latest.id)}>
                    Show failed run
                  </Button>
                </div>
              )}
            </div>
          </div>
        )
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
        <Tabs.Content value="runs" className="tab-content">
          {runs.isPending ? (
            <Loading rows={2} />
          ) : runs.error && !runs.data ? (
            <ResourceError
              error={runs.error}
              retry={() => void runs.refetch()}
              subject="build runs"
            />
          ) : !items.length ? (
            <ResourceEmpty
              icon="branch"
              title="No builds have run"
              description={
                workflow.installed
                  ? `Run a build to produce the first verified image from ${build.branch}.`
                  : 'Install the workflow first; builds run from the committed workflow.'
              }
              action={
                canWrite &&
                workflow.installed && (
                  <Button onClick={() => setStart(true)}>Run build on {build.branch}</Button>
                )
              }
            />
          ) : (
            <div className="build-runs">
              <section className="build-run-list" aria-labelledby="build-runs-heading">
                <div className="build-run-list-head">
                  <h2 id="build-runs-heading">
                    Runs <span className="builds-count">{items.length}</span>
                  </h2>
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label="Refresh runs"
                    disabled={runs.isFetching}
                    onClick={() => void runs.refetch()}
                  >
                    <Icon name="refresh" size={15} />
                  </Button>
                </div>
                {runs.error && (
                  <StaleNotice updatedAt={runs.dataUpdatedAt} retry={() => void runs.refetch()} />
                )}
                <ul aria-label="Build run history">
                  {items.map((run) => (
                    <li key={run.id}>
                      <button
                        type="button"
                        className="build-run-item"
                        aria-current={run.id === runId || undefined}
                        onClick={() => setSelected(run.id)}
                      >
                        <span className="build-run-item-top">
                          <code>{run.commit_sha.slice(0, 9) || run.id.slice(0, 8)}</code>
                          <RunStatus run={run} />
                        </span>
                        <span className="build-run-item-meta">
                          {run.automatic ? 'Push' : 'Manual'} · r{run.config_revision} ·{' '}
                          <time dateTime={run.created_at} title={timestamp(run.created_at)}>
                            {relative(run.created_at)}
                          </time>
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
                <p className="build-run-list-note">Latest {plural(items.length, 'run')}</p>
              </section>
              {runId && <BuildRunDetail key={runId} build={build} runId={runId} />}
            </div>
          )}
        </Tabs.Content>
        <Tabs.Content value="configuration" className="tab-content">
          <BuildConfiguration
            build={build}
            canWrite={canWrite}
            previewing={previewing}
            onReview={() => void reviewWorkflow()}
          />
        </Tabs.Content>
      </Tabs.Root>
      {start && (
        <RunDialog
          build={build}
          onClose={() => setStart(false)}
          onCreated={(run) => {
            setStart(false)
            showRun(run.id)
            void runs.refetch()
          }}
        />
      )}
      <Dialog
        open={Boolean(preview)}
        wide
        onOpenChange={(open) => {
          if (!installing && !open) {
            setPreview(null)
            setError('')
          }
        }}
        onCloseAutoFocus={(event) => {
          if (!reviewTrigger.current?.isConnected) return
          event.preventDefault()
          reviewTrigger.current.focus()
        }}
        title="Review generated workflow"
        description={`Installing commits this file to ${preview?.config.repository || build.repository} on its default branch.`}
      >
        <div className="dialog-body build-dialog-body">
          {preview && (
            <>
              <dl className="app-workspace-details build-facts">
                <div>
                  <dt>File</dt>
                  <dd>
                    <code>{preview.workflow_path}</code>
                  </dd>
                </div>
                <div>
                  <dt>Image repository</dt>
                  <dd>
                    <code>{preview.image_repository}</code>
                  </dd>
                </div>
                <div>
                  <dt>Configuration</dt>
                  <dd>r{preview.config.revision}</dd>
                </div>
              </dl>
              {preview.requirements.length > 0 && (
                <ul className="build-requirements">
                  {preview.requirements.map((requirement) => (
                    <li key={requirement}>{requirement}</li>
                  ))}
                </ul>
              )}
              <div className="code-panel">
                <div>
                  <span>
                    Generated{' '}
                    {build.provider === 'gitlab' ? 'GitLab CI pipeline' : 'GitHub Actions workflow'}
                  </span>
                  <Copy value={preview.workflow} label="Copy workflow" />
                </div>
                <TOMLEditor
                  label="Generated build workflow"
                  language="yaml"
                  purpose="workflow"
                  value={preview.workflow}
                  onChange={() => {}}
                  readOnly
                />
              </div>
              {preview.config.auto_build && (
                <Note>
                  The workflow includes a push trigger. Installing it starts builds for future
                  pushes to {preview.config.branch}.
                </Note>
              )}
            </>
          )}
          {!canInstall && <Note>A repository manager must commit this reviewed workflow.</Note>}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button
            disabled={installing}
            onClick={() => {
              setPreview(null)
              setError('')
            }}
          >
            Close
          </Button>
          {canInstall && (
            <PendingButton
              variant="primary"
              pending={installing}
              pendingLabel="Committing workflow…"
              disabled={!preview}
              onClick={async () => {
                if (!preview || installing) return
                setInstalling(true)
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
                  setInstalling(false)
                }
              }}
            >
              Commit workflow to {providerName(build.provider)}
            </PendingButton>
          )}
        </div>
      </Dialog>
    </div>
  )
}

function BuildConfiguration({
  build,
  canWrite,
  previewing,
  onReview,
}: {
  build: Build
  canWrite: boolean
  previewing: boolean
  onReview: () => void
}) {
  const runner = providerRunner(build.provider)
  const buildArgs = Object.keys(build.build_args || {})
  const secrets = Object.entries(build.build_secrets || {})
  const variables = build.env === undefined ? undefined : Object.keys(build.env)
  return (
    <div className="build-config">
      <section className="build-config-section" aria-labelledby="build-workflow-heading">
        <div className="build-section-head">
          <h2 id="build-workflow-heading">{runner} workflow</h2>
          {/* Until it is installed, the page header carries this action. */}
          {canWrite && workflowState(build).installed && (
            <PendingButton
              size="sm"
              pending={previewing}
              pendingLabel="Preparing workflow…"
              onClick={onReview}
            >
              Review workflow
            </PendingButton>
          )}
        </div>
        <dl className="app-workspace-details">
          <Row label="State">
            <WorkflowStatus build={build} />
          </Row>
          <Row label="Installed configuration">
            {build.installed_revision > 0 ? `r${build.installed_revision}` : 'Not installed'}
          </Row>
          <Row label="Installed commit">
            {build.installed_commit ? (
              <span className="build-copy-value">
                <code>{build.installed_commit.slice(0, 12)}</code>
                <Copy value={build.installed_commit} label="Copy installed commit" iconOnly />
              </span>
            ) : (
              'None'
            )}
          </Row>
          <Row label="Builds run in">{runner}</Row>
        </dl>
      </section>
      <section className="build-config-section" aria-labelledby="build-recipe-heading">
        <div className="build-section-head">
          <h2 id="build-recipe-heading">Recipe</h2>
          <span className="builds-count">r{build.revision}</span>
        </div>
        <dl className="app-workspace-details">
          <Row label="Method">{buildMethodLabel(build)}</Row>
          <Row label="Build directory">
            <code>{build.context_path}</code>
          </Row>
          <Row label="Submodules">
            {build.submodules ? 'Checked out recursively' : 'Not checked out'}
          </Row>
          {build.mode === 'dockerfile' && (
            <Row label="Dockerfile">
              <code>{build.dockerfile}</code>
            </Row>
          )}
          {build.mode === 'framework' &&
            build.framework &&
            Object.entries({
              'Package manager': build.framework.package_manager,
              Install: build.framework.install_command,
              Build: build.framework.build_command,
              Start: build.framework.start_command,
              Output: build.framework.output_directory,
            })
              .filter(([, value]) => value)
              .map(([label, value]) => (
                <Row key={label} label={label}>
                  <code>{value}</code>
                </Row>
              ))}
          <Row label="Architecture">Linux {build.architecture.toUpperCase()}</Row>
          <Row label="Public build values">
            {buildArgs.length ? <code>{buildArgs.join(', ')}</code> : 'None'}
          </Row>
          <Row label="Build secret references">
            {secrets.length ? (
              <span className="build-review-commands">
                {secrets.map(([id, ref]) => (
                  <code key={id}>
                    {id} → {ref}
                  </code>
                ))}
              </span>
            ) : (
              'None'
            )}
          </Row>
        </dl>
      </section>
      <section className="build-config-section" aria-labelledby="build-delivery-heading">
        <div className="build-section-head">
          <h2 id="build-delivery-heading">Delivery</h2>
        </div>
        <dl className="app-workspace-details">
          <Row label="Deploys to">
            {build.application_id ? (
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: build.application_id }}
                search={{ service: build.service }}
              >
                {build.name} / {build.service}
              </Link>
            ) : (
              `${build.name} / ${build.service} · created on first deployment`
            )}
          </Row>
          {!build.application_id && (
            <Row label="Resources">
              {build.size} · port {build.port} · {build.public ? 'public HTTP' : 'private'}
            </Row>
          )}
          {build.reuse_services && build.reuse_services.length > 0 && (
            <Row label="Also updates">{build.reuse_services.join(', ')}</Row>
          )}
          <Row label="Run command">
            {build.command === undefined && build.args === undefined ? (
              'Keep service settings'
            ) : !build.command?.length && !build.args?.length ? (
              'Image defaults'
            ) : (
              <code>{[...(build.command || []), ...(build.args || [])].join(' ')}</code>
            )}
          </Row>
          <Row label="Variables">
            {variables === undefined ? (
              'Keep service variables'
            ) : variables.length ? (
              <code>{variables.join(', ')}</code>
            ) : (
              'None'
            )}
          </Row>
          <Row label="Pull credential">
            {build.managed_registry
              ? 'Hakopod private registry'
              : build.registry_credential || 'Automatic'}
          </Row>
          <Row label="Automation">
            {build.auto_build ? 'Build on push' : 'Manual builds'} ·{' '}
            {build.auto_deploy ? 'deploy verified builds' : 'deploy after review'}
          </Row>
        </dl>
      </section>
    </div>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children}</dd>
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
  const runner = providerRunner(build.provider)
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={`Build ${build.name} / ${build.service}`}
      description={`Run configuration r${build.revision} from ${build.repository}.`}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy || !e.currentTarget.reportValidity()) return
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
        <div className="dialog-body create-flow">
          <label>
            Commit <span className="muted-text">Optional</span>
            <Input
              value={commit}
              onChange={(e) => {
                setCommit(e.target.value)
                setKey(crypto.randomUUID())
              }}
              placeholder={`Latest commit on ${build.branch}`}
              pattern="[0-9a-f]{40,64}"
              title="A full 40 or 64 character commit SHA in lowercase hexadecimal."
              maxLength={64}
              spellCheck={false}
            />
            <span className="field-help">
              Leave blank to build the latest commit on {build.branch}. Hakopod dispatches the
              committed {runner} workflow on your runners, then verifies the image digest.
            </span>
          </label>
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <PendingButton
            type="submit"
            variant="primary"
            pending={busy}
            pendingLabel="Dispatching build…"
          >
            {commit ? `Build ${commit.slice(0, 9)}` : `Build latest ${build.branch}`}
          </PendingButton>
        </div>
      </form>
    </Dialog>
  )
}
