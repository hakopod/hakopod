import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useScope } from '../lib/scope'
import { useLicense } from '../lib/license'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Dialog } from './ui/dialog'
import { PageHeader, HeadingHelp, ErrorState, Loading, Note, RequestError } from './shared'
import { Trash2 } from 'lucide-react'

export const rolePermissions = [
  { id: 'deployments:read', name: 'Read applications and deployments' },
  { id: 'deployments:write', name: 'Deploy and operate applications' },
  { id: 'logs:read', name: 'Read logs and requests' },
]
export function useCustomRoles() {
  return useQuery({
    queryKey: ['custom-roles'],
    queryFn: ({ signal }) => unwrap(client.GET('/roles', { signal })),
  })
}
export function CustomRoles() {
  const scope = useScope(),
    license = useLicense(),
    roles = useCustomRoles(),
    cache = useQueryClient()
  const [removal, setRemoval] = useState<{ id: string; name: string; revision: number } | null>(
    null,
  )
  const [error, setError] = useState(''),
    [busy, setBusy] = useState(false)
  const enabled = license.data?.catalog.some((f) => f.id === 'custom_roles' && f.enabled)
  return (
    <section className="grid min-w-0 gap-4 wrap-anywhere">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="hako-section-heading-title">
          <h2>Custom roles</h2>
          <HeadingHelp title="Custom roles">
            Choose project permissions. Installation administration and membership management remain
            with administrators.
          </HeadingHelp>
        </div>
        {scope.identity.admin && (
          <Button asChild size="sm" variant="outline">
            <a href="/settings/roles/new">Create role</a>
          </Button>
        )}
      </div>
      {!enabled && (
        <Note>
          Pro custom roles require an explicit entitlement. Fixed roles remain available. Existing
          custom assignments grant no access while the entitlement is inactive.
        </Note>
      )}
      {roles.isPending ? (
        <Loading />
      ) : roles.error ? (
        <ErrorState error={roles.error} />
      ) : (
        roles.data?.items.map((role) => (
          <div key={role.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
            <div className="min-w-0 wrap-anywhere">
              <p>
                {role.name} <span className="muted-text text-xs">Custom</span>
              </p>
              <p className="muted-text text-sm">
                {role.permissions
                  .map((p) => rolePermissions.find((x) => x.id === p)?.name || p)
                  .join(' · ')}
              </p>
            </div>
            {scope.identity.admin && (
              <div className="flex gap-2">
                <Button asChild variant="outline" size="sm">
                  <a href={`/settings/roles/${encodeURIComponent(role.id)}/edit`}>Edit</a>
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={`Delete ${role.name}`}
                  onClick={() => {
                    setError('')
                    setRemoval(role)
                  }}
                >
                  <Trash2 size={16} />
                </Button>
              </div>
            )}
          </div>
        ))
      )}
      <Dialog
        className="wrap-anywhere"
        open={Boolean(removal)}
        onOpenChange={(open) => {
          if (!busy && !open) setRemoval(null)
        }}
        title="Delete custom role?"
        description={`This removes ${removal?.name || 'this role'} and its project assignments and pending invitations. It does not replace access with another role.`}
      >
        <div className="dialog-body">{error && <RequestError error={error} />}</div>
        <div className="dialog-footer">
          <Button variant="ghost" disabled={busy} onClick={() => setRemoval(null)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              if (!removal) return
              setBusy(true)
              try {
                await unwrap(
                  client.DELETE('/roles/{role}', {
                    params: { path: { role: removal.id } },
                    body: { expected_revision: removal.revision },
                  }),
                )
                setRemoval(null)
                await cache.invalidateQueries({ queryKey: ['custom-roles'] })
                await cache.invalidateQueries({ queryKey: ['project-members'] })
              } catch (e) {
                setError(message(e))
              } finally {
                setBusy(false)
              }
            }}
          >
            <Trash2 size={16} />
            Delete role
          </Button>
        </div>
      </Dialog>
    </section>
  )
}

