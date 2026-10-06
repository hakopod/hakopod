import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { endpointName } from '../lib/database-view'
import { useDatabase } from '../lib/databases'
import { useScope, useResourceScope, canAccess } from '../lib/scope'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import type { components } from '../lib/api.generated'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Empty, ErrorState, Loading, Note, PageHeader } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { DatabaseBindingFields, DatabaseBindingSummary } from '../components/database-binding-options'
import { databaseBindingIssue, databaseBindingOptions, databaseConnectionReviewIssue, emptyDatabaseBindingDraft, saveDatabaseBindingPassword } from '../lib/database-binding'

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
  const [options, setOptions] = useState(emptyDatabaseBindingDraft)
  const [savedSecret, setSavedSecret] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [plan, setPlan] = useState<components['schemas']['DatabaseConnectionPlan'] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [cursor, setCursor] = useState('')
  const [now, setNow] = useState(Date.now)
  const key = useRef('')
  const review = useRef<HTMLDivElement>(null)
  const navigate = useNavigate()
  const cache = useQueryClient()
  const d = database.data
  useResourceScope(d)
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 5000)
    return () => clearInterval(timer)
  }, [])
  useEffect(() => { if (plan) review.current?.focus() }, [plan])
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
    refetchInterval: 5000,
    gcTime: 0,
  })
  const selectedApplication = application.data?.id === applicationId && application.data.project === d?.project && application.data.environment === d?.environment ? application.data : undefined
  const secretScope = selectedApplication ? { project: selectedApplication.project, environment: selectedApplication.environment, application: selectedApplication.name } : undefined
  const secretScopeKey = secretScope ? `${secretScope.project}/${secretScope.environment}/${secretScope.application}` : ''
  useEffect(() => {
    setOptions((previous) => ({ ...previous, passwordSource: 'managed', passwordRef: '', passwordValue: '' }))
    setSavedSecret('')
    setPlan(null)
    setConfirmation('')
    key.current = ''
  }, [secretScopeKey])
  const secrets = useQuery({
    queryKey: ['secrets', secretScope?.project, secretScope?.environment, secretScope?.application],
    queryFn: ({ signal }) => unwrap(client.GET('/secrets', { signal, params: { query: secretScope! } })),
    enabled: Boolean(secretScope),
    retry: false,
    gcTime: 0,
  })
  if (database.error && !d)
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
  if (applications.error && !applications.data)
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
  const optionIssue = databaseBindingIssue(d.spec, options, chosenEndpoint)
  const reviewIssue = databaseConnectionReviewIssue(plan, d.revision, selectedApplication?.revision, Math.max(now, Date.now()))
  const refreshFailed = Boolean(database.error || applications.error || application.error)
  const secretNames = [...new Set([...(secrets.data?.items.map((secret) => secret.name) || []), ...(savedSecret ? [savedSecret] : [])])].sort()
  const secretsFailed = Boolean(secrets.error) && (!savedSecret || options.passwordRef !== savedSecret)
  const unavailableSecret = options.passwordSource === 'existing' && (secretsFailed || !secretNames.includes(options.passwordRef))
  const selectedService = Boolean(selectedApplication?.spec.services[service])
  const canSavePassword = Boolean(selectedApplication && canAccess(identity, selectedApplication.project, 'deployments:write'))
  const invalid = refreshFailed || Boolean(reviewIssue) || Boolean(optionIssue) || unavailableSecret || !selectedService
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
        className="grid gap-4"
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy || invalid || !selectedApplication || !secretScope) return
          if (plan && databaseConnectionReviewIssue(plan, d.revision, selectedApplication.revision, Date.now())) {
            setNow(Date.now())
            return
          }
          if (!applicationId || !service || chosenEndpoint === 'cluster' && !clusterAware) return
          setBusy(true)
          setError('')
          try {
            if (!plan) {
              let draft = options
              if (draft.passwordSource === 'enter') {
                if (!canSavePassword) throw new Error('Saving an application password requires deployment permission.')
                const passwordRef = await saveDatabaseBindingPassword(draft.passwordValue, secretScope)
                draft = { ...draft, passwordSource: 'existing', passwordRef, passwordValue: '' }
                setOptions(draft)
                setSavedSecret(passwordRef)
                void cache.invalidateQueries({ queryKey: ['secrets', secretScope.project, secretScope.environment, secretScope.application] })
              }
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
                      ...databaseBindingOptions(draft),
                    },
                  }),
                ),
              )
            } else {
              if (confirmation !== plan.application_name) return
              if (!key.current) key.current = crypto.randomUUID()
              const deployment = await unwrap(
                client.POST('/databases/{id}/connect', {
                  params: { path: { id }, header: { 'Idempotency-Key': key.current } },
                  body: { review_id: plan.id, confirm_application: confirmation },
                }),
              )
              void cache.invalidateQueries({ queryKey: ['applications'] })
              void cache.invalidateQueries({ queryKey: ['application', plan.application_id] })
              void cache.invalidateQueries({ queryKey: ['database-connections', id] })
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
        {refreshFailed && <FormError focus={false}>Database or application status could not be refreshed. Your entries are preserved; retry after the connection recovers.</FormError>}
        <FormSection title="Saved connection">
          <p>
            Database: {d.spec.name} · {d.project} / {d.environment}
          </p>
          <div className="grid min-w-0 gap-4 sm:grid-cols-2">
          <label className="min-w-0">
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
            <div className="flex flex-wrap gap-2 sm:col-span-2">
              <Button
                disabled={busy || Boolean(plan) || !cursor}
                onClick={() => {
                  setCursor('')
                  reset()
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
                  reset()
                  setApplicationId('')
                  setService('')
                }}
              >
                Next applications
              </Button>
            </div>
          )}
          {application.error && <ErrorState error={application.error} />}
          <label className="min-w-0">
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
                ...Object.keys(selectedApplication?.spec.services || {}).map((name) => ({
                  value: name,
                  label: name,
                })),
              ]}
            />
          </label>
          <label className="min-w-0">
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
          <label className="min-w-0">
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
          </div>
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
        {application.data && !selectedApplication && <FormError focus={false}>This application is outside the database’s project or environment. Choose an application in the displayed scope.</FormError>}
        <DatabaseBindingFields
          spec={d.spec}
          draft={options}
          onChange={(draft) => { reset(); setOptions(draft) }}
          disabled={busy || Boolean(plan)}
          applicationName={selectedApplication?.name}
          secretNames={secretNames}
          secretsLoading={secrets.isPending}
          secretsFailed={secretsFailed}
          canSavePassword={canSavePassword}
        />
        {secrets.error && options.passwordSource === 'existing' && <Button className="justify-self-start" disabled={busy || secrets.isFetching || Boolean(plan)} onClick={() => void secrets.refetch()}>Retry application secrets</Button>}
        {savedSecret && <p role="status" className="break-all text-sm">Password saved as {savedSecret} for {selectedApplication?.name}. It remains in application secrets if you leave without connecting.</p>}
        {optionIssue && <p className="field-help" role="status">{optionIssue}</p>}
        {unavailableSecret && options.passwordRef && <Note>The selected password is unavailable in this application. Refresh the secret list or choose another password.</Note>}
        {plan && (
          <div ref={review} tabIndex={-1} className="outline-offset-4 focus:outline-2 focus:outline-ring">
          <FormSection title="Review connection and redeployment">
            <p>
              {plan.application_name} / {plan.service} · revision {plan.application_revision} →{' '}
              {plan.application_revision + 1}
            </p>
            <DatabaseBindingSummary binding={plan.binding} />
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
                disabled={busy || Boolean(reviewIssue) || refreshFailed}
                onChange={(e) => setConfirmation(e.target.value)}
              />
            </label>
            <Button className="justify-self-start" disabled={busy} onClick={reset}>
              Edit or refresh review
            </Button>
          </FormSection>
          </div>
        )}
        {error && (
          <FormError>{error}</FormError>
        )}
        {reviewIssue && <Note>{reviewIssue}</Note>}
        <div className="py-4">
          <Button
            type="submit"
            variant="primary"
            disabled={
              busy ||
              !applicationId ||
              !service ||
              invalid ||
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
