import { useState, type ReactNode } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { StatusTone } from '@hakopod/hatch-ui/components/status'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { APIError, activeDeployment, message, relative, timestamp } from '../lib/api'
import { buildRunNeedsObservation } from '../lib/build-runs'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { Copy, ErrorState, Loading, Note, RequestError } from './shared'
import { DiffTable } from './deploy-dialog'
import { DeploymentSecrets } from './deployment-secrets'
import { DeploymentLog } from './deployment-log'
import { PendingButton } from './create-flow'
import { RunStatus, StateLabel, providerName, providerRunner, runState } from './build-summary'

type Build = components['schemas']['BuildConfig']
type Run = components['schemas']['BuildRun']
type Deployment = components['schemas']['Deployment']

export type BuildPhase = {
  id: 'source' | 'build' | 'image' | 'deploy'
  label: string
  state: [string, StatusTone]
  // A failure or work in progress opens the phase; finished routine phases stay collapsed.
  open: boolean
}

const providerLink = /^https:\/\/(?:github|gitlab)\.com\//

// Four phases from the recorded run: Hakopod never sees the provider's own step log.
export function buildPhases(run: Run, release?: Deployment, releaseError = false): BuildPhase[] {
  const [buildLabel, buildTone] = runState(run)
  const failed = buildTone === 'error'
  const active = buildTone === 'running' || buildRunNeedsObservation(run)
  const succeeded = run.conclusion === 'success'
  const deployState: [string, StatusTone] = release
    ? release.status === 'succeeded'
      ? ['Succeeded', 'success']
      : release.status === 'failed'
        ? ['Failed', 'error']
        : activeDeployment(release.status)
          ? ['Deploying', 'running']
          : [release.status.charAt(0).toUpperCase() + release.status.slice(1), 'neutral']
    : releaseError
      ? ['Not observed', 'warning']
      : run.deployment_id
        ? ['Loading', 'neutral']
        : run.automatic && run.auto_status
          ? [run.auto_status.charAt(0).toUpperCase() + run.auto_status.slice(1), 'neutral']
          : ['Not deployed', 'neutral']
  return [
    {
      id: 'source',
      label: 'Source commit',
      state: run.commit_sha
        ? ['Resolved', 'success']
        : failed
          ? ['Not resolved', 'neutral']
          : ['Resolving', 'running'],
      open: false,
    },
    {
      id: 'build',
      label: `Build in ${providerRunner(run.provider)}`,
      state: [buildLabel, buildTone],
      open: failed || buildTone === 'running',
    },
    {
      id: 'image',
      label: 'Verify image digest',
      state: run.image
        ? ['Verified', 'success']
        : succeeded && active
          ? ['Verifying', 'running']
          : succeeded
            ? ['Not verified', 'warning']
            : ['Not started', 'neutral'],
      open: succeeded && !run.image,
    },
    {
      id: 'deploy',
      label: 'Deploy',
      state: deployState,
      open: deployState[1] === 'error' || deployState[1] === 'running',
    },
  ]
}

