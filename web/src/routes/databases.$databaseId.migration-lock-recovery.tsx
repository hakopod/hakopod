import { useRef, useState, type ReactNode } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import {
  databaseWorkflow,
  type MigrationRecoveryPlan,
  type WorkflowOperation,
} from '../lib/database-workflows'
import { useDatabase } from '../lib/databases'
import { message, timestamp } from '../lib/api'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { Empty, ErrorState, Loading, Note } from '../components/shared'
import { canAccess, useResourceScope, useScope } from '../lib/scope'

export const Route = createFileRoute('/databases/$databaseId/migration-lock-recovery')({
  component: Page,
})
function Page() {
  const id = Route.useParams().databaseId
  const database = useDatabase(id)
  useResourceScope(database.data)
  const { identity } = useScope()
  const apps = useQuery({
    queryKey: [
      'applications',
      database.data?.project,
      database.data?.environment,
      'migration-lock',
    ],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications', {
          signal,
          params: {
            query: {
              project: database.data!.project,
              environment: database.data!.environment,
              limit: 25,
            },
          },
        }),
      ),
    enabled: Boolean(database.data),
    gcTime: 0,
  })
  const [applicationId, setApplicationId] = useState('')
  const app = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    enabled: Boolean(applicationId),
    gcTime: 0,
  })
  const [service, setService] = useState('')
  const [variable, setVariable] = useState('DB_CONNECTION_URI')
  const [plan, setPlan] = useState<MigrationRecoveryPlan | null>(null)
  const [confirmApplication, setConfirmApplication] = useState('')
  const [confirmDatabase, setConfirmDatabase] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const key = useRef('')
  const navigate = useNavigate()
  const d = database.data
  const frame = (children: ReactNode) => (
    <FormPage
      title="Recover migration lock"
      description="Inspect the pinned Infisical and Knex runtime, then release only an abandoned migration lock with fresh evidence."
      breadcrumbs={[]}
    >
      {children}
    </FormPage>
  )
  if (!d || !apps.data)
    return frame(
      database.error || apps.error ? (
        <ErrorState error={database.error || apps.error} />
      ) : (
        <Loading />
      ),
    )
  const back = (
    <Button asChild>
      <Link
        to="/databases/$databaseId"
        params={{ databaseId: id }}
        search={{ project: d.project, environment: d.environment }}
      >
        Back to database
      </Link>
    </Button>
  )
  if (d.spec.engine !== 'postgresql')
    return frame(
      <Empty
        title="PostgreSQL required"
        description="The supported Infisical lock profile uses PostgreSQL and Knex."
        action={back}
      />,
    )
  if (identity.application || !canAccess(identity, d.project, 'deployments:write'))
    return frame(<Note>Migration recovery requires project deployment permission.</Note>)
  const selected = app.data?.id === applicationId ? app.data : undefined
  const services = selected ? Object.keys(selected.spec.services) : []
  const selectedService = selected?.spec.services[service]
  const binding = selectedService?.bindings?.[variable]
  const supported = Boolean(
    selectedService?.image ===
      'docker.io/infisical/infisical:v0.165.10@sha256:204bd63c7a281d9157752ce0bf8d506e7380cac5a0665324eeab8d580b069266' &&
    binding?.managed_database === id &&
    binding?.database,
  )
  const stale = Boolean(
    plan &&
    (plan.evidence.database_revision !== d.revision ||
      plan.evidence.application_revision !== selected?.revision ||
      Date.parse(plan.expires_at) <= Date.now()),
  )
  return frame(
    <form
      className="grid gap-4"
      onSubmit={async (e) => {
        e.preventDefault()
        if (busy || stale || !selected || !service || !supported) return
        setBusy(true)
        setError('')
        try {
          if (!plan) {
            setPlan(
              await databaseWorkflow<MigrationRecoveryPlan>(
                `/databases/${id}/migration-lock-recovery-plan`,
                {
                  application_id: selected.id,
                  service,
                  variable,
                  profile: 'infisical-knex-postgresql-v1',
                },
              ),
            )
            setConfirmApplication('')
            setConfirmDatabase('')
          } else {
            if (
              confirmApplication !== plan.application_name ||
              confirmDatabase !== plan.database_name
            )
              return
            if (!key.current) key.current = crypto.randomUUID()
            const op = await databaseWorkflow<WorkflowOperation>(
              `/databases/${id}/migration-lock-recover`,
              {
                review_id: plan.id,
                confirm_application: confirmApplication,
                confirm_database: confirmDatabase,
              },
              key.current,
            )
            if (!op.id) throw new Error('The accepted operation is missing its identifier.')
            void navigate({
              to: '/databases/$databaseId',
              params: { databaseId: id },
              search: { project: d.project, environment: d.environment, tab: 'activity' },
            })
          }
        } catch (err) {
          setError(message(err))
        } finally {
          setBusy(false)
        }
      }}
    >
      <FormSection title="Pinned migration runtime">
        <div className="grid gap-4 sm:grid-cols-2">
          <SelectField
            label="Application"
            required
            value={applicationId}
            disabled={busy || Boolean(plan)}
            onValueChange={(v) => {
              setApplicationId(v)
              setService('')
              setPlan(null)
            }}
            options={[
              { value: '', label: 'Choose an application' },
              ...apps.data.items.map((a) => ({ value: a.id, label: a.name })),
            ]}
          />
          <SelectField
            label="Service"
            required
            value={service}
            disabled={busy || Boolean(plan) || !selected}
            onValueChange={setService}
            options={[
              { value: '', label: 'Choose a service' },
              ...services.map((value) => ({ value, label: value })),
            ]}
          />
          <label className="grid gap-2 sm:col-span-2">
            Database binding variable
            <Input
              required
              value={variable}
              disabled={busy || Boolean(plan)}
              onChange={(e) => setVariable(e.target.value)}
            />
          </label>
        </div>
        {selected && service && !supported && (
          <Note>
            This action supports only the pinned Infisical v0.165.10 image with an explicit binding
            to this logical PostgreSQL database.
          </Note>
        )}
      </FormSection>
      {plan && (
        <FormSection title="Reviewed abandonment evidence">
          <dl className="grid gap-3 text-sm sm:grid-cols-2">
            <div>
              <dt className="text-muted-foreground">Profile</dt>
              <dd>{plan.evidence.profile}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Pinned application image</dt>
              <dd className="break-all">{plan.evidence.application_image}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Logical database</dt>
              <dd>{plan.evidence.logical_database}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Observed</dt>
              <dd>{timestamp(plan.evidence.observed_at)}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Knex lock rows</dt>
              <dd>
                {plan.evidence.knex_locked_rows} locked of {plan.evidence.knex_lock_rows}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Startup lock rows</dt>
              <dd>
                {plan.evidence.startup_locked_rows} locked of {plan.evidence.startup_lock_rows} ·{' '}
                {plan.evidence.fresh_startup_heartbeats} fresh heartbeats
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Active sessions, pods, jobs, locks</dt>
              <dd>
                {plan.evidence.active_migrator_sessions}, {plan.evidence.active_application_pods},{' '}
                {plan.evidence.active_migration_jobs}, {plan.evidence.active_migration_locks}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Schema fingerprint</dt>
              <dd className="break-all">{plan.evidence.schema_fingerprint}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Evidence digest</dt>
              <dd className="break-all">{plan.evidence_sha256}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Review expires</dt>
              <dd>{timestamp(plan.expires_at)}</dd>
            </div>
          </dl>
          {plan.warnings.map((w) => (
            <Note key={w}>{w}</Note>
          ))}
        </FormSection>
      )}
      {plan && (
        <FormSection title="Confirm repair">
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="grid gap-2">
              Type {plan.application_name}
              <Input
                required
                autoComplete="off"
                value={confirmApplication}
                disabled={busy}
                onChange={(e) => setConfirmApplication(e.target.value)}
              />
            </label>
            <label className="grid gap-2">
              Type {plan.database_name}
              <Input
                required
                autoComplete="off"
                value={confirmDatabase}
                disabled={busy}
                onChange={(e) => setConfirmDatabase(e.target.value)}
              />
            </label>
          </div>
          {stale && (
            <Note>
              The evidence or reviewed revisions are stale. Inspect the current runtime again.
            </Note>
          )}
        </FormSection>
      )}
      {error && <FormError>{error}</FormError>}
      <div className="form-actions flex flex-wrap gap-2">
        {back}
        {plan && (
          <Button
            type="button"
            disabled={busy}
            onClick={() => {
              setPlan(null)
              setConfirmApplication('')
              setConfirmDatabase('')
              key.current = ''
              setError('')
            }}
          >
            Inspect again
          </Button>
        )}
        <Button
          type="submit"
          variant="primary"
          disabled={
            busy ||
            stale ||
            !supported ||
            Boolean(
              plan &&
              (confirmApplication !== plan.application_name ||
                confirmDatabase !== plan.database_name),
            )
          }
        >
          {busy ? 'Checking…' : plan ? 'Release abandoned lock' : 'Inspect migration lock'}
        </Button>
      </div>
    </form>,
  )
}
