import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { timestamp, message } from '../lib/api'
import { sourceLabel, byteSize, engineManagedEngine } from '../lib/backups'
import { FormPage, FormSection } from '../components/form-page'
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
  if (job.isPending) return <Loading />
  if (job.error || !job.data) return <ErrorState error={job.error} />
  const item = job.data
  const active = ['queued', 'running'].includes(item.status)
  return (
    <FormPage
      title={item.kind === 'restore' ? 'Database restore' : 'Database backup'}
      description={sourceLabel(item.source)}
      breadcrumbs={[{ label: 'Backups', to: '/backups' }, { label: item.id.slice(0, 12) }]}
      icon="archive"
    >
      <FormSection title="Job status" icon="activity">
        <div className="section-toolbar">
          <Status value={item.status} />
          {active && (
            <Button
              variant="danger"
              disabled={item.cancel_requested}
              onClick={() => setCancel(true)}
            >
              {item.cancel_requested ? 'Cancellation requested' : 'Cancel job'}
            </Button>
          )}
        </div>
        {item.status === 'queued' &&
          engineManagedEngine(item.source?.engine) &&
          !item.cancel_requested && (
            <p className="field-note">
              Waiting on the database engine, which writes this backup to object storage itself.
              Hakopod polls until it finishes and stops waiting after two hours. A queued job here
              is not an idle one.
            </p>
          )}
        <dl className="service-definition-list">
          <div>
            <dt>Job ID</dt>
            <dd>
              <code>{item.id}</code>
              <Copy value={item.id} />
            </dd>
          </div>
          <div>
            <dt>Created</dt>
            <dd>{timestamp(item.created_at)}</dd>
          </div>
          <div>
            <dt>Started / finished</dt>
            <dd>
              {timestamp(item.started_at)} / {timestamp(item.finished_at)}
            </dd>
          </div>
          <div>
            <dt>Transferred</dt>
            <dd>{byteSize(item.bytes)}</dd>
          </div>
          {item.target && (
            <div>
              <dt>Restore target</dt>
              <dd>{sourceLabel(item.target)}</dd>
            </div>
          )}
        </dl>
        {item.error && <ErrorState error={item.error} />}
        <Note>
          {item.status === 'succeeded'
            ? 'The job completed successfully. Stored backups can be reviewed in the artifact list.'
            : active
              ? 'This page follows the active job. Completion is reported only when the backend finishes the operation.'
              : 'Inspect the recorded error before retrying.'}
        </Note>
        {item.artifact_id && (
          <Link
            className="button"
            to="/backups/artifacts/$artifactId/restore"
            params={{ artifactId: item.artifact_id }}
          >
            Inspect stored backup
          </Link>
        )}
      </FormSection>
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
          <Note>
            Cancellation does not guarantee that a partially restored fresh database has been
            removed. Inspect the final job result.
          </Note>
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
