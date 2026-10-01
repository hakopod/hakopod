import { useRef, useState, type ReactNode } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { endpointName } from '../lib/database-view'
import { useDatabase } from '../lib/databases'
import { useScope, canAccess } from '../lib/scope'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import type { components } from '../lib/api.generated'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Empty, ErrorState, Loading, Note, PageHeader } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'

export const Route = createFileRoute('/databases/$databaseId/connect')({ component: Page })
function Page() {
  const id = Route.useParams().databaseId
  return <Connect key={id} id={id} />
}
function ConnectionState({ children }: { children: ReactNode }) {
  return (
    <div className="ops-page">
      <PageHeader title="Connect application" />
      {children}
    </div>
  )
}
function Connect({ id }: { id: string }) {
  const database = useDatabase(id)
  const { identity } = useScope()
  const [applicationId, setApplicationId] = useState('')
  const [service, setService] = useState('')
  const [variable, setVariable] = useState('DATABASE_URL')
  const [endpoint, setEndpoint] = useState('')
  const [clusterAware, setClusterAware] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const [plan, setPlan] = useState<components['schemas']['DatabaseConnectionPlan'] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [cursor, setCursor] = useState('')
  const key = useRef('')
  const navigate = useNavigate()
  const cache = useQueryClient()
  const d = database.data
  const applications = useQuery({
    queryKey: ['applications', d?.project, d?.environment, cursor],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications', {
          signal,
          params: {
            query: { project: d!.project, environment: d!.environment, cursor, limit: 25 },
          },
        }),
      ),
    enabled: Boolean(d),
    gcTime: 0,
  })
  const application = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    enabled: Boolean(applicationId),
    gcTime: 0,
  })
  if (database.error)
    return (
      <ConnectionState>
        <ErrorState error={database.error} />
      </ConnectionState>
    )
  if (database.isPending || applications.isPending)
    return (
      <ConnectionState>
        <Loading />
      </ConnectionState>
    )
  if (database.error || applications.error)
    return (
      <ConnectionState>
        <ErrorState error={database.error || applications.error} />
      </ConnectionState>
    )
  if (!d) return null
  if (!applications.data.items.length && !cursor)
    return (
      <ConnectionState>
        <Empty
          title="No applications in this environment"
          description="Create an application in this project and environment before connecting a managed database."
        />
      </ConnectionState>
    )
  if (identity.application || !canAccess(identity, d.project, 'deployments:write'))
    return (
      <ConnectionState>
        <Note>Connecting a database requires project deployment permission.</Note>
      </ConnectionState>
    )
  if (d.status !== 'ready')
    return (
      <ConnectionState>
        <Note>Wait for the database to be ready before connecting an application.</Note>
      </ConnectionState>
    )
  if (d.recovery && !d.recovery.inspected_at)
    return (
      <ConnectionState>
        <Note>
          Inspect the recovered data and record the inspection on the database page before
          connecting an application.
        </Note>
      </ConnectionState>
    )
  const chosenEndpoint =
    endpoint || (d.spec.engine === 'mongodb' || ['redis', 'clickhouse'].includes(d.spec.engine) && d.spec.mode === 'cluster' ? 'cluster' : 'read_write')
  const expired = Boolean(plan && Date.parse(plan.expires_at) <= Date.now())
  function reset() {
    setPlan(null)
    setConfirmation('')
    key.current = ''
    setError('')
  }
  return (
    <FormPage
      title="Connect application"
      description="Review the saved connection replacement and queue an explicit redeployment."
      breadcrumbs={[]}
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          setBusy(true)
          setError('')
          try {
            if (!plan) {
              setPlan(
                await unwrap(
                  client.POST('/databases/{id}/connection-plan', {
                    params: { path: { id } },
                    body: {
                      application_id: applicationId,
                      service,
                      variable,
                      endpoint: chosenEndpoint,
                      cluster_aware: clusterAware,
                    },
                  }),
                ),
              )
            } else {
              if (!key.current) key.current = crypto.randomUUID()
              const deployment = await unwrap(
                client.POST('/databases/{id}/connect', {
                  params: { path: { id }, header: { 'Idempotency-Key': key.current } },
                  body: { review_id: plan.id, confirm_application: confirmation },
                }),
              )
              void cache.invalidateQueries({ queryKey: ['applications'] })
              void cache.invalidateQueries({ queryKey: ['application', plan.application_id] })
              void navigate({
                to: '/deployments/$deploymentId',
                params: { deploymentId: deployment.id },
              })
            }
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <FormSection title="Saved connection">
          <p>
            Database: {d.spec.name} · {d.project} / {d.environment}
          </p>
          <label>
            Application
            <SelectField
              label="Application"
              required
              value={applicationId}
              disabled={busy || Boolean(plan)}
              onValueChange={(value) => {
                reset()
                setApplicationId(value)
                setService('')
              }}
              options={[
                { value: '', label: 'Choose an application' },
                ...applications.data.items.map((a) => ({ value: a.id, label: a.name })),
              ]}
            />
          </label>
          {(cursor || applications.data.next_cursor) && (
            <div className="flex flex-wrap gap-2">
              <Button
                disabled={busy || Boolean(plan) || !cursor}
                onClick={() => {
                  setCursor('')
                  setApplicationId('')
                  setService('')
                }}
              >
                First page
              </Button>
              <Button
                disabled={busy || Boolean(plan) || !applications.data.next_cursor}
                onClick={() => {
                  setCursor(applications.data.next_cursor || '')
                  setApplicationId('')
                  setService('')
                }}
              >
                Next applications
              </Button>
            </div>
          )}
          {application.error && <ErrorState error={application.error} />}
          <label>
            Service
            <SelectField
              label="Service"
              required
              value={service}
              disabled={busy || Boolean(plan) || application.isPending}
              onValueChange={(value) => {
                reset()
                setService(value)
              }}
              options={[
                { value: '', label: 'Choose a service' },
                ...Object.keys(application.data?.spec.services || {}).map((name) => ({
                  value: name,
                  label: name,
                })),
              ]}
            />
          </label>
          <label>
            Environment variable
            <Input
              required
              maxLength={128}
              pattern="[A-Za-z_][A-Za-z0-9_]*"
              value={variable}
              disabled={busy || Boolean(plan)}
              onChange={(e) => {
                reset()
                setVariable(e.target.value)
              }}
            />
          </label>
          <label>
            Endpoint
            <SelectField
              label="Endpoint"
              required
              value={chosenEndpoint}
              disabled={busy || Boolean(plan)}
              onValueChange={(value) => {
                reset()
                setEndpoint(value)
                setClusterAware(false)
              }}
              options={(d.observation.endpoints || []).map((e) => ({
                value: e.purpose,
                label: endpointName(e.purpose, d.spec.engine),
              }))}
            />
          </label>
          {chosenEndpoint.startsWith('pooled_') && <Note>PgBouncer uses {d.spec.pooling?.mode} pooling. Choose write or replica traffic explicitly. Clients must reconnect after failover.{d.spec.pooling?.mode === 'transaction' ? ' Session settings and temporary tables across transactions need a direct or session connection.' : ''}</Note>}
          {chosenEndpoint === 'cluster' && (
            <label className="flex min-h-11 items-center gap-2">
              <Input
                type="checkbox"
                required
                checked={clusterAware}
                disabled={busy || Boolean(plan)}
                onChange={(e) => {
                  reset()
                  setClusterAware(e.target.checked)
                }}
              />
              {d.spec.engine === 'mongodb' ? 'This application uses a MongoDB driver that supports replica-set discovery.' : d.spec.engine === 'clickhouse' ? 'This application uses Distributed tables or explicit shard routing. The endpoint does not combine local tables automatically.' : 'This application uses a cluster-aware Redis client.'}
            </label>
          )}
        </FormSection>
        {plan && (
          <FormSection title="Review connection and redeployment">
            <p>
              {plan.application_name} / {plan.service} · revision {plan.application_revision} →{' '}
              {plan.application_revision + 1}
            </p>
            <p>
              {plan.variable}: replace {plan.previous_kind} with {plan.database_name} (
              {plan.binding.endpoint?.replaceAll('_', ' ')}), database revision{' '}
              {plan.database_revision}.
            </p>
            {plan.recovery && (
              <p>
                Recovery point: {timestamp(plan.recovery.captured_at)} · Inspected:{' '}
                {timestamp(plan.recovery.inspected_at)}
              </p>
            )}
            {plan.warnings.map((warning) => (
              <Note key={warning}>{warning}</Note>
            ))}
            <p>Review expires {timestamp(plan.expires_at)}.</p>
            <label>
              Type {plan.application_name} to confirm redeployment
              <Input
                required
                value={confirmation}
                disabled={busy}
                onChange={(e) => setConfirmation(e.target.value)}
              />
            </label>
            <Button className="justify-self-start" disabled={busy} onClick={reset}>
              Edit or refresh review
            </Button>
          </FormSection>
        )}
        {error && (
          <FormError>{error}</FormError>
        )}
        {expired && <Note>This review expired. Refresh it before continuing.</Note>}
        <div className="py-4">
          <Button
            type="submit"
            variant="primary"
            disabled={
              busy ||
              !applicationId ||
              !service ||
              expired ||
              (chosenEndpoint === 'cluster' && !clusterAware) ||
              Boolean(plan && confirmation !== plan.application_name)
            }
          >
            {busy ? 'Working…' : plan ? 'Replace connection and redeploy' : 'Review connection'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
