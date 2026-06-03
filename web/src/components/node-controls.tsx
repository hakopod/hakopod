import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import type { Node } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Copy, ErrorState, Loading, Note, Status } from './shared'

export function NodeAction({
  node,
  action,
  onClose,
  onChanged,
}: {
  node: Node
  action: 'cordon' | 'drain'
  onClose: () => void
  onChanged: () => void
}) {
  const [snapshot, setSnapshot] = useState(node)
  const [result, setResult] = useState<components['schemas']['NodeDrain'] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const title =
    action === 'drain'
      ? `Drain ${node.name}`
      : `${snapshot.unschedulable ? 'Resume scheduling on' : 'Cordon'} ${node.name}`
  return (
    <Dialog
      open
      wide
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={title}
      description={
        result
          ? 'Result returned by the cluster.'
          : action === 'drain'
            ? 'Cordon the worker and request safe workload evictions.'
            : snapshot.unschedulable
              ? 'Allow Kubernetes to schedule new workloads on this worker.'
              : 'Prevent new workloads from scheduling on this worker. Existing workloads remain running.'
      }
    >
      <div className="dialog-body">
        {result ? (
          <>
            <dl className="service-definition-list">
              <div>
                <dt>Scheduling</dt>
                <dd>{result.cordoned ? 'Cordoned' : 'Allowed'}</dd>
              </div>
              {action === 'drain' && (
                <>
                  <div>
                    <dt>Drain</dt>
                    <dd>
                      <Status value={result.complete ? 'complete' : 'incomplete'} />
                    </dd>
                  </div>
                  <div>
                    <dt>Observed workloads remaining</dt>
                    <dd>{result.remaining}</dd>
                  </div>
                  <div>
                    <dt>Evictions accepted</dt>
                    <dd>{result.evicted.length}</dd>
                  </div>
                </>
              )}
            </dl>
            {result.evicted.length > 0 && (
              <details>
                <summary>Accepted eviction requests</summary>
                <ul>
                  {result.evicted.map((pod) => (
                    <li key={pod}>
                      <code>{pod}</code>
                    </li>
                  ))}
                </ul>
              </details>
            )}
            {result.blockers.map((blocker) => (
              <Note key={blocker}>{blocker}</Note>
            ))}
            {action === 'drain' && !result.complete && (
              <Note>
                Eviction acceptance is not drain completion. Refresh and review another drain step
                after workloads finish terminating.
              </Note>
            )}
          </>
        ) : (
          <>
            <dl className="service-definition-list">
              <div>
                <dt>Node</dt>
                <dd>{snapshot.name}</dd>
              </div>
              <div>
                <dt>Observed resource version</dt>
                <dd>
                  <code>{snapshot.resource_version}</code>
                </dd>
              </div>
              <div>
                <dt>Current pods</dt>
                <dd>{snapshot.pods}</dd>
              </div>
            </dl>
            {action === 'drain' && (
              <Note>
                Disruption budgets are honored. Node-local data, unmanaged pods, the last ready
                scheduling node, and control-plane nodes block this action. Each request processes a
                bounded batch.
              </Note>
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
        <Button disabled={busy} onClick={onClose}>
          {result ? 'Close result' : 'Cancel'}
        </Button>
        {result ? (
          action === 'drain' &&
          !result.complete && (
            <Button
              disabled={busy}
              onClick={async () => {
                setBusy(true)
                setError('')
                try {
                  const list = await unwrap(client.GET('/nodes'))
                  const current = list.items.find((item) => item.name === node.name)
                  if (!current) throw new Error('This node is no longer reported by the cluster.')
                  setSnapshot(current)
                  setResult(null)
                  onChanged()
                } catch (err) {
                  setError(message(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              Refresh and review next step
            </Button>
          )
        ) : (
          <Button
            variant={action === 'drain' ? 'danger' : 'primary'}
            disabled={busy || snapshot.control_plane}
            onClick={async () => {
              if (busy) return
              setBusy(true)
              setError('')
              try {
                const next =
                  action === 'drain'
                    ? await unwrap(
                        client.POST('/nodes/{name}/drain', {
                          params: { path: { name: node.name } },
                          body: { expected_resource_version: snapshot.resource_version },
                        }),
                      )
                    : await unwrap(
                        client.POST('/nodes/{name}/cordon', {
                          params: { path: { name: node.name } },
                          body: {
                            expected_resource_version: snapshot.resource_version,
                            unschedulable: !snapshot.unschedulable,
                          },
                        }),
                      )
                setResult(next)
                onChanged()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy
              ? 'Requesting…'
              : action === 'drain'
                ? 'Drain workloads'
                : snapshot.unschedulable
                  ? 'Resume scheduling'
                  : 'Cordon node'}
          </Button>
        )}
      </div>
    </Dialog>
  )
}

export default function NodeEnrollments() {
  const [create, setCreate] = useState(false)
  const [revoke, setRevoke] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const list = useQuery({
    queryKey: ['node-enrollments'],
    queryFn: ({ signal }) => unwrap(client.GET('/nodes/enrollments', { signal })),
    gcTime: 0,
    refetchInterval: 30000,
    refetchIntervalInBackground: false,
  })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Worker enrollment</h2>
          <p>Short-lived credentials for a compatible K3s worker.</p>
        </div>
        <Button variant="primary" disabled={!list.data?.configured} onClick={() => setCreate(true)}>
          Create enrollment token
        </Button>
      </div>
      {list.isPending ? (
        <Loading />
      ) : list.error ? (
        <ErrorState error={list.error} />
      ) : (
        <>
          {!list.data?.configured && (
            <Note>
              {list.data?.message || 'Worker enrollment is not configured for this cluster.'}
            </Note>
          )}
          {list.data?.server && (
            <section className="panel service-summary-panel">
              <h3>Configured supervisor</h3>
              <p className="mono break-text">{list.data.server}</p>
              <p className="field-help">
                The new worker must reach this address over HTTPS. A development container-network
                hostname is only reachable from that network.
              </p>
            </section>
          )}
          <div className="panel settings-session-list">
            {list.data?.items.length ? (
              list.data.items.map((entry) => (
                <div className="settings-list-row" key={entry.id}>
                  <div>
                    <strong>Enrollment {entry.id}</strong>
                    <small>
                      Created {timestamp(entry.created_at)} · Expires {timestamp(entry.expires_at)}
                    </small>
                    <Status value={entry.expired ? 'expired' : 'active'} small />
                  </div>
                  <Button
                    size="sm"
                    onClick={() => {
                      setError('')
                      setRevoke(entry.id)
                    }}
                  >
                    Revoke
                  </Button>
                </div>
              ))
            ) : (
              <p className="field-help">No enrollment credentials have been issued.</p>
            )}
          </div>
        </>
      )}
      {create && (
        <EnrollmentForm onClose={() => setCreate(false)} onCreated={() => void list.refetch()} />
      )}
      <Dialog
        open={Boolean(revoke)}
        onOpenChange={(open) => {
          if (!busy && !open) setRevoke('')
        }}
        title="Revoke enrollment credential?"
        description="New worker joins with this credential will be denied. Existing joined workers remain registered."
      >
        <div className="dialog-body">
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRevoke('')}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.DELETE('/nodes/enrollments/{id}', { params: { path: { id: revoke } } }),
                )
                setRevoke('')
                void list.refetch()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Revoke credential
          </Button>
        </div>
      </Dialog>
    </>
  )
}

function EnrollmentForm({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [ttl, setTTL] = useState(30)
  const [created, setCreated] = useState<components['schemas']['Enrollment'] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const shellQuote = (value: string) => "'" + value.replace(/'/g, "'\\''") + "'"
  const command = created?.server
    ? `sudo k3s agent --server ${shellQuote(created.server)} --token-file /etc/rancher/k3s/hakopod-token`
    : ''
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={created ? 'Worker enrollment ready' : 'Create worker enrollment'}
      description={
        created
          ? 'Copy this token now. It will not be returned again.'
          : 'Choose the window during which a new worker may join.'
      }
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy || created) return
          setBusy(true)
          setError('')
          try {
            setCreated(
              await unwrap(client.POST('/nodes/enrollments', { body: { ttl_minutes: ttl } })),
            )
            onCreated()
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body auth-form">
          {created ? (
            <>
              <p>Expires {timestamp(created.expires_at)}</p>
              <div className="secret-once">
                <code>{created.token}</code>
                <Copy value={created.token || ''} />
              </div>
              <Note>
                On the new worker, install the compatible K3s version, then save the token in a
                root-owned file readable only by root at <code>/etc/rancher/k3s/hakopod-token</code>
                .
              </Note>
              <div className="code-panel">
                <div>
                  <span>Start the worker agent</span>
                  <Copy value={command} />
                </div>
                <pre>{command}</pre>
              </div>
            </>
          ) : (
            <>
              <label>
                Expires in minutes
                <input
                  type="number"
                  value={ttl}
                  onChange={(e) => setTTL(Number(e.target.value))}
                  min={5}
                  max={60}
                  required
                />
              </label>
              <Note>
                This credential authorizes worker enrollment into the cluster. Share it only with
                the operator of the intended worker.
              </Note>
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
            {created ? 'I saved the token' : 'Cancel'}
          </Button>
          {!created && (
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? 'Creating…' : 'Create enrollment token'}
            </Button>
          )}
        </div>
      </form>
    </Dialog>
  )
}