export function BuildRunDetail({ build, runId }: { build: Build; runId: string }) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [toggled, setToggled] = useState<Record<string, boolean>>({})
  const [deploy, setDeploy] = useState(false)
  const [cancel, setCancel] = useState(false)
  const [plan, setPlan] = useState<components['schemas']['BuildDeployPlan'] | null>(null)
  const [busy, setBusy] = useState<'' | 'plan' | 'deploy' | 'cancel'>('')
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
    refetchInterval: (query) => (buildRunNeedsObservation(query.state.data) ? 10000 : false),
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
  const runner = providerRunner(current.provider)
  const ready = current.conclusion === 'success' && Boolean(current.image)
  const active = runState(current)[1] === 'running' || buildRunNeedsObservation(current)
  const phases = buildPhases(current, release.data, Boolean(release.error))
  const isOpen = (phase: BuildPhase) => toggled[phase.id] ?? phase.open
  const runURL = providerLink.test(current.run_url) ? current.run_url : ''
  const registry = current.image ? current.image.split('/')[0] : ''
  const canWrite = scope.can('deployments:write')
  const remoteRun = current.remote_run_id || current.github_run_id
  const planDeployment = async () => {
    setBusy('plan')
    setError('')
    try {
      setPlan(
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
      setBusy('')
    }
  }
  const phaseBody: Record<BuildPhase['id'], ReactNode> = {
    source: (
      <dl className="app-workspace-details build-facts">
        <Fact label="Commit">
          {current.commit_sha ? (
            <span className="build-copy-value">
              <code>{current.commit_sha}</code>
              <Copy value={current.commit_sha} label="Copy commit" iconOnly />
            </span>
          ) : (
            'Not resolved'
          )}
        </Fact>
        <Fact label="Branch">
          <code>{build.branch}</code>
        </Fact>
        <Fact label="Trigger">{current.automatic ? 'Git push' : 'Manual run'}</Fact>
        <Fact label="Configuration">r{current.config_revision}</Fact>
      </dl>
    ),
    build: (
      <div className="build-phase-text">
        <p>
          {runURL
            ? `The build log stays in ${runner}; Hakopod records only the result and does not copy the output.`
            : `${providerName(current.provider)} has not returned a link to this run's log.`}
        </p>
        {runURL && (
          <Button asChild size="sm" className="justify-self-start">
            <a href={runURL} target="_blank" rel="noreferrer">
              Open {remoteRun ? `run ${remoteRun}` : 'run'} in {providerName(current.provider)}
              <Icon name="external" size={13} />
              <span className="sr-only"> (opens in a new tab)</span>
            </a>
          </Button>
        )}
      </div>
    ),
    image: (
      <div className="build-phase-text">
        <p>
          {current.image
            ? 'Only verified image digests can be deployed.'
            : current.conclusion === 'success'
              ? 'The build succeeded but no image digest has been verified yet.'
              : 'An image is verified after the build succeeds.'}
        </p>
      </div>
    ),
    deploy: release.data ? (
      <DeploymentLog key={release.data.id} release={release.data} />
    ) : release.error ? (
      <ErrorState error={release.error} retry={() => void release.refetch()} />
    ) : current.deployment_id ? (
      <Loading rows={2} />
    ) : (
      <div className="build-phase-text">
        <p>
          {current.automatic && current.auto_status
            ? `Automatic deployment: ${current.auto_status.replaceAll('_', ' ')}.`
            : ready
              ? 'This image has not been deployed. Review the deployment to see exactly what changes.'
              : 'A verified image is needed before deployment.'}
        </p>
      </div>
    ),
  }
  return (
    <section className="build-run" aria-labelledby={`build-run-${current.id}`}>
      <div className="build-run-head">
        <div className="build-run-identity">
          <h2 id={`build-run-${current.id}`}>
            Build <code>{current.commit_sha.slice(0, 9) || current.id.slice(0, 8)}</code>
          </h2>
          <RunStatus run={current} small={false} />
          <p>
            Started{' '}
            <time dateTime={current.created_at} title={timestamp(current.created_at)}>
              {relative(current.created_at).toLowerCase()}
            </time>{' '}
            · {current.automatic ? 'Git push' : 'Manual run'} · configuration r
            {current.config_revision}
            {active && ' · checking every 10 seconds'}
          </p>
        </div>
        <div className="build-run-actions">
          {canWrite && active && remoteRun > 0 && (
            <Button size="sm" variant="ghost" onClick={() => setCancel(true)}>
              Cancel build
            </Button>
          )}
          {current.deployment_id && (
            <Button asChild size="sm">
              <Link
                to="/deployments/$deploymentId"
                params={{ deploymentId: current.deployment_id }}
              >
                Open deployment
              </Link>
            </Button>
          )}
          {canWrite && ready && (
            <PendingButton
              size="sm"
              variant="primary"
              pending={busy === 'plan'}
              pendingLabel="Preparing review…"
              onClick={() => void planDeployment()}
            >
              Review deployment
            </PendingButton>
          )}
        </div>
      </div>
      {error && !deploy && !cancel && <RequestError error={error} />}
      <dl className="build-output" aria-label="Build output">
        <div>
          <dt>Image</dt>
          <dd>
            {current.image ? (
              <span className="build-copy-value">
                <code>{current.image}</code>
                <Copy value={current.image} label="Copy image digest" iconOnly />
              </span>
            ) : (
              <span className="muted-text">No verified image</span>
            )}
          </dd>
        </div>
        <div>
          <dt>Registry</dt>
          <dd>
            {registry ? (
              <code>{registry}</code>
            ) : (
              <span className="muted-text">Known once an image is verified</span>
            )}
          </dd>
        </div>
        <div>
          <dt>Build log</dt>
          <dd>
            {runURL ? (
              <a href={runURL} target="_blank" rel="noreferrer">
                {runner}
                <Icon name="external" size={12} />
                <span className="sr-only"> (opens in a new tab)</span>
              </a>
            ) : (
              <span className="muted-text">Not returned by {providerName(current.provider)}</span>
            )}
          </dd>
        </div>
        <div>
          <dt>Deployment log</dt>
          <dd>
            {current.deployment_id ? (
              <Link
                to="/deployments/$deploymentId"
                params={{ deploymentId: current.deployment_id }}
              >
                Hakopod deployment
              </Link>
            ) : (
              <span className="muted-text">Not deployed</span>
            )}
          </dd>
        </div>
      </dl>
      {current.message && runState(current)[1] !== 'error' && <Note>{current.message}</Note>}
      <div className="inspect-log build-phases" aria-label="Build phases">
        {phases.map((phase) => {
          const expanded = isOpen(phase)
          const failed = phase.state[1] === 'error'
          return (
            <section key={phase.id} className="inspect-phase" data-phase={phase.id}>
              <h3>
                <button
                  type="button"
                  className="inspect-phase-header"
                  aria-expanded={expanded}
                  aria-controls={`build-phase-${current.id}-${phase.id}`}
                  onClick={() => setToggled((value) => ({ ...value, [phase.id]: !expanded }))}
                >
                  <Icon name={expanded ? 'down' : 'chevron'} size={14} />
                  <span>{phase.label}</span>
                  <StateLabel label={phase.state[0]} tone={phase.state[1]} />
                  {phase.id === 'source' && current.commit_sha && (
                    <span className="inspect-phase-meta">{current.commit_sha.slice(0, 9)}</span>
                  )}
                </button>
              </h3>
              {failed && phase.id === 'build' && (
                <div className="inspect-phase-error">
                  <strong>
                    {runner} reported {phase.state[0].toLowerCase()}
                  </strong>
                  <p>
                    {current.message ||
                      `No error message was returned. Open the ${runner} log for the failing step.`}
                  </p>
                </div>
              )}
              <div
                id={`build-phase-${current.id}-${phase.id}`}
                className="build-phase-body"
                hidden={!expanded}
              >
                {phaseBody[phase.id]}
              </div>
            </section>
          )
        })}
      </div>
      <Dialog
        open={deploy}
        onOpenChange={(open) => {
          if (busy) return
          setDeploy(open)
          if (!open) {
            setPlan(null)
            setError('')
          }
        }}
        wide
        title={`Deploy image to ${build.name} / ${build.service}`}
        description={`Review the change to ${build.project} / ${build.environment} before deploying.`}
      >
        <div className="dialog-body build-dialog-body">
          <dl className="app-workspace-details build-facts">
            <Fact label="Service">
              {build.name} / {build.service}
              {build.reuse_services?.length ? ` and ${build.reuse_services.join(', ')}` : ''}
            </Fact>
            <Fact label="Application revision">
              {plan?.expected_revision === 0 ? 'New application' : `r${plan?.expected_revision}`} →
              r{(plan?.expected_revision || 0) + 1}
            </Fact>
            <Fact label="Build configuration">r{plan?.expected_config_revision}</Fact>
            <Fact label="Image">
              <code>{current.image}</code>
            </Fact>
          </dl>
          {plan && (
            <>
              {plan.warnings.map((warning) => (
                <Note key={warning}>{warning}</Note>
              ))}
              <DeploymentSecrets
                plan={plan}
                project={build.project}
                environment={build.environment}
                busy={Boolean(busy)}
                onBusy={(value) => setBusy(value ? 'deploy' : '')}
                onChange={(missing) =>
                  setPlan((value) => (value ? { ...value, missing_secrets: missing } : value))
                }
              />
              <DiffTable changes={plan.changes} />
            </>
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button
            disabled={Boolean(busy)}
            onClick={() => {
              setDeploy(false)
              setPlan(null)
            }}
          >
            Cancel
          </Button>
          <PendingButton
            variant="primary"
            pending={busy === 'deploy'}
            pendingLabel="Deploying…"
            disabled={Boolean(busy) || !plan || Boolean(plan.missing_secrets?.length)}
            onClick={async () => {
              if (!plan) return
              setBusy('deploy')
              setError('')
              try {
                const result = await unwrap(
                  client.POST('/builds/{id}/runs/{run}/deploy', {
                    params: { path: { id: build.id, run: runId } },
                    body: {
                      expected_revision: plan.expected_revision,
                      expected_config_revision: plan.expected_config_revision,
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
                  setPlan(null)
                }
              } finally {
                setBusy('')
              }
            }}
          >
            Deploy to {build.service}
          </PendingButton>
        </div>
      </Dialog>
      <Dialog
        open={cancel}
        onOpenChange={(open) => {
          if (busy) return
          setCancel(open)
          if (!open) setError('')
        }}
        title="Cancel this build?"
        description={`Ask ${runner} to stop the run for commit ${current.commit_sha.slice(0, 9) || 'not resolved'}. A verified image or accepted deployment is not removed.`}
      >
        <div className="dialog-body">{error && <RequestError error={error} />}</div>
        <div className="dialog-footer">
          <Button disabled={Boolean(busy)} onClick={() => setCancel(false)}>
            Keep building
          </Button>
          <PendingButton
            variant="danger"
            pending={busy === 'cancel'}
            pendingLabel="Cancelling…"
            onClick={async () => {
              setBusy('cancel')
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
                setBusy('')
              }
            }}
          >
            Cancel build
          </PendingButton>
        </div>
      </Dialog>
    </section>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}
