import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { Plan, Spec } from '../lib/types'
import { connectVirtualNetwork, type VirtualNetworkDetail } from '../lib/virtual-networks'
import { ChoiceCards, FlowSteps, PendingButton, plural } from './create-flow'
import { DiffTable } from './deploy-dialog'
import { FormPage } from './form-page'
import { Icon } from './icons'
import { ErrorState, Loading, Note, RequestError } from './shared'
import { ResourceEmpty } from './resource-card'
import { Consequence, Consequences, ReviewFacts, ScopeLabel } from './task-review'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { dashboardEdition } from '../lib/dashboard-edition'

function suggestedName(network: string, segment: string) {
  return `${network.slice(0, 20)}-${segment.slice(0, 19)}`.replace(/-+$/, '')
}

// The reverse of connectVirtualNetwork: drop one service membership, and the local
// network declaration once no service uses it.
function disconnectVirtualNetwork(spec: Spec, network: string, serviceName: string, local: string) {
  const next = structuredClone(spec)
  const service = next.services[serviceName]
  if (!service) throw new Error('The service is no longer in this application.')
  const memberships = service.networks || ['default']
  if (!memberships.includes(local))
    throw new Error(`${serviceName} is no longer connected through ${local}.`)
  const remaining = memberships.filter((name) => name !== local)
  if (!remaining.length)
    throw new Error(
      `${local} is the only network of ${serviceName}. Add another network to it in application TOML before disconnecting.`,
    )
  service.networks = remaining
  const unused = !Object.values(next.services).some((item) =>
    (item.networks || ['default']).includes(local),
  )
  let removedLocal = false
  if (unused && next.networks?.[local]?.virtual_network === network) {
    const { [local]: _removed, ...rest } = next.networks
    next.networks = rest
    removedLocal = true
  }
  return { spec: next, removedLocal }
}

