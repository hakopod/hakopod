import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { lazy, Suspense, useState } from 'react'
import { createFileRoute, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { SettingsLayout } from '@hakopod/hatch-ui/blocks/settings-layout'
import { message, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { APIKey } from '../lib/types'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Copy, Empty, ErrorState, Loading, Note } from '../components/shared'
const AppearanceSettings = lazy(() =>
  import('../components/appearance-settings').then((m) => ({ default: m.AppearanceSettings })),
)
const GitHubSettings = lazy(() =>
  import('../components/application-source').then((m) => ({ default: m.GitHubSettings })),
)
const AccountSettings = lazy(() => import('../components/account-settings'))
const LicenseSettings = lazy(() => import('../components/license-settings'))
const TeamSettings = lazy(() => import('../components/team-settings'))
const InstallationUsers = lazy(() =>
  import('../components/team-settings').then((m) => ({ default: m.InstallationUsers })),
)

export const Route = createFileRoute('/settings')({
  validateSearch: (search: Record<string, unknown>): { tab?: string } => ({
    tab:
      typeof search.tab === 'string' &&
      ['account', 'teams', 'users', 'github', 'keys', 'audit', 'appearance', 'license'].includes(
        search.tab,
      )
        ? search.tab
        : undefined,
  }),
  component: AdministrationRoute,
})
function AdministrationRoute() {
  return useLocation().pathname === '/settings' ? <Administration /> : <Outlet />
}
function Administration() {
  const scope = useScope()
  const selected = Route.useSearch().tab || 'account'
  const navigate = Route.useNavigate()
  const sections = [
    { id: 'account', label: 'Account security', group: 'Personal' },
    { id: 'appearance', label: 'Appearance', group: 'Personal' },
    { id: 'teams', label: 'Teams & access', group: 'Workspace' },
    { id: 'license', label: 'License & features', group: 'Workspace' },
    ...(scope.identity.admin
      ? [
          { id: 'users', label: 'People', group: 'Installation' },
          { id: 'github', label: 'Git providers', group: 'Installation' },
          { id: 'keys', label: 'API keys', group: 'Installation' },
          { id: 'audit', label: 'Audit events', group: 'Installation' },
        ]
      : []),
  ]
  const tab = sections.some((section) => section.id === selected) ? selected : 'account'
  return (
    <div className="settings-page">
      <header className="hako-settings-header">
        <h1>Workspace settings</h1>
      </header>
      <SettingsLayout
        sections={sections}
        active={tab}
        onSectionChange={(next) => void navigate({ search: { tab: next } })}
      >
        <Suspense fallback={<Loading />}>
          {tab === 'account' && <AccountSettings />}
          {tab === 'appearance' && <AppearanceSettings />}
          {tab === 'teams' && <TeamSettings />}
          {tab === 'license' && <LicenseSettings />}
          {scope.identity.admin && (
            <>
              {tab === 'users' && <InstallationUsers />}
              {tab === 'github' && <GitHubSettings />}
              {tab === 'keys' && <Keys />}
              {tab === 'audit' && <AuditLog />}
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
    queryKey: ['keys'],
    queryFn: ({ signal }) => unwrap(client.GET('/keys', { signal })),
    staleTime: 30000,
  })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>API keys</h2>
          <p>Give each workflow its own identity, scope, and expiration.</p>
        </div>
        <Button
          variant="primary"
          onClick={() => {
            setRotation(null)
            setCreateOpen(true)
          }}
        >
          <Icon name="plus" size={15} />
          Create API key
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
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
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
                await unwrap(client.DELETE('/keys/{id}', { params: { path: { id: revoke.id } } }))
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
function CreateKey({
  open,
  onOpenChange,
  project,
  environment,
  rotation,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  project: string
  environment: string
  rotation?: APIKey | null
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [application, setApplication] = useState('')
  const [days, setDays] = useState(30)
  const [permissions, setPermissions] = useState([
    'deployments:read',
    'deployments:write',
    'logs:read',
  ])
  const [created, setCreated] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const close = (value: boolean) => {
    if (!busy) {
      onOpenChange(value)
      if (!value) {
        setCreated('')
        setName('')
        setError('')
      }
    }
  }
  return (
    <Dialog
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
          setBusy(true)
          setError('')
          try {
            const expires_at = new Date(Date.now() + days * 86400000).toISOString()
            const result = rotation
              ? await unwrap(
                  client.POST('/keys/{id}/rotate', {
                    params: { path: { id: rotation.id } },
                    body: { expires_at },
                  }),
                )
              : await unwrap(
                  client.POST('/keys', {
                    body: { name, project, environment, application, permissions, expires_at },
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
        <div className="dialog-body field-stack">
          {created ? (
            <>
              <div className="created-key">
                <code>{created}</code>
                <Copy value={created} label="Copy key" />
              </div>
              <Note>
                Store this key in your CI secret store or the CLI credential store. It is never
                placed in URLs, exported configuration, or key lists.
              </Note>
            </>
          ) : rotation ? (
            <>
              <Note>
                The previous key expires within 15 minutes. Update your integrations with the
                replacement before that overlap ends. Existing workloads keep running.
              </Note>
              <label>
                Replacement expires in
                <SelectField
                  label="Replacement expires in"
                  value={String(days)}
                  onValueChange={(value) => setDays(Number(value))}
                  options={[
                    {
                      value: '7',
                      label: '7 days',
                    },
                    {
                      value: '30',
                      label: '30 days',
                    },
                    {
                      value: '60',
                      label: '60 days',
                    },
                    {
                      value: '89',
                      label: '89 days',
                    },
                  ]}
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
                <label>
                  Application restriction
                  <Input
                    placeholder="All in this environment"
                    value={application}
                    onChange={(event) => setApplication(event.target.value)}
                  />
                </label>
                <label>
                  Expires in
                  <SelectField
                    label="Expires in"
                    value={String(days)}
                    onValueChange={(value) => setDays(Number(value))}
                    options={[
                      {
                        value: '7',
                        label: '7 days',
                      },
                      {
                        value: '30',
                        label: '30 days',
                      },
                      {
                        value: '60',
                        label: '60 days',
                      },
                      {
                        value: '89',
                        label: '89 days',
                      },
                    ]}
                  />
                </label>
              </div>
              <label>Permissions</label>
              <div className="permission-checkboxes">
                {['deployments:read', 'deployments:write', 'logs:read'].map((permission) => (
                  <label className="checkbox-label" key={permission}>
                    <Input
                      type="checkbox"
                      checked={permissions.includes(permission)}
                      onChange={(event) =>
                        setPermissions((previous) =>
                          event.target.checked
                            ? [...previous, permission]
                            : previous.filter((item) => item !== permission),
                        )
                      }
                    />
                    <code>{permission}</code>
                  </label>
                ))}
              </div>
              <Note>Scoped keys cannot administer the platform or retrieve secret values.</Note>
            </>
          )}
          {error && <div className="inline-error">{error}</div>}
        </div>
        <div className="dialog-footer">
          {created ? (
            <Button type="button" variant="primary" onClick={() => close(false)}>
              Done
            </Button>
          ) : (
            <>
              <Button type="button" disabled={busy} onClick={() => close(false)}>
                Cancel
              </Button>
              <Button
                type="submit"
                variant="primary"
                disabled={busy || (!rotation && (!permissions.length || !project || !environment))}
              >
                {busy ? 'Creating…' : rotation ? 'Rotate API key' : 'Create API key'}
              </Button>
            </>
          )}
        </div>
      </form>
    </Dialog>
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
          <h2>Audit events</h2>
          <p>Recent management actions and their attribution.</p>
        </div>
        <Button size="sm" onClick={() => void audit.refetch()}>
          <Icon name="refresh" size={14} />
          Refresh
        </Button>
      </div>
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
