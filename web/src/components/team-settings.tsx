import { CustomRoles, OrganizationSecurity, useCustomRoles, rolePermissions } from './pro-access'
import { Input } from './ui/input'
import { Icon } from './icons'
import { useAuthStatus } from '../lib/installation-settings'
import { SelectField } from './ui/select'
import { Avatar } from './avatar'
import { useLicense } from '../lib/license'
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
import { focusedDialog, plural, ReviewFacts, ReviewNotice } from './operation-review'
import { Copy, ErrorState, Loading, Note, RequestError } from './shared'
import {
  SettingsEmpty,
  SettingsRow,
  SettingsRows,
  SettingsSection,
  SettingsState,
} from './settings-section'
import { GuidedEmpty } from './resource-card'

export default function TeamSettings() {
  const scope = useScope()
  const license = useLicense()
  const authStatus = useAuthStatus()
  const hasFeature = (feature: string) =>
    Boolean(license.data?.catalog.find((item) => item.id === feature)?.enabled)
  const customRoles = useCustomRoles()
  const projectRoles = [
    'admin',
    'developer',
    'viewer',
    ...(hasFeature('custom_roles') ? customRoles.data?.items.map((r) => r.id) || [] : []),
  ]
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
  const [grantReview, setGrantReview] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const teams = useQuery({
    queryKey: ['teams'],
    queryFn: ({ signal }) => unwrap(client.GET('/teams', { signal })),
    staleTime: 30000,
  })
  const team = teams.data?.items.some((item) => item.id === selected)
    ? selected
    : teams.data?.items[0]?.id || ''
  const mayCreateTeam =
    scope.identity.admin &&
    teams.isSuccess &&
    (teams.data.items.length === 0 ||
      authStatus.data?.deployment_mode === 'managed-cloud' ||
      hasFeature('multi_team'))
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
  const memberCount = members.data?.items.length
  const singleTeamLimit =
    scope.identity.admin &&
    teams.isSuccess &&
    teams.data.items.length > 0 &&
    authStatus.data?.deployment_mode === 'self-hosted' &&
    !hasFeature('multi_team')
  const createTeam = scope.identity.admin && (
    <Button
      variant="outline"
      disabled={!mayCreateTeam}
      onClick={() => {
        setError('')
        setAdding(true)
      }}
    >
      Create team
    </Button>
  )
  const inviteMember = current && canTeam && (
    <Button
      variant="primary"
      disabled={!hasFeature('invitations')}
      onClick={() => setInvite('team')}
    >
      Invite member
    </Button>
  )
  const inviteProject = canShareProject && scope.project && (
    <Button
      variant="primary"
      disabled={!hasFeature('invitations')}
      onClick={() => setInvite('project')}
    >
      Invite to project
    </Button>
  )
  const projectState = !scope.project
    ? 'Choose a project to manage access.'
    : !currentProject
      ? projects.isSuccess
        ? 'This project is unavailable. Choose another project to manage access.'
        : undefined
      : personalProject
        ? `${scope.project} is a personal workspace. Personal workspaces cannot be shared with members or teams.`
        : !canProject
          ? `Project administrators manage access to ${scope.project}.`
          : project.data?.items.length
            ? `${scope.project} · ${plural(project.data.items.length, 'direct member or team', 'direct members or teams')}`
            : scope.project
  return (
    <>
      {license.error && <ErrorState error={license.error} retry={() => void license.refetch()} />}
      <SettingsSection
        title="Teams"
        help="Manage people together, then grant teams access to projects. Team roles control the team’s membership."
        state={
          teams.data &&
          (current
            ? `${current.name} · ${memberCount === undefined ? 'loading members' : plural(memberCount, 'member')}${singleTeamLimit ? ' · Free includes one team; a multi-team license adds more' : ''}`
            : undefined)
        }
        actions={
          !!teams.data?.items.length && (
            <>
              {teams.data.items.length > 1 && (
                <div className="w-full min-w-0 sm:w-52">
                  <SelectField
                    compact
                    label="Selected team"
                    value={team}
                    onValueChange={setSelected}
                    options={teams.data.items.map((item) => ({ value: item.id, label: item.name }))}
                  />
                </div>
              )}
              {createTeam}
              {inviteMember}
              {current && scope.identity.admin && (
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={`Delete team ${current.name}`}
                  onClick={() => {
                    setError('')
                    setConfirmation('')
                    setRemoveTeam({ id: current.id, name: current.name })
                  }}
                >
                  <Icon name="trash" size={16} />
                </Button>
              )}
            </>
          )
        }
      >
        {teams.isPending ? (
          <Loading rows={2} />
        ) : teams.error ? (
          <ErrorState error={teams.error} retry={() => void teams.refetch()} />
        ) : !teams.data?.items.length ? (
          <GuidedEmpty
            compact
            icon="user"
            title="Work with your team"
            description="Create a team to invite collaborators and share projects with them."
            steps={[
              { title: 'Create a team', detail: 'Name it for the people in it.' },
              { title: 'Invite members', detail: 'Each with the role they need.' },
              { title: 'Share projects', detail: 'Give the team access in one step.' },
            ]}
            primary={createTeam}
          />
        ) : members.isPending ? (
          <Loading rows={2} />
        ) : members.error ? (
          <ErrorState error={members.error} />
        ) : !members.data?.items.length ? (
          <SettingsEmpty
            title={`No members in ${current?.name || 'this team'}`}
            action={inviteMember}
          >
            Invite people to work in this team’s projects.
          </SettingsEmpty>
        ) : (
          <SettingsRows label="Team members">
            {members.data.items.map((member) => (
              <SettingsRow
                key={member.id}
                className="settings-row-member"
                mark={<Avatar name={member.name} url={member.avatar_url} />}
                name={
                  <>
                    {member.name}
                    {member.username && <span className="muted-text"> @{member.username}</span>}
                  </>
                }
                detail={member.email}
                status={
                  canTeam && member.role !== 'owner' ? (
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
                  )
                }
                actions={
                  hasFeature('teams') &&
                  (canTeam || member.id === scope.identity.id) && (
                    <Menu
                      trigger={
                        <Button
                          size="icon"
                          variant="ghost"
                          aria-label={`Actions for ${member.name || member.email}`}
                        >
                          <MoreHorizontal size={16} />
                        </Button>
                      }
                    >
                      <MenuItem
                        onSelect={() => {
                          setUsernameMember(member)
                          setUsername(member.username || '')
                          setUsernameError('')
                        }}
                      >
                        Edit team username
                      </MenuItem>
                    </Menu>
                  )
                }
              />
            ))}
          </SettingsRows>
        )}
      </SettingsSection>
      <SettingsSection
        title="Project access"
        help="Viewers inspect applications and logs. Developers also deploy. Project administrators manage membership. Role changes apply to new requests."
        state={projectState}
        actions={!!project.data?.items.length && inviteProject}
      >
        {scope.project && projects.isPending ? (
          <Loading rows={2} />
        ) : scope.project && projects.error ? (
          <ErrorState error={projects.error} retry={() => void projects.refetch()} />
        ) : !canShareProject || !scope.project ? null : project.isPending ? (
          <Loading rows={2} />
        ) : project.error ? (
          <ErrorState error={project.error} />
        ) : (
          <>
            {project.data?.items.length ? (
              <SettingsRows label="Project members">
                {project.data.items.map((member) => (
                  <SettingsRow
                    key={member.identity_id || member.team_id}
                    className="settings-row-member"
                    mark={<Icon name={member.team_id ? 'grid' : 'user'} size={16} />}
                    name={member.name}
                    detail={member.team_id ? 'Team' : 'Member'}
                    status={
                      <RoleEditor
                        key={`${member.identity_id || member.team_id}-${member.role}`}
                        role={member.role}
                        subject={member.name || member.identity_id || member.team_id || 'member'}
                        scopeName={`project ${scope.project}`}
                        roles={hasFeature('project_rbac') ? projectRoles : [member.role]}
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
                    }
                  />
                ))}
              </SettingsRows>
            ) : (
              <SettingsEmpty title="No direct members or teams" action={inviteProject}>
                Installation administrators keep access.
              </SettingsEmpty>
            )}
            {hasFeature('project_rbac') &&
              hasFeature('teams') &&
              Boolean(teams.data?.items.length) && (
                <form
                  className="settings-inline-controls"
                  onSubmit={async (e) => {
                    e.preventDefault()
                    if (grantRole.startsWith('custom:') && !grantReview) {
                      setGrantReview(true)
                      return
                    }
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
                      setGrantReview(false)
                      refresh()
                    } catch (err) {
                      setError(message(err))
                    } finally {
                      setBusy(false)
                    }
                  }}
                >
                  <SelectField
                    label="Grant a team access"
                    value={grant}
                    onValueChange={(value) => {
                      setGrant(value)
                      setGrantReview(false)
                    }}
                    required
                    options={[
                      { value: '', label: 'Choose a team' },
                      ...(teams.data?.items.map((item) => ({ value: item.id, label: item.name })) ??
                        []),
                    ]}
                  />
                  <SelectField
                    label="Role"
                    value={grantRole}
                    onValueChange={(value) => {
                      setGrantRole(value)
                      setGrantReview(false)
                    }}
                    options={projectRoles.map((role) => ({
                      value: role,
                      label: customRoles.data?.items.find((r) => r.id === role)?.name || role,
                    }))}
                  />
                  <Button type="submit" disabled={busy || !grant}>
                    {grantRole.startsWith('custom:') && !grantReview
                      ? 'Review access'
                      : 'Grant access'}
                  </Button>
                  {grantReview && (
                    <Note>
                      Grant {customRoles.data?.items.find((r) => r.id === grantRole)?.name} to every
                      member of this team in {scope.project}:{' '}
                      {customRoles.data?.items
                        .find((r) => r.id === grantRole)
                        ?.permissions.map((p) => rolePermissions.find((x) => x.id === p)?.name || p)
                        .join(' · ')}
                      . Other memberships can grant additional access.
                    </Note>
                  )}
                </form>
              )}
          </>
        )}
      </SettingsSection>
      {error && !removeTeam && !adding && <RequestError error={error} />}
      <Dialog
        open={Boolean(usernameMember)}
        onOpenChange={(open) => {
          if (!open && !busy) setUsernameMember(null)
        }}
        className={focusedDialog}
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
                pattern="[a-z][a-z0-9_\-]{1,29}"
                required
                autoComplete="off"
              />
            </label>
            <p className="field-help">
              2–30 lowercase letters, digits, underscores, or hyphens. Start with a letter.
              Usernames are unique within this team.
            </p>
            {usernameError && <RequestError error={usernameError} />}
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
        className={focusedDialog}
        title={`Delete ${removeTeam?.name || 'this team'}?`}
        description="This cannot be undone. Members lose access granted through this team."
      >
        <div className="dialog-body">
          <ReviewFacts
            facts={[
              ['Team', removeTeam?.name],
              [
                'Removed',
                'Memberships, project grants in every project and pending team invitations.',
              ],
              ['Kept', 'Individual accounts and access granted outside this team.'],
            ]}
          />
          <p className="field-help">This cleanup remains available on Free.</p>
          <label className="grid gap-1.5">
            Type <code>{removeTeam?.name}</code> to delete this team
            <Input
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              aria-label="Confirm team name"
            />
          </label>
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRemoveTeam(null)}>
            Cancel
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
            {busy ? 'Deleting…' : `Delete ${removeTeam?.name || 'team'}`}
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
        className={focusedDialog}
        title="Create a team"
        description="You become this team’s owner."
      >
        <form
          onSubmit={async (e) => {
            e.preventDefault()
            if (busy || !mayCreateTeam) return
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
            {error && <RequestError error={error} />}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setAdding(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={busy || !mayCreateTeam}>
              Create team
            </Button>
          </div>
        </form>
      </Dialog>
      <CustomRoles />
      <OrganizationSecurity />
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
  const customRoles = useCustomRoles()
  const [customReview, setCustomReview] = useState(false)
  const [next, setNext] = useState(role)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const save = async (value: string) => {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await onSave(value)
      setConfirmOpen(false)
      setCustomReview(false)
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
            setConfirmOpen(true)
          } else if (next.startsWith('custom:')) setCustomReview(true)
          else void save(next)
        }}
      >
        <SelectField
          label={`Role for ${subject}`}
          value={next}
          onValueChange={(value) => setNext(value)}
          options={[
            ...([...new Set([...roles, role])].map((value) => ({
              value: value,
              label: customRoles.data?.items.find((r) => r.id === value)?.name || value,
            })) ?? []),
            {
              value: '',
              label: 'Remove access',
            },
          ]}
        />
        {next !== role && (
          <>
            <Button size="sm" type="submit" disabled={busy} variant={next ? 'outline' : 'danger'}>
              {!next && <Icon name="trash" size={14} />}
              {next ? 'Save' : 'Remove'}
            </Button>
            <Button
              size="sm"
              type="button"
              variant="outline"
              disabled={busy}
              onClick={() => {
                setNext(role)
                setError('')
              }}
            >
              Cancel
            </Button>
          </>
        )}
        {error && !confirmOpen && !customReview && <RequestError error={error} />}
      </form>
      <Dialog
        className={`${focusedDialog} wrap-anywhere`}
        open={customReview}
        onOpenChange={(open) => {
          if (!busy) setCustomReview(open)
        }}
        title="Review custom access"
        description={`Apply this role to ${subject} in ${scopeName}. Other memberships may grant additional access.`}
      >
        <div className="dialog-body">
          <ReviewFacts
            facts={[
              ['Member', subject],
              ['Scope', scopeName],
              ['Role', customRoles.data?.items.find((r) => r.id === next)?.name || next],
              [
                'Permissions',
                customRoles.data?.items
                  .find((r) => r.id === next)
                  ?.permissions.map((p) => rolePermissions.find((x) => x.id === p)?.name || p)
                  .join(' · '),
              ],
            ]}
          />
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setCustomReview(false)}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy}
            onClick={async () => {
              await save(next)
            }}
          >
            {busy ? 'Applying…' : `Apply role to ${subject}`}
          </Button>
        </div>
      </Dialog>
      <Dialog
        open={confirmOpen}
        className={focusedDialog}
        onOpenChange={(open) => {
          if (!busy) setConfirmOpen(open)
        }}
        title={`Remove ${subject}’s access?`}
        description={`New requests from ${subject} in ${scopeName} are denied.`}
      >
        <div className="dialog-body">
          <ReviewFacts
            facts={[
              ['Member', subject],
              ['Scope', scopeName],
              ['Removed', `The ${role} membership.`],
              ['Kept', 'The account and its other memberships. You can invite them again later.'],
            ]}
          />
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setConfirmOpen(false)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            aria-busy={busy || undefined}
            disabled={busy}
            onClick={() => void save('')}
          >
            {busy ? 'Removing…' : `Remove ${subject}`}
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
  const customRoles = useCustomRoles()
  const license = useLicense()
  const custom = license.data?.catalog.some((f) => f.id === 'custom_roles' && f.enabled)
    ? customRoles.data?.items || []
    : []
  const [review, setReview] = useState(false)
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
          if (role.startsWith('custom:') && !review) {
            setReview(true)
            return
          }
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
                  onChange={(e) => {
                    setEmail(e.target.value)
                    setReview(false)
                  }}
                  maxLength={254}
                  required
                />
              </label>
              <label>
                Role
                <SelectField
                  label="Role"
                  value={role}
                  onValueChange={(value) => {
                    setRole(value)
                    setReview(false)
                  }}
                  options={
                    (target === 'team'
                      ? ['member', 'admin']
                      : ['viewer', 'developer', 'admin', ...custom.map((r) => r.id)]
                    ).map((value) => ({
                      value: value,
                      label: custom.find((r) => r.id === value)?.name || value,
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
          {review && !result && (
            <Note>
              Invite {email} with {custom.find((r) => r.id === role)?.name} in {project}:{' '}
              {custom
                .find((r) => r.id === role)
                ?.permissions.map((p) => rolePermissions.find((x) => x.id === p)?.name || p)
                .join(' · ')}
              .
            </Note>
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            {result ? 'Done' : 'Cancel'}
          </Button>
          {!result && (
            <Button type="submit" variant="primary" disabled={busy}>
              {busy
                ? 'Creating…'
                : role.startsWith('custom:') && !review
                  ? 'Review invitation'
                  : 'Create invitation'}
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
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const removingAccess = changeAction === 'remove-admin' || changeAction === 'disable'
  const subject = change?.name || change?.email || change?.id || ''
  const items = users.data?.items || []
  const admins = items.filter((user) => user.admin && !user.disabled).length
  const disabled = items.filter((user) => user.disabled).length
  return (
    <SettingsSection
      title="People"
      help="Every account that can sign in to this installation. Administrators have access across projects. The owner account is protected."
      state={
        users.data &&
        [
          plural(items.length, 'account'),
          plural(admins, 'administrator'),
          disabled ? `${disabled} disabled` : '',
        ]
          .filter(Boolean)
          .join(' · ')
      }
    >
      {users.isPending ? (
        <Loading />
      ) : users.error ? (
        <ErrorState error={users.error} />
      ) : (
        <SettingsRows label="Installation accounts">
          {items.map((user) => (
            <SettingsRow
              key={user.id}
              className="settings-row-member"
              muted={user.disabled}
              mark={<Icon name="user" size={16} />}
              name={user.name}
              detail={user.email}
              status={
                <SettingsState tone={user.disabled ? 'muted' : 'ok'}>
                  {user.disabled
                    ? 'Disabled'
                    : user.owner
                      ? 'Owner'
                      : user.admin
                        ? 'Administrator'
                        : 'Member'}
                </SettingsState>
              }
              actions={
                !user.owner && (
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
                        setChangeAction(user.disabled ? 'enable' : 'disable')
                        setChange({ ...user, disabled: !user.disabled })
                      }}
                    >
                      {user.disabled ? 'Enable' : 'Disable'}
                    </MenuItem>
                  </Menu>
                )
              }
            />
          ))}
        </SettingsRows>
      )}
      <Dialog
        open={Boolean(change)}
        onOpenChange={(open) => {
          if (!busy && !open) setChange(null)
        }}
        className={focusedDialog}
        title={`${userAction(changeAction, subject)}?`}
        description={
          removingAccess
            ? 'Takes effect on their next request. You can reverse it later.'
            : 'Takes effect on their next request.'
        }
      >
        <div className="dialog-body">
          <ReviewFacts
            facts={[
              [
                'Account',
                change
                  ? `${subject}${change.email && change.email !== subject ? ` · ${change.email}` : ''}`
                  : '',
              ],
              ['Sign-in', change?.disabled ? 'Blocked' : 'Allowed'],
              [
                'Access',
                change?.admin
                  ? 'Installation administrator across every project'
                  : 'Member; project and team roles still apply',
              ],
            ]}
          />
          {changeAction === 'disable' && (
            <ReviewNotice tone="warn" title="This account can no longer sign in">
              Re-enable it here to restore access.
            </ReviewNotice>
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setChange(null)}>
            Cancel
          </Button>
          <Button
            variant={removingAccess ? 'danger' : 'primary'}
            aria-busy={busy || undefined}
            disabled={busy || !change}
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
            {busy ? 'Updating…' : userAction(changeAction, subject)}
          </Button>
        </div>
      </Dialog>
    </SettingsSection>
  )
}

function userAction(action: 'grant-admin' | 'remove-admin' | 'enable' | 'disable', name: string) {
  if (action === 'disable') return `Disable ${name}`
  if (action === 'enable') return `Enable ${name}`
  if (action === 'remove-admin') return `Remove administrator access from ${name}`
  return `Make ${name} an administrator`
}
