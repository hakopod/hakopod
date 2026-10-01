import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import {
  useDatabase,
  useDatabasePublicEndpointCapabilities,
  useDatabasePublicEndpoints,
  type DatabasePublicEndpoint,
  type DatabasePublicEndpointPlan,
  type DatabasePublicEndpointRoute,
} from '../lib/databases'
import { canAccess, useResourceScope, useScope } from '../lib/scope'
import { Copy, Empty, ErrorState, Loading, Note, Status } from '../components/shared'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { Textarea } from '../components/ui/textarea'
import { Icon } from '../components/icons'
import {
  databasePublicEndpointPublicationAvailable,
  databasePublicEndpointPublicationUnavailableReason,
  databasePublicEndpointRevocationDisabled,
  databasePublicEndpointOperationTerminal,
  databasePublicEndpointAccessLabel,
  databasePublicEndpointProtocolLabel,
  databasePublicEndpointRouteForPurpose,
  databasePublicEndpointRouteLabel,
  databasePublicEndpointRouteOptions,
  databasePublicEndpointRoutingLabel,
  parseDatabasePublicEndpointCIDRs,
} from '../lib/database-public-endpoints'

export const Route = createFileRoute('/databases/$databaseId/public-endpoints')({
  validateSearch: (search: Record<string, unknown>): { operation?: string } => ({
    operation:
      typeof search.operation === 'string' && /^[a-f0-9]{32}$/.test(search.operation)
        ? search.operation
        : undefined,
  }),
  component: Page,
})

function Frame({ children, databaseName }: { children: ReactNode; databaseName?: string }) {
  return (
    <FormPage
      title={databaseName ? `Public endpoints for ${databaseName}` : 'Public database endpoints'}
      description="Publish a restricted database listener from operator-owned address and port inventory."
      breadcrumbs={[]}
    >
      {children}
    </FormPage>
  )
}

