import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { Plan } from '../lib/types'
import { connectVirtualNetwork, type VirtualNetworkDetail } from '../lib/virtual-networks'
import { DiffTable } from './deploy-dialog'
import { FormHint, FormPage, FormSection } from './form-page'
import { Icon } from './icons'
import { ErrorState, Loading, Note } from './shared'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { dashboardEdition } from '../lib/dashboard-edition'

function suggestedName(network: string, segment: string) {
  return `${network.slice(0, 20)}-${segment.slice(0, 19)}`.replace(/-+$/, '')
}

export function VirtualNetworkConnect({
  project,
  environment,
  network,
}: {
  project: string
  environment: string
  network: VirtualNetworkDetail
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const networkName = network.network.name
  const segments = Object.keys(network.spec.segments)
  const [segment, setSegment] = useState(segments[0] || '')
  const [applicationId, setApplicationId] = useState('')
  const [serviceName, setServiceName] = useState('')
  const [localName, setLocalName] = useState(() =>
    suggestedName(networkName, segments[0] || 'shared'),
  )
  const [plan, setPlan] = useState<Plan | null>(null)
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
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  const allowed = network.spec.segments[segment]?.applications || []
  const choices = (candidates.data?.items || []).filter((application) =>
    allowed.includes(application.name),
  )
  const selected = choices.find((application) => application.id === applicationId)
  const application = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    enabled: Boolean(selected),
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  const services = application.data
    ? Object.keys(application.data.spec.services)
    : selected?.services || []
  const service = application.data?.spec.services[serviceName]
  const close = () =>
    void navigate({ to: '/networks/$networkName', params: { networkName }, search: {} })
  async function review() {
    if (!selected || !serviceName) return
    setBusy(true)
    setError('')
    const controller = new AbortController()
    request.current = controller
    try {
      const [current, currentNetwork] = await Promise.all([
        unwrap(
          client.GET('/applications/{id}', {
            signal: controller.signal,
            params: { path: { id: applicationId } },
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
      const spec = connectVirtualNetwork(
        current.spec,
        currentNetwork.spec,
        segment,
        serviceName,
        localName,
      )
      const result = await unwrap(
        client.POST('/plan', { signal: controller.signal, body: { project, environment, spec } }),
      )
      if (result.application_id !== applicationId || result.expected_revision !== current.revision)
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
      void cache.invalidateQueries({ queryKey: ['application', applicationId] })
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
  return (
    <FormPage
      title={plan ? 'Review service connection' : `Connect to ${networkName}`}
      description={`${project} / ${environment} · Connect an existing service to an allowed segment.`}
      icon="network"
      breadcrumbs={[
        { label: 'Virtual networks', to: '/networks' },
        { label: networkName, to: `/networks/${networkName}` },
        { label: 'Connect service' },
      ]}
      help={
        <>
          <FormHint title="Keep existing connections">
            This adds one network membership. Existing networks remain attached, including default
            internet access where it is already allowed.
          </FormHint>
          <FormHint title="Peer restrictions still apply">
            The service’s existing peer permissions remain unchanged. Grant access to the
            application in this segment, then use service networking settings for any additional
            restrictions.
          </FormHint>
        </>
      }
    >
      <div className="form-body virtual-network-form">
        {plan ? (
          <>
            <div className="review-summary">
              <div>
                <span className="muted-text">SERVICE</span>
                <strong>
                  {plan.spec.name} / {serviceName}
                </strong>
              </div>
              <div>
                <span className="muted-text">SEGMENT</span>
                <strong>
                  {networkName} / {segment}
                </strong>
              </div>
              <div>
                <span className="muted-text">REVISION</span>
                <strong className="mono">
                  r{plan.expected_revision} → r{plan.expected_revision + 1}
                </strong>
              </div>
            </div>
            <Note>
              This adds the local network <strong>{localName}</strong> to{' '}
              <strong>{serviceName}</strong>. Existing networks, internet access, and peer
              restrictions remain configured as before. The application-wide plan below includes
              every recorded change.
            </Note>
            <DiffTable changes={plan.changes} />
            {plan.warnings?.map((warning, index) => (
              <Note key={index}>{warning}</Note>
            ))}
          </>
        ) : (
          <FormSection
            title="Service connection"
            description="Only applications granted access to the selected segment appear here."
            icon="network"
          >
            <div className="network-connect-fields">
              <label className="field-stack">
                Segment
                <SelectField
                  label="Segment"
                  value={segment}
                  disabled={busy}
                  onValueChange={(value) => {
                    setSegment(value)
                    setApplicationId('')
                    setServiceName('')
                    setLocalName(suggestedName(networkName, value))
                  }}
                  options={segments.map((name) => ({
                    value: name,
                    label: `${name} · ${network.spec.segments[name].applications.length} allowed applications`,
                  }))}
                />
              </label>
              <label className="field-stack">
                Application
                <SelectField
                  label="Application"
                  value={applicationId}
                  disabled={busy || candidates.isPending || !choices.length}
                  onValueChange={(value) => {
                    setApplicationId(value)
                    setServiceName('')
                  }}
                  options={[
                    { value: '', label: 'Choose an allowed application' },
                    ...choices.map((application) => ({
                      value: application.id,
                      label: application.name,
                    })),
                  ]}
                />
              </label>
              <label className="field-stack">
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
              <label className="field-stack">
                Local network name
                <Input
                  value={localName}
                  disabled={busy}
                  maxLength={40}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(event) => setLocalName(event.target.value)}
                />
                <span className="field-help">
                  The name used in this application’s TOML. Existing networks cannot be replaced
                  here.
                </span>
              </label>
            </div>
            {candidates.isPending && <Loading rows={1} />}
            {candidates.error && (
              <ErrorState error={candidates.error} retry={() => void candidates.refetch()} />
            )}
            {candidates.data && !choices.length && (
              <Note>
                No existing application you can deploy is allowed in this segment. A{' '}
                {dashboardEdition.cloud ? 'workspace owner' : 'project administrator'} can grant an
                application name in network configuration.
              </Note>
            )}
            {application.error && selected && (
              <ErrorState error={application.error} retry={() => void application.refetch()} />
            )}
            {service && (
              <div className="network-connection-context">
                <p>
                  <strong>Existing networks:</strong> {(service.networks || ['default']).join(', ')}
                </p>
                {service.network_access && (
                  <p>This service has peer restrictions. They are preserved when connecting.</p>
                )}
                <Link
                  to="/applications/$applicationId"
                  params={{ applicationId }}
                  search={{ service: serviceName, tab: 'network' }}
                >
                  Inspect service networking
                  <Icon name="arrow" size={13} />
                </Link>
              </div>
            )}
          </FormSection>
        )}
        {error && (
          <div className="inline-error" role="alert">
            {error}
          </div>
        )}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />A connection creates an application revision
        </span>
        <div className="deploy-footer-actions">
          <Button disabled={busy} onClick={() => (plan ? setPlan(null) : close())}>
            {plan ? 'Back to connection' : 'Cancel'}
          </Button>
          <Button
            variant="primary"
            disabled={
              busy ||
              (!plan &&
                (!selected ||
                  !serviceName ||
                  !localName ||
                  application.isPending ||
                  Boolean(application.error)))
            }
            onClick={() => void (plan ? deploy() : review())}
          >
            {busy
              ? plan
                ? 'Submitting…'
                : 'Validating…'
              : plan
                ? 'Deploy connection'
                : 'Review connection'}
            <Icon name="arrow" size={15} />
          </Button>
        </div>
      </div>
    </FormPage>
  )
}
