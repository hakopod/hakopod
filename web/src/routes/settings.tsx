import { dashboardEdition, useEditionFeatures } from '../lib/dashboard-edition'
import { AuditHistory } from '../components/audit-history'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { lazy, Suspense, useState } from 'react'
import { createFileRoute, Outlet, useLocation, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { SettingsLayout } from '@hakopod/hatch-ui/blocks/settings-layout'
import { message, timestamp } from '../lib/api'
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
  PageHeader,
  HeadingHelp,
  Copy,
  Empty,
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
const ProxySettings = lazy(() => import('../components/proxy-settings'))
const SecretProviders = lazy(() =>
  import('../components/secret-providers').then((module) => ({
    default: module.SecretProviders,
  })),
)
const DNSProviders = lazy(() =>
  import('./settings.dns-providers').then((module) => ({
    default: module.DNSProviders,
  })),
)
const InstallationUsers = lazy(() =>
  import('../components/team-settings').then((m) => ({
    default: m.InstallationUsers,
  })),
)

export const Route = createFileRoute('/settings')({
  validateSearch: (search: Record<string, unknown>): { tab?: string } => ({
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
  const selected = Route.useSearch().tab || 'account'
  const navigate = Route.useNavigate()
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
            id: 'dns-providers',
            label: 'DNS providers',
            group: 'Installation',
          },
          { id: 'keys', label: 'API keys', group: 'Installation' },
          { id: 'audit', label: 'Audit events', group: 'Installation' },
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
  const navigationRoot = useActiveSection(tab)
  return (
    <div className="settings-page" ref={navigationRoot}>
      <PageHeader title={features.operator ? 'Installation settings' : 'Settings'} />
      <SettingsLayout
        sections={visibleSections}
        active={tab}
        onSectionChange={(next) => {
          if (next === 'integrations') {
            void navigate({ to: '/settings/integrations' })
            return
          }
          void navigate({ search: { tab: next } })
        }}
      >
        <Suspense fallback={<Loading />}>
          {tab === 'account' && <AccountSettings />}
          {tab === 'appearance' && <AppearanceSettings />}
          {tab === 'teams' && <TeamSettings />}
          {tab === 'license' && <LicenseSettings />}
          {(scope.identity.admin || scope.identity.can_manage_keys) && tab === 'keys' && <Keys />}
          {(scope.identity.admin || scope.identity.can_manage_git) && tab === 'github' && (
            <GitConnectionsPanel />
          )}
          {scope.identity.admin && (
            <>
              {tab === 'users' && <InstallationUsers />}
              {tab === 'secret-providers' && <SecretProviders />}
              {tab === 'dns-providers' && <DNSProviders />}
              {tab === 'audit' && <AuditLog />}
              {installation.allowed && tab === 'login-providers' && <LoginProviderSettings />}
              {installation.allowed && tab === 'smtp' && <SMTPSettings />}
              {installation.allowed && tab === 'edge' && <ProxySettings />}
            </>
          )}
        </Suspense>
      </SettingsLayout>
    </div>
  )
}

function Keys() {
  const scope = useScope()
  const queryClient = useQueryClient()
  const [createOpen, setCreateOpen] = useState(false)
  const [rotation, setRotation] = useState<APIKey | null>(null)
  const [revoke, setRevoke] = useState<APIKey | null>(null)
  const [confirmName, setConfirmName] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const keys = useQuery({
    queryKey: ['keys', scope.project, scope.environment],
    queryFn: ({ signal }) => unwrap(client.GET('/keys', { signal })),
    staleTime: 30000,
  })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>API keys</h2>
            <HeadingHelp title="API keys">
              Give each workflow its own identity, scope, and expiration.
            </HeadingHelp>
          </div>
        </div>
        <Button variant="primary" asChild>
          <Link
            to="/settings/keys/new"
            search={{ project: scope.project, environment: scope.environment }}
          >
            <Icon name="plus" size={15} />
            Create API key
          </Link>
        </Button>
      </div>
      {keys.isPending ? (
        <Loading />
      ) : keys.error ? (
        <ErrorState error={keys.error} retry={() => void keys.refetch()} />
      ) : !keys.data?.items.length ? (
        <Empty
          icon="key"
          title="No API keys"
          description="Create a scoped key for CLI and CI deployments."
        />
      ) : (
        <div className="table-container">
          <table>
            <thead>
              <tr>
                <th>Key</th>
                <th>Scope</th>
                <th>Permissions</th>
                <th>Expires</th>
                <th>Last used</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {keys.data.items.map((key) => (
                <tr key={key.id}>
                  <td>
                    <div className="key-name">
                      <Icon name="key" size={16} />
                      <div>
                        <strong>{key.name}</strong>
                        <code>{key.prefix || key.id.slice(0, 8)}…</code>
                      </div>
                    </div>
                  </td>
                  <td>
                    {key.project ? (
                      <span>
                        {key.project}
                        <br />
                        <small className="muted-text">
                          {key.environment}
                          {key.application ? ` / ${key.application}` : ''}
                        </small>
                      </span>
                    ) : (
                      <span className="label-chip">Installation</span>
                    )}
                  </td>
                  <td>
                    <div className="permission-list">
                      {key.permissions.map((permission) => (
                        <code key={permission}>{permission}</code>
                      ))}
                    </div>
                  </td>
                  <td>
                    {key.revoked_at ? (
                      <span className="revoked-label">Revoked</span>
                    ) : key.never_expires && key.expires_at === null ? (
                      'Never expires'
                    ) : (
                      timestamp(key.expires_at)
                    )}
                  </td>
                  <td>{timestamp(key.last_used_at)}</td>
                  <td>
                    {!key.revoked_at && (
                      <>
                        <Button
                          size="sm"
                          variant="ghost"
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
                          onClick={() => {
                            setRevoke(key)
                            setConfirmName('')
                            setError('')
                          }}
                        >
                          Revoke
                        </Button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Note>
        API keys never receive Kubernetes credentials. Deployment access allows running code in
        authorized services; treat it as a trusted capability.
      </Note>
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
        onOpenChange={(open) => {
          if (!busy && !open) setRevoke(null)
        }}
        title={`Revoke “${revoke?.name || ''}”?`}
        description="New requests using this key will be denied immediately."
      >
        <div className="dialog-body">
          <Note>
            Existing workloads remain running. Accepted operations revalidate authority before new
            privileged steps.
          </Note>
          <label>
            Type {revoke?.name} to revoke this key
            <Input
              value={confirmName}
              autoComplete="off"
              onChange={(event) => setConfirmName(event.target.value)}
            />
          </label>
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRevoke(null)}>
            Keep key
          </Button>
          <Button
            variant="danger"
            disabled={busy || confirmName !== revoke?.name}
            onClick={async () => {
              if (!revoke || confirmName !== revoke.name || busy) return
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
            {busy ? 'Revoking…' : 'Revoke API key'}
          </Button>
        </div>
      </Dialog>
    </>
  )
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
  const close = (value: boolean) => {
    if (!busy) {
      onOpenChange(value)
      if (!value) {
        setCreated('')
        setName('')
        setReviewing(false)
        setError('')
      }
    }
  }
  return (
    <KeyContainer
      page={page}
      open={open}
      onOpenChange={close}
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
                <Copy value={created} label="Copy key" />
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
                disabled={busy || (!rotation && (!permissions.length || !project || !environment))}
              >
                {busy
                  ? 'Creating…'
                  : !reviewing
                    ? 'Review API key'
                    : rotation
                      ? 'Rotate API key'
                      : 'Create API key'}
              </Button>
            </>
          )}
        </div>
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
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Audit events</h2>
            <HeadingHelp title="Audit events">
              Recent management actions and their attribution.
            </HeadingHelp>
          </div>
        </div>
        <Button size="sm" onClick={() => void audit.refetch()}>
          <Icon name="refresh" size={14} />
          Refresh
        </Button>
      </div>
      <AuditHistory />
      {audit.isPending ? (
        <Loading />
      ) : audit.error ? (
        <ErrorState error={audit.error} retry={() => void audit.refetch()} />
      ) : !audit.data?.items.length ? (
        <Empty
          icon="activity"
          title="No audit events yet"
          description="Recorded management actions will appear here."
        />
      ) : (
        <div className="table-container">
          <table>
            <thead>
              <tr>
                <th>Action</th>
                <th>Resource</th>
                <th>Identity</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody>
              {audit.data.items.map((event) => (
                <tr key={event.id}>
                  <td>
                    <span className="table-name">
                      <Icon name="activity" size={14} />
                      {event.action}
                    </span>
                  </td>
                  <td>
                    <code>{event.resource}</code>
                  </td>
                  <td>
                    <code>{event.identity_id.slice(0, 12)}</code>
                  </td>
                  <td>{timestamp(event.time)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
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
