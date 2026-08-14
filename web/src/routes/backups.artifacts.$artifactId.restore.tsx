import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { byteSize, sourceKey, sourceLabel, useBackupTargets } from '../lib/backups'
import type { components } from '../lib/api.generated'
import { FormPage, FormSection, FormHint } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Copy, ErrorState, Loading, Note } from '../components/shared'
export const Route = createFileRoute('/backups/artifacts/$artifactId/restore')({
  component: RestoreBackup,
})
function RestoreBackup() {
  const { artifactId } = Route.useParams()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const targets = useBackupTargets()
  const artifact = useQuery({
    queryKey: ['backup-artifact', artifactId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/backup-artifacts/{id}', { signal, params: { path: { id: artifactId } } }),
      ),
    gcTime: 0,
  })
  const [selected, setSelected] = useState('')
  const [plan, setPlan] = useState<components['schemas']['BackupRestorePlan'] | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [requestKey, setRequestKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (artifact.isPending || targets.isPending) return <Loading />
  if (artifact.error || targets.error || !artifact.data)
    return <ErrorState error={artifact.error || targets.error} />
  const item = artifact.data
  const eligible =
    targets.data?.items.filter(
      (target) =>
        target.kind === 'database' && target.engine === item.source.engine && target.available,
    ) || []
  const target = eligible.find((value) => sourceKey(value) === selected)
  return (
    <FormPage
      title={plan ? 'Review database restore' : 'Restore a stored backup'}
      description="Restore into a fresh database on a compatible live service."
      breadcrumbs={[
        { label: 'Backups', to: '/backups' },
        { label: artifactId.slice(0, 12) },
        { label: 'Restore' },
      ]}
      icon="database"
      help={
        <>
          <FormHint title="A fresh database">
            Restores do not overwrite an existing database. Update your application connection only
            after checking the restored data.
          </FormHint>
          <FormHint title="Review the exact target">
            The plan pins the target pod and application revision and expires shortly. Changes
            require another review.
          </FormHint>
        </>
      }
    >
      <div className="form-body">
        {item.deletion_pending && (
          <Note>
            Deletion pending. This backup is being removed from storage and cannot be restored.
          </Note>
        )}
        <FormSection title="Stored backup" icon="archive">
          <dl className="service-definition-list">
            <div>
              <dt>Source</dt>
              <dd>{sourceLabel(item.source)}</dd>
            </div>
            <div>
              <dt>Created</dt>
              <dd>{timestamp(item.created_at)}</dd>
            </div>
            <div>
              <dt>Size / format</dt>
              <dd>
                {byteSize(item.bytes)} / {item.format}
              </dd>
            </div>
            <div>
              <dt>Scope</dt>
              <dd>{item.scope}</dd>
            </div>
            <div>
              <dt>Checksum</dt>
              <dd className="break-text">
                <code>{item.sha256}</code>
                <Copy value={item.sha256} />
              </dd>
            </div>
          </dl>
        </FormSection>
        {plan ? (
          <FormSection
            title="Restore plan"
            description={`Expires ${timestamp(plan.expires_at)}`}
            icon="database"
          >
            <dl className="service-definition-list">
              <div>
                <dt>Target service</dt>
                <dd>{sourceLabel(plan.target)}</dd>
              </div>
              <div>
                <dt>Pod / application revision</dt>
                <dd>
                  <code>{plan.target.pod}</code> / r{plan.target.revision}
                </dd>
              </div>
              <div>
                <dt>New database</dt>
                <dd>
                  <code>{plan.confirmation}</code>
                  <Copy value={plan.confirmation} />
                </dd>
              </div>
              <div>
                <dt>Restore scope</dt>
                <dd>{plan.scope}</dd>
              </div>
            </dl>
            {plan.warnings.map((warning) => (
              <Note key={warning}>{warning}</Note>
            ))}
            <label>
              Type the new database name to confirm
              <input
                value={confirmation}
                disabled={item.deletion_pending}
                onChange={(event) => setConfirmation(event.target.value)}
                autoComplete="off"
                spellCheck={false}
                maxLength={128}
                placeholder={plan.confirmation}
              />
            </label>
          </FormSection>
        ) : (
          <FormSection
            title="Target service"
            description={`Only available ${item.source.engine} services are shown.`}
            icon="database"
          >
            <label>
              Restore to
              <select
                value={selected}
                disabled={item.deletion_pending}
                onChange={(event) => setSelected(event.target.value)}
              >
                <option value="">Choose a compatible database service</option>
                {eligible.map((value) => (
                  <option key={sourceKey(value)} value={sourceKey(value)}>
                    {value.application_name || value.application_id} / {value.service}
                  </option>
                ))}
              </select>
            </label>
            {!eligible.length && (
              <Note>Create a compatible database service before restoring this artifact.</Note>
            )}
          </FormSection>
        )}
        {error && <ErrorState error={error} />}
      </div>
      <div className="form-footer">
        {plan ? (
          <Button
            disabled={busy}
            onClick={() => {
              setPlan(null)
              setConfirmation('')
            }}
          >
            Choose another target
          </Button>
        ) : (
          <Link className="button" to="/backups" search={{ tab: 'artifacts' }}>
            Cancel
          </Link>
        )}
        <Button
          variant="primary"
          disabled={
            busy || item.deletion_pending || (plan ? confirmation !== plan.confirmation : !target)
          }
          onClick={async () => {
            if (busy || item.deletion_pending) return
            setBusy(true)
            setError('')
            try {
              if (plan) {
                const job = await unwrap(
                  client.POST('/backup-artifacts/{id}/restore', {
                    params: { path: { id: artifactId }, header: { 'Idempotency-Key': requestKey } },
                    body: { plan_id: plan.id, confirmation },
                  }),
                )
                void cache.invalidateQueries({ queryKey: ['backups'] })
                void navigate({ to: '/backups/$jobId', params: { jobId: job.id } })
              } else if (target?.application_id && target.service) {
                const value = await unwrap(
                  client.POST('/backup-artifacts/{id}/restore-plan', {
                    params: { path: { id: artifactId } },
                    body: { application_id: target.application_id, service: target.service },
                  }),
                )
                setPlan(value)
                setRequestKey(crypto.randomUUID())
                setConfirmation('')
              }
            } catch (err) {
              setError(message(err))
              void artifact.refetch()
            } finally {
              setBusy(false)
            }
          }}
        >
          {busy ? 'Working…' : plan ? 'Restore to new database' : 'Review restore plan'}
        </Button>
      </div>
    </FormPage>
  )
}
