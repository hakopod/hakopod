import { DeploymentSecrets } from './deployment-secrets'
import { GitRepositoryField } from './git-repository-field'
import { GitConnectionField } from './git-connection-field'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { HeadingHelp, Copy, Empty, ErrorState, Loading, Note, RequestError } from './shared'
import { DiffTable } from './deploy-dialog'

import { FormPage, FormHint, FormSection } from './form-page'
import { ServiceIcon } from './service-icon'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Status } from './shared'

export default function ApplicationSource({ application }: { application: Application }) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
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
          <div className="hako-section-heading-title">
            <h2>Repository configuration source</h2>
            <HeadingHelp title="Repository configuration source">
              Fetch a committed Hakopod TOML file, review it, and deploy an immutable revision.
            </HeadingHelp>
          </div>
        </div>
        {scope.can('deployments:write') && (
          <Button
            disabled={source.isPending}
            onClick={() =>
              void navigate({
                to: '/applications/$applicationId/source',
                params: { applicationId: application.id },
              })
            }
          >
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
              <dt>Provider / repository</dt>
              <dd>
                <a
                  href={`https://${binding.provider === 'gitlab' ? 'gitlab.com' : 'github.com'}/${binding.repository}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  {binding.provider === 'gitlab' ? 'GitLab' : 'GitHub'} / {binding.repository}
                </a>
              </dd>
            </div>
            <div>
              <dt>Connection</dt>
              <dd>{binding.connection_id || `${binding.provider}-default`}</dd>
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
                  ? 'Enabled for authenticated push events'
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
          {binding.last_error && <ErrorState error={binding.last_error} />}
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
      {error && <RequestError error={error} />}
      {scope.can('deployments:write') && (
        <section className="panel service-summary-panel">
          <div className="section-toolbar">
            <div>
              <div className="hako-section-heading-title">
                <h2>Build this application from code</h2>
                <HeadingHelp title="Build this application from code">
                  Use a Dockerfile or Cloud Native Buildpacks to produce a verified service image.
                </HeadingHelp>
              </div>
            </div>
            <Button
              onClick={() =>
                void navigate({ to: '/builds/new', search: { application: application.id } })
              }
            >
              Configure source build
            </Button>
          </div>
        </section>
      )}
      <Note>
        Source review pins the fetched commit and source mapping revision. Private repositories
        require the installation’s matching Git provider connection.
      </Note>
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
              <DeploymentSecrets
                plan={plan}
                project={application.project}
                environment={application.environment}
                busy={busy}
                onBusy={setBusy}
                onChange={(missing) =>
                  setPlan((current) =>
                    current ? { ...current, missing_secrets: missing } : current,
                  )
                }
              />
              <DiffTable changes={plan.changes} />
            </>
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setPlan(null)}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy || !plan || Boolean(plan.missing_secrets?.length)}
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

export function SourceForm({
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
  const [provider, setProvider] = useState<'github' | 'gitlab'>(source?.provider || 'github')
  const [connectionId, setConnectionId] = useState(source?.connection_id || '')
  const [repository, setRepository] = useState(source?.repository || '')
  const [branch, setBranch] = useState(source?.branch || 'main')
  const [path, setPath] = useState(source?.path || 'hakopod.toml')
  const [automatic, setAutomatic] = useState(source?.auto_deploy || false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <FormPage
      breadcrumbs={[
        { label: 'Applications', to: `/projects/${encodeURIComponent(application.project)}` },
        { label: application.name, to: `/applications/${application.id}` },
        { label: 'Repository source' },
      ]}
      icon="branch"
      help={
        <FormHint title="A precise source">
          Choose the branch and exact TOML path. Fetching for review pins an immutable commit before
          deployment.
        </FormHint>
      }
      title="Configure repository source"
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
                  provider,
                  connection_id: connectionId,
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
        <div className="form-body auth-form">
          <FormSection
            title="Repository mapping"
            description="Paths are relative to the repository root."
            icon="branch"
          >
            <label>
              Git provider
              <SelectField
                label="Git provider"
                value={provider}
                onValueChange={(value) => {
                  setProvider(value as 'github' | 'gitlab')
                  setConnectionId('')
                }}
                options={[
                  {
                    value: 'github',
                    label: 'GitHub',
                  },
                  {
                    value: 'gitlab',
                    label: 'GitLab.com',
                  },
                ]}
              />
            </label>
            <GitConnectionField
              provider={provider}
              value={connectionId}
              onValueChange={setConnectionId}
            />
            <GitRepositoryField
              provider={provider}
              connectionId={connectionId}
              value={repository}
              onChange={setRepository}
              onBranchChange={setBranch}
            />
            <label>
              Branch
              <Input
                value={branch}
                onChange={(e) => setBranch(e.target.value)}
                maxLength={200}
                required
              />
            </label>
            <label>
              Configuration path
              <Input
                value={path}
                onChange={(e) => setPath(e.target.value)}
                maxLength={512}
                required
              />
            </label>
          </FormSection>
          <FormSection
            title="Automatic deployment"
            description="Enable only after configuring the matching webhook."
            icon="refresh"
          >
            <label className="checkbox-row">
              <Input
                type="checkbox"
                checked={automatic}
                onChange={(e) => setAutomatic(e.target.checked)}
              />
              Deploy automatically on authenticated pushes to this branch
            </label>
            {automatic && (
              <Note>
                Every matching authenticated push will fetch and deploy its configuration with your
                current authority. Configure the repository webhook through your administrator.
              </Note>
            )}
          </FormSection>
          {error && <RequestError error={error} />}
        </div>
        <div className="form-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            Save source mapping
          </Button>
        </div>
      </form>
    </FormPage>
  )
}

export function GitHubSettings() {
  return (
    <>
      <div className="provider-section-title">
        <div className="hako-section-heading-title">
          <h2>Git providers</h2>
          <HeadingHelp title="Git providers">
            Connect repository access and signed webhooks for automatic deployments.
          </HeadingHelp>
        </div>
      </div>
      <div className="integration-grid">
        {(['github', 'gitlab'] as const).map((provider) => (
          <GitProviderCard key={provider} provider={provider} />
        ))}
      </div>
    </>
  )
}
function GitProviderCard({ provider }: { provider: 'github' | 'gitlab' }) {
  const connection = useQuery({
    queryKey: ['git-settings', provider],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET(provider === 'github' ? '/integrations/github' : '/integrations/gitlab', {
          signal,
        }),
      ),
    gcTime: 0,
  })
  const ready = connection.data?.configured && connection.data?.token_configured
  return (
    <Link
      to="/settings/git/connections/new"
      search={{ provider }}
      className="panel integration-card interactive"
    >
      <Brackets />
      <ServiceIcon name={provider} size={32} />
      <div>
        <h2>{provider === 'github' ? 'GitHub' : 'GitLab'}</h2>
        <p>Repository access, build workflows, and authenticated push events.</p>
      </div>
      <div className="integration-state">
        <Status
          value={
            connection.isPending
              ? 'loading'
              : connection.error
                ? 'unavailable'
                : ready
                  ? 'configured'
                  : 'setup needed'
          }
        />
      </div>
    </Link>
  )
}
export function GitConnectionSettings({ provider }: { provider: 'github' | 'gitlab' }) {
  const label = provider === 'github' ? 'GitHub' : 'GitLab'
  const endpoint = provider === 'github' ? '/integrations/github' : '/integrations/gitlab'
  const [token, setToken] = useState('')
  const [webhook, setWebhook] = useState('')
  const [created, setCreated] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const connection = useQuery({
    queryKey: ['git-settings', provider],
    queryFn: ({ signal }) => unwrap(client.GET(endpoint, { signal })),
    gcTime: 0,
  })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>{label} integration</h2>
            <HeadingHelp title={`${label} integration`}>
              Repository access and authenticated push events for connected applications.
            </HeadingHelp>
          </div>
        </div>
      </div>
      {connection.isPending ? (
        <Loading rows={2} />
      ) : connection.error ? (
        <ErrorState error={connection.error} />
      ) : (
        <section className="panel service-summary-panel git-connection-panel">
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
              <dt>{label} webhook URL</dt>
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
                  client.PUT(endpoint, { body: { token, webhook_secret: webhook } }),
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
              {label} personal access token
              <Input
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
              <Input
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
            {error && <RequestError error={error} />}
            {saved && <Note>{label} connection saved.</Note>}
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
              {busy ? 'Saving…' : `Save ${label} connection`}
            </Button>
          </form>
        </section>
      )}
      <Note>
        {provider === 'gitlab'
          ? 'Use the publicly reachable dashboard webhook URL above for GitLab Push Hook and Pipeline Hook events. Set the saved webhook secret as the GitLab Secret token. The access token needs permission to read the connected repository.'
          : 'Use the publicly reachable dashboard webhook URL above for JSON push and workflow_run events. The token must have access to the selected repositories; workflow installation also needs permission to write workflow files.'}
      </Note>
    </>
  )
}
