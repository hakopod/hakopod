import { dashboardEdition, useEditionFeatures } from '../lib/dashboard-edition'
import { AuditHistory } from '../components/audit-history'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { Fragment, lazy, Suspense, useState } from 'react'
import { createFileRoute, Outlet, useLocation, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { message, relative, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { executionGrants, toggleGrant } from '../lib/agent-grants'
import { keyLifetimeInput, keyLifetimeOptions } from '../lib/key-lifetime'
import type { APIKey } from '../lib/types'
import { useScope } from '../lib/scope'
import { useInstallationAccess } from '../lib/installation-settings'
import { useActiveSection } from '../lib/use-active-section'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import {
  CopyValue,
  DiscardPrompt,
  focusedDialog,
  plural,
  ReviewFacts,
  ReviewNotice,
} from '../components/operation-review'
import {
  SettingsEmpty,
  SettingsRow,
  SettingsRows,
  SettingsSection,
  SettingsState,
} from '../components/settings-section'
import {
  PageHeader,
  ErrorState,
  Loading,
  Note,
  RequestError,
} from '../components/shared'
const AppearanceSettings = lazy(() =>
  import('../components/appearance-settings').then((m) => ({
    default: m.AppearanceSettings,
  })),
)
const AccountSettings = lazy(() => import('../components/account-settings'))
const LicenseSettings = lazy(() => import('../components/license-settings'))
const TeamSettings = lazy(() => import('../components/team-settings'))
const GitConnectionsPanel = lazy(() =>
  import('../components/git-connections').then((module) => ({
    default: module.GitConnectionsPanel,
  })),
)
const LoginProviderSettings = lazy(() =>
  import('../components/login-provider-settings').then((module) => ({
    default: module.LoginProviderSettingsPanel,
  })),
)
const SMTPSettings = lazy(() =>
  import('../components/smtp-settings').then((module) => ({
    default: module.SMTPSettingsPanel,
  })),
)
const AppLogSettings = lazy(() => import('../components/app-log-settings'))
const ProxySettings = lazy(() => import('../components/proxy-settings'))
const SecretProviders = lazy(() =>
  import('../components/secret-providers').then((module) => ({
    default: module.SecretProviders,
  })),
)
const PrivateDestinations = lazy(() =>
  import('../components/private-destinations').then((module) => ({
    default: module.PrivateDestinations,
  })),
)
const DNSProviders = lazy(() =>
  import('./settings.dns-providers').then((module) => ({
    default: module.DNSProviders,
  })),
)
const SlackSettingsPanel = lazy(() =>
  import('../components/slack-settings').then((m) => ({ default: m.SlackSettingsPanel })),
)
const InstallationUsers = lazy(() =>
  import('../components/team-settings').then((m) => ({
    default: m.InstallationUsers,
  })),
)

export const Route = createFileRoute('/settings')({
  validateSearch: (search: Record<string, unknown>): { tab?: string; saved?: string } => ({
    // A provider task page returns here with the saved name so the list can confirm it.
    saved:
      typeof search.saved === 'string' &&
      search.saved.length > 0 &&
      search.saved.length <= 80 &&
      !/[\x00-\x1f]/.test(search.saved)
        ? search.saved
        : undefined,
    tab:
      typeof search.tab === 'string' &&
      [
        'account',
        'teams',
        'users',
        'github',
        'keys',
        'audit',
        'appearance',
        'license',
        'secret-providers',
        'dns-providers',
        'login-providers',
        'smtp',
        'edge',
        'logs',
        'integrations',
        'private-destinations',
      ].includes(search.tab)
        ? search.tab
        : undefined,
  }),
  component: AdministrationRoute,
})
function AdministrationRoute() {
  return useLocation().pathname === '/settings' ? <Administration /> : <Outlet />
}
function Administration() {
  const features = useEditionFeatures()
  const scope = useScope()
  const installation = useInstallationAccess()
  const search = Route.useSearch()
  const selected = search.tab || 'account'
  const sections = [
    { id: 'account', label: 'Account security', group: 'Personal' },
    { id: 'appearance', label: 'Appearance', group: 'Personal' },
    { id: 'teams', label: 'Teams & access', group: 'Workspace' },
    { id: 'license', label: 'License & features', group: 'Workspace' },
    {
      id: 'integrations',
      label: 'Integrations',
      group: dashboardEdition.cloud ? 'Workspace' : 'Installation',
    },
    ...(scope.identity.can_manage_keys && !scope.identity.admin
      ? [{ id: 'keys', label: 'API keys', group: 'Workspace' }]
      : []),
    ...(scope.identity.admin || scope.identity.can_manage_git
      ? [
          {
            id: 'github',
            label: 'Git providers',
            group: scope.identity.admin ? 'Installation' : 'Workspace',
          },
        ]
      : []),
    ...(scope.identity.admin
      ? [
          { id: 'users', label: 'People', group: 'Installation' },
          {
            id: 'secret-providers',
            label: 'Secret providers',
            group: 'Installation',
          },
          {
            id: 'private-destinations',
            label: 'Private destinations',
            group: 'Installation',
          },
          {
            id: 'dns-providers',
            label: 'DNS providers',
            group: 'Installation',
          },
          { id: 'keys', label: 'API keys', group: 'Installation' },
          { id: 'audit', label: 'Audit events', group: 'Installation' },
          { id: 'logs', label: 'Logs', group: 'Installation' },
          ...(installation.allowed
            ? [
                {
                  id: 'login-providers',
                  label: 'Sign-in providers',
                  group: 'Installation',
                },
                { id: 'smtp', label: 'Email delivery', group: 'Installation' },
                { id: 'edge', label: 'Hakopod Edge', group: 'Installation' },
              ]
            : []),
        ]
      : []),
  ]
  const visibleSections = sections.filter((section) =>
    dashboardEdition.settings(section.id, features),
  )
  const tab = visibleSections.some((section) => section.id === selected) ? selected : 'account'
  const navigationRoot = useActiveSection(tab, '.app-settings-nav')
  return (
    <div className="settings-page" ref={navigationRoot}>
      <PageHeader title={features.operator ? 'Installation settings' : 'Settings'} />
      <div className="app-settings settings-hub">
        <nav className="app-settings-nav settings-hub-nav" aria-label="Settings sections">
          {visibleSections.map((section, index) => (
            <Fragment key={section.id}>
              {section.group !== visibleSections[index - 1]?.group && (
                <span className="settings-hub-group">{section.group}</span>
              )}
              <Link
                to="/settings"
                search={{ tab: section.id }}
                className="app-settings-link interactive"
                aria-current={tab === section.id ? 'page' : undefined}
              >
                {section.label}
              </Link>
            </Fragment>
          ))}
        </nav>
        <div className="app-settings-panel settings-hub-panel">
          <Suspense fallback={<Loading />}>
            {tab === 'account' && <AccountSettings />}
            {tab === 'appearance' && <AppearanceSettings />}
            {tab === 'teams' && <TeamSettings />}
            {tab === 'license' && <LicenseSettings />}
            {tab === 'integrations' && <SlackSettingsPanel />}
            {(scope.identity.admin || scope.identity.can_manage_keys) && tab === 'keys' && <Keys />}
            {(scope.identity.admin || scope.identity.can_manage_git) && tab === 'github' && (
              <GitConnectionsPanel />
            )}
            {scope.identity.admin && (
              <>
                {tab === 'users' && <InstallationUsers />}
                {tab === 'secret-providers' && <SecretProviders saved={search.saved} />}
                {tab === 'private-destinations' && <PrivateDestinations />}
                {tab === 'dns-providers' && <DNSProviders saved={search.saved} />}
                {tab === 'audit' && <AuditLog />}
                {tab === 'logs' && <AppLogSettings />}
                {installation.allowed && tab === 'login-providers' && <LoginProviderSettings />}
                {installation.allowed && tab === 'smtp' && <SMTPSettings />}
                {installation.allowed && tab === 'edge' && <ProxySettings />}
              </>
            )}
          </Suspense>
        </div>
      </div>
    </div>
  )
}

function Keys() {
  const scope = useScope()
  const queryClient = useQueryClient()
  const [createOpen, setCreateOpen] = useState(false)
  const [rotation, setRotation] = useState<APIKey | null>(null)
  const [revoke, setRevoke] = useState<APIKey | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const keys = useQuery({
    queryKey: ['keys', scope.project, scope.environment],
    queryFn: ({ signal }) => unwrap(client.GET('/keys', { signal })),
    staleTime: 30000,
  })
  const items = keys.data?.items || []
  const active = items.filter((key) => keyState(key).tone === 'ok').length
  const create = (
    <Button variant="primary" asChild>
      <Link to="/settings/keys/new" search={{ project: scope.project, environment: scope.environment }}>
        <Icon name="plus" size={15} />
        Create API key
      </Link>
    </Button>
  )
  return (
    <SettingsSection
      title="API keys"
      help="Give each CLI or CI workflow its own key, scope and expiry. Keys never receive Kubernetes credentials."
      state={
        keys.data
          ? items.length
            ? `${plural(active, 'active key')}${items.length > active ? `, ${items.length - active} revoked or expired` : ''}`
            : 'No keys yet'
          : undefined
      }
      actions={items.length > 0 && create}
    >
      {keys.isPending ? (
        <Loading />
      ) : keys.error ? (
        <ErrorState error={keys.error} retry={() => void keys.refetch()} />
      ) : !items.length ? (
        <SettingsEmpty title="No API keys" action={create}>
          CLI and CI deployments need a scoped key.
        </SettingsEmpty>
      ) : (
        <SettingsRows label="API keys">
          {items.map((key) => {
            const state = keyState(key)
            return (
              <SettingsRow
                key={key.id}
                muted={Boolean(key.revoked_at)}
                mark={<Icon name="key" size={16} />}
                name={key.name}
                detail={
                  <>
                    <code>{key.prefix || key.id.slice(0, 8)}…</code> · {keyScope(key)}
                    <span className="block">{key.permissions.join(', ')}</span>
                  </>
                }
                status={<SettingsState tone={state.tone}>{state.label}</SettingsState>}
                meta={
                  <>
                    <span>
                      {key.revoked_at
                        ? `Revoked ${timestamp(key.revoked_at)}`
                        : key.never_expires && key.expires_at === null
                          ? 'Never expires'
                          : `Expires ${timestamp(key.expires_at)}`}
                    </span>
                    <span>
                      {key.last_used_at ? `Last used ${timestamp(key.last_used_at)}` : 'Never used'}
                    </span>
                  </>
                }
                actions={
                  !key.revoked_at && (
                    <>
                      <Button
                        size="sm"
                        aria-label={`Rotate ${key.name}`}
                        onClick={() => {
                          setRotation(key)
                          setCreateOpen(true)
                        }}
                      >
                        Rotate
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={`Revoke ${key.name}`}
                        onClick={() => {
                          setRevoke(key)
                          setError('')
                        }}
                      >
                        Revoke
                      </Button>
                    </>
                  )
                }
              />
            )
          })}
        </SettingsRows>
      )}
      <CreateKey
        key={`${scope.project}:${scope.environment}`}
        open={createOpen}
        onOpenChange={setCreateOpen}
        project={scope.project}
        environment={scope.environment}
        rotation={rotation}
      />
      <Dialog
        open={Boolean(revoke)}
        className={focusedDialog}
        onOpenChange={(open) => {
          if (!busy && !open) setRevoke(null)
        }}
        title={`Revoke ${revoke?.name || 'API key'}?`}
        description="New requests using this key are denied immediately. This cannot be undone."
      >
        <div className="dialog-body">
          {revoke && (
            <ReviewFacts
              facts={[
                [
                  'Key',
                  <>
                    {revoke.name} · <code>{revoke.prefix || revoke.id.slice(0, 8)}…</code>
                  </>,
                ],
                ['Scope', keyScope(revoke)],
                ['Permissions', revoke.permissions.join(', ')],
                ['Last used', revoke.last_used_at ? timestamp(revoke.last_used_at) : 'Never used'],
                [
                  'Kept running',
                  'Existing workloads. Accepted operations revalidate authority before new privileged steps.',
                ],
              ]}
            />
          )}
          <ReviewNotice tone="warn" title="Integrations using this key stop working">
            Update CI and CLI configurations with another key first, or rotate this key to get a
            replacement with the same scope.
          </ReviewNotice>
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRevoke(null)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            aria-busy={busy || undefined}
            disabled={busy}
            onClick={async () => {
              if (!revoke || busy) return
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.DELETE('/keys/{id}', {
                    params: { path: { id: revoke.id } },
                  }),
                )
                setRevoke(null)
                void queryClient.invalidateQueries({ queryKey: ['keys'] })
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Revoking…' : `Revoke ${revoke?.name || 'API key'}`}
          </Button>
        </div>
      </Dialog>
    </SettingsSection>
  )
}

function keyScope(key: APIKey) {
  if (!key.project) return 'Installation'
  return [key.project, key.environment, key.application].filter(Boolean).join(' / ')
}

export function keyState(key: APIKey, now = Date.now()) {
  if (key.revoked_at) return { label: 'Revoked', tone: 'muted' } as const
  if (key.expires_at && Date.parse(key.expires_at) <= now)
    return { label: 'Expired', tone: 'danger' } as const
  return { label: 'Active', tone: 'ok' } as const
}
export function CreateKey({
  open,
  onOpenChange,
  project,
  environment,
  rotation,
  page = false,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  project: string
  environment: string
  rotation?: APIKey | null
  page?: boolean
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [application, setApplication] = useState('')
  const [lifetime, setLifetime] = useState('30')
  const [access, setAccess] = useState('deploy')
  const [reviewing, setReviewing] = useState(false)
  const [extraGrants, setExtraGrants] = useState<string[]>([])
  const basePermissions =
    access === 'read'
      ? ['deployments:read', 'logs:read']
      : access === 'manage'
        ? [
            'deployments:read',
            'deployments:write',
            'logs:read',
            'networks:write',
            'git:manage',
            'applications:manage',
          ]
        : ['deployments:read', 'deployments:write', 'logs:read']
  const permissions = [...new Set([...basePermissions, ...extraGrants])]
  const [created, setCreated] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [discarding, setDiscarding] = useState(false)
  const close = (value: boolean) => {
    if (!busy) {
      onOpenChange(value)
      setDiscarding(false)
      if (!value) {
        setCreated('')
        setName('')
        setReviewing(false)
        setError('')
      }
    }
  }
  // Escape and the close button must not drop a one-time key or a filled-in draft.
  const dismiss = (value: boolean) => {
    if (!value && (created || name || application || extraGrants.length || reviewing)) {
      if (!busy) setDiscarding(true)
      return
    }
    close(value)
  }
  return (
    <KeyContainer
      page={page}
      open={open}
      onOpenChange={dismiss}
      className={focusedDialog}
      title={
        created
          ? 'Your API key is ready'
          : rotation
            ? `Rotate “${rotation.name}”`
            : 'Create a scoped API key'
      }
      description={
        created
          ? 'Copy this value now. It is shown only once.'
          : rotation
            ? 'Create a replacement with the same scope and permissions.'
            : `Grant access to ${project} / ${environment}.`
      }
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy || created) return
          if (!reviewing) {
            if (
              !rotation &&
              application &&
              (access === 'manage' ||
                extraGrants.some(
                  (grant) => grant.startsWith('databases:') || grant === 'agent:credentials',
                ))
            ) {
              setError(
                'The selected grants need the whole environment. Clear the application restriction or remove those grants.',
              )
              return
            }
            setError('')
            setReviewing(true)
            return
          }
          setBusy(true)
          setError('')
          try {
            const expiry = keyLifetimeInput(lifetime, dashboardEdition.cloud)
            const result = rotation
              ? await unwrap(
                  client.POST('/keys/{id}/rotate', {
                    params: { path: { id: rotation.id } },
                    body: expiry,
                  }),
                )
              : await unwrap(
                  client.POST('/keys', {
                    body: {
                      name,
                      project,
                      environment,
                      application,
                      permissions,
                      ...expiry,
                    },
                  }),
                )
            setCreated(result.key)
            void queryClient.invalidateQueries({ queryKey: ['keys'] })
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className={page ? 'field-stack py-4' : 'dialog-body field-stack'}>
          {created ? (
            <>
              <div className="created-key">
                <code>{created}</code>
                <CopyValue value={created} what="API key" />
              </div>
              <Note>
                Store this key as HAKOPOD_API_KEY in your secret manager. Set HAKOPOD_API_URL to
                this dashboard's address. The SDK reads the key's scope when it connects.
              </Note>
            </>
          ) : reviewing ? (
            <>
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
                <dt>Name</dt>
                <dd className="min-w-0 break-words">{rotation?.name || name}</dd>
                <dt>Project</dt>
                <dd className="min-w-0 break-words">
                  {rotation ? rotation.project || 'Installation' : project}
                </dd>
                <dt>Environment</dt>
                <dd className="min-w-0 break-words">
                  {rotation ? rotation.environment || 'All environments' : environment}
                </dd>
                <dt>Application</dt>
                <dd className="min-w-0 break-words">
                  {rotation
                    ? rotation.application || 'All in this environment'
                    : application || 'All in this environment'}
                </dd>
                <dt>Lifetime</dt>
                <dd>{lifetime === 'never' ? 'Never expires' : `${lifetime} days`}</dd>
              </dl>
              <p className="text-sm break-words">
                Permissions: {(rotation?.permissions || permissions).join(', ')}
              </p>
              <Note>
                Deployment access can run code and change workloads. Execution grants permit
                commands or SQL in the selected scope. Credential access lets the selected agent
                retrieve authorized credentials. Returned secrets may enter its context. Grant this
                only to agents and integrations you trust. Keep the key in a secret store. Existing
                workloads continue if the key expires or is revoked.
                {lifetime === 'never' &&
                  ' This key has no expiry date. Revoke it when the CI workflow no longer needs it.'}
                {rotation && ' The previous key expires within 15 minutes of rotation.'}
              </Note>
            </>
          ) : rotation ? (
            <>
              <Note>
                The previous key expires within 15 minutes. Update your integrations with the
                replacement before that overlap ends. Existing workloads keep running.
              </Note>
              <label>
                Replacement lifetime
                <SelectField
                  label="Replacement lifetime"
                  value={lifetime}
                  onValueChange={setLifetime}
                  options={keyLifetimeOptions(dashboardEdition.cloud)}
                />
              </label>
            </>
          ) : (
            <>
              <label>
                Key name
                <Input
                  required
                  placeholder="production-deploy"
                  value={name}
                  maxLength={80}
                  onChange={(event) => setName(event.target.value)}
                />
              </label>
              <div className="form-grid-two">
                {!dashboardEdition.cloud && (
                  <label>
                    Application restriction
                    <Input
                      placeholder="All in this environment"
                      value={application}
                      onChange={(event) => {
                        setApplication(event.target.value)
                        if (event.target.value)
                          setExtraGrants((grants) =>
                            grants.filter(
                              (grant) =>
                                !grant.startsWith('databases:') && grant !== 'agent:credentials',
                            ),
                          )
                      }}
                    />
                  </label>
                )}
                <label>
                  Lifetime
                  <SelectField
                    label="Lifetime"
                    value={lifetime}
                    onValueChange={setLifetime}
                    options={keyLifetimeOptions(dashboardEdition.cloud)}
                  />
                </label>
              </div>
              <label>
                Access
                <SelectField
                  label="Access"
                  value={access}
                  onValueChange={setAccess}
                  options={[
                    {
                      value: 'read',
                      label: 'Read only — inspect workloads and logs',
                    },
                    {
                      value: 'deploy',
                      label: 'Deploy — apps, services and databases',
                    },
                    {
                      value: 'manage',
                      label: 'Manage workloads — includes Git, networks and app deletion',
                    },
                  ]}
                />
              </label>
              <fieldset className="grid gap-2">
                <legend className="mb-2 text-sm">Explicit execution and credential access</legend>
                {executionGrants.map(([grant, label]) => (
                  <label key={grant} className="flex min-h-11 items-center gap-3 text-sm">
                    <Input
                      type="checkbox"
                      disabled={
                        Boolean(application) &&
                        (grant.startsWith('databases:') || grant === 'agent:credentials')
                      }
                      checked={extraGrants.includes(grant)}
                      onChange={(event) =>
                        setExtraGrants(toggleGrant(extraGrants, grant, event.target.checked))
                      }
                    />
                    {label}
                  </label>
                ))}
              </fieldset>
              <Note>
                Execution and credential grants are selected separately. SQL writes require SQL
                query access. Credential retrieval also requires deployment access.
              </Note>
              <Note>
                Deployment access can run code that reads application credentials. Manage workloads
                also permits Git setup, private network changes and application deletion; it needs
                access to the whole environment. These keys cannot administer the installation.
              </Note>
            </>
          )}
          {error && <RequestError error={error} />}
        </div>
        {discarding ? (
          <DiscardPrompt
            discardLabel={created ? 'Close without copying' : 'Discard draft'}
            keepLabel={created ? 'Keep key visible' : 'Keep editing'}
            onKeep={() => setDiscarding(false)}
            onDiscard={() => close(false)}
          >
            {created
              ? 'This key is not shown again. Copy it before closing.'
              : 'Close and discard this API key draft?'}
          </DiscardPrompt>
        ) : (
          <div className={page ? 'flex justify-end gap-3 py-4' : 'dialog-footer'}>
            {created ? (
              <Button type="button" variant="primary" onClick={() => close(false)}>
                Done
              </Button>
            ) : (
              <>
                <Button
                  type="button"
                  disabled={busy}
                  onClick={() => (reviewing ? setReviewing(false) : close(false))}
                >
                  {reviewing ? 'Back' : 'Cancel'}
                </Button>
                <Button
                  type="submit"
                  variant="primary"
                  disabled={
                    busy || (!rotation && (!permissions.length || !project || !environment))
                  }
                >
                  {busy
                    ? rotation
                      ? 'Rotating…'
                      : 'Creating…'
                    : !reviewing
                      ? 'Review API key'
                      : rotation
                        ? 'Rotate API key'
                        : 'Create API key'}
                </Button>
              </>
            )}
          </div>
        )}
      </form>
    </KeyContainer>
  )
}
function AuditLog() {
  const audit = useQuery({
    queryKey: ['audit'],
    queryFn: ({ signal }) => unwrap(client.GET('/audit', { signal })),
    staleTime: 30000,
  })
  // Audit is installation-admin only, so the user list is readable here.
  const users = useQuery({
    queryKey: ['users'],
    queryFn: ({ signal }) => unwrap(client.GET('/users', { signal })),
    staleTime: 30000,
  })
  const names = new Map(users.data?.items.map((user) => [user.id, user.name || user.email]))
  const items = audit.data?.items || []
  return (
    <>
      <SettingsSection
        title="Audit events"
        help="Recent management actions and who made them."
        state={
          audit.data
            ? items.length
              ? `${plural(items.length, 'recent event')}, latest ${relative(items[0].time).toLowerCase()}`
              : 'Nothing recorded yet'
            : undefined
        }
        actions={
          <Button size="sm" onClick={() => void audit.refetch()} disabled={audit.isFetching}>
            <Icon name="refresh" size={14} />
            {audit.isFetching && !audit.isPending ? 'Refreshing…' : 'Refresh'}
          </Button>
        }
      >
        {audit.isPending ? (
          <Loading />
        ) : audit.error ? (
          <ErrorState error={audit.error} retry={() => void audit.refetch()} />
        ) : !items.length ? (
          <SettingsEmpty title="No audit events">
            Management actions are recorded here as they happen.
          </SettingsEmpty>
        ) : (
          <SettingsRows label="Audit events">
            {items.map((event) => (
              <SettingsRow
                key={event.id}
                className="settings-row-audit"
                name={event.action}
                detail={<code>{event.resource}</code>}
                status={
                  <time dateTime={event.time} title={timestamp(event.time)}>
                    {relative(event.time)}
                  </time>
                }
                meta={
                  names.get(event.identity_id) || (
                    <code title={event.identity_id}>{event.identity_id.slice(0, 12)}</code>
                  )
                }
              />
            ))}
          </SettingsRows>
        )}
      </SettingsSection>
      <AuditHistory />
    </>
  )
}

function KeyContainer({
  page,
  children,
  title,
  description,
  ...dialog
}: import('react').ComponentProps<typeof Dialog> & { page: boolean }) {
  if (!page)
    return (
      <Dialog title={title} description={description} {...dialog}>
        {children}
      </Dialog>
    )
  return (
    <div className="page-content hako-form-page">
      <PageHeader
        title={title}
        description={typeof description === 'string' ? description : undefined}
      />
      {children}
    </div>
  )
}
