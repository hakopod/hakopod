import { useDatabase } from '../lib/databases'
import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { timestamp, message, relative } from '../lib/api'
import {
  sourceLabel,
  byteSize,
  engineLabel,
  engineManagedSource,
  sourceKey,
  useBackupSchedules,
} from '../lib/backups'
import { FormPage } from '../components/form-page'
import { duration, useBackupNames } from '../components/backup-names'
import { StaleNotice } from '../components/resource-card'
import { ReviewFacts } from '../components/task-review'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Copy, ErrorState, Loading, Note, Status } from '../components/shared'
export const Route = createFileRoute('/backups/$jobId')({ component: BackupJob })
function BackupJob() {
  const { jobId } = Route.useParams()
  const [cancel, setCancel] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const job = useQuery({
    queryKey: ['backup-job', jobId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/backups/{id}', { signal, params: { path: { id: jobId } } })),
    gcTime: 0,
    refetchInterval: (query) =>
      ['queued', 'running'].includes(query.state.data?.status || '') ? 2500 : false,
  })
  const names = useBackupNames()
  const schedules = useBackupSchedules()
  if (job.isPending) return <Loading />
  if (!job.data) return <ErrorState error={job.error} retry={() => void job.refetch()} />
  const item = job.data
  const active = ['queued', 'running'].includes(item.status)
  const restore = item.kind === 'restore'
  const subject = restore
    ? item.target?.managed_database_name ||
      item.target?.application_name ||
      (item.target ? sourceLabel(item.target) : 'a new database')
    : names.source(item.source)
  const schedule = item.schedule_id
    ? schedules.data?.items.find((entry) => entry.id === item.schedule_id)
    : undefined
  const status = active && item.cancel_requested ? 'cancelling' : item.status
  return (
    <FormPage
      title={restore ? `Restore into ${subject}` : `Backup of ${subject}`}
      description="One backup or restore job: what it read, where it stored the result and how it ended."
      breadcrumbs={[{ label: 'Backups', to: '/backups' }, { label: item.id.slice(0, 12) }]}
      icon="archive"
    >
      <div className="backup-job">
        <header className="backup-job-outcome" data-status={item.status}>
          <Status value={status} />
          <p role="status">
            {item.status === 'succeeded'
              ? restore
                ? `Restored ${byteSize(item.bytes)} in ${duration(item.started_at, item.finished_at) || 'an unrecorded time'}.`
                : `Stored ${byteSize(item.bytes)} in ${names.destination(item.destination_id)} in ${duration(item.started_at, item.finished_at) || 'an unrecorded time'}.`
              : item.status === 'failed'
                ? `Failed${item.finished_at ? ` ${relative(item.finished_at).toLowerCase()}` : ''}${item.started_at && item.finished_at ? ` after ${duration(item.started_at, item.finished_at)}` : ''}. ${restore ? 'The target database may contain partial data; inspect it before use.' : 'No backup was recorded.'}`
                : item.status === 'cancelled'
                  ? 'Cancelled before it finished.'
                  : item.cancel_requested
                    ? 'Cancellation requested. The job stops at its next safe point.'
                    : item.status === 'queued'
                      ? engineManagedSource(item.source) && item.started_at
                        ? 'Waiting on the database engine, which writes this backup to object storage itself. Hakopod checks until it finishes, for up to two hours.'
                        : 'Queued. It starts when a backup worker is free.'
                      : `Running for ${duration(item.started_at, new Date().toISOString()) || 'a moment'}. This page updates until it finishes.`}
          </p>
          <div className="backup-job-actions">
            {active && (
              <Button
                variant="danger"
                size="sm"
                disabled={item.cancel_requested}
                onClick={() => setCancel(true)}
              >
                {item.cancel_requested ? 'Cancellation requested' : 'Cancel job'}
              </Button>
            )}
            {!active && !restore && item.status !== 'succeeded' && (
              <Button asChild size="sm" variant="primary">
                <Link
                  to="/backups/new"
                  search={{ source: sourceKey(item.source), destination: item.destination_id }}
                >
                  Run this backup again
                </Link>
              </Button>
            )}
            {item.status === 'succeeded' &&
              (restore && item.target?.managed_database_id ? (
                <RecoveredDatabaseLink id={item.target.managed_database_id} />
              ) : (
                item.artifact_id &&
                !restore && (
                  <Button asChild size="sm" variant="primary">
                    <Link
                      to="/backups/artifacts/$artifactId/restore"
                      params={{ artifactId: item.artifact_id }}
                    >
                      Restore this backup
                    </Link>
                  </Button>
                )
              ))}
          </div>
        </header>
        {item.error && (
          <section className="backup-job-error" aria-labelledby="backup-job-error-heading">
            <h2 id="backup-job-error-heading">
              {item.status === 'failed' ? 'Why it failed' : 'Recorded error'}
            </h2>
            <pre>{item.error}</pre>
          </section>
        )}
        {job.error && (
          <StaleNotice updatedAt={job.dataUpdatedAt} retry={() => void job.refetch()} />
        )}
        <ReviewFacts
          label="Job"
          items={[
            [restore ? 'Restored from' : 'Database', names.source(item.source)],
            ['Engine', engineLabel(item.source.engine)],
            [
              restore ? 'Read from' : 'Stored in',
              <span>
                {names.destination(item.destination_id)}
                {names.bucket(item.destination_id) && (
                  <span className="block muted-text mono">{names.bucket(item.destination_id)}</span>
                )}
              </span>,
            ],
            ['Size', item.status === 'succeeded' ? byteSize(item.bytes) : active ? 'Not known until it finishes' : 'Not recorded'],
            ['Queued', timestamp(item.created_at)],
            ['Started', item.started_at ? timestamp(item.started_at) : 'Not started'],
            ['Finished', item.finished_at ? timestamp(item.finished_at) : active ? 'Still running' : 'Not recorded'],
            ['Duration', duration(item.started_at, item.finished_at) || (active ? 'Still running' : 'Not recorded')],
            [
              'Started by',
              item.schedule_id ? (
                schedule ? (
                  <Link
                    to="/backups/schedules/$scheduleId/edit"
                    params={{ scheduleId: schedule.id }}
                  >
                    Schedule {schedule.name}
                  </Link>
                ) : schedules.isPending ? (
                  'Schedule (loading name…)'
                ) : schedules.error ? (
                  'Schedule (name unavailable)'
                ) : (
                  'A removed schedule'
                )
              ) : (
                'Run by hand'
              ),
            ],
            [
              'Job ID',
              <span className="copy-id">
                <code>{item.id.slice(0, 12)}</code>
                <Copy value={item.id} label={`Copy job ID ${item.id}`} iconOnly />
              </span>,
            ],
          ]}
        />
        {restore && item.status === 'succeeded' && (
          <Note>
            Inspect the recovered data before pointing an application at it. The original database
            was not changed.
          </Note>
        )}
      </div>
      <Dialog
        open={cancel}
        onOpenChange={(open) => {
          if (!busy) setCancel(open)
        }}
        title="Cancel this job?"
        description="The server stops the job at its next safe cancellation point."
      >
        <div className="dialog-body">
          {error && <ErrorState error={error} />}
          {restore && (
            <Note>
              Cancellation does not guarantee that a partially restored fresh database has been
              removed. Inspect the final job result.
            </Note>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setCancel(false)}>
            Keep running
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.POST('/backups/{id}/cancel', {
                    params: { path: { id: jobId } },
                    body: {},
                  }),
                )
                setCancel(false)
                void job.refetch()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Request cancellation
          </Button>
        </div>
      </Dialog>
    </FormPage>
  )
}

function RecoveredDatabaseLink({ id }: { id: string }) {
  const database = useDatabase(id)
  if (database.isPending) return null
  if (database.error || !database.data)
    return <span className="muted-text">The recovered database is not available to you.</span>
  return (
    <Button
      asChild
      variant="primary"
      size="sm"
      className="max-w-full whitespace-normal text-center"
    >
      <Link
        to="/databases/$databaseId"
        params={{ databaseId: id }}
        search={{ project: database.data.project, environment: database.data.environment }}
      >
        Inspect recovered database
      </Link>
    </Button>
  )
}
