import { useScope, canAccess } from '../lib/scope'
import { useRef, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useDatabase } from '../lib/databases'
import { engineName } from '../lib/database-view'
import { client, unwrap } from '../lib/client'
import type { components } from '../lib/api.generated'
import { message, timestamp } from '../lib/api'
import { Empty, ErrorState, Loading, Note } from '../components/shared'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import {
  CompatibilityReport,
  type CompatibilityReportValue,
} from '../components/compatibility-report'

export const Route = createFileRoute('/databases/$databaseId/recover')({ component: Page })
function Page() {
  const id = Route.useParams().databaseId
  return <Recover key={id} id={id} />
}
function Recover({ id }: { id: string }) {
  const database = useDatabase(id)
  const artifacts = useQuery({
    queryKey: ['database-recovery-artifacts'],
    queryFn: ({ signal }) => unwrap(client.GET('/backup-artifacts', { signal })),
    gcTime: 0,
  })
  const [artifact, setArtifact] = useState('')
  const [relatedArtifacts, setRelatedArtifacts] = useState<string[]>([])
  const [plan, setPlan] = useState<components['schemas']['BackupRestorePlan'] | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const key = useRef('')
  const navigate = useNavigate()
  const { identity } = useScope()
  const frame = (content: React.ReactNode) => (
    <FormPage
      title="Recover into database"
      description="Restore into a separate database and inspect it before replacing an application connection."
      breadcrumbs={[]}
    >
      {content}
    </FormPage>
  )
  if (database.isPending || artifacts.isPending) return frame(<Loading />)
  if (database.error || artifacts.error)
    return frame(<ErrorState error={database.error || artifacts.error} />)
  const d = database.data
  if (identity.application || !canAccess(identity, d.project, 'deployments:write'))
    return frame(<Note>Recovery requires project deployment permission.</Note>)
  const eligible = artifacts.data.items.filter(
    (a) =>
      a.source.engine === d.spec.engine &&
      (d.spec.engine !== 'vitess' ||
        (a.source_version === d.spec.version &&
          a.format === 'age-v1+vitess-logical-v1' &&
          Boolean(a.captured_at) &&
          Boolean(a.verified_at))) &&
      a.source.managed_database_id !== id &&
      !a.deletion_pending,
  )
  const selected = artifacts.data.items.find((a) => a.id === artifact)
  const relatedOptions = artifacts.data.items
    .filter(
      (value) =>
        value.id !== artifact && value.scope === selected?.scope && !value.deletion_pending,
    )
    .slice(0, 16)
  const expired = Boolean(plan && Date.parse(plan.expires_at) <= Date.now())
  const search = { project: d.project, environment: d.environment }
  if (d.status !== 'ready' || d.revision !== 1 || d.recovery)
    return frame(
      <Empty
        title="Use a separate, unused database"
        description="Recovery requires an empty database at revision 1. The source remains available."
        action={
          <Button asChild variant="primary">
            <Link to="/databases/new" search={search}>
              Create recovery target
            </Link>
          </Button>
        }
      />,
    )
  return (
    <FormPage
      title="Recover into database"
      description="Restore into this separate database, inspect it, then explicitly replace the application connection and redeploy."
      breadcrumbs={[]}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          setBusy(true)
          setError('')
          try {
            if (!plan) {
              setPlan(
                await unwrap(
                  client.POST('/databases/{id}/restore-plan', {
                    params: { path: { id } },
                    body: {
                      artifact_id: artifact,
                      related_artifact_ids: relatedArtifacts,
                    } as any,
                  }),
                ),
              )
              return
            }
            if (!key.current) key.current = crypto.randomUUID()
            const job = await unwrap(
              client.POST('/backup-artifacts/{id}/restore', {
                params: { path: { id: artifact }, header: { 'Idempotency-Key': key.current } },
                body: { plan_id: plan.id, confirmation },
              }),
            )
            void navigate({ to: '/backups/$jobId', params: { jobId: job.id } })
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <FormSection title="Archive and target">
          <p>
            Target: {d.spec.name} · {engineName(d.spec.engine)} {d.spec.version} · {d.project} /{' '}
            {d.environment}
          </p>
          {!eligible.length && (
            <Empty
              title="No eligible archives"
              description={
                d.spec.engine === 'vitess'
                  ? 'Create a verified Vitess 23 logical backup from another database. Restore also requires the same shard count and table-routing schema.'
                  : 'Create a backup from another database with a compatible engine and version before recovering into this target.'
              }
              action={
                <Button asChild>
                  <Link to="/backups/new">Run backup</Link>
                </Button>
              }
            />
          )}
          <label>
            Archive
            <SelectField
              label="Archive"
              value={artifact}
              disabled={busy}
              required
              onValueChange={(value) => {
                setArtifact(value)
                setPlan(null)
                setRelatedArtifacts([])
                setConfirmation('')
                key.current = ''
              }}
              options={[
                { value: '', label: 'Choose a matching archive' },
                ...eligible.map((a) => ({
                  value: a.id,
                  label: `${timestamp(a.captured_at || a.created_at)} · ${a.source.managed_database_id?.slice(0, 8) || a.source.service || a.source.kind} · ${a.verified_at ? 'Verified' : 'Requires verification'}`,
                })),
              ]}
            />
          </label>
          {artifacts.data.next_cursor && (
            <Note>
              Showing the most recent 100 archives. Older archives remain available through the API.
            </Note>
          )}
          <Note>
            The archive is authenticated before recovery.{' '}
            {d.spec.engine === 'postgresql' &&
              'PostgreSQL 17 archives can be staged in a separate PostgreSQL 18 database. '}
            Changes after the recovery point require a fresh capture before final cutover.
          </Note>
          {d.spec.engine === 'vitess' && (
            <Note>
              Vitess restore authenticates the complete archive and validates its version, shard map
              and table-routing schema before importing data. The target keeps its separately
              approved native backup destination.
            </Note>
          )}
          {artifact && (
            <div className="grid gap-2">
              <p className="text-sm font-medium">Related recovery set</p>
              <p className="text-sm text-muted-foreground">
                Optionally select same-scope archives that must have a consistent recovery point.
              </p>
              {relatedOptions.map((value) => (
                <label className="checkbox-row" key={value.id}>
                  <Input
                    type="checkbox"
                    checked={relatedArtifacts.includes(value.id)}
                    disabled={busy}
                    onChange={(event) =>
                      setRelatedArtifacts((current) =>
                        event.target.checked
                          ? current.length < 16
                            ? [...current, value.id]
                            : current
                          : current.filter((valueID) => valueID !== value.id),
                      )
                    }
                  />
                  <span className="min-w-0 break-words">
                    {timestamp(value.captured_at || value.created_at)} ·{' '}
                    {value.source.managed_database_id?.slice(0, 8) ||
                      value.source.service ||
                      value.source.kind}
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
            </div>
          )}
        </FormSection>
        {plan && (
          <FormSection title="Review recovery">
            <CompatibilityReport
              value={
                (plan as unknown as { compatibility?: CompatibilityReportValue }).compatibility
              }
            />
            <p>Archive: {selected?.id}</p>
            <p>
              Captured: {timestamp(selected?.captured_at || selected?.created_at)} · Verified:{' '}
              {timestamp(selected?.verified_at)}
            </p>
            <p className="break-all font-mono">SHA-256: {selected?.sha256 || 'Not recorded'}</p>
            <p>
              Target: {d.spec.name} · {engineName(d.spec.engine)} {d.spec.version}
            </p>
            <p>
              Review expires {timestamp(plan.expires_at)}.
              {expired ? ' Refresh this review before proceeding.' : ''}
            </p>
            <Button
              type="button"
              onClick={() => {
                setPlan(null)
                setConfirmation('')
                key.current = ''
              }}
            >
              Edit or refresh review
            </Button>
            {plan.warnings.map((w) => (
              <Note key={w}>{w}</Note>
            ))}
            <label>
              Type {plan.confirmation} to confirm
              <Input
                required
                value={confirmation}
                onChange={(e) => setConfirmation(e.target.value)}
              />
            </label>
          </FormSection>
        )}
        {error && <FormError>{error}</FormError>}
        <div className="form-footer">
          <Button asChild>
            <Link to="/databases/$databaseId" params={{ databaseId: id }} search={search}>
              Cancel
            </Link>
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={
              busy || expired || !artifact || Boolean(plan && confirmation !== plan.confirmation)
            }
          >
            {busy ? 'Working…' : plan ? 'Start recovery' : 'Review recovery'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
