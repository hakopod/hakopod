import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import {
  byteSize,
  engineLabel,
  engineManaged,
  sourceKey,
  sourceLabel,
  useBackupTargets,
} from '../lib/backups'
import type { components } from '../lib/api.generated'
import { FormPage, FormSection, FormHint } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Copy, ErrorState, HeadingHelp, Loading, Note } from '../components/shared'
import {
  CompatibilityReport,
  type CompatibilityReportValue,
} from '../components/compatibility-report'
export const Route = createFileRoute('/backups/artifacts/$artifactId/restore')({
  component: RestoreBackup,
})
function RestoreBackup() {
  const { artifactId } = Route.useParams()
  return <Restore key={artifactId} artifactId={artifactId} />
}
function Restore({ artifactId }: { artifactId: string }) {
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
  const artifacts = useQuery({
    queryKey: ['backup-artifacts', 'restore-related'],
    queryFn: ({ signal }) => unwrap(client.GET('/backup-artifacts', { signal })),
    gcTime: 0,
  })
  const [selected, setSelected] = useState('')
  const [relatedArtifacts, setRelatedArtifacts] = useState<string[]>([])
  const [plan, setPlan] = useState<components['schemas']['BackupRestorePlan'] | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [requestKey, setRequestKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (artifact.isPending || artifacts.isPending || targets.isPending) return <Loading />
  if (artifact.error || artifacts.error || targets.error || !artifact.data)
    return <ErrorState error={artifact.error || artifacts.error || targets.error} />
  const item = artifact.data
  const relatedOptions = artifacts.data.items
    .filter(
      (value) => value.id !== item.id && value.scope === item.scope && !value.deletion_pending,
    )
    .slice(0, 16)
  const eligible =
    targets.data?.items.filter(
      (target) =>
        (target.kind === 'managed_database' ||
          (target.kind === 'database' &&
            item.source.kind !== 'managed_database' &&
            item.source.kind !== 'docker_import')) &&
        target.engine === item.source.engine &&
        target.available &&
        (!target.managed_database_id ||
          target.managed_database_id !== item.source.managed_database_id),
    ) || []
  const target = eligible.find((value) => sourceKey(value) === selected)
  const compatibilityBlocked = Boolean(
    (plan as unknown as { compatibility?: CompatibilityReportValue } | null)?.compatibility
      ?.blocked,
  )
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
              <dt>Recovery point</dt>
              <dd>{timestamp(item.captured_at || item.created_at)}</dd>
            </div>
            <div>
              <dt>Size / format</dt>
              <dd>
                {byteSize(item.bytes)} / {item.format}
                {engineManaged(item) && (
                  <HeadingHelp title="Size and format">
                    The database engine wrote this backup to object storage itself and reported its
                    size and file count. Hakopod listed the destination prefix and confirmed the
                    objects are there; it did not read them.
                  </HeadingHelp>
                )}
              </dd>
            </div>
            <div>
              <dt>Scope</dt>
              <dd>{item.scope}</dd>
            </div>
            <div>
              <dt>Checksum</dt>
              {engineManaged(item) ? (
                <dd>
                  None. The database engine wrote this backup itself.
                  <HeadingHelp title="Checksum">
                    The bytes never passed through Hakopod, so there is no checksum to compute and
                    no payload authentication to verify before this restore starts. Hakopod
                    confirmed the objects exist in the destination rather than reading them, and the
                    engine performs the restore from the objects it wrote.
                  </HeadingHelp>
                </dd>
              ) : (
                <dd className="break-text">
                  <code>{item.sha256}</code>
                  <Copy value={item.sha256 || ''} />
                </dd>
              )}
            </div>
          </dl>
        </FormSection>
        {plan ? (
          <FormSection
            title="Restore plan"
            description={`Expires ${timestamp(plan.expires_at)}`}
            icon="database"
          >
            <CompatibilityReport
              value={
                (plan as unknown as { compatibility?: CompatibilityReportValue }).compatibility
              }
            />
            <dl className="service-definition-list">
              <div>
                <dt>Target database</dt>
                <dd>{plan.target.managed_database_name || sourceLabel(plan.target)}</dd>
              </div>
              <div>
                <dt>Target revision</dt>
                <dd>r{plan.target.revision}</dd>
              </div>
              <div>
                <dt>New database</dt>
                <dd>
                  <code>{plan.confirmation}</code>
                  <Copy value={plan.confirmation} />
                </dd>
              </div>
              <div>
                <dt>Review expires</dt>
                <dd>{timestamp(plan.expires_at)}</dd>
              </div>
              <div>
                <dt>Restore scope</dt>
                <dd>{plan.scope}</dd>
              </div>
            </dl>
            {Date.parse(plan.expires_at) <= Date.now() && (
              <Note>This review expired. Choose the target again to refresh it.</Note>
            )}
            {plan.warnings.map((warning) => (
              <Note key={warning}>{warning}</Note>
            ))}
            <label>
              Type the new database name to confirm
              <Input
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
          <>
            <FormSection
              title="Target database"
              description={`Only available ${engineLabel(item.source.engine)} targets are shown.`}
              icon="database"
            >
              <label>
                Restore to
                <SelectField
                  label="Restore to"
                  value={selected}
                  disabled={item.deletion_pending}
                  onValueChange={(value) => setSelected(value)}
                  options={[
                    {
                      value: '',
                      label: 'Choose a compatible database',
                    },
                    ...(eligible.map((value) => ({
                      value: sourceKey(value),
                      label: value.managed_database_name || sourceLabel(value),
                    })) ?? []),
                  ]}
                />
              </label>
              {!eligible.length && (
                <Note>
                  Create a separate, unused compatible database before restoring this artifact.
                </Note>
              )}
            </FormSection>
            <FormSection title="Related recovery set">
              <p className="text-sm text-muted-foreground">
                Optionally select same-scope archives that must have a consistent recovery point.
              </p>
              {relatedOptions.map((value) => (
                <label className="checkbox-row" key={value.id}>
                  <Input
                    type="checkbox"
                    checked={relatedArtifacts.includes(value.id)}
                    disabled={item.deletion_pending}
                    onChange={(event) =>
                      setRelatedArtifacts((current) =>
                        event.target.checked
                          ? current.length < 16
                            ? [...current, value.id]
                            : current
                          : current.filter((id) => id !== value.id),
                      )
                    }
                  />
                  <span className="min-w-0 break-words">
                    {timestamp(value.captured_at || value.created_at)} · {sourceLabel(value.source)}
                  </span>
                </label>
              ))}
              {!relatedOptions.length && <Note>No other same-scope archives are available.</Note>}
              {!relatedArtifacts.length && (
                <Note>
                  No related archives selected. Cross-application recovery-point compatibility will
                  remain unknown.
                </Note>
              )}
            </FormSection>
          </>
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
          <Button asChild>
            <Link to="/backups" search={{ tab: 'artifacts' }}>
              Cancel
            </Link>
          </Button>
        )}
        <Button
          variant="primary"
          disabled={
            busy ||
            item.deletion_pending ||
            Boolean(plan && Date.parse(plan.expires_at) <= Date.now()) ||
            compatibilityBlocked ||
            (plan ? confirmation !== plan.confirmation : !target)
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
              } else if (target?.managed_database_id) {
                const value = await unwrap(
                  client.POST('/databases/{id}/restore-plan', {
                    params: { path: { id: target.managed_database_id } },
                    body: {
                      artifact_id: artifactId,
                      related_artifact_ids: relatedArtifacts,
                    } as any,
                  }),
                )
                setPlan(value)
                setRequestKey(crypto.randomUUID())
                setConfirmation('')
              } else if (target?.application_id && target.service) {
                const value = await unwrap(
                  client.POST('/backup-artifacts/{id}/restore-plan', {
                    params: { path: { id: artifactId } },
                    body: {
                      application_id: target.application_id,
                      service: target.service,
                      related_artifact_ids: relatedArtifacts,
                    } as any,
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
