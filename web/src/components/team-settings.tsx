import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Avatar } from './avatar'
import { useLicense } from '../lib/license'
import { FeatureLock } from './license-settings'
import { useState } from 'react'
import { MoreHorizontal } from 'lucide-react'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import type { components } from '../lib/api.generated'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { HeadingHelp, Copy, Empty, ErrorState, Loading, Note } from './shared'

export default function TeamSettings() {
  const scope = useScope()
  const license = useLicense()
  const hasFeature = (feature: string) =>
    Boolean(license.data?.catalog.find((item) => item.id === feature)?.enabled)
  const cache = useQueryClient()
  const [selected, setSelected] = useState('')
  const [teamName, setTeamName] = useState('')
  const [adding, setAdding] = useState(false)
  const [removeTeam, setRemoveTeam] = useState<{ id: string; name: string } | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [usernameMember, setUsernameMember] = useState<components['schemas']['TeamMember'] | null>(
    null,
  )
  const [username, setUsername] = useState('')
  const [usernameError, setUsernameError] = useState('')
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
  const projects = useQuery({
    queryKey: ['projects'],
    queryFn: ({ signal }) => unwrap(client.GET('/projects', { signal })),
    staleTime: 60000,
  })
  const currentProject = projects.data?.items.find((item) => item.name === scope.project)
  const personalProject = currentProject?.personal === true
  const canShareProject = canProject && currentProject?.personal === false
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
    enabled: Boolean(scope.project && canShareProject),
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
      {license.error && <ErrorState error={license.error} retry={() => void license.refetch()} />}
      {license.data && !hasFeature('teams') && <FeatureLock />}
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Your teams</h2>
            <HeadingHelp title="Your teams">
              Manage people together, then grant teams access to projects.
            </HeadingHelp>
          </div>
        </div>
        <Button
          disabled={!scope.identity.admin || !hasFeature('teams')}
          title={
            !scope.identity.admin
              ? 'Requires installation administrator access'
              : !hasFeature('teams')
                ? 'Requires Hakopod Pro'
                : undefined
          }
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
              <SelectField
                label="Team"
                value={team}
                onValueChange={(value) => setSelected(value)}
                options={
                  teams.data.items.map((item) => ({
                    value: item.id,
                    label: item.name,
                  })) ?? []
                }
              />
            </label>
            {scope.identity.admin && (
              <Button
                variant="ghost"
                onClick={() => {
                  setError('')
                  setConfirmation('')
                  if (current) setRemoveTeam({ id: current.id, name: current.name })
                }}
              >
                Delete team
              </Button>
            )}
            {canTeam && (
              <Button disabled={!hasFeature('invitations')} onClick={() => setInvite('team')}>
                Invite member{!hasFeature('invitations') && ' · Pro'}
              </Button>
            )}
          </div>
          {members.isPending ? (
            <Loading rows={2} />
          ) : members.error ? (
            <ErrorState error={members.error} />
          ) : (
            members.data?.items.map((member) => (
              <div className="settings-list-row" key={member.id}>
                <div className="member-identity">
                  <Avatar name={member.name} url={member.avatar_url} />
                  <div>
                    <strong>
                      {member.name}
                      {member.username && <span className="muted-text"> @{member.username}</span>}
                    </strong>
                    <small>{member.email}</small>
                  </div>
                </div>
                {canTeam && member.role !== 'owner' ? (
                  <RoleEditor
                    key={`${member.id}-${member.role}`}
                    role={member.role}
                    subject={member.name || member.email || member.id}
                    scopeName={`team ${current?.name || team}`}
                    roles={hasFeature('teams') ? ['admin', 'member'] : [member.role]}
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
                {hasFeature('teams') && (canTeam || member.id === scope.identity.id) && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setUsernameMember(member)
                      setUsername(member.username || '')
                      setUsernameError('')
                    }}
                  >
                    Username
                  </Button>
                )}
              </div>
            ))
          )}
        </section>
      )}
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Project access</h2>
            <HeadingHelp title="Project access">
              Role changes take effect on new requests.
            </HeadingHelp>
          </div>
          <span className="muted-text">{scope.project || 'Select a project'}</span>
        </div>
        {canShareProject && scope.project && (
          <Button disabled={!hasFeature('invitations')} onClick={() => setInvite('project')}>
            Invite to project{!hasFeature('invitations') && ' · Pro'}
          </Button>
        )}
      </div>
      {scope.project ? (
        projects.isPending ? (
          <Loading rows={2} />
        ) : projects.error ? (
          <ErrorState error={projects.error} retry={() => void projects.refetch()} />
        ) : !currentProject ? (
          <Note>This project is unavailable. Choose another project to manage access.</Note>
        ) : personalProject ? (
          <Note>
            This personal workspace is private to its owner. Personal workspaces cannot be shared
            with members or teams.
          </Note>
        ) : !canProject ? (
          <Note>Project administrators manage access to this project.</Note>
        ) : project.isPending ? (
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
                  {canShareProject ? (
                    <RoleEditor
                      key={`${member.identity_id || member.team_id}-${member.role}`}
                      role={member.role}
                      subject={member.name || member.identity_id || member.team_id || 'member'}
                      scopeName={`project ${scope.project}`}
                      roles={
                        hasFeature('project_rbac')
                          ? ['admin', 'developer', 'viewer']
                          : [member.role]
                      }
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
            {canShareProject &&
              hasFeature('project_rbac') &&
              hasFeature('teams') &&
              Boolean(teams.data?.items.length) && (
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
                    <SelectField
                      label="Grant a team access"
                      value={grant}
                      onValueChange={(value) => setGrant(value)}
                      required
                      options={[
                        {
                          value: '',
                          label: 'Choose a team',
                        },
                        ...(teams.data?.items.map((item) => ({
                          value: item.id,
                          label: item.name,
                        })) ?? []),
                      ]}
                    />
                  </label>
                  <label>
                    Role
                    <SelectField
                      label="Role"
                      value={grantRole}
                      onValueChange={(value) => setGrantRole(value)}
                      options={
                        ['viewer', 'developer', 'admin'].map((role) => ({
                          value: role,
                          label: role,
                        })) ?? []
                      }
                    />
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
      {error && !removeTeam && !adding && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      {!personalProject && (
        <Note>
          Viewers can inspect applications and logs. Developers can deploy and read logs. Project
          admins also manage membership. Team roles control the team’s own membership.
        </Note>
      )}
      <Dialog
        open={Boolean(usernameMember)}
        onOpenChange={(open) => {
          if (!open && !busy) setUsernameMember(null)
        }}
        title="Team username"
        description={`A unique username for ${usernameMember?.name || 'this member'} in ${current?.name || 'this team'}.`}
      >
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (!usernameMember || busy) return
            setBusy(true)
            setUsernameError('')
            try {
              await unwrap(
                client.PUT('/teams/{id}/members/{user}/username', {
                  params: { path: { id: team, user: usernameMember.id } },
                  body: { username },
                }),
              )
              setUsernameMember(null)
              refresh()
            } catch (err) {
              setUsernameError(message(err))
            } finally {
              setBusy(false)
            }
          }}
        >
          <div className="dialog-body field-stack">
            <label>
              Username
              <Input
                value={username}
                onChange={(event) => setUsername(event.target.value.toLowerCase())}
                minLength={2}
                maxLength={30}
                pattern="[a-z][a-z0-9_-]{1,29}"
                required
                autoComplete="off"
              />
            </label>
            <p className="field-help">
              2–30 lowercase letters, digits, underscores, or hyphens. Start with a letter.
              Usernames are unique within this team.
            </p>
            {usernameError && (
              <div className="inline-error" role="alert">
                {usernameError}
              </div>
            )}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setUsernameMember(null)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? 'Saving…' : 'Save username'}
            </Button>
          </div>
        </form>
      </Dialog>
      <Dialog
        open={Boolean(removeTeam)}
        onOpenChange={(open) => {
          if (!busy && !open) setRemoveTeam(null)
        }}
        title={`Delete ${removeTeam?.name || 'this team'}?`}
        description="Remove the team, its memberships, project grants, and pending team invitations. Individual accounts remain."
      >
        <div className="dialog-body field-stack">
          <p>
            This also removes team access to every project. This cleanup remains available on Free.
          </p>
          <label>
            Type <code>{removeTeam?.name}</code> to delete this team
            <Input
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              aria-label="Confirm team name"
            />
          </label>
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRemoveTeam(null)}>
            Keep team
          </Button>
          <Button
            variant="danger"
            disabled={busy || !removeTeam || confirmation !== removeTeam.name}
            onClick={async () => {
              if (busy || !removeTeam || confirmation !== removeTeam.name) return
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.DELETE('/teams/{id}', { params: { path: { id: removeTeam.id } } }),
                )
                setRemoveTeam(null)
                setSelected('')
                refresh()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Delete team
          </Button>
        </div>
      </Dialog>
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
              <Input
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
  subject,
  scopeName,
  onSave,
}: {
  role: string
  roles: string[]
  subject: string
  scopeName: string
  onSave: (role: string) => Promise<void>
}) {
  const [next, setNext] = useState(role)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const save = async (value: string) => {
    if (busy || (!value && confirmation !== subject)) return
    setBusy(true)
    setError('')
    try {
      await onSave(value)
      setConfirmOpen(false)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <form
        className="role-editor"
        onSubmit={(event) => {
          event.preventDefault()
          if (busy || next === role) return
          if (!next) {
            setError('')
            setConfirmation('')
            setConfirmOpen(true)
          } else void save(next)
        }}
      >
        <SelectField
          label={`Role for ${subject}`}
          value={next}
          onValueChange={(value) => setNext(value)}
          options={[
            ...(roles.map((value) => ({
              value: value,
              label: value,
            })) ?? []),
            {
              value: '',
              label: 'Remove access',
            },
          ]}
        />
        <Button
          size="sm"
          type="submit"
          disabled={busy || next === role}
          variant={next ? 'secondary' : 'danger'}
        >
          {next ? 'Save' : 'Remove'}
        </Button>
        {error && !confirmOpen && (
          <span className="inline-error" role="alert">
            {error}
          </span>
        )}
      </form>
      <Dialog
        open={confirmOpen}
        onOpenChange={(open) => {
          if (!busy) setConfirmOpen(open)
        }}
        title={`Remove ${subject}’s access?`}
        description={`Remove this membership from ${scopeName}. Other memberships and the account remain.`}
      >
        <div className="dialog-body field-stack">
          <label>
            Type <code>{subject}</code> to remove this membership
            <Input
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              aria-label="Confirm member name"
            />
          </label>
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button variant="ghost" disabled={busy} onClick={() => setConfirmOpen(false)}>
            Keep access
          </Button>
          <Button
            variant="danger"
            disabled={busy || confirmation !== subject}
            onClick={() => void save('')}
          >
            {busy ? 'Removing…' : 'Remove access'}
          </Button>
        </div>
      </Dialog>
    </>
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
                <Input
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  maxLength={254}
                  required
                />
              </label>
              <label>
                Role
                <SelectField
                  label="Role"
                  value={role}
                  onValueChange={(value) => setRole(value)}
                  options={
                    (target === 'team'
                      ? ['member', 'admin']
                      : ['viewer', 'developer', 'admin']
                    ).map((value) => ({
                      value: value,
                      label: value,
                    })) ?? []
                  }
                />
              </label>
              <label className="checkbox-row">
                <Input
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
  const [changeAction, setChangeAction] = useState<
    'grant-admin' | 'remove-admin' | 'enable' | 'disable'
  >('enable')
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const removingAccess = changeAction === 'remove-admin' || changeAction === 'disable'
  const confirmationTarget = change?.name || change?.email || change?.id || ''
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Installation accounts</h2>
            <HeadingHelp title="Installation accounts">
              Administrators have access across projects. The owner account is protected.
            </HeadingHelp>
          </div>
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
                <Menu
                  trigger={
                    <Button
                      variant="ghost"
                      size="icon"
                      disabled={busy}
                      aria-label={`Actions for ${user.name || user.email}`}
                    >
                      <MoreHorizontal size={16} strokeWidth={1.75} />
                    </Button>
                  }
                >
                  <MenuItem
                    destructive={user.admin}
                    disabled={busy}
                    onSelect={() => {
                      setError('')
                      setConfirmation('')
                      setChangeAction(user.admin ? 'remove-admin' : 'grant-admin')
                      setChange({ ...user, admin: !user.admin })
                    }}
                  >
                    {user.admin ? 'Remove admin' : 'Make admin'}
                  </MenuItem>
                  <MenuItem
                    destructive={!user.disabled}
                    disabled={busy}
                    onSelect={() => {
                      setError('')
                      setConfirmation('')
                      setChangeAction(user.disabled ? 'enable' : 'disable')
                      setChange({ ...user, disabled: !user.disabled })
                    }}
                  >
                    {user.disabled ? 'Enable' : 'Disable'}
                  </MenuItem>
                </Menu>
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
        <div className="dialog-body field-stack">
          {removingAccess && (
            <label>
              Type <code>{confirmationTarget}</code> to{' '}
              {changeAction === 'disable' ? 'disable this account' : 'remove administrator access'}
              <Input
                value={confirmation}
                onChange={(event) => setConfirmation(event.target.value)}
                autoComplete="off"
                spellCheck={false}
                aria-label="Confirm account name"
              />
            </label>
          )}
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
            variant={removingAccess ? 'danger' : 'primary'}
            disabled={busy || !change || (removingAccess && confirmation !== confirmationTarget)}
            onClick={async () => {
              if (!change || busy || (removingAccess && confirmation !== confirmationTarget)) return
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
            {busy
              ? 'Updating…'
              : changeAction === 'disable'
                ? 'Disable account'
                : changeAction === 'enable'
                  ? 'Enable account'
                  : changeAction === 'remove-admin'
                    ? 'Remove administrator'
                    : 'Make administrator'}
          </Button>
        </div>
      </Dialog>
    </>
  )
}