function Page() {
  const id = Route.useParams().databaseId
  const { operation: operationID } = Route.useSearch()
  const database = useDatabase(id)
  useResourceScope(database.data)
  const { identity } = useScope()
  const d = database.data
  const canManage = Boolean(
    d && !identity.application && canAccess(identity, d.project, 'deployments:write'),
  )
  const capabilities = useDatabasePublicEndpointCapabilities(id, canManage)
  const endpoints = useDatabasePublicEndpoints(id, canManage)
  const cache = useQueryClient()
  const navigate = useNavigate()
  const [purpose, setPurpose] = useState<DatabasePublicEndpointRoute['purpose']>('read_write')
  const [sourceCIDRs, setSourceCIDRs] = useState('')
  const [maxConnections, setMaxConnections] = useState('32')
  const [plan, setPlan] = useState<DatabasePublicEndpointPlan | null>(null)
  const [planEndpointStatus, setPlanEndpointStatus] = useState('')
  const [revoke, setRevoke] = useState<DatabasePublicEndpoint | null>(null)
  const [publishBusy, setPublishBusy] = useState(false)
  const [revokeBusy, setRevokeBusy] = useState(false)
  const [publishError, setPublishError] = useState('')
  const [revokeError, setRevokeError] = useState('')
  const publishKey = useRef('')
  const revokeKey = useRef('')
  const publishReview = useRef<HTMLDivElement>(null)
  const hadPublishReview = useRef(false)
  const revokeReview = useRef<HTMLDivElement>(null)
  const operation = useQuery({
    queryKey: ['database-public-endpoint-operation', operationID],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/database-public-endpoint-operations/{id}', {
          signal,
          params: { path: { id: operationID! } },
        }),
      ),
    enabled: Boolean(operationID && canManage),
    refetchInterval: (query) =>
      databasePublicEndpointOperationTerminal(query.state.data?.status) ? false : 2000,
    gcTime: 0,
  })
  useEffect(() => {
    if (!operation.data || !databasePublicEndpointOperationTerminal(operation.data.status)) return
    void cache.invalidateQueries({ queryKey: ['database-public-endpoints', id] })
    void cache.invalidateQueries({ queryKey: ['managed-database', id] })
  }, [cache, id, operation.data])
  useEffect(() => {
    if (revoke) revokeReview.current?.focus()
  }, [revoke?.id])
  useEffect(() => {
    if (plan || hadPublishReview.current) publishReview.current?.focus()
    hadPublishReview.current = Boolean(plan)
  }, [plan?.id])
  useEffect(() => {
    const routes = capabilities.data?.routes
    if (!routes?.length || databasePublicEndpointRouteForPurpose(routes, purpose)) return
    setPurpose(routes[0].purpose)
  }, [capabilities.data, purpose])
  if (!d)
    return <Frame>{database.error ? <ErrorState error={database.error} /> : <Loading />}</Frame>
  const reviewedDatabase = { project: d.project, environment: d.environment, revision: d.revision }
  const search = { project: d.project, environment: d.environment }
  const back = (
    <Button asChild>
      <Link
        to="/databases/$databaseId"
        params={{ databaseId: id }}
        search={{ ...search, tab: 'connections' }}
      >
        Back to database
      </Link>
    </Button>
  )
  if (!canManage)
    return (
      <Frame databaseName={d.spec.name}>
        <Note>Managing public endpoints requires project deployment permission.</Note>
        {back}
      </Frame>
    )
  const rows = endpoints.data?.items || []
  const routes = capabilities.data?.routes || []
  const publicationAvailable = databasePublicEndpointPublicationAvailable(
    capabilities.data,
    Boolean(capabilities.error),
  )
  const publicationUnavailableReason = databasePublicEndpointPublicationUnavailableReason(
    capabilities.data,
    capabilities.isPending,
    Boolean(capabilities.error),
  )
  const routeOptions = databasePublicEndpointRouteOptions(routes)
  const selectedRoute = databasePublicEndpointRouteForPurpose(routes, purpose)
  const parsedCIDRs = parseDatabasePublicEndpointCIDRs(sourceCIDRs)
  const selectedPlan = plan?.plan
  const reviewedRoute = selectedPlan?.route
  const expired = Boolean(selectedPlan && Date.parse(selectedPlan.expires_at) <= Date.now())
  const operationMismatch = Boolean(operation.data && operation.data.database_id !== id)
  const operationBlocksMutations = Boolean(
    operationID &&
    (!operation.data ||
      operationMismatch ||
      !databasePublicEndpointOperationTerminal(operation.data.status)),
  )
  const reviewedEndpoint = selectedPlan
    ? rows.find((endpoint) => endpoint.id === selectedPlan.endpoint_id)
    : undefined
  const publishReviewStale = Boolean(
    selectedPlan &&
    (selectedPlan.database_revision !== d.revision ||
      !reviewedEndpoint ||
      reviewedEndpoint.revision !== selectedPlan.endpoint_revision ||
      reviewedEndpoint.status !== planEndpointStatus),
  )
  const currentRevoke = revoke ? rows.find((endpoint) => endpoint.id === revoke.id) : undefined
  const revokeReviewStale = Boolean(
    revoke &&
    (!currentRevoke ||
      currentRevoke.revision !== revoke.revision ||
      currentRevoke.status !== revoke.status),
  )
  const busy = publishBusy || revokeBusy
  const resetReview = () => {
    setPlan(null)
    setPlanEndpointStatus('')
    publishKey.current = ''
    setPublishError('')
  }
  async function submit(event: React.FormEvent) {
    event.preventDefault()
    if (!publicationAvailable || revoke || operationBlocksMutations || publishReviewStale) return
    setPublishBusy(true)
    setPublishError('')
    try {
      if (!selectedPlan) {
        const reviewed = await unwrap(
          client.POST('/databases/{id}/public-endpoint-plan', {
            params: { path: { id } },
            body: {
              purpose,
              source_cidrs: parsedCIDRs,
              max_connections: Number(maxConnections),
            },
          }),
        )
        if (
          reviewed.plan.database_id !== id ||
          reviewed.plan.project !== reviewedDatabase.project ||
          reviewed.plan.environment !== reviewedDatabase.environment ||
          reviewed.plan.database_revision !== reviewedDatabase.revision
        )
          throw new Error('The returned review does not match this database. Review again.')
        const reviewedRoute = reviewed.plan.route
        const selectedRoute = databasePublicEndpointRouteForPurpose(routes, purpose)
        if (
          !selectedRoute ||
          !reviewedRoute ||
          reviewedRoute.purpose !== selectedRoute.purpose ||
          reviewedRoute.protocol !== selectedRoute.protocol ||
          reviewedRoute.routing !== selectedRoute.routing ||
          reviewedRoute.read_only !== selectedRoute.read_only ||
          reviewedRoute.pooled !== selectedRoute.pooled
        )
          throw new Error('The returned review does not include the selected route. Review again.')
        await cache.invalidateQueries({ queryKey: ['database-public-endpoints', id] })
        const refreshedEndpoints = cache.getQueryData<{ items: DatabasePublicEndpoint[] }>([
          'database-public-endpoints',
          id,
        ])
        const reviewedEndpoint = refreshedEndpoints?.items.find(
          (endpoint) => endpoint.id === reviewed.plan.endpoint_id,
        )
        if (!reviewedEndpoint || reviewedEndpoint.revision !== reviewed.plan.endpoint_revision)
          throw new Error('The endpoint changed while its review was loading. Review again.')
        setRevoke(null)
        revokeKey.current = ''
        setRevokeError('')
        setPlan(reviewed)
        setPlanEndpointStatus(reviewedEndpoint.status)
        publishKey.current = ''
      } else {
        if (!publishKey.current) publishKey.current = crypto.randomUUID()
        const accepted = await unwrap(
          client.POST('/databases/{id}/public-endpoints', {
            params: { path: { id }, header: { 'Idempotency-Key': publishKey.current } },
            body: {
              review_id: plan!.id,
              expected_database_revision: selectedPlan.database_revision,
              expected_endpoint_revision: selectedPlan.endpoint_revision,
            },
          }),
        )
        if (
          accepted.database_id !== id ||
          accepted.endpoint_id !== selectedPlan.endpoint_id ||
          accepted.kind !== 'publish'
        )
          throw new Error('The API returned a different public endpoint operation.')
        setPlan(null)
        setPlanEndpointStatus('')
        await navigate({
          to: '/databases/$databaseId/public-endpoints',
          params: { databaseId: id },
          search: { ...search, operation: accepted.id },
          replace: true,
        })
      }
    } catch (err) {
      setPublishError(message(err))
    } finally {
      setPublishBusy(false)
    }
  }
  async function revokeEndpoint(endpoint: DatabasePublicEndpoint) {
    if (operationBlocksMutations || revokeReviewStale) return
    setRevokeBusy(true)
    setRevokeError('')
    try {
      if (!revokeKey.current) revokeKey.current = crypto.randomUUID()
      const accepted = await unwrap(
        client.DELETE('/databases/{id}/public-endpoints/{endpoint}', {
          params: {
            path: { id, endpoint: endpoint.id },
            header: { 'Idempotency-Key': revokeKey.current },
          },
          body: { expected_endpoint_revision: endpoint.revision },
        }),
      )
      if (
        accepted.database_id !== id ||
        accepted.endpoint_id !== endpoint.id ||
        accepted.kind !== 'revoke'
      )
        throw new Error('The API returned a different public endpoint operation.')
      setRevoke(null)
      await navigate({
        to: '/databases/$databaseId/public-endpoints',
        params: { databaseId: id },
        search: { ...search, operation: accepted.id },
        replace: true,
      })
    } catch (err) {
      setRevokeError(message(err))
    } finally {
      setRevokeBusy(false)
    }
  }
  return (
    <Frame databaseName={d.spec.name}>
      <div className="grid gap-4">
        {endpoints.error && <Note>Endpoint refresh failed. Showing the last received state.</Note>}
        <FormSection
          title="Endpoint routes"
          description="This inventory includes reviewed reservations, pending changes and active routes. Configured means every owned HAProxy worker acknowledged the exact route."
        >
          {endpoints.isPending ? (
            <Loading />
          ) : !rows.length ? (
            <Empty
              title="No public endpoints"
              description="This database accepts connections only through private application bindings."
            />
          ) : (
            <ul className="grid gap-3">
              {rows.map((endpoint) => (
                <li
                  key={endpoint.id}
                  className="grid gap-2 border-b border-border pb-3 last:border-0 last:pb-0"
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <strong>
                      {(() => {
                        const route = databasePublicEndpointRouteForPurpose(
                          routes,
                          endpoint.spec.purpose,
                        )
                        return route
                          ? databasePublicEndpointRouteLabel(route)
                          : endpoint.spec.purpose.replace(/_/g, ' ')
                      })()}
                    </strong>
                    <Status value={endpoint.status} />
                  </div>
                  <dl className="db-facts db-endpoint-facts">
                    <div>
                      <dt>Hostname</dt>
                      <dd className="flex min-w-0 items-center gap-2">
                        <code className="truncate">{endpoint.allocation.host}</code>
                        <Copy iconOnly value={endpoint.allocation.host} label="Copy hostname" />
                      </dd>
                    </div>
                    <div>
                      <dt>Port</dt>
                      <dd>{endpoint.allocation.port}</dd>
                    </div>
                    <div>
                      <dt>Configured</dt>
                      <dd>
                        {endpoint.observation.configured ? 'Acknowledged' : 'Not acknowledged'}
                      </dd>
                    </div>
                    <div>
                      <dt>Connection cap</dt>
                      <dd>{endpoint.spec.max_connections}</dd>
                    </div>
                    <div>
                      <dt>Last checked</dt>
                      <dd>
                        {endpoint.observation.checked_at
                          ? timestamp(endpoint.observation.checked_at)
                          : 'Not checked'}
                      </dd>
                    </div>
                    <div>
                      <dt>External verification</dt>
                      <dd>
                        {endpoint.observation.externally_verified ? 'Verified' : 'Not verified'}
                      </dd>
                    </div>
                    <div className="col-span-full">
                      <dt>Allowed sources</dt>
                      <dd className="break-all">{endpoint.spec.source_cidrs.join(', ')}</dd>
                    </div>
                  </dl>
                  {endpoint.observation.message && (
                    <p className="text-sm text-muted-foreground">{endpoint.observation.message}</p>
                  )}
                  {!['review', 'pending', 'revoking'].includes(endpoint.status) && (
                    <div>
                      <Button
                        disabled={databasePublicEndpointRevocationDisabled(
                          busy,
                          operationBlocksMutations,
                          false,
                        )}
                        onClick={() => {
                          setPlan(null)
                          setPlanEndpointStatus('')
                          setRevoke(endpoint)
                          publishKey.current = ''
                          revokeKey.current = ''
                          setPublishError('')
                          setRevokeError('')
                        }}
                      >
                        <Icon name="trash" size={14} />
                        Review revocation
                      </Button>
                    </div>
                  )}
                </li>
              ))}
            </ul>
          )}
        </FormSection>
        {revoke && (
          <div
            ref={revokeReview}
            role="region"
            tabIndex={-1}
            aria-label={`Review revocation for ${revoke.allocation.host}`}
            className="outline-offset-2 focus:outline-2 focus:outline-current"
          >
            <FormSection title="Review revocation">
              <Note>
                Revoking {revoke.allocation.host}:{revoke.allocation.port} closes its existing
                connections before releasing the operator allocation. Private database connections
                remain available.
              </Note>
              {revokeReviewStale && (
                <Note>
                  This endpoint changed after the review opened. Close this review and inspect the
                  current route before revoking it.
                </Note>
              )}
              {revokeError && <FormError>{revokeError}</FormError>}
              <div className="flex flex-wrap gap-2">
                <Button
                  disabled={revokeBusy}
                  onClick={() => {
                    setRevoke(null)
                    revokeKey.current = ''
                    setRevokeError('')
                  }}
                >
                  Keep endpoint
                </Button>
                <Button
                  variant="danger"
                  disabled={databasePublicEndpointRevocationDisabled(
                    busy,
                    operationBlocksMutations,
                    revokeReviewStale,
                  )}
                  onClick={() => void revokeEndpoint(revoke)}
                >
                  <Icon name="trash" size={14} />
                  {revokeBusy ? 'Revoking…' : 'Revoke endpoint'}
                </Button>
              </div>
            </FormSection>
          </div>
        )}
        <form className="grid gap-4" onSubmit={(event) => void submit(event)}>
          <div
            ref={publishReview}
            role="region"
            tabIndex={-1}
            aria-label={selectedPlan ? 'Review public endpoint' : 'Endpoint controls'}
            className="outline-offset-2 focus:outline-2 focus:outline-current"
          >
            <FormSection
              title={selectedPlan ? 'Review public endpoint' : 'Endpoint controls'}
              description="The installation operator owns the address, hostname and port. You choose an available route, source networks and connection cap."
            >
              {revoke && (
                <Note>Close the revocation review before reviewing another endpoint change.</Note>
              )}
              {!publicationAvailable && <Note>{publicationUnavailableReason}</Note>}
              {!selectedPlan ? (
                <div className="grid gap-3">
                  <label className="grid gap-2">
                    Route
                    <SelectField
                      label="Route"
                      required
                      value={purpose}
                      disabled={
                        busy || !publicationAvailable || Boolean(revoke) || operationBlocksMutations
                      }
                      onValueChange={(value) => {
                        const route = databasePublicEndpointRouteForPurpose(routes, value)
                        if (!route) return
                        resetReview()
                        setPurpose(route.purpose)
                      }}
                      options={routeOptions}
                    />
                    {!publicationAvailable && selectedRoute && (
                      <span className="field-help">
                        {databasePublicEndpointRoutingLabel(selectedRoute)} ·{' '}
                        {databasePublicEndpointAccessLabel(selectedRoute)}
                      </span>
                    )}
                  </label>
                  <label className="grid gap-2">
                    Allowed IPv4 networks
                    <Textarea
                      required
                      rows={4}
                      value={sourceCIDRs}
                      disabled={
                        busy || !publicationAvailable || Boolean(revoke) || operationBlocksMutations
                      }
                      placeholder={'192.0.2.0/24\n198.51.100.8/32'}
                      onChange={(event) => {
                        resetReview()
                        setSourceCIDRs(event.target.value)
                      }}
                    />
                    <span className="field-help">
                      Enter one CIDR per line. Use /32 for one address.
                    </span>
                  </label>
                  <label className="grid gap-2">
                    Connection cap
                    <Input
                      type="number"
                      required
                      min={1}
                      max={256}
                      value={maxConnections}
                      disabled={
                        busy || !publicationAvailable || Boolean(revoke) || operationBlocksMutations
                      }
                      onChange={(event) => {
                        resetReview()
                        setMaxConnections(event.target.value)
                      }}
                    />
                  </label>
                </div>
              ) : (
                <div className="grid gap-3">
                  <dl className="db-facts">
                    <div>
                      <dt>Hostname</dt>
                      <dd className="break-all">{selectedPlan.allocation.host}</dd>
                    </div>
                    <div>
                      <dt>Address and port</dt>
                      <dd>
                        {selectedPlan.allocation.address}:{selectedPlan.allocation.port}
                      </dd>
                    </div>
                    <div>
                      <dt>Route</dt>
                      <dd>
                        {reviewedRoute
                          ? databasePublicEndpointRouteLabel(reviewedRoute)
                          : selectedPlan.spec.purpose}
                      </dd>
                    </div>
                    {reviewedRoute && (
                      <>
                        <div>
                          <dt>Protocol</dt>
                          <dd>{databasePublicEndpointProtocolLabel(reviewedRoute)}</dd>
                        </div>
                        <div>
                          <dt>Routing</dt>
                          <dd>{databasePublicEndpointRoutingLabel(reviewedRoute)}</dd>
                        </div>
                        <div>
                          <dt>Access</dt>
                          <dd>{databasePublicEndpointAccessLabel(reviewedRoute)}</dd>
                        </div>
                      </>
                    )}
                    <div>
                      <dt>Connection cap</dt>
                      <dd>{selectedPlan.spec.max_connections}</dd>
                    </div>
                    <div className="col-span-full">
                      <dt>Allowed sources</dt>
                      <dd className="break-all">{selectedPlan.spec.source_cidrs.join(', ')}</dd>
                    </div>
                    <div>
                      <dt>Database revision</dt>
                      <dd>{selectedPlan.database_revision}</dd>
                    </div>
                    <div>
                      <dt>Endpoint revision</dt>
                      <dd>{selectedPlan.endpoint_revision}</dd>
                    </div>
                    <div>
                      <dt>Review expires</dt>
                      <dd>{timestamp(selectedPlan.expires_at)}</dd>
                    </div>
                  </dl>
                  {selectedPlan.warnings.map((warning) => (
                    <Note key={warning}>{warning}</Note>
                  ))}
                  {expired && (
                    <Note>This review expired. Request a new review before publishing.</Note>
                  )}
                  {publishReviewStale && (
                    <Note>
                      The database or endpoint changed after this review. Edit the controls and
                      review the current state again.
                    </Note>
                  )}
                </div>
              )}
            </FormSection>
          </div>
          {operation.data && !operationMismatch && (
            <FormSection title="Latest operation">
              <div className="flex flex-wrap items-center gap-2">
                <Status value={operation.data.status} />
                <span>{operation.data.phase}</span>
              </div>
              {operation.data.message && <p>{operation.data.message}</p>}
              <p className="text-sm text-muted-foreground">
                Configured route state is checked inside the cluster. External reachability remains
                unverified until a separate probe records it.
              </p>
            </FormSection>
          )}
          {operation.error && (
            <FormError focus={false}>
              The tracked operation could not be refreshed. Operation ID: {operationID}
            </FormError>
          )}
          {operationMismatch && (
            <FormError>The API returned an operation for another database.</FormError>
          )}
          {(operation.error || operationMismatch) && (
            <Button
              type="button"
              onClick={() =>
                void navigate({
                  to: '/databases/$databaseId/public-endpoints',
                  params: { databaseId: id },
                  search: { ...search, operation: undefined },
                  replace: true,
                })
              }
            >
              Clear tracked operation
            </Button>
          )}
          {publishError && <FormError>{publishError}</FormError>}
          <div className="form-actions flex flex-wrap gap-2">
            {back}
            {selectedPlan && (
              <Button type="button" disabled={publishBusy} onClick={resetReview}>
                Edit controls
              </Button>
            )}
            <Button
              type="submit"
              variant="primary"
              disabled={
                busy ||
                Boolean(revoke) ||
                operationBlocksMutations ||
                !publicationAvailable ||
                (selectedPlan
                  ? expired || publishReviewStale
                  : !sourceCIDRs.trim() ||
                    Number(maxConnections) < 1 ||
                    Number(maxConnections) > 256)
              }
            >
              {publishBusy ? 'Checking…' : selectedPlan ? 'Publish endpoint' : 'Review endpoint'}
            </Button>
          </div>
        </form>
      </div>
    </Frame>
  )
}