export function CustomRoleEditor({ id }: { id?: string }) {
  const roles = useCustomRoles(),
    scope = useScope(),
    license = useLicense()
  const [name, setName] = useState<string | null>(null),
    [permissions, setPermissions] = useState<string[] | null>(null)
  const [review, setReview] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState('')
  if (!scope.identity.admin)
    return <Note>Only installation administrators can manage custom roles.</Note>
  if (roles.isPending || license.isPending) return <Loading />
  if (roles.error || license.error) return <ErrorState error={roles.error || license.error} />
  const role = roles.data?.items.find((r) => r.id === id)
  if (id && !role) return <Note>This role no longer exists.</Note>
  const currentName = name ?? role?.name ?? '',
    currentPermissions = permissions ?? role?.permissions ?? ['deployments:read']
  const enabled = license.data?.catalog.some((f) => f.id === 'custom_roles' && f.enabled)
  return (
    <div className="ops-page min-w-0 wrap-anywhere">
      <PageHeader
        title={id ? 'Edit custom role' : 'Create custom role'}
        action={
          <Button asChild variant="outline">
            <a href="/settings?tab=teams">Back to access</a>
          </Button>
        }
      />
      <form
        className="grid min-w-0 gap-5 py-6"
        onSubmit={async (e) => {
          e.preventDefault()
          if (!enabled || busy) return
          if (!review) {
            setReview(true)
            return
          }
          setBusy(true)
          setError('')
          try {
            const body = {
              name: currentName,
              permissions: currentPermissions,
              expected_revision: role?.revision ?? 0,
            }
            if (id)
              await unwrap(client.PUT('/roles/{role}', { params: { path: { role: id } }, body }))
            else await unwrap(client.POST('/roles', { body }))
            window.location.assign('/settings?tab=teams')
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        {!enabled && <Note>A Pro license with custom roles is required to save.</Note>}
        {review ? (
          <div className="grid gap-3">
            <h2>Review permissions</h2>
            <p className="min-w-0 wrap-anywhere">{currentName}</p>
            <ul className="list-disc pl-5">
              {currentPermissions.map((p) => (
                <li key={p}>{rolePermissions.find((x) => x.id === p)?.name}</li>
              ))}
            </ul>
            <Note>
              Changes apply to every person and team assigned this role on their next authorized
              request.
            </Note>
          </div>
        ) : (
          <>
            <label className="grid gap-2">
              Role name
              <Input
                required
                maxLength={80}
                value={currentName}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <fieldset className="grid gap-3">
              <legend className="mb-3">Project permissions</legend>
              {rolePermissions.map((p) => (
                <label key={p.id} className="flex min-h-11 items-center gap-3">
                  <input
                    type="checkbox"
                    checked={currentPermissions.includes(p.id)}
                    onChange={(e) => {
                      const next = e.target.checked
                        ? [...currentPermissions, p.id]
                        : currentPermissions.filter((x) => x !== p.id)
                      setPermissions(
                        p.id === 'deployments:write' && e.target.checked
                          ? [...new Set([...next, 'deployments:read'])]
                          : p.id === 'deployments:read' && !e.target.checked
                            ? next.filter((x) => x !== 'deployments:write')
                            : next,
                      )
                    }}
                  />
                  {p.name}
                </label>
              ))}
            </fieldset>
          </>
        )}
        {error && <RequestError error={error} />}
        <div className="flex gap-3">
          {review && (
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={() => setReview(false)}
            >
              Edit
            </Button>
          )}
          <Button
            type="submit"
            variant="primary"
            disabled={!enabled || busy || !currentName.trim() || !currentPermissions.length}
          >
            {busy ? 'Saving…' : review ? 'Save role' : 'Review role'}
          </Button>
        </div>
      </form>
    </div>
  )
}

export function OrganizationSecurity() {
  const scope = useScope(),
    license = useLicense(),
    cache = useQueryClient()
  const query = useQuery({
    queryKey: ['organization-security'],
    queryFn: ({ signal }) => unwrap(client.GET('/organization/security', { signal })),
    enabled: scope.identity.admin && !scope.identity.mfa_required,
  })
  const [review, setReview] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState('')
  if (!scope.identity.admin || scope.identity.mfa_required) return null
  const enabled = license.data?.catalog.some((f) => f.id === 'team_mfa' && f.enabled)
  return (
    <section className="grid gap-4 py-6">
      <div className="hako-section-heading-title">
        <h2>Organization MFA</h2>
        <HeadingHelp title="Organization MFA">
          Require every human browser and CLI session to verify an authenticator or passkey. Scoped
          machine credentials remain independent.
        </HeadingHelp>
      </div>
      {query.isPending ? (
        <Loading />
      ) : query.error ? (
        <ErrorState error={query.error} />
      ) : (
        query.data && (
          <>
            <p>
              {query.data.require_mfa
                ? 'Required for this installation'
                : 'Optional for this installation'}{' '}
              · {query.data.ready_members} of {query.data.members} people have a factor enrolled.
            </p>
            <Note>
              Existing unverified sessions must complete MFA before accessing workloads. People can
              still set up their personal authenticator. License expiry does not switch off an
              enabled policy.
            </Note>
            {!enabled && !query.data.require_mfa && (
              <Note>A Pro license with organization MFA is required to enable this policy.</Note>
            )}
            {!scope.identity.mfa_verified && (
              <Note>
                Verify your own MFA session in <a href="/settings?tab=account">Account security</a>{' '}
                before enabling this policy.
              </Note>
            )}
            <div>
              <Button
                variant="outline"
                disabled={!query.data.require_mfa && (!enabled || !scope.identity.mfa_verified)}
                onClick={() => {
                  setError('')
                  setReview(true)
                }}
              >
                {query.data.require_mfa ? 'Review disabling requirement' : 'Review MFA requirement'}
              </Button>
            </div>
            <Dialog
              className="wrap-anywhere"
              open={review}
              onOpenChange={(open) => {
                if (!busy) setReview(open)
              }}
              title={query.data.require_mfa ? 'Make MFA optional?' : 'Require MFA for everyone?'}
              description={
                query.data.require_mfa
                  ? 'People keep their enrolled factors, but password-only sessions can access this installation again.'
                  : 'Unverified sessions will lose workload access immediately. Personal security setup remains available.'
              }
            >
              <div className="dialog-body">{error && <RequestError error={error} />}</div>
              <div className="dialog-footer">
                <Button variant="ghost" disabled={busy} onClick={() => setReview(false)}>
                  Cancel
                </Button>
                <Button
                  variant="primary"
                  disabled={busy}
                  onClick={async () => {
                    if (!query.data) return
                    setBusy(true)
                    try {
                      await unwrap(
                        client.PUT('/organization/security', {
                          body: {
                            require_mfa: !query.data.require_mfa,
                            expected_revision: query.data.revision,
                          },
                        }),
                      )
                      setReview(false)
                      await cache.invalidateQueries({ queryKey: ['organization-security'] })
                      await cache.invalidateQueries({ queryKey: ['account-security'] })
                    } catch (e) {
                      setError(message(e))
                    } finally {
                      setBusy(false)
                    }
                  }}
                >
                  Confirm policy
                </Button>
              </div>
            </Dialog>
          </>
        )
      )}
    </section>
  )
}
