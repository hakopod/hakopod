import { SelectField } from '../components/ui/select'
import { Input } from '../components/ui/input'
import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
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
  const [confirmation, setConfirmation] = useState('')
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
                <Button
                  size="sm"
                  variant="danger"
                  aria-label={`Revoke host access for ${grant.identity_id} on ${grant.node === '*' ? 'all nodes' : grant.node}`}
                  onClick={() => {
                    setError('')
                    setConfirmation('')
                    setRevoke(grant)
                  }}
                >
                  Revoke
                </Button>
              </div>
            ))
          ) : (
            <Note>No delegated host permissions. The super admin retains owner authority.</Note>
          )}
        </FormSection>
        {error && !revoke && (
          <div className="inline-error" role="alert">
            {error}
          </div>
        )}
      </div>
      <Dialog
        open={Boolean(revoke)}
        onOpenChange={(open) => {
          if (!open && !busy) setRevoke(null)
        }}
        title="Revoke host access?"
        description={`Remove ${users.data?.items.find((item) => item.id === revoke?.identity_id)?.name || revoke?.identity_id || 'this person'}’s terminal authority for ${revoke?.node === '*' ? 'all nodes' : revoke?.node || 'this node'}.`}
      >
        <div className="dialog-body field-stack">
          <label>
            Type <code>{revoke?.identity_id}</code> to revoke this grant
            <Input
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              aria-label="Confirm account ID"
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
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy || !revoke || confirmation !== revoke.identity_id}
            onClick={async () => {
              if (!revoke || busy || confirmation !== revoke.identity_id) return
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
