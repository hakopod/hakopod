import { DatabaseInspection } from '../components/database-inspection'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useDatabase, databaseSummary, databaseHealth } from '../lib/databases'
import { useResourceScope, useScope, canAccess } from '../lib/scope'
import { message, timestamp } from '../lib/api'
import { Empty, ErrorState, Loading, Note, PageHeader } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Badge } from '../components/ui/surfaces'
import { Icon } from '../components/icons'

export const Route = createFileRoute('/databases/$databaseId')({ component: DetailRoute })
function DetailRoute() {
  const id = Route.useParams().databaseId
  return useLocation().pathname === `/databases/${id}` ? <Detail key={id} /> : <Outlet />
}
function Detail() {
  const id = Route.useParams().databaseId
  const query = useDatabase(id)
  useResourceScope(query.data)
  const operations = useQuery({
    queryKey: ['database-operations', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/databases/{id}/operations', { signal, params: { path: { id } } })),
    refetchInterval: 5000,
    gcTime: 0,
  })
  const [credentials, setCredentials] = useState<{
    username: string
    password: string
    database: string
  } | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const navigate = useNavigate()
  const { identity } = useScope()
  if (query.isPending) return <Loading />
  if (query.error) return <ErrorState error={query.error} />
  const d = query.data
  const canManage = !identity.application && canAccess(identity, d.project, 'deployments:write')
  const search = { project: d.project, environment: d.environment }
  return (
    <div className="ops-page">
      <PageHeader
        title={d.spec.name}
        description={databaseSummary(d.spec)}
        action={
          <div className="flex flex-wrap gap-2">
            {canManage && d.status === 'ready' && (
              <Button asChild variant="primary">
                <Link
                  to="/databases/$databaseId/connect"
                  params={{ databaseId: id }}
                  search={search}
                >
                  Connect application
                </Link>
              </Button>
            )}
            {canManage && d.status === 'ready' && (
              <Button asChild>
                <Link
                  to="/databases/$databaseId/resize"
                  params={{ databaseId: id }}
                  search={search}
                >
                  Resize
                </Link>
              </Button>
            )}
            {canManage && (
              <Button asChild>
                <Link
                  to="/databases/$databaseId/recover"
                  params={{ databaseId: id }}
                  search={search}
                >
                  Recover into this database
                </Link>
              </Button>
            )}
          </div>
        }
      />
      <div className="flex flex-wrap items-center gap-3 py-3">
        <Badge>{d.status}</Badge>
        <span>{databaseSummary(d.spec)}</span>
        <span>Revision {d.revision}</span>
        <span>
          {d.spec.cpu} CPU · {d.spec.memory} · {d.spec.storage_gib} GiB per member
        </span>
      </div>
      {d.observation.message && <Note>{d.observation.message}</Note>}
      <p className="py-2">
        Health: {databaseHealth(d)} · Last observed {timestamp(d.observation.observed_at)}
      </p>
      {d.recovery && (
        <Note>
          Recovery job{' '}
          <Link to="/backups/$jobId" params={{ jobId: d.recovery.job_id }}>
            {d.recovery.job_id.slice(0, 8)}
          </Link>
          . Captured {timestamp(d.recovery.captured_at)}.{' '}
          {d.recovery.restored_at
            ? 'Recovery completed. Inspect the data before changing an application connection.'
            : 'Recovery has not been confirmed complete.'}{' '}
          Writes after the recovery point require a fresh capture before final cutover.
        </Note>
      )}
      {canManage && <DatabaseInspection database={d} />}
      <section className="py-4" aria-label="Private endpoints">
        <h2>Private endpoints</h2>
        {!d.observation.endpoints?.length ? (
          <p>No verified endpoints yet.</p>
        ) : (
          <dl className="grid gap-2 py-2">
            {d.observation.endpoints.map((e) => (
              <div key={e.purpose} className="flex flex-wrap gap-x-3">
                <dt>{e.purpose.replaceAll('_', ' ')}</dt>
                <dd className="break-all font-mono">
                  {e.host}:{e.port}
                </dd>
              </div>
            ))}
          </dl>
        )}
        {d.spec.engine === 'redis' && d.spec.mode === 'cluster' && (
          <Note>
            Use a cluster-aware Redis client. The database controller owns replication and failover.
          </Note>
        )}
        <Button
          disabled={busy || !canManage}
          onClick={async () => {
            if (credentials) {
              setCredentials(null)
              return
            }
            setBusy(true)
            setError('')
            try {
              setCredentials(
                await unwrap(
                  client.POST('/databases/{id}/credentials', {
                    params: { path: { id } },
                    body: {},
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
          {credentials ? 'Hide credentials' : 'Reveal credentials'}
        </Button>
        {credentials && (
          <dl className="grid gap-2 py-3">
            <div>
              <dt>Username</dt>
              <dd>{credentials.username}</dd>
            </div>
            <div>
              <dt>Database</dt>
              <dd>{credentials.database}</dd>
            </div>
            <div>
              <dt>Password</dt>
              <dd className="break-all font-mono select-all">{credentials.password}</dd>
            </div>
          </dl>
        )}
      </section>
      <section className="py-4" aria-label="Members">
        <h2>Members</h2>
        {!d.observation.members?.length ? (
          <p>No observed members yet.</p>
        ) : (
          <ul className="grid gap-2 py-2">
            {d.observation.members.map((m) => (
              <li key={m.uid} className="flex flex-wrap gap-3">
                <span className="break-all font-mono">{m.name}</span>
                <span>{m.role}</span>
                <span>{m.ready ? 'Ready' : 'Not ready'}</span>
                {m.node && <span>{m.node}</span>}
              </li>
            ))}
          </ul>
        )}
      </section>
      <section className="py-4" aria-label="Backups">
        <h2>Backups</h2>
        <div className="flex flex-wrap gap-2 py-2">
          <Button asChild>
            <Link to="/backups/new">Run backup</Link>
          </Button>
          <Button asChild>
            <Link to="/backups/schedules/new">Schedule backup</Link>
          </Button>
          <Button asChild>
            <Link to="/backups" search={{ tab: 'artifacts' }}>
              Stored backups
            </Link>
          </Button>
        </div>
      </section>
      <section className="py-4" aria-label="Operations">
        <h2>Operations</h2>
        {operations.isPending ? (
          <Loading rows={2} />
        ) : operations.error ? (
          <ErrorState error={operations.error} />
        ) : !operations.data.items.length ? (
          <Empty title="No operations" description="No database operations have been recorded." />
        ) : (
          <ul className="grid gap-2 py-2">
            {operations.data.items.map((op) => (
              <li key={op.id} className="flex flex-wrap gap-3">
                <strong>{op.kind}</strong>
                <span>r{op.revision}</span>
                <Badge>{op.status}</Badge>
                <span>{op.phase}</span>
                <span>{timestamp(op.created_at)}</span>
                {op.message && <span>{op.message}</span>}
              </li>
            ))}
          </ul>
        )}
      </section>
      {error && (
        <p role="alert" className="text-destructive py-3">
          {error}
        </p>
      )}
      <div className="py-4">
        <Button disabled={!canManage} variant="danger" onClick={() => setDeleting((v) => !v)}>
          <Icon name="trash" size={14} />
          Delete database
        </Button>
      </div>
      {deleting && (
        <form
          className="grid gap-3 pb-4"
          onSubmit={async (e) => {
            e.preventDefault()
            setBusy(true)
            setError('')
            try {
              await unwrap(
                client.DELETE('/databases/{id}', {
                  params: { path: { id }, header: { 'Idempotency-Key': crypto.randomUUID() } },
                  body: { expected_revision: d.revision, confirm_name: confirmation },
                }),
              )
              void navigate({ to: '/databases', search })
            } catch (err) {
              setError(message(err))
            } finally {
              setBusy(false)
            }
          }}
        >
          <Note>Deletion removes every database member and its data. Saved backups remain.</Note>
          <label>
            Type {d.spec.name} to confirm
            <Input value={confirmation} onChange={(e) => setConfirmation(e.target.value)} />
          </label>
          <div>
            <Button type="submit" variant="danger" disabled={busy || confirmation !== d.spec.name}>
              <Icon name="trash" size={14} />
              Delete database and data
            </Button>
          </div>
        </form>
      )}
    </div>
  )
}
