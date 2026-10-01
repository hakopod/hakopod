import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import type { ManagedDatabase } from '../lib/databases'
import { client, unwrap } from '../lib/client'
import { byteSize, useBackupSchedules } from '../lib/backups'
import { timestamp } from '../lib/api'
import { Button } from './ui/button'
import { ErrorState, Loading, Status } from './shared'

export function DatabaseBackups({ database: d }: { database: ManagedDatabase }) {
  const artifacts = useQuery({
    queryKey: ['backup-artifacts'],
    queryFn: ({ signal }) => unwrap(client.GET('/backup-artifacts', { signal })),
    refetchInterval: 30000,
    gcTime: 0,
  })
  const schedules = useBackupSchedules()
  const saved =
    artifacts.data?.items
      .filter((a) => a.source.managed_database_id === d.id && !a.deleted_at)
      .slice(0, 3) || []
  const active =
    schedules.data?.items.filter((s) => s.source.managed_database_id === d.id && s.enabled) || []
  return (
    <section className="db-panel" aria-label="Database backups">
      <div className="db-panel-heading">
        <h2>Backups & recovery</h2>
        <Button asChild size="sm" variant="ghost">
          <Link to="/backups" search={{ tab: 'artifacts' }}>
            Stored backups
          </Link>
        </Button>
      </div>
      <div className="flex flex-wrap gap-2 px-4 py-3">
        <Button asChild size="sm">
          <Link to="/backups/new">Run backup</Link>
        </Button>
        <Button asChild size="sm">
          <Link to="/backups/schedules/new">Schedule backup</Link>
        </Button>
      </div>
      {d.spec.engine === 'vitess' && <p className="db-inline-notice">Vitess requires a dedicated operator-approved native backup destination for member recovery. Exported archives capture shards separately and do not promise a single transactionally consistent point across shards. Recovery needs a separate compatible target and data inspection before application access. Native recovery acceptance remains incomplete.</p>}
      {d.spec.engine === 'mysql' && <p className="db-inline-notice">MySQL backups hold a read lock during capture. Writes wait until it finishes. Schedule captures during a suitable maintenance window.</p>}
      {d.spec.engine === 'mongodb' && <p className="db-inline-notice">MongoDB captures the application database at one read timestamp. Schema changes invalidate a capture. Recovery uses a separate empty target and keeps application access closed until recovery succeeds and you inspect the data.</p>}
      {d.spec.engine === 'clickhouse' && <p className="db-inline-notice">Recovery captures each shard separately, not one transactionally consistent point across the cluster. Restore requires a separate empty database with matching topology. Schema changes invalidate a capture. Inspect recovered data before opening application access.</p>}
      {d.spec.engine === 'oracle' && <p className="db-inline-notice">Oracle Free captures the APP schema with Data Pump at a flashback SCN. Schema changes are rejected during capture and must be retried; data changes can continue. This is not an RMAN or point-in-time backup. Recovery requires a separate empty Free database and inspection before application access.</p>}
      {schedules.error ? (
        <ErrorState error={schedules.error} />
      ) : (
        <p className="db-inline-notice">
          {schedules.isPending
            ? 'Checking backup schedules…'
            : active.length
              ? `${active.length} active ${active.length === 1 ? 'schedule' : 'schedules'}`
              : 'No active backup schedule for this database.'}
        </p>
      )}
      {artifacts.isPending ? (
        <Loading rows={2} />
      ) : artifacts.error ? (
        <ErrorState error={artifacts.error} />
      ) : saved.length ? (
        <ul>
          {saved.map((a) => (
            <li className="db-operation" key={a.id}>
              <div className="db-operation-header">
                <Link to="/backups/$jobId" params={{ jobId: a.job_id }}>
                  <strong>{timestamp(a.created_at)}</strong>
                </Link>
                <Status value={a.deletion_pending ? 'pending' : 'stored'} />
              </div>
              <div className="db-operation-meta">
                <span>{byteSize(a.bytes)}</span>
                <span>{a.format}</span>
                <span>{a.sha256 ? 'Digest recorded' : 'No digest recorded'}</span>
              </div>
            </li>
          ))}
        </ul>
      ) : (
        <p className="db-inline-notice">
          No saved backups for this database in the recent artifact page.
        </p>
      )}
      {artifacts.data && !artifacts.error && (
        <p className="db-monitor-footnote">
          Latest matching artifacts from the most recent {artifacts.data?.items.length ?? 0}{' '}
          accessible records.
          {artifacts.data?.next_cursor ? ' Older artifacts are available in Stored backups.' : ''}
        </p>
      )}
    </section>
  )
}
