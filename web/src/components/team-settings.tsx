import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import type { components } from '../lib/api.generated'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Copy, Empty, ErrorState, Loading, Note } from './shared'

export default function TeamSettings() {
  const scope = useScope()
  const cache = useQueryClient()
  const [selected, setSelected] = useState('')
  const [teamName, setTeamName] = useState('')
  const [adding, setAdding] = useState(false)
  const [invite, setInvite] = useState<'team' | 'project' | null>(null)
  const [grant, setGrant] = useState('')
  const [grantRole, setGrantRole] = useState('viewer')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const teams = useQuery({
    queryKey: ['teams'],
    queryFn: ({ signal }) => unwrap(client.GET('/teams', { signal })),
    staleTime: 30000,
  })
  const team = selected || teams.data?.items[0]?.id || ''
  const current = teams.data?.items.find((item) => item.id === team)
  const canTeam = scope.identity.admin || ['owner', 'admin'].includes(current?.role || '')
  const canProject =
    scope.identity.admin ||
    scope.identity.project_roles?.some(
      (item) => item.project === scope.project && item.role === 'admin',
    )
  const members = useQuery({
    queryKey: ['team-members', team],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/teams/{id}/members', { signal, params: { path: { id: team } } })),
    enabled: Boolean(team),
    gcTime: 0,
  })
  const project = useQuery({
    queryKey: ['project-members', scope.project],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/projects/{project}/members', {
          signal,
          params: { path: { project: scope.project } },
        }),
      ),
    enabled: Boolean(scope.project),
    gcTime: 0,
  })
  const refresh = () => {
    void cache.invalidateQueries({ queryKey: ['teams'] })
    void cache.invalidateQueries({ queryKey: ['team-members'] })
    void cache.invalidateQueries({ queryKey: ['project-members'] })
    void cache.invalidateQueries({ queryKey: ['projects'] })
  }
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Your teams</h2>
          <p>Manage people together, then grant teams access to projects.</p>
        </div>
        <Button
          onClick={() => {
            setError('')
            setAdding(true)
          }}
        >
          Create team
        </Button>
      </div>
      {teams.isPending ? (
        <Loading rows={2} />
      ) : teams.error ? (
        <ErrorState error={teams.error} />
      ) : !teams.data?.items.length ? (
        <Empty title="No teams yet" description="Create a team to invite collaborators." />
      ) : (
        <section className="panel service-summary-panel">
          <div className="section-toolbar">
            <label className="inline-label">
              Team
              <select value={team} onChange={(e) => setSelected(e.target.value)}>
                {teams.data.items.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </select>
            </label>
            {canTeam && <Button onClick={() => setInvite('team')}>Invite member</Button>}
          </div>
          {members.isPending ? (
            <Loading rows={2} />
          ) : members.error ? (
            <ErrorState error={members.error} />
          ) : (
            members.data?.items.map((member) => (
              <div className="settings-list-row" key={member.id}>
                <div>
                  <strong>{member.name}</strong>
                  <small>{member.email}</small>
                </div>
                {canTeam && member.role !== 'owner' ? (
                  <RoleEditor
                    key={`${member.id}-${member.role}`}
                    role={member.role}
                    roles={['admin', 'member']}
                    onSave={async (role) => {
                      await unwrap(
                        client.PUT('/teams/{id}/members/{user}', {
                          params: { path: { id: team, user: member.id } },
                          body: { role },
                        }),
                      )
                      refresh()
                    }}
                  />
                ) : (
                  <span className="label-chip">{member.role}</span>
                )}
              </div>
            ))
          )}
        </section>
      )}
      <div className="section-toolbar">
        <div>
          <h2>Project access</h2>
          <p>{scope.project || 'Select a project'} · Role changes take effect on new requests.</p>
        </div>
        {canProject && scope.project && (
          <Button onClick={() => setInvite('project')}>Invite to project</Button>
        )}
      </div>
      {scope.project ? (
        project.isPending ? (
          <Loading rows={2} />
        ) : project.error ? (
          <ErrorState error={project.error} />
        ) : (
          <section className="panel service-summary-panel">
            {project.data?.items.length ? (
              project.data.items.map((member) => (
                <div className="settings-list-row" key={member.identity_id || member.team_id}>
                  <div>
                    <strong>{member.name}</strong>
                    <small>{member.team_id ? 'Team' : 'Member'}</small>
                  </div>
                  {canProject ? (
                    <RoleEditor
                      key={`${member.identity_id || member.team_id}-${member.role}`}
                      role={member.role}
                      roles={['admin', 'developer', 'viewer']}
                      onSave={async (role) => {
                        await unwrap(
                          client.PUT('/projects/{project}/members', {
                            params: { path: { project: scope.project } },
                            body: {
                              identity_id: member.identity_id,
                              team_id: member.team_id,
                              role,
                            },
                          }),
                        )
                        refresh()
                      }}
                    />
                  ) : (
                    <span className="label-chip">{member.role}</span>
                  )}
                </div>
              ))
            ) : (
              <p className="field-help">
                No direct members or teams. Installation administrators retain access.
              </p>
            )}
            {canProject && Boolean(teams.data?.items.length) && (
              <form
                className="inline-form"
                onSubmit={async (e) => {
                  e.preventDefault()
                  setBusy(true)
                  setError('')
                  try {
                    await unwrap(
                      client.PUT('/projects/{project}/members', {
                        params: { path: { project: scope.project } },
                        body: { team_id: grant, role: grantRole },
                      }),
                    )
                    setGrant('')
                    refresh()
                  } catch (err) {
                    setError(message(err))
                  } finally {
                    setBusy(false)
                  }
                }}
              >
                <label>
                  Grant a team access
                  <select value={grant} onChange={(e) => setGrant(e.target.value)} required>
                    <option value="">Choose a team</option>
                    {teams.data?.items.map((item) => (
                      <option key={item.id} value={item.id}>
                        {item.name}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  Role
                  <select value={grantRole} onChange={(e) => setGrantRole(e.target.value)}>
                    {['viewer', 'developer', 'admin'].map((role) => (
                      <option key={role}>{role}</option>
                    ))}
                  </select>
                </label>
                <Button type="submit" disabled={busy || !grant}>
                  Grant access
                </Button>
              </form>
            )}
          </section>
        )
      ) : (
        <Note>Choose a project to manage access.</Note>
      )}
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <Note>
        Viewers can inspect applications and logs. Developers can deploy and read logs. Project
        admins also manage membership. Team roles control the team’s own membership.
      </Note>
      {invite && (
        <InviteDialog
          target={invite}
          team={team}
          project={scope.project}
          onClose={() => setInvite(null)}
        />
      )}
      <Dialog
        open={adding}
        onOpenChange={(open) => {
          if (!busy) setAdding(open)
        }}
        title="Create a team"
        description="You become this team’s owner."
      >
        <form
          onSubmit={async (e) => {
            e.preventDefault()
            if (busy) return
            setBusy(true)
            setError('')
            try {
              const result = await unwrap(client.POST('/teams', { body: { name: teamName } }))
              setSelected(result.id)
              setTeamName('')
              setAdding(false)
              refresh()
            } catch (err) {
              setError(message(err))
            } finally {
              setBusy(false)
            }
          }}
        >
          <div className="dialog-body auth-form">
            <label>
              Team name
              <input
                value={teamName}
                onChange={(e) => setTeamName(e.target.value)}
                maxLength={80}
                required
              />
            </label>
            {error && (
              <div className="inline-error" role="alert">
                {error}
              </div>
            )}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setAdding(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={busy}>
              Create team
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  )
}

function RoleEditor({
  role,
  roles,
  onSave,
}: {
  role: string
  roles: string[]
  onSave: (role: string) => Promise<void>
}) {
  const [next, setNext] = useState(role)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <form
      className="role-editor"
      onSubmit={async (e) => {
        e.preventDefault()
        if (busy || next === role) return
        setBusy(true)
        setError('')
        try {
          await onSave(next)
        } catch (err) {
          setError(message(err))
        } finally {
          setBusy(false)
        }
      }}
    >
      <select aria-label="Member role" value={next} onChange={(e) => setNext(e.target.value)}>
        {roles.map((value) => (
          <option key={value}>{value}</option>
        ))}
        <option value="">Remove access</option>
      </select>
      <Button
        size="sm"
        type="submit"
        disabled={busy || next === role}
        variant={next ? 'secondary' : 'danger'}
      >
        {next ? 'Save' : 'Remove'}
      </Button>
      {error && (
        <span className="inline-error" role="alert">
          {error}
        </span>
      )}
    </form>
  )
}

function InviteDialog({
  target,
  team,
  project,
  onClose,
}: {
  target: 'team' | 'project'
  team: string
  project: string
  onClose: () => void
}) {
  const [email, setEmail] = useState('')
  const [role, setRole] = useState(target === 'team' ? 'member' : 'viewer')
  const [deliver, setDeliver] = useState(false)
  const [result, setResult] = useState<components['schemas']['InviteCreated'] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const status = useQuery({
    queryKey: ['auth-status'],
    queryFn: ({ signal }) => unwrap(client.GET('/auth/status', { signal })),
    staleTime: 30000,
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={result ? 'Invitation ready' : `Invite to ${target}`}
      description={
        result
          ? `Expires ${timestamp(result.invite.expires_at)}`
          : 'Invite a person with a specific role.'
      }
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy || result) return
          setBusy(true)
          setError('')
          try {
            setResult(
              target === 'team'
                ? await unwrap(
                    client.POST('/teams/{id}/invites', {
                      params: { path: { id: team } },
                      body: { email, role, deliver },
                    }),
                  )
                : await unwrap(
                    client.POST('/projects/{project}/invites', {
                      params: { path: { project } },
                      body: { email, role, deliver },
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
          {result ? (
            <>
              <Note>
                {result.delivered
                  ? 'The invitation email was sent.'
                  : 'Share this invitation link directly with the intended person.'}
              </Note>
              <div className="secret-once">
                <code>{result.invite_url}</code>
                <Copy value={result.invite_url} />
              </div>
            </>
          ) : (
            <>
              <label>
                Email address
                <input
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  maxLength={254}
                  required
                />
              </label>
              <label>
                Role
                <select value={role} onChange={(e) => setRole(e.target.value)}>
                  {(target === 'team' ? ['member', 'admin'] : ['viewer', 'developer', 'admin']).map(
                    (value) => (
                      <option key={value}>{value}</option>
                    ),
                  )}
                </select>
              </label>
              <label className="checkbox-row">
                <input
                  type="checkbox"
                  checked={deliver}
                  disabled={!status.data?.email_delivery}
                  onChange={(e) => setDeliver(e.target.checked)}
                />
                Send invitation email
              </label>
              {!status.data?.email_delivery && (
                <p className="field-help">
                  Email delivery is not configured. You can copy the invitation link.
                </p>
              )}
            </>
          )}
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            {result ? 'Done' : 'Cancel'}
          </Button>
          {!result && (
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? 'Creating…' : 'Create invitation'}
            </Button>
          )}
        </div>
      </form>
    </Dialog>
  )
}

export function InstallationUsers() {
  const cache = useQueryClient()
  const users = useQuery({
    queryKey: ['installation-users'],
    queryFn: ({ signal }) => unwrap(client.GET('/users', { signal })),
    gcTime: 0,
  })
  const [change, setChange] = useState<components['schemas']['User'] | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Installation accounts</h2>
          <p>Administrators have access across projects. The owner account is protected.</p>
        </div>
      </div>
      {users.isPending ? (
        <Loading />
      ) : users.error ? (
        <ErrorState error={users.error} />
      ) : (
        <div className="panel settings-session-list">
          {users.data?.items.map((user) => (
            <div className="settings-list-row" key={user.id}>
              <div>
                <strong>
                  {user.name}
                  {user.owner ? ' · Owner' : ''}
                </strong>
                <small>
                  {user.email} ·{' '}
                  {user.disabled ? 'Disabled' : user.admin ? 'Administrator' : 'Member'}
                </small>
              </div>
              {!user.owner && (
                <div className="toolbar-actions">
                  <Button
                    size="sm"
                    onClick={() => {
                      setError('')
                      setChange({ ...user, admin: !user.admin })
                    }}
                  >
                    {user.admin ? 'Remove admin' : 'Make admin'}
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => {
                      setError('')
                      setChange({ ...user, disabled: !user.disabled })
                    }}
                  >
                    {user.disabled ? 'Enable' : 'Disable'}
                  </Button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
      <Dialog
        open={Boolean(change)}
        onOpenChange={(open) => {
          if (!busy && !open) setChange(null)
        }}
        title={`Update ${change?.name || 'account'}?`}
        description={`Result: ${change?.disabled ? 'disabled account' : 'enabled account'}, ${change?.admin ? 'installation administrator' : 'member'}.`}
      >
        <div className="dialog-body">
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setChange(null)}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy}
            onClick={async () => {
              if (!change || busy) return
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.PATCH('/users/{id}', {
                    params: { path: { id: change.id } },
                    body: { disabled: change.disabled, admin: change.admin },
                  }),
                )
                setChange(null)
                void cache.invalidateQueries({ queryKey: ['installation-users'] })
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Update account
          </Button>
        </div>
      </Dialog>
    </>
  )
}
