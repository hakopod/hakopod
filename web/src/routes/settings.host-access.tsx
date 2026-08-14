import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { FormPage, FormSection, FormHint } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Empty, ErrorState, Loading, Note } from '../components/shared'
import type { components } from '../lib/api.generated'
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
                <select value={user} onChange={(event) => setUser(event.target.value)} required>
                  <option value="">Choose a person</option>
                  {users.data?.items
                    .filter((item) => !item.disabled && !item.owner)
                    .map((item) => (
                      <option key={item.id} value={item.id}>
                        {item.name} · {item.email}
                      </option>
                    ))}
                </select>
              </label>
            )}
            <label>
              Node
              <select value={node} onChange={(event) => setNode(event.target.value)} required>
                <option value="">Choose a node</option>
                {access.data.nodes.map((item) => (
                  <option key={item.name}>{item.name}</option>
                ))}
                <option value="*">All nodes</option>
              </select>
            </label>
            <label>
              Expires after
              <select value={hours} onChange={(event) => setHours(Number(event.target.value))}>
                <option value={1}>1 hour</option>
                <option value={8}>8 hours</option>
                <option value={24}>24 hours</option>
              </select>
            </label>
            <Button type="submit" variant="primary" disabled={busy || !user || !node}>
              Grant host access
            </Button>
          </form>
        </FormSection>
        <FormSection title="Current grants" icon="terminal">
          {access.data.grants.length ? (
            access.data.grants.map((grant) => (
              <div className="settings-list-row" key={`${grant.identity_id}/${grant.node}`}>
                <div>
                  <strong>
                    {users.data?.items.find((item) => item.id === grant.identity_id)?.name ||
                      grant.identity_id}
                  </strong>
                  <small>
                    {grant.node === '*' ? 'All nodes' : grant.node} · Expires{' '}
                    {timestamp(grant.expires_at)}
                  </small>
                </div>
                <Button size="sm" variant="danger" onClick={() => setRevoke(grant)}>
                  Revoke
                </Button>
              </div>
            ))
          ) : (
            <Note>No delegated host permissions. The super admin retains owner authority.</Note>
          )}
        </FormSection>
        {error && <ErrorState error={error} />}
      </div>
      <div className="form-footer">
        <Link className="button" to="/infrastructure">
          Back to infrastructure
        </Link>
      </div>
      <Dialog
        open={Boolean(revoke)}
        onOpenChange={(open) => {
          if (!open && !busy) setRevoke(null)
        }}
        title="Revoke host access?"
        description={`Remove delegated terminal authority for ${revoke?.node || 'this node'}.`}
      >
        <div className="dialog-body">{error && <ErrorState error={error} />}</div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRevoke(null)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              if (!revoke) return
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
            Revoke grant
          </Button>
        </div>
      </Dialog>
    </FormPage>
  )
}
