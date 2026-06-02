import { lazy, Suspense, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Copy, Empty, ErrorState, Loading, Note } from './shared'
import { DiffTable } from './deploy-dialog'

const BuildForm = lazy(() => import('./build-form'))

export default function ApplicationSource({ application }: { application: Application }) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [edit, setEdit] = useState(false)
  const [buildOpen, setBuildOpen] = useState(false)
  const [plan, setPlan] = useState<components['schemas']['SourcePlan'] | null>(null)
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const source = useQuery({
    queryKey: ['application-source', application.id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/source', {
          signal,
          params: { path: { id: application.id } },
        }),
      ),
    gcTime: 0,
    refetchInterval: 30000,
    refetchIntervalInBackground: false,
  })
  const binding = source.data?.source
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>GitHub configuration source</h2>
          <p>Fetch a committed Hakopod TOML file, review it, and deploy an immutable revision.</p>
        </div>
        {scope.can('deployments:write') && (
          <Button disabled={source.isPending} onClick={() => setEdit(true)}>
            {binding ? 'Edit source' : 'Connect repository'}
          </Button>
        )}
      </div>
      {source.isPending ? (
        <Loading />
      ) : source.error ? (
        <ErrorState error={source.error} />
      ) : binding ? (
        <section className="panel service-summary-panel">
          <dl className="service-definition-list">
            <div>
              <dt>Repository</dt>
              <dd>
                <a
                  href={`https://github.com/${binding.repository}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  {binding.repository}
                </a>
              </dd>
            </div>
            <div>
              <dt>Branch / path</dt>
              <dd>
                <code>
                  {binding.branch} / {binding.path}
                </code>
              </dd>
            </div>
            <div>
              <dt>Automatic deployment</dt>
              <dd>
                {binding.auto_deploy
                  ? 'Enabled for signed push events'
                  : 'Off · Review and deploy manually'}
              </dd>
            </div>
            <div>
              <dt>Last commit</dt>
              <dd className="mono break-text">
                {binding.last_commit || 'No source deployment yet'}
              </dd>
            </div>
            <div>
              <dt>Mapping updated</dt>
              <dd>{timestamp(binding.updated_at)}</dd>
            </div>
          </dl>
          {binding.last_error && <Note>{binding.last_error}</Note>}
          {scope.can('deployments:write') && (
            <Button
              variant="primary"
              disabled={busy}
              onClick={async () => {
                setBusy(true)
                setError('')
                try {
                  setPlan(
                    await unwrap(
                      client.POST('/applications/{id}/source/plan', {
                        params: { path: { id: application.id } },
                        body: {},
                      }),
                    ),
                  )
                  setKey(crypto.randomUUID())
                } catch (err) {
                  setError(message(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              {busy ? 'Fetching source…' : 'Fetch and review changes'}
            </Button>
          )}
        </section>
      ) : (
        <Empty
          icon="branch"
          title="No repository connected"
          description="Connect a repository containing hakopod.toml. Its services must already reference container images."
        />
      )}
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      {scope.can('deployments:write') && (
        <section className="panel service-summary-panel">
          <div className="section-toolbar">
            <div>
              <h2>Build this application from code</h2>
              <p>
                Use a Dockerfile or Cloud Native Buildpacks to produce a verified service image.
              </p>
            </div>
            <Button onClick={() => setBuildOpen(true)}>Configure source build</Button>
          </div>
        </section>
      )}
      {buildOpen && (
        <Suspense fallback={<Loading />}>
          <BuildForm application={application} onClose={() => setBuildOpen(false)} />
        </Suspense>
      )}
      <Note>
        Source review pins the fetched commit and source mapping revision. Private repositories
        require the installation’s GitHub connection.
      </Note>
      {edit && (
        <SourceForm
          application={application}
          source={binding}
          onClose={() => setEdit(false)}
          onSaved={() => {
            setEdit(false)
            void source.refetch()
          }}
        />
      )}
      <Dialog
        open={Boolean(plan)}
        wide
        onOpenChange={(open) => {
          if (!busy && !open) setPlan(null)
        }}
        title="Review source deployment"
        description={`Deploy the reviewed commit to ${application.project} / ${application.environment}.`}
      >
        <div className="dialog-body">
          {plan && (
            <>
              <Note>
                Commit <code>{plan.commit_sha}</code> · Revision r{plan.expected_revision} → r
                {plan.expected_revision + 1}
              </Note>
              {plan.warnings.map((warning) => (
                <Note key={warning}>{warning}</Note>
              ))}
              <DiffTable changes={plan.changes} />
            </>
          )}
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setPlan(null)}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy || !plan}
            onClick={async () => {
              if (!plan || busy) return
              setBusy(true)
              setError('')
              try {
                const result = await unwrap(
                  client.POST('/applications/{id}/source/deploy', {
                    params: { path: { id: application.id }, header: { 'Idempotency-Key': key } },
                    body: {
                      expected_revision: plan.expected_revision,
                      expected_source_revision: plan.expected_source_revision,
                      commit_sha: plan.commit_sha,
                    },
                  }),
                )
                setPlan(null)
                void cache.invalidateQueries({ queryKey: ['application', application.id] })
                void navigate({
                  to: '/deployments/$deploymentId',
                  params: { deploymentId: result.id },
                })
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Submitting…' : 'Deploy reviewed commit'}
          </Button>
        </div>
      </Dialog>
    </>
  )
}

function SourceForm({
  application,
  source,
  onClose,
  onSaved,
}: {
  application: Application
  source?: components['schemas']['SourceBinding']
  onClose: () => void
  onSaved: () => void
}) {
  const [repository, setRepository] = useState(source?.repository || '')
  const [branch, setBranch] = useState(source?.branch || 'main')
  const [path, setPath] = useState(source?.path || 'hakopod.toml')
  const [automatic, setAutomatic] = useState(source?.auto_deploy || false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title="Configure GitHub source"
      description="Save the source mapping for this application."
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy) return
          setBusy(true)
          setError('')
          try {
            await unwrap(
              client.PUT('/applications/{id}/source', {
                params: { path: { id: application.id } },
                body: {
                  repository,
                  branch,
                  path,
                  auto_deploy: automatic,
                  expected_source_revision: source?.revision || 0,
                },
              }),
            )
            onSaved()
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body auth-form">
          <label>
            Repository
            <input
              value={repository}
              onChange={(e) => setRepository(e.target.value)}
              placeholder="owner/repository"
              maxLength={201}
              required
            />
          </label>
          <label>
            Branch
            <input
              value={branch}
              onChange={(e) => setBranch(e.target.value)}
              maxLength={200}
              required
            />
          </label>
          <label>
            Configuration path
            <input
              value={path}
              onChange={(e) => setPath(e.target.value)}
              maxLength={512}
              required
            />
          </label>
          <label className="checkbox-row">
            <input
              type="checkbox"
              checked={automatic}
              onChange={(e) => setAutomatic(e.target.checked)}
            />
            Deploy automatically on signed pushes to this branch
          </label>
          {automatic && (
            <Note>
              Every matching signed push will fetch and deploy its configuration with your current
              authority. Configure the repository webhook through your administrator.
            </Note>
          )}
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
            Save source mapping
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

export function GitHubSettings() {
  const [token, setToken] = useState('')
  const [webhook, setWebhook] = useState('')
  const [created, setCreated] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const connection = useQuery({
    queryKey: ['github-settings'],
    queryFn: ({ signal }) => unwrap(client.GET('/integrations/github', { signal })),
    gcTime: 0,
  })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>GitHub integration</h2>
          <p>Repository access and signed push events for connected applications.</p>
        </div>
      </div>
      {connection.isPending ? (
        <Loading rows={2} />
      ) : connection.error ? (
        <ErrorState error={connection.error} />
      ) : (
        <section className="panel service-summary-panel settings-narrow">
          <dl className="service-definition-list">
            <div>
              <dt>Repository credential</dt>
              <dd>
                {connection.data?.token_configured
                  ? 'Configured'
                  : 'Not configured · public repositories only'}
              </dd>
            </div>
            <div>
              <dt>Webhook signing</dt>
              <dd>{connection.data?.configured ? 'Configured' : 'Not configured'}</dd>
            </div>
            <div>
              <dt>GitHub webhook URL</dt>
              <dd>
                <span className="copyable-address">
                  <code>
                    {window.location.origin}
                    {connection.data?.webhook_path}
                  </code>
                  <Copy value={window.location.origin + (connection.data?.webhook_path || '')} />
                </span>
              </dd>
            </div>
          </dl>
          <form
            className="auth-form"
            onSubmit={async (e) => {
              e.preventDefault()
              if (busy) return
              setBusy(true)
              setError('')
              setSaved(false)
              try {
                const result = await unwrap(
                  client.PUT('/integrations/github', { body: { token, webhook_secret: webhook } }),
                )
                setCreated(result.webhook_secret || '')
                setToken('')
                setWebhook('')
                setSaved(true)
                void connection.refetch()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            <label>
              GitHub personal access token
              <input
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder={
                  connection.data?.token_configured
                    ? 'Leave blank to keep the saved token'
                    : 'Optional for public repository reads'
                }
                autoComplete="off"
                maxLength={1024}
              />
            </label>
            <label>
              Webhook signing secret
              <input
                type="password"
                value={webhook}
                onChange={(e) => setWebhook(e.target.value)}
                placeholder={
                  connection.data?.configured
                    ? 'Leave blank to keep the saved secret'
                    : 'Leave blank to generate a secret'
                }
                minLength={32}
                maxLength={256}
                autoComplete="off"
              />
            </label>
            {error && (
              <div className="inline-error" role="alert">
                {error}
              </div>
            )}
            {saved && <Note>GitHub connection saved.</Note>}
            {created && (
              <>
                <Note>Copy this webhook secret now. It will not be shown again.</Note>
                <div className="secret-once">
                  <code>{created}</code>
                  <Copy value={created} />
                </div>
                <Button type="button" onClick={() => setCreated('')}>
                  I saved the webhook secret
                </Button>
              </>
            )}
            <Button type="submit" variant="primary" disabled={busy || Boolean(created)}>
              {busy ? 'Saving…' : 'Save GitHub connection'}
            </Button>
          </form>
        </section>
      )}
      <Note>
        Use the publicly reachable dashboard webhook URL above for JSON push and workflow_run
        events. The token must have access to the selected repositories; workflow installation also
        needs permission to write workflow files.
      </Note>
    </>
  )
}
