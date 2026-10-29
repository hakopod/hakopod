import { SelectField } from '../components/ui/select'
import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { FormPage, FormSection, FormHint } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Empty, ErrorState, Loading, RequestError } from '../components/shared'
import type { components } from '../lib/api.generated'
import { Icon } from '../components/icons'
import { focusedDialog, ReviewFacts } from '../components/operation-review'
import { SettingsEmpty, SettingsRow, SettingsRows } from '../components/settings-section'
export const Route = createFileRoute('/settings/host-access')({ component: HostAccess })
function HostAccess() {
  const cache = useQueryClient()
  const [user, setUser] = useState('')
  const [node, setNode] = useState('')
  const [hours, setHours] = useState(1)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [revoke, setRevoke] = useState<components['schemas']['HostGrant'] | null>(null)
  const access = useQuery({
    queryKey: ['host-access'],
    queryFn: ({ signal }) => unwrap(client.GET('/host-access', { signal })),
    gcTime: 0,
  })
  const users = useQuery({
    queryKey: ['users'],
    queryFn: ({ signal }) => unwrap(client.GET('/users', { signal })),
    enabled: Boolean(access.data?.super_admin),
    gcTime: 0,
  })
  const nameOf = (id: string) => users.data?.items.find((item) => item.id === id)?.name || id
  const refresh = () => {
    void access.refetch()
    void cache.invalidateQueries({ queryKey: ['me'] })
  }
  if (access.isPending) return <Loading />
  if (access.error || !access.data) return <ErrorState error={access.error} />
  if (!access.data.super_admin)
    return (
      <Empty
        icon="lock"
        title="Super admin required"
        description="Only the installation owner can grant host terminal permissions."
      />
    )
  return (
    <FormPage
      title="Host terminal access"
      description="Grant temporary authority for a specific node. Ordinary administrator access does not include host shells."
      breadcrumbs={[{ label: 'Account & access', to: '/settings' }, { label: 'Host access' }]}
      icon="terminal"
      help={
        <>
          <FormHint title="Choose the narrowest scope">
            Prefer a named node and a short expiration. Wildcard access applies to every node.
          </FormHint>
          <FormHint title="Host authority is privileged">
            Commands run on the host outside application container boundaries.
          </FormHint>
        </>
      }
    >
      <div className="form-body">
        <FormSection title="Grant temporary access" icon="key">
          <form
            className="auth-form"
            onSubmit={async (event) => {
              event.preventDefault()
              if (busy || !user || !node) return
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.PUT('/host-access/{user}', {
                    params: { path: { user } },
                    body: {
                      node,
                      permission: 'nodes:terminal',
                      expires_at: new Date(Date.now() + hours * 3600000).toISOString(),
                    },
                  }),
                )
                setUser('')
                refresh()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {users.error ? (
              <ErrorState error={users.error} />
            ) : (
              <label>
                Person
                <SelectField
                  label="Person"
                  value={user}
                  onValueChange={(value) => setUser(value)}
                  required
                  options={[
                    {
                      value: '',
                      label: 'Choose a person',
                    },
                    ...(users.data?.items
                      .filter((item) => !item.disabled && !item.owner)
                      .map((item) => ({
                        value: item.id,
                        label: item.name + ' · ' + item.email,
                      })) ?? []),
                  ]}
                />
              </label>
            )}
            <label>
              Node
              <SelectField
                label="Node"
                value={node}
                onValueChange={(value) => setNode(value)}
                required
                options={[
                  {
                    value: '',
                    label: 'Choose a node',
                  },
                  ...(access.data.nodes.map((item) => ({
                    value: item.name,
                    label: item.name,
                  })) ?? []),
                  {
                    value: '*',
                    label: 'All nodes',
                  },
                ]}
              />
            </label>
            <label>
              Expires after
              <SelectField
                label="Expires after"
                value={String(hours)}
                onValueChange={(value) => setHours(Number(value))}
                options={[
                  {
                    value: '1',
                    label: '1 hour',
                  },
                  {
                    value: '8',
                    label: '8 hours',
                  },
                  {
                    value: '24',
                    label: '24 hours',
                  },
                ]}
              />
            </label>
            <Button type="submit" variant="primary" disabled={busy || !user || !node}>
              Grant host access
            </Button>
          </form>
        </FormSection>
        <FormSection title="Current grants" icon="terminal">
          {access.data.grants.length ? (
            <SettingsRows label="Current host grants">
              {access.data.grants.map((grant) => (
                <SettingsRow
                  key={`${grant.identity_id}/${grant.node}`}
                  mark={<Icon name="terminal" size={16} />}
                  name={nameOf(grant.identity_id)}
                  detail={grant.node === '*' ? 'All nodes' : grant.node}
                  meta={`Expires ${timestamp(grant.expires_at)}`}
                  actions={
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label={`Revoke host access for ${nameOf(grant.identity_id)} on ${grant.node === '*' ? 'all nodes' : grant.node}`}
                      onClick={() => {
                        setError('')
                        setRevoke(grant)
                      }}
                    >
                      Revoke
                    </Button>
                  }
                />
              ))}
            </SettingsRows>
          ) : (
            <SettingsEmpty title="No delegated host access">
              The super admin keeps owner authority.
            </SettingsEmpty>
          )}
        </FormSection>
        {error && !revoke && <RequestError error={error} />}
      </div>
      <Dialog
        open={Boolean(revoke)}
        className={focusedDialog}
        onOpenChange={(open) => {
          if (!open && !busy) setRevoke(null)
        }}
        title="Revoke host access?"
        description={`Remove ${revoke ? nameOf(revoke.identity_id) : 'this person'}’s terminal authority for ${revoke?.node === '*' ? 'all nodes' : revoke?.node || 'this node'}.`}
      >
        <div className="dialog-body">
          {revoke && (
            <ReviewFacts
              facts={[
                ['Person', nameOf(revoke.identity_id)],
                ['Node', revoke.node === '*' ? 'All nodes' : revoke.node],
                ['Expires', timestamp(revoke.expires_at)],
              ]}
            />
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRevoke(null)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy || !revoke}
            onClick={async () => {
              if (!revoke || busy) return
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.DELETE('/host-access/{user}/{node}', {
                    params: { path: { user: revoke.identity_id, node: revoke.node } },
                  }),
                )
                setRevoke(null)
                refresh()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Revoking…' : 'Revoke host access'}
          </Button>
        </div>
      </Dialog>
    </FormPage>
  )
}
