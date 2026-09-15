import { useEffect, useState, type ReactNode } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Trash2 } from 'lucide-react'
import type { components } from '../lib/api.generated'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import {
  authNames,
  gitNames,
  useGitConnections,
  type GitConnection,
  type GitProvider,
} from '../lib/git-connections'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Dialog } from './ui/dialog'
import { Copy, Empty, ErrorState, HeadingHelp, Loading, Note, Status } from './shared'
import { FormPage, FormHint, FormSection } from './form-page'
import { InstallationReviewRows, useInstallationFormFocus } from './installation-form-fields'
import { ServiceIcon } from './service-icon'
import { GitOAuthAuthorize, GitWebhookAddress } from './git-oauth'
import { GitHubAppSetup, GitLabRegistrationCallback } from './git-app-setup'

function AccessRequired() {
  return (
    <Empty
      title="Repository management access required"
      description="A workspace owner or installation administrator manages repository connections."
    />
  )
}
export function GitConnectionsPanel() {
  const scope = useScope()
  const query = useGitConnections()
  if (!(scope.identity.admin || scope.identity.can_manage_git)) return <AccessRequired />
  return (
    <>
      <div className="section-toolbar">
        <div className="hako-section-heading-title">
          <h2>Git connections</h2>
          <HeadingHelp title="Git connections">
            Keep sign-in and repository access separate. Select a named connection for each
            application source or build.
          </HeadingHelp>
        </div>
        <Button asChild variant="primary">
          <Link to="/settings/git/connections/new">Add connection</Link>
        </Button>
      </div>
      {query.isPending ? (
        <Loading />
      ) : query.error ? (
        <ErrorState error={query.error} />
      ) : (
        <div className="integration-grid">
          {query.data.items.map((item) => (
            <Link
              key={item.id}
              to={
                item.legacy
                  ? '/settings/git/connections/new'
                  : '/settings/git/connections/$connectionId'
              }
              params={item.legacy ? {} : { connectionId: item.id }}
              search={item.legacy ? { provider: item.provider } : {}}
              className="panel integration-card interactive"
            >
              <ServiceIcon name={item.provider} size={32} />
              <div className="min-w-0">
                <h3 className="break-words">{item.legacy ? gitNames[item.provider] : item.name}</h3>
                <p>
                  {item.legacy
                    ? item.provider === 'github'
                      ? 'GitHub App'
                      : 'GitLab OAuth App'
                    : `${gitNames[item.provider]} · ${authNames[item.auth_kind]}`}
                </p>
                <p>
                  {item.legacy
                    ? item.configured
                      ? 'Set up an App connection. Your existing connection stays active.'
                      : 'Connect an App owned by your account or organization.'
                    : item.account || 'Select this connection in an application or build'}
                </p>
              </div>
              <div className="integration-state">
                {item.legacy ? (
                  <span className="field-help shrink-0">Set up</span>
                ) : (
                  <Status
                    value={
                      item.status === 'ready' ? 'configured' : item.status.replaceAll('_', ' ')
                    }
                  />
                )}
              </div>
            </Link>
          ))}
        </div>
      )}
      {query.data?.items.length === 0 && (
        <Empty
          title="No Git connections"
          description="Add a GitHub or GitLab connection to deploy from your repositories."
        />
      )}
    </>
  )
}
export function GitConnectionEditor({
  id,
  initialProvider = 'github',
}: {
  id?: string
  initialProvider?: GitProvider
}) {
  const scope = useScope()
  const query = useQuery({
    queryKey: ['git-connection', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/git/connections/{id}', { signal, params: { path: { id: id! } } })),
    enabled: (scope.identity.admin || scope.identity.can_manage_git) && Boolean(id),
    retry: false,
    gcTime: 0,
  })
  const navigate = useNavigate()
  const legacyProvider = query.data?.legacy ? query.data.provider : undefined
  // Keep the resolved redirect stable while query and route state update.
  useEffect(() => {
    if ((scope.identity.admin || scope.identity.can_manage_git) && legacyProvider) {
      void navigate({
        to: '/settings/git/connections/new',
        search: { provider: legacyProvider },
        replace: true,
      })
    }
  }, [navigate, legacyProvider, scope.identity.admin || scope.identity.can_manage_git])
  if (!(scope.identity.admin || scope.identity.can_manage_git)) return <AccessRequired />
  if (id && query.isPending) return <Loading />
  if (id && query.error) return <ErrorState error={query.error} />
  if (legacyProvider) return <Loading />
  if (!id) return <NewGitConnection initialProvider={initialProvider} />
  return <GitConnectionForm key={id} current={query.data} />
}
function NewGitConnection({ initialProvider }: { initialProvider: GitProvider }) {
  const [provider, setProvider] = useState<GitProvider>(initialProvider)
  const picker = (
    <FormSection title="Provider">
      <div className="max-w-64">
        <SelectField
          label="Git provider"
          value={provider}
          onValueChange={(v) => setProvider(v as GitProvider)}
          options={[
            { value: 'github', label: 'GitHub' },
            { value: 'gitlab', label: 'GitLab' },
          ]}
        />
      </div>
    </FormSection>
  )
  return provider === 'github' ? (
    <FormPage
      title="Add Git connection"
      description="Install an App owned by your GitHub account."
      breadcrumbs={[]}
    >
      {picker}
      <GitHubAppSetup />
    </FormPage>
  ) : (
    <GitConnectionForm providerPicker={picker} />
  )
}
function GitConnectionForm({
  current,
  providerPicker,
}: {
  current?: GitConnection
  providerPicker?: ReactNode
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [baseRevision] = useState(current?.revision)
  const [name, setName] = useState(current?.name || '')
  const [provider, setProvider] = useState<GitProvider>(current?.provider || 'gitlab')
  const [kind, setKind] = useState<GitConnection['auth_kind']>(current?.auth_kind || 'gitlab_oauth')
  const [clientID, setClientID] = useState(current?.oauth_client_id || '')
  const [clientSecret, setClientSecret] = useState('')
  const [oauthScopes, setOAuthScopes] = useState<'api' | 'read_api'>(
    current?.oauth_scopes === 'read_api' ? 'read_api' : 'api',
  )
  const [enabled, setEnabled] = useState(current?.enabled ?? true)
  const [token, setToken] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [webhook, setWebhook] = useState('')
  const [appID, setAppID] = useState(current?.github_app_id ? String(current.github_app_id) : '')
  const [installationID, setInstallationID] = useState(
    current?.installation_id ? String(current.installation_id) : '',
  )
  const [replaceCredential, setReplaceCredential] = useState(!current)
  const [replaceWebhook, setReplaceWebhook] = useState(false)
  const [review, setReview] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const [saved, setSaved] = useState<GitConnection | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const pendingApp = current?.managed_app && current.installation_id === 0
  const formRef = useInstallationFormFocus(review)
  const back = () => void navigate({ to: '/settings', search: { tab: 'github' } })
  function input(): components['schemas']['GitConnectionInput'] {
    const bytes = (value: string) => new TextEncoder().encode(value).length
    if (!name.trim() || bytes(name.trim()) > 80 || /[\r\n\0]/.test(name))
      throw new Error('Use a connection name of 1–80 bytes without line breaks.')
    if (
      replaceCredential &&
      !(kind === 'token' ? token : kind === 'gitlab_oauth' ? clientSecret : privateKey)
    )
      throw new Error('Enter the connection credential.')
    if (
      kind === 'gitlab_oauth' &&
      (!clientID.trim() ||
        bytes(clientID) > 1024 ||
        bytes(clientSecret) > 4096 ||
        /[\r\n\0]/.test(clientID + clientSecret))
    )
      throw new Error('Enter a client ID and a bounded client secret without line breaks.')
    if (bytes(token) > 16384 || bytes(privateKey) > 16384 || /[\r\n\0]/.test(token))
      throw new Error('The credential exceeds its limit or contains invalid characters.')
    if (replaceWebhook && (bytes(webhook) < 32 || bytes(webhook) > 256 || /[\r\n\0]/.test(webhook)))
      throw new Error('Use a webhook secret of 32–256 bytes without line breaks.')
    if (
      kind === 'github_app' &&
      (!/^\d+$/.test(appID) ||
        !/^\d+$/.test(installationID) ||
        !Number.isSafeInteger(Number(installationID)) ||
        Number(installationID) < 1)
    )
      throw new Error('Enter the numeric GitHub App and installation IDs.')
    return {
      name: name.trim(),
      provider,
      auth_kind: kind,
      enabled,
      ...(current ? { expected_revision: baseRevision! } : {}),
      ...(replaceCredential
        ? kind === 'token'
          ? { token }
          : kind === 'gitlab_oauth'
            ? { oauth_client_secret: clientSecret }
            : { github_private_key: privateKey }
        : {}),
      ...(kind === 'gitlab_oauth'
        ? { oauth_client_id: clientID.trim(), oauth_scopes: oauthScopes }
        : {}),
      ...(replaceWebhook ? { webhook_secret: webhook } : {}),
      ...(kind === 'github_app'
        ? { github_app_id: appID, github_installation_id: Number(installationID) }
        : {}),
    }
  }
  if (saved)
    return (
      <FormPage
        title="Connection saved"
        description="Select this connection when configuring an application source or build."
        breadcrumbs={[]}
      >
        <FormSection title={saved.name}>
          <InstallationReviewRows
            rows={[
              ['Provider', gitNames[saved.provider]],
              ['Status', saved.status],
              ['Webhook URL', <GitWebhookAddress path={saved.webhook_path} />],
            ]}
          />
          {saved.webhook_secret && (
            <div className="grid gap-3">
              <Note>
                Copy this webhook secret now. It will not be shown again. Add it to the provider
                webhook with this URL before enabling automatic deployments.
              </Note>
              <div className="flex min-w-0 items-center gap-3">
                <code className="break-all">{saved.webhook_secret}</code>
                <Copy value={saved.webhook_secret} />
              </div>
            </div>
          )}
          {saved.auth_kind === 'gitlab_oauth' && <GitOAuthAuthorize connection={saved} />}
        </FormSection>
        <div className="form-footer">
          <Button variant="primary" onClick={back}>
            Done
          </Button>
        </div>
      </FormPage>
    )
  return (
    <FormPage
      title={
        review ? 'Review Git connection' : current ? `Edit ${current.name}` : 'Add Git connection'
      }
      breadcrumbs={[]}
      description="Shared repository credentials stay encrypted on the server."
      help={
        !pendingApp && (
          <>
            <FormHint title="Choose repository access">
              A connection can serve several applications. Restrict its GitHub or GitLab permissions
              to the repositories you need.
            </FormHint>
            <FormHint title="App ownership">
              New GitHub connections use App registration on GitHub. GitLab requires you to register
              a personal or group OAuth application before authorizing repository access.
            </FormHint>
            <FormHint title="Webhooks">
              Each connection has a signed webhook path. Installations of the same GitHub App share
              that app’s webhook secret.
            </FormHint>
          </>
        )
      }
    >
      {providerPicker}
      {pendingApp && (
        <GitHubAppSetup
          id={current.id}
          footerActions={
            <>
              <Button
                type="button"
                aria-label={`Delete ${current.name}`}
                onClick={() => setDeleting(true)}
              >
                <Trash2 size={16} aria-hidden="true" />
              </Button>
              <Button type="button" onClick={back}>
                Cancel
              </Button>
            </>
          }
        />
      )}
      {!current && (
        <FormSection title="Register your GitLab application">
          <p className="field-help">
            GitLab does not offer automated registration for user-owned OAuth apps. Create an
            application in your personal settings or your group’s Settings → Applications, then
            enter its application ID and secret below.
          </p>
          <p className="field-help">
            Use the exact callback URL below, enable Confidential, and choose api for builds or
            read_api for source-only access. Repository authorization happens on GitLab after
            saving.
          </p>
          <GitLabRegistrationCallback />
          <Button asChild>
            <a
              href="https://gitlab.com/-/user_settings/applications"
              target="_blank"
              rel="noreferrer"
            >
              Create app on GitLab
            </a>
          </Button>
        </FormSection>
      )}
      {current?.auth_kind === 'gitlab_oauth' && (
        <FormSection title="GitLab account">
          <GitOAuthAuthorize connection={current} />
        </FormSection>
      )}
      <form
        hidden={Boolean(pendingApp)}
        ref={formRef}
        tabIndex={-1}
        aria-label={review ? 'Review Git connection' : 'Git connection settings'}
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy || conflict) return
          setError('')
          try {
            const body = input()
            if (!review) {
              setReview(true)
              return
            }
            setBusy(true)
            const result = current
              ? await unwrap(
                  client.PUT('/git/connections/{id}', {
                    params: { path: { id: current.id } },
                    body,
                  }),
                )
              : await unwrap(client.POST('/git/connections', { body }))
            setToken('')
            setClientSecret('')
            setPrivateKey('')
            setWebhook('')
            setSaved(result)
            void cache.invalidateQueries({ queryKey: ['git-connections'] })
            void cache.invalidateQueries({ queryKey: ['git-connection', current?.id] })
          } catch (cause) {
            setError(message(cause))
            if (
              cause instanceof APIError &&
              ['git_connection_conflict', 'git_connection_changed', 'conflict'].includes(
                cause.code || '',
              )
            )
              setConflict(true)
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="form-body auth-form" hidden={Boolean(pendingApp)}>
          {review ? (
            <FormSection title="Changes">
              <InstallationReviewRows
                rows={[
                  ['Name', name],
                  ['Provider', gitNames[provider]],
                  ['Authentication', authNames[kind]],
                  ['Connection', enabled ? 'Enabled' : 'Disabled'],
                  ...(kind === 'gitlab_oauth'
                    ? ([
                        ['Client ID', clientID],
                        [
                          'Permissions',
                          oauthScopes === 'api'
                            ? 'Repository access and builds'
                            : 'Read repository configuration',
                        ],
                      ] as [string, string][])
                    : []),
                  ...(kind === 'github_app'
                    ? ([
                        ['App ID', appID],
                        ['Installation ID', installationID],
                      ] as [string, string][])
                    : []),
                  [
                    'Credential',
                    replaceCredential ? 'Use the entered credential' : 'Keep the stored credential',
                  ],
                  [
                    'Webhook secret',
                    replaceWebhook
                      ? 'Replace with entered secret'
                      : current
                        ? 'Keep stored secret'
                        : 'Generate a secret and show it once',
                  ],
                ]}
              />
              <Note>
                Applications using this connection will use these credentials for future source
                requests and builds.
              </Note>
            </FormSection>
          ) : (
            <>
              <FormSection title="Connection">
                <label>
                  Name
                  <Input
                    required
                    value={name}
                    maxLength={80}
                    onChange={(e) => setName(e.target.value)}
                  />
                </label>
                {current && (
                  <>
                    <label>
                      Provider
                      <SelectField
                        label="Provider"
                        value={provider}
                        disabled={Boolean(current)}
                        onValueChange={(value) => {
                          setProvider(value as GitProvider)
                          setKind('token')
                          setToken('')
                          setPrivateKey('')
                        }}
                        options={[
                          { value: 'github', label: 'GitHub' },
                          { value: 'gitlab', label: 'GitLab' },
                        ]}
                      />
                    </label>
                    <label>
                      Authentication
                      <SelectField
                        label="Authentication"
                        value={kind}
                        disabled={Boolean(current)}
                        onValueChange={(value) => {
                          setKind(value as GitConnection['auth_kind'])
                          setClientSecret('')
                          setToken('')
                          setPrivateKey('')
                        }}
                        options={
                          provider === 'github'
                            ? [
                                { value: 'token', label: 'Access token' },
                                { value: 'github_app', label: 'GitHub App' },
                              ]
                            : [
                                { value: 'token', label: 'Access token' },
                                { value: 'gitlab_oauth', label: 'GitLab OAuth' },
                              ]
                        }
                      />
                    </label>
                  </>
                )}
                <label className="checkbox-label">
                  <Input
                    type="checkbox"
                    checked={enabled}
                    onChange={(e) => setEnabled(e.target.checked)}
                  />
                  Enable connection
                </label>
              </FormSection>
              {!current?.managed_app && (
                <FormSection title="Credentials">
                  {kind === 'gitlab_oauth' && (
                    <>
                      <label>
                        OAuth application ID
                        <Input
                          required
                          value={clientID}
                          maxLength={1024}
                          readOnly={Boolean(current)}
                          onChange={(e) => setClientID(e.target.value)}
                        />
                      </label>
                      <label>
                        OAuth permissions
                        <SelectField
                          label="OAuth permissions"
                          value={oauthScopes}
                          onValueChange={(value) => setOAuthScopes(value as 'api' | 'read_api')}
                          options={[
                            { value: 'api', label: 'Repository access and builds (api)' },
                            {
                              value: 'read_api',
                              label: 'Read repository configuration (read_api)',
                            },
                          ]}
                        />
                      </label>
                      {current && (
                        <>
                          <p className="field-help">
                            Register this exact callback URL in your GitLab OAuth application.
                          </p>
                          <GitLabRegistrationCallback />
                        </>
                      )}
                      <Note>
                        Changing permissions requires authorization again. OAuth grants the GitLab
                        user's access; choose a dedicated account when access should be limited.
                      </Note>
                    </>
                  )}
                  {kind === 'github_app' && (
                    <div className="form-grid-two">
                      <label>
                        App ID
                        <Input
                          value={appID}
                          required
                          inputMode="numeric"
                          maxLength={100}
                          readOnly={Boolean(current)}
                          onChange={(e) => setAppID(e.target.value)}
                        />
                      </label>
                      <label>
                        Installation ID
                        <Input
                          value={installationID}
                          required
                          inputMode="numeric"
                          readOnly={Boolean(current)}
                          onChange={(e) => setInstallationID(e.target.value)}
                        />
                      </label>
                    </div>
                  )}
                  {current && (
                    <label className="checkbox-label">
                      <Input
                        type="checkbox"
                        checked={replaceCredential}
                        onChange={(e) => {
                          setReplaceCredential(e.target.checked)
                          setToken('')
                          setClientSecret('')
                          setPrivateKey('')
                        }}
                      />
                      Replace stored credential
                    </label>
                  )}
                  {replaceCredential &&
                    (kind === 'token' || kind === 'gitlab_oauth' ? (
                      <label>
                        {kind === 'gitlab_oauth' ? 'OAuth client secret' : 'Access token'}
                        <Input
                          type="password"
                          required
                          value={kind === 'gitlab_oauth' ? clientSecret : token}
                          maxLength={kind === 'gitlab_oauth' ? 4096 : 16384}
                          autoComplete="new-password"
                          onChange={(e) =>
                            kind === 'gitlab_oauth'
                              ? setClientSecret(e.target.value)
                              : setToken(e.target.value)
                          }
                        />
                      </label>
                    ) : (
                      <label>
                        Private key (PEM)
                        <textarea
                          required
                          className="min-h-40 resize-y font-mono text-xs"
                          value={privateKey}
                          maxLength={16384}
                          spellCheck={false}
                          autoComplete="off"
                          onChange={(e) => setPrivateKey(e.target.value)}
                        />
                      </label>
                    ))}
                  <p className="field-help">
                    Stored credentials are never returned to the browser. Repository access is
                    approved with your provider.
                  </p>
                </FormSection>
              )}
              {!current?.managed_app && (
                <FormSection title="Webhook">
                  <label className="checkbox-label">
                    <Input
                      type="checkbox"
                      checked={replaceWebhook}
                      onChange={(e) => {
                        setReplaceWebhook(e.target.checked)
                        setWebhook('')
                      }}
                    />
                    {current ? 'Replace webhook secret' : 'Use my own webhook secret'}
                  </label>
                  {replaceWebhook ? (
                    <label>
                      Webhook secret
                      <Input
                        type="password"
                        required
                        minLength={32}
                        maxLength={256}
                        value={webhook}
                        autoComplete="new-password"
                        onChange={(e) => setWebhook(e.target.value)}
                      />
                    </label>
                  ) : (
                    <p className="field-help">
                      {current
                        ? 'The stored webhook secret is retained.'
                        : 'A secret is generated when you save. Copy it from the confirmation page.'}
                    </p>
                  )}
                  {current && <GitWebhookAddress path={current.webhook_path} />}
                </FormSection>
              )}
              {current?.managed_app && (
                <FormSection title="GitHub App">
                  <p className="field-help">
                    Credentials and webhooks were configured during registration. Manage repository
                    access and App settings on GitHub.
                  </p>
                  <GitWebhookAddress path={current.webhook_path} />
                  <Button asChild>
                    <a
                      href="https://github.com/settings/installations"
                      target="_blank"
                      rel="noreferrer"
                    >
                      Manage on GitHub
                    </a>
                  </Button>
                </FormSection>
              )}
            </>
          )}
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
          {conflict && (
            <Note>
              The connection changed. Your draft is kept. Return to settings and reopen it to load
              the current revision.
            </Note>
          )}
        </div>
        <div className="form-footer">
          {current && (
            <Button
              type="button"
              aria-label={`Delete ${current.name}`}
              disabled={busy}
              onClick={() => setDeleting(true)}
            >
              <Trash2 size={16} aria-hidden="true" />
            </Button>
          )}
          <Button type="button" disabled={busy} onClick={review ? () => setReview(false) : back}>
            {review ? 'Back to edit' : 'Cancel'}
          </Button>
          {!pendingApp && (
            <Button type="submit" variant="primary" disabled={busy || conflict}>
              {busy ? 'Saving…' : review ? 'Save connection' : 'Review connection'}
            </Button>
          )}
        </div>
      </form>
      <Dialog
        open={deleting}
        onOpenChange={(value) => {
          if (!busy) setDeleting(value)
        }}
        title="Delete connection"
        description="Update applications or builds that use this connection first. Deleting the connection does not uninstall or delete the App on GitHub or GitLab."
      >
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (!current || busy || confirmation !== current.name) return
            setBusy(true)
            setError('')
            try {
              await unwrap(
                client.DELETE('/git/connections/{id}', {
                  params: {
                    path: { id: current.id },
                    query: { expected_revision: baseRevision! },
                  },
                }),
              )
              void cache.invalidateQueries({ queryKey: ['git-connections'] })
              back()
            } catch (cause) {
              setError(message(cause))
            } finally {
              setBusy(false)
            }
          }}
        >
          <div className="dialog-body">
            <label>
              Type {current?.name} to confirm
              <Input
                value={confirmation}
                onChange={(e) => setConfirmation(e.target.value)}
                autoComplete="off"
              />
            </label>
            {error && (
              <div className="inline-error" role="alert">
                {error}
              </div>
            )}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setDeleting(false)}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="danger"
              disabled={busy || confirmation !== current?.name}
            >
              Delete connection
            </Button>
          </div>
        </form>
      </Dialog>
    </FormPage>
  )
}
