import { useRef, useState, type ReactNode } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import {
  databaseWorkflow,
  type ApplicationProvisioningPlan,
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

export const Route = createFileRoute('/databases/$databaseId/provision-application')({
  component: Page,
})
function Page() {
  const { id } = { id: Route.useParams().databaseId }
  const database = useDatabase(id)
  useResourceScope(database.data)
  const { identity } = useScope()
  const apps = useQuery({
    queryKey: ['applications', database.data?.project, database.data?.environment, 'provision'],
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
  const [variable, setVariable] = useState('DATABASE_URL')
  const [endpoint, setEndpoint] = useState('read_write')
  const [role, setRole] = useState('')
  const [logicalDatabase, setLogicalDatabase] = useState('')
  const [secret, setSecret] = useState('database-password')
  const [plan, setPlan] = useState<ApplicationProvisioningPlan | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const key = useRef('')
  const navigate = useNavigate()
  const d = database.data
  const frame = (children: ReactNode) => (
    <FormPage
      title="Create database for application"
      description="Create a scoped PostgreSQL login and logical database, verify its privileges, then deploy the reviewed binding."
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
        description="Application database provisioning currently supports managed PostgreSQL only."
        action={back}
      />,
    )
  if (identity.application || !canAccess(identity, d.project, 'deployments:write'))
    return frame(
      <Note>Creating an application database requires project deployment permission.</Note>,
    )
  const selected = app.data?.id === applicationId ? app.data : undefined
  const services = selected ? Object.keys(selected.spec.services) : []
  const stale = Boolean(
    plan &&
    (plan.database_revision !== d.revision ||
      plan.application_revision !== selected?.revision ||
      Date.parse(plan.expires_at) <= Date.now()),
  )
  return frame(
    <form
      className="grid gap-4"
      onSubmit={async (e) => {
        e.preventDefault()
        if (busy || stale || !selected || !service) return
        setBusy(true)
        setError('')
        try {
          if (!plan) {
            setPlan(
              await databaseWorkflow<ApplicationProvisioningPlan>(
                `/databases/${id}/application-provisioning-plan`,
                {
                  application_id: selected.id,
                  service,
                  variable,
                  endpoint,
                  role,
                  database: logicalDatabase,
                  secret_reference: secret,
                },
              ),
            )
            setConfirmation('')
          } else {
            if (confirmation !== plan.application_name) return
            if (!key.current) key.current = crypto.randomUUID()
            const op = await databaseWorkflow<WorkflowOperation>(
              `/databases/${id}/application-provision`,
              { review_id: plan.id, confirm_application: confirmation },
              key.current,
            )
            void navigate({
              to: '/databases/$databaseId',
              params: { databaseId: id },
              search: { project: d.project, environment: d.environment, tab: 'activity' },
            })
            if (!op.id) throw new Error('The accepted operation is missing its identifier.')
          }
        } catch (err) {
          setError(message(err))
        } finally {
          setBusy(false)
        }
      }}
    >
      <FormSection title="Application and connection">
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
          <label className="grid gap-2">
            Environment variable
            <Input
              required
              value={variable}
              disabled={busy || Boolean(plan)}
              onChange={(e) => setVariable(e.target.value)}
            />
          </label>
          <SelectField
            label="Endpoint"
            value={endpoint}
            disabled={busy || Boolean(plan)}
            onValueChange={setEndpoint}
            options={[
              { value: 'read_write', label: 'Direct read/write' },
              { value: 'pooled_read_write', label: 'Pooled read/write' },
            ]}
          />
        </div>
      </FormSection>
      <FormSection title="Scoped database access">
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="grid gap-2">
            PostgreSQL role
            <Input
              value={role}
              disabled={busy || Boolean(plan)}
              placeholder="Generated from application name"
              onChange={(e) => setRole(e.target.value)}
            />
          </label>
          <label className="grid gap-2">
            Logical database
            <Input
              value={logicalDatabase}
              disabled={busy || Boolean(plan)}
              placeholder="Generated from application name"
              onChange={(e) => setLogicalDatabase(e.target.value)}
            />
          </label>
          <label className="grid gap-2 sm:col-span-2">
            Password secret reference
            <Input
              required
              value={secret}
              disabled={busy || Boolean(plan)}
              onChange={(e) => setSecret(e.target.value)}
            />
          </label>
        </div>
        {plan && (
          <dl className="grid gap-3 text-sm sm:grid-cols-2">
            <div>
              <dt className="text-muted-foreground">Role</dt>
              <dd>{plan.role}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Database</dt>
              <dd>{plan.logical_database}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Review expires</dt>
              <dd>{timestamp(plan.expires_at)}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Secret</dt>
              <dd>{plan.secret_reference}</dd>
            </div>
          </dl>
        )}
        {plan?.warnings.map((w) => (
          <Note key={w}>{w}</Note>
        ))}
      </FormSection>
      {plan && (
        <FormSection title="Confirm deployment">
          <label className="grid gap-2">
            Type {plan.application_name} to confirm
            <Input
              required
              autoComplete="off"
              value={confirmation}
              disabled={busy}
              onChange={(e) => setConfirmation(e.target.value)}
            />
          </label>
          {stale && (
            <Note>The database, application, or review changed. Review current state again.</Note>
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
              setConfirmation('')
              key.current = ''
              setError('')
            }}
          >
            Review again
          </Button>
        )}
        <Button
          type="submit"
          variant="primary"
          disabled={
            busy ||
            stale ||
            !selected ||
            !service ||
            Boolean(plan && confirmation !== plan.application_name)
          }
        >
          {busy ? 'Checking…' : plan ? 'Create and deploy' : 'Review database access'}
        </Button>
      </div>
    </form>,
  )
}