export function VirtualNetworkConnect({
  project,
  environment,
  network,
  initialSegment,
  initialApplication,
  disconnect,
}: {
  project: string
  environment: string
  network: VirtualNetworkDetail
  initialSegment?: string
  initialApplication?: string
  disconnect?: string
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const networkName = network.network.name
  const segments = Object.keys(network.spec.segments)
  const [disconnectApp = '', disconnectService = '', disconnectLocal = ''] = disconnect
    ? disconnect.split('/')
    : []
  const leaving = disconnect
    ? network.connections.find(
        (item) =>
          item.application_id === disconnectApp &&
          item.service === disconnectService &&
          item.network === disconnectLocal,
      )
    : undefined
  const startSegment =
    leaving?.segment || (initialSegment && segments.includes(initialSegment) ? initialSegment : '')
  const [segment, setSegment] = useState(startSegment || segments[0] || '')
  const [applicationId, setApplicationId] = useState(leaving?.application_id || '')
  const [serviceName, setServiceName] = useState(leaving?.service || '')
  const [localName, setLocalName] = useState(
    () => leaving?.network || suggestedName(networkName, startSegment || segments[0] || 'shared'),
  )
  const [plan, setPlan] = useState<Plan | null>(null)
  const [removedLocal, setRemovedLocal] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const request = useRef<AbortController | null>(null)
  const idempotency = useRef('')
  useEffect(() => () => request.current?.abort(), [])
  const candidates = useQuery({
    queryKey: ['virtual-network-candidates', project, environment, networkName],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/virtual-networks/{name}/candidates', {
          signal,
          params: { path: { name: networkName }, query: { project, environment } },
        }),
      ),
    enabled: !disconnect,
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  const allowed = network.spec.segments[segment]?.applications || []
  const choices = (candidates.data?.items || []).filter((application) =>
    allowed.includes(application.name),
  )
  // A link from the network page names the application; select it once candidates load.
  const chosenId =
    applicationId ||
    (initialApplication && choices.find((item) => item.name === initialApplication)?.id) ||
    ''
  const selected = choices.find((application) => application.id === chosenId)
  const application = useQuery({
    queryKey: ['application', chosenId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: chosenId } } })),
    enabled: Boolean(selected),
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  const services = application.data
    ? Object.keys(application.data.spec.services)
    : selected?.services || []
  const service = application.data?.spec.services[serviceName]
  const appName = leaving?.application || selected?.name || plan?.spec.name || ''
  const peers = network.connections.filter(
    (item) =>
      item.segment === segment &&
      !(item.application === appName && item.service === serviceName),
  )
  const close = () =>
    void navigate({ to: '/networks/$networkName', params: { networkName }, search: {} })
  async function review() {
    const id = leaving?.application_id || selected?.id
    if (!id || !serviceName) return
    setBusy(true)
    setError('')
    const controller = new AbortController()
    request.current = controller
    try {
      const [current, currentNetwork] = await Promise.all([
        unwrap(
          client.GET('/applications/{id}', {
            signal: controller.signal,
            params: { path: { id } },
          }),
        ),
        unwrap(
          client.GET('/virtual-networks/{name}', {
            signal: controller.signal,
            params: { path: { name: networkName }, query: { project, environment } },
          }),
        ),
      ])
      if (current.project !== project || current.environment !== environment)
        throw new Error('The selected application belongs to a different project or environment.')
      let spec: Spec
      if (leaving) {
        const result = disconnectVirtualNetwork(current.spec, networkName, serviceName, localName)
        spec = result.spec
        setRemovedLocal(result.removedLocal)
      } else
        spec = connectVirtualNetwork(
          current.spec,
          currentNetwork.spec,
          segment,
          serviceName,
          localName,
        )
      const result = await unwrap(
        client.POST('/plan', { signal: controller.signal, body: { project, environment, spec } }),
      )
      if (result.application_id !== id || result.expected_revision !== current.revision)
        throw new Error(
          'The application changed during review. Your choices are kept. Review again against the latest revision.',
        )
      setPlan(result)
      idempotency.current = crypto.randomUUID()
    } catch (cause) {
      if (!controller.signal.aborted) setError(message(cause))
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  // Disconnecting has nothing to choose: check its impact straight away.
  const reviewed = useRef(false)
  useEffect(() => {
    if (!leaving || reviewed.current) return
    reviewed.current = true
    void review()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  async function deploy() {
    if (!plan) return
    setBusy(true)
    setError('')
    const controller = new AbortController()
    request.current = controller
    try {
      const result = await unwrap(
        client.POST('/deployments', {
          signal: controller.signal,
          body: {
            project,
            environment,
            spec: plan.spec,
            expected_revision: plan.expected_revision,
          },
          params: { header: { 'Idempotency-Key': idempotency.current } },
        }),
      )
      void cache.invalidateQueries({ queryKey: ['applications'] })
      void cache.invalidateQueries({ queryKey: ['application', plan.application_id] })
      void cache.invalidateQueries({
        queryKey: ['virtual-network', project, environment, networkName],
      })
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: result.id } })
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(message(cause))
        if (cause instanceof APIError && cause.status === 409) setPlan(null)
      }
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  const target = `${appName} / ${serviceName}`
  const nextRevision = plan ? `r${plan.expected_revision + 1}` : ''
  if (disconnect && !leaving)
    return (
      <FormPage
        title="Connection not found"
        breadcrumbs={[{ label: 'Virtual networks', to: '/networks' }]}
      >
        <ResourceEmpty
          icon="network"
          title={`This service is not connected to ${networkName}`}
          description="It may already be disconnected. The network page shows the current connections."
          action={<Button onClick={close}>Back to {networkName}</Button>}
        />
      </FormPage>
    )
  return (
    <FormPage
      title={
        leaving
          ? `Disconnect ${target}`
          : plan
            ? `Review connection of ${target}`
            : `Connect a service to ${networkName}`
      }
      description="A connection is part of the application’s configuration, so connecting or disconnecting deploys a new application revision."
      icon="network"
      breadcrumbs={[
        { label: 'Virtual networks', to: '/networks' },
        { label: networkName, to: `/networks/${networkName}` },
        { label: leaving ? 'Disconnect service' : 'Connect service' },
      ]}
    >
      <div className="network-flow-context">
        <ScopeLabel project={project} environment={environment} />
        {!leaving && (
          <FlowSteps
            steps={['Service', 'Review']}
            current={plan ? 1 : 0}
            disabled={busy}
            onSelect={() => setPlan(null)}
          />
        )}
      </div>
      <div className="form-body create-flow virtual-network-form">
        {plan || leaving ? (
          busy && !plan ? (
            <Loading rows={3} />
          ) : (
            plan && (
              <>
                <ReviewFacts
                  label="Connection"
                  items={[
                    ['Service', <strong className="mono">{target}</strong>],
                    ['Network segment', <span className="mono">{networkName} / {segment}</span>],
                    [
                      'Application revision',
                      <span className="mono">
                        r{plan.expected_revision} → {nextRevision}
                      </span>,
                    ],
                    ['Local network name', <span className="mono">{localName}</span>],
                  ]}
                />
                <section className="create-section" aria-labelledby="network-reach-heading">
                  <div className="create-section-heading">
                    <h2 id="network-reach-heading">
                      {leaving ? 'Reach lost after deployment' : 'Reach after deployment'}
                    </h2>
                    <span>Segment {segment}</span>
                  </div>
                  {peers.length ? (
                    <>
                      <p className="network-flow-lead">
                        {leaving
                          ? `${serviceName} and these services can no longer reach each other over ${segment}:`
                          : `${serviceName} and these services can reach each other over ${segment}:`}
                      </p>
                      <ul className="network-chip-list" aria-label="Services in this segment">
                        {peers.map((peer) => (
                          <li
                            key={`${peer.application_id}:${peer.service}`}
                            className="label-chip"
                          >
                            {peer.application} / {peer.service}
                          </li>
                        ))}
                      </ul>
                    </>
                  ) : (
                    <p className="network-flow-lead">
                      {leaving
                        ? `No other service is connected to ${segment}, so no private path is lost.`
                        : `No other service is connected to ${segment} yet. ${serviceName} can reach services that connect later.`}
                    </p>
                  )}
                </section>
                <Consequences>
                  <Consequence>
                    Deploys {appName} {nextRevision}. {serviceName} is redeployed with{' '}
                    {leaving ? `${localName} removed from` : `${localName} added to`} its networks.
                  </Consequence>
                  {leaving ? (
                    <>
                      <Consequence tone="warning">
                        {serviceName} loses its private address <code>{leaving.address}</code>.
                        Services calling it at that address will fail to connect.
                      </Consequence>
                      {removedLocal && (
                        <Consequence>
                          No other service of {appName} uses {localName}, so that local network is
                          removed from the application configuration too.
                        </Consequence>
                      )}
                    </>
                  ) : (
                    <Consequence>
                      Other networks, internet access and peer restrictions of {serviceName} stay as
                      configured.
                    </Consequence>
                  )}
                  {plan.warnings?.map((warning, index) => (
                    <Consequence key={index} tone="warning">
                      {warning}
                    </Consequence>
                  ))}
                </Consequences>
                <details className="virtual-network-toml">
                  <summary>All configuration changes ({plan.changes.length})</summary>
                  <DiffTable changes={plan.changes} />
                </details>
              </>
            )
          )
        ) : (
          <>
            {segments.length > 1 ? (
              <section className="create-section" aria-labelledby="network-segment-heading">
                <div className="create-section-heading">
                  <h2 id="network-segment-heading">Segment</h2>
                </div>
                <ChoiceCards
                  label="Segment"
                  compact
                  value={segment}
                  disabled={busy}
                  onChange={(value) => {
                    setSegment(value)
                    setApplicationId('')
                    setServiceName('')
                    setLocalName(suggestedName(networkName, value))
                  }}
                  choices={segments.map((name) => {
                    const connected = network.connections.filter((item) => item.segment === name)
                    return {
                      value: name,
                      title: name,
                      description: `${plural(network.spec.segments[name].applications.length, 'application')} allowed · ${plural(connected.length, 'service')} connected`,
                    }
                  })}
                />
              </section>
            ) : (
              <p className="network-flow-lead">
                Joins segment <code>{segment}</code>, the only segment in {networkName}.
              </p>
            )}
            <section className="create-section" aria-labelledby="network-service-heading">
              <div className="create-section-heading">
                <h2 id="network-service-heading">Service</h2>
                <span>Only applications allowed in {segment} are listed</span>
              </div>
              <div className="create-fields network-connect-fields">
                <label>
                  Application
                  <SelectField
                    label="Application"
                    value={chosenId}
                    disabled={busy || candidates.isPending || !choices.length}
                    onValueChange={(value) => {
                      setApplicationId(value)
                      setServiceName('')
                    }}
                    options={[
                      {
                        value: '',
                        label: candidates.isPending
                          ? 'Loading applications…'
                          : 'Choose an allowed application',
                      },
                      ...choices.map((item) => ({ value: item.id, label: item.name })),
                    ]}
                  />
                </label>
                <label>
                  Service
                  <SelectField
                    label="Service"
                    value={serviceName}
                    disabled={busy || !selected || application.isPending}
                    onValueChange={setServiceName}
                    options={[
                      { value: '', label: 'Choose a service' },
                      ...services.map((name) => ({ value: name, label: name })),
                    ]}
                  />
                </label>
              </div>
              {candidates.error && (
                <ErrorState error={candidates.error} retry={() => void candidates.refetch()} />
              )}
              {candidates.data && !choices.length && (
                <Note>
                  No deployed application you can change is allowed in {segment}
                  {allowed.length ? ` (allowed: ${allowed.join(', ')})` : ''}. A{' '}
                  {dashboardEdition.cloud ? 'workspace owner' : 'project administrator'} can grant
                  an application in the network’s configuration.
                </Note>
              )}
              {application.error && selected && (
                <ErrorState error={application.error} retry={() => void application.refetch()} />
              )}
              {service && (
                <div className="network-connection-context">
                  <span>
                    {serviceName} is on {(service.networks || ['default']).join(', ')} now.
                    {service.network_access
                      ? ' Its peer restrictions still apply after connecting.'
                      : ''}
                  </span>
                  <Link
                    to="/applications/$applicationId"
                    params={{ applicationId: chosenId }}
                    search={{ service: serviceName }}
                  >
                    Open {serviceName}
                    <Icon name="arrow" size={13} />
                  </Link>
                </div>
              )}
              <details className="network-advanced">
                <summary>Local network name: {localName}</summary>
                <label>
                  Local network name
                  <Input
                    value={localName}
                    disabled={busy}
                    maxLength={40}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => setLocalName(event.target.value)}
                    aria-describedby="network-local-help"
                  />
                  <span id="network-local-help" className="field-help">
                    The name this application’s TOML uses for the shared segment. Change it only if
                    the application already uses this name for another network.
                  </span>
                </label>
              </details>
            </section>
          </>
        )}
        {error && <RequestError error={error} />}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {plan ? 'Deploys only the reviewed revision' : 'Nothing changes until you deploy'}
        </span>
        <div className="deploy-footer-actions">
          <Button disabled={busy} onClick={() => (plan && !leaving ? setPlan(null) : close())}>
            {plan && !leaving ? 'Back' : 'Cancel'}
          </Button>
          {leaving && !plan && error ? (
            <PendingButton pending={busy} pendingLabel="Checking…" onClick={() => void review()}>
              Check again
            </PendingButton>
          ) : (
            <PendingButton
              variant="primary"
              pending={busy}
              pendingLabel={plan ? 'Deploying…' : 'Checking…'}
              disabled={
                !plan &&
                (Boolean(leaving) ||
                  !selected ||
                  !serviceName ||
                  !localName ||
                  application.isPending ||
                  Boolean(application.error))
              }
              onClick={() => void (plan ? deploy() : review())}
            >
              <span className="network-primary-label">
                {plan
                  ? `Deploy ${appName} ${nextRevision} to ${leaving ? 'disconnect' : 'connect'} ${serviceName}`
                  : 'Review connection'}
              </span>
              <Icon name="arrow" size={15} />
            </PendingButton>
          )}
        </div>
      </div>
    </FormPage>
  )
}
