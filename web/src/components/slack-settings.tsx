import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { components } from '../lib/api.generated'
import { Link, useLocation, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { APIError, message, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useLicense } from '../lib/license'
import { useScope } from '../lib/scope'
import { dashboardEdition, useEditionWorkspace } from '../lib/dashboard-edition'
import { Icon } from './icons'
import { SlackLogo } from './slack-logo'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Badge, Card } from './ui/surfaces'
import { FormPage, FormHint, FormSection } from './form-page'
import { InstallationReviewRows, useInstallationFormFocus } from './installation-form-fields'
import { Copy, Empty, ErrorState, HeadingHelp, Loading, Note, RequestError, Status } from './shared'

type SlackEvent = string
type SlackChannel = { id: string; name: string; is_private?: boolean }
type SlackChannelPage = { items: SlackChannel[]; next_before?: string }
type SlackDelivery = components['schemas']['SlackDelivery']
type SlackEventCatalogEntry = {
  id: string
  category: string
  label: string
  description: string
}
type SlackIntegration = {
  mode: 'cloud' | 'self_hosted'
  available: boolean
  configured: boolean
  setup_available: boolean
  reason?: string
  team?: { id: string; name: string }
  channel?: SlackChannel
  events: SlackEvent[]
  event_catalog?: SlackEventCatalogEntry[]
  revision: number
  manifest?: Record<string, unknown>
}

const fallbackEventLabels: Record<string, string> = {
  'alarm.opened': 'Alarm opened',
  'alarm.resolved': 'Alarm resolved',
  audit: 'Audit event',
}

function eventLabel(event: string, catalog: SlackEventCatalogEntry[] = []) {
  return catalog.find((candidate) => candidate.id === event)?.label || fallbackEventLabels[event] || event
}

function useSlackIntegration() {
  const workspace = useEditionWorkspace()
  return useQuery({
    queryKey: ['slack-integration', workspace.id || 'installation'],
    queryFn: ({ signal }) => unwrap(client.GET('/integrations/slack', { signal })),
    enabled: !dashboardEdition.cloud || Boolean(workspace.id),
    staleTime: 30000,
    refetchOnWindowFocus: false,
  })
}

function useSlackDeliveries(enabled: boolean) {
  const workspace = useEditionWorkspace()
  return useQuery({
    queryKey: ['slack-deliveries', workspace.id || 'installation'],
    queryFn: ({ signal }) => unwrap(client.GET('/integrations/slack/deliveries', { signal })),
    enabled,
    staleTime: 15000,
    refetchOnWindowFocus: false,
  })
}

function useSlackAccess() {
  const scope = useScope()
  const workspace = useEditionWorkspace()
  return scope.identity.admin || (dashboardEdition.cloud && workspace.canManageSlack)
}

function AccessRequired() {
  if (useSlackAccess()) return null
  return (
    <Empty
      icon="lock"
      title={
        dashboardEdition.cloud ? 'Workspace owner access required' : 'Administrator access required'
      }
      description={
        dashboardEdition.cloud
          ? 'The selected workspace owner connects Slack and chooses notification delivery.'
          : 'An installation administrator connects Slack and chooses notification delivery.'
      }
    />
  )
}

function useSlackEntitlement() {
  // The API is the source of enforcement; this only lets the dashboard name a Pro denial clearly.
  const license = useLicense()
  return {
    license,
    enabled: Boolean(
      license.data?.catalog.find((item) => item.id === 'slack_notifications')?.enabled,
    ),
  }
}

function InstallationSlackUnavailable({ state }: { state: SlackIntegration }) {
  const { license, enabled } = useSlackEntitlement()
  if (!license.isPending && !license.error && !enabled)
    return (
      <Card className="feature-lock slack-feature-lock">
        <div className="feature-lock-symbol">
          <Icon name="lock" size={23} />
        </div>
        <div>
          <h2>Slack requires Hakopod Pro</h2>
          <p>
            Activate Hakopod Pro on this installation to connect Slack and receive notifications.
          </p>
        </div>
        <Button asChild className="slack-feature-lock-action" variant="outline" size="sm">
          <Link to="/settings" search={{ tab: 'license' }}>
            View license
          </Link>
        </Button>
      </Card>
    )
  return (
    <Note>
      {state.reason
        ? `Slack notifications are unavailable: ${state.reason.replaceAll('_', ' ')}.`
        : 'Slack notifications are not available for this installation. Check the active Pro license and Slack setup.'}
    </Note>
  )
}

function CloudSlackUnavailable({ state }: { state: SlackIntegration }) {
  const proRequired = state.reason === 'license_required' || state.reason === 'pro_required'
  if (!proRequired)
    return (
      <Note>
        {state.reason === 'operator_configuration_required'
          ? 'Slack notifications are waiting for Cloud operator configuration.'
          : state.reason === 'node_upgrade_required'
            ? 'Slack notifications need an upgrade on this workspace node.'
            : state.reason === 'relay_configuration_required'
              ? 'Slack notifications need this workspace node’s relay configuration.'
              : 'Slack notifications are not available for this workspace yet.'}
      </Note>
    )
  return (
    <Card className="feature-lock slack-feature-lock">
      <div className="feature-lock-symbol">
        <Icon name="lock" size={23} />
      </div>
      <div>
        <h2>Slack requires Cloud Pro</h2>
        <p role="status">This workspace needs an active Hakopod Cloud Pro plan.</p>
      </div>
      <Button asChild className="slack-feature-lock-action" variant="primary" size="sm">
        <a href="/workspaces/plans">View Cloud plans</a>
      </Button>
    </Card>
  )
}

function hasSearchMarker(value: unknown) {
  return value === true || value === 1 || value === '1' || value === 'true'
}

function SlackUnavailable({ state }: { state: SlackIntegration }) {
  return dashboardEdition.cloud ? (
    <CloudSlackUnavailable state={state} />
  ) : (
    <InstallationSlackUnavailable state={state} />
  )
}

export function SlackSettingsPanel() {
  const allowed = useSlackAccess()
  const query = useSlackIntegration()
  if (!allowed)
    return (
      <section className="grid gap-3" aria-label="Available integrations">
        <IntegrationCatalogGrid>
          <IntegrationCatalogCard
            name="Slack"
            badge={<Badge tone="accent">PRO</Badge>}
            description="Send selected Hakopod events to one Slack channel."
          >
            <Note>
              {dashboardEdition.cloud
                ? 'A workspace owner can connect Slack and choose notification delivery.'
                : 'An installation administrator can connect Slack and choose notification delivery.'}
            </Note>
          </IntegrationCatalogCard>
        </IntegrationCatalogGrid>
      </section>
    )
  if (query.isPending) return <Loading />
  if (query.error || !query.data)
    return <ErrorState error={query.error} retry={() => void query.refetch()} />
  const state = query.data as SlackIntegration
  return <SlackCatalogEntry state={state} />
}

function IntegrationCatalogGrid({ children }: { children: ReactNode }) {
  return <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">{children}</div>
}

function IntegrationCatalogCard({
  name,
  description,
  badge,
  children,
}: {
  name: string
  description: string
  badge?: ReactNode
  children: ReactNode
}) {
  return (
    <Card className="grid min-w-0 content-start gap-3 p-4">
      <div className="flex min-w-0 items-start gap-3">
        <SlackLogo className="size-9 shrink-0" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="min-w-0 text-base font-medium [overflow-wrap:anywhere]">{name}</h2>
            {badge}
          </div>
          <p className="muted-text m-0 mt-1 text-sm">{description}</p>
        </div>
      </div>
      {children}
    </Card>
  )
}

function SlackCatalogEntry({ state }: { state: SlackIntegration }) {
  const unavailable = !state.available
  const connection = state.configured
    ? `${state.team?.name || 'Slack workspace'}${state.channel ? ` · #${state.channel.name}` : ''}`
    : unavailable
      ? 'Unavailable'
      : 'Not connected'
  return (
    <section className="grid gap-3" aria-label="Available integrations">
      <IntegrationCatalogGrid>
        <IntegrationCatalogCard
          name="Slack"
          badge={<Badge tone="accent">PRO</Badge>}
          description="Send selected Hakopod events to one Slack channel."
        >
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
            <Status
              value={unavailable ? 'unavailable' : state.configured ? 'connected' : 'not connected'}
              small
            />
            <span className="min-w-0 [overflow-wrap:anywhere]">{connection}</span>
          </div>
          <div>
            <Button asChild size="sm" variant="primary">
              <Link to="/settings/integrations/slack" search={{ configure: undefined, connected: undefined }}>
                {state.configured ? 'Manage' : 'Set up Slack'}
              </Link>
            </Button>
          </div>
        </IntegrationCatalogCard>
      </IntegrationCatalogGrid>
      {unavailable && <SlackUnavailable state={state} />}
    </section>
  )
}

function SlackOverview({ state }: { state: SlackIntegration }) {
  const cache = useQueryClient()
  const navigate = useNavigate()
  const workspace = useEditionWorkspace()
  const deliveries = useSlackDeliveries(state.available && state.configured)
  const [busy, setBusy] = useState(false)
  const [testStatus, setTestStatus] = useState('')
  const [error, setError] = useState('')
  const [disconnecting, setDisconnecting] = useState(false)
  const eventCatalog = state.event_catalog || []
  const refresh = () => {
    const scope = workspace.id || 'installation'
    void cache.invalidateQueries({ queryKey: ['slack-integration', scope] })
    void cache.invalidateQueries({ queryKey: ['slack-deliveries', scope] })
    if (workspace.id) void cache.invalidateQueries({ queryKey: ['cloud-workspace', workspace.id] })
  }
  const startConnect = async () => {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(client.POST('/integrations/slack/connect', { body: {} }))
      const target = new URL(result.authorization_url)
      if (
        target.origin !== 'https://slack.com' ||
        target.pathname !== '/oauth/v2/authorize' ||
        target.username ||
        target.password
      )
        throw new Error('Slack returned an unsupported authorization address.')
      window.location.assign(target.href)
    } catch (cause) {
      setError(message(cause))
      setBusy(false)
    }
  }
  const test = async () => {
    if (busy) return
    setBusy(true)
    setError('')
    setTestStatus('')
    try {
      const result = await unwrap(
        client.POST('/integrations/slack/test', { body: { expected_revision: state.revision } }),
      )
      setTestStatus(`Test delivery ${result.id} is ${result.status}.`)
      refresh()
    } catch (cause) {
      setError(message(cause))
      if (cause instanceof APIError && (cause.status === 403 || cause.status === 409)) refresh()
    } finally {
      setBusy(false)
    }
  }
  const connectionRows: [string, ReactNode][] = [
    ['Workspace', state.team?.name || 'Connected workspace'],
    ['Channel', state.channel ? `#${state.channel.name}` : 'Choose a channel'],
    [
      'Events',
      state.events.length
        ? state.events.map((event) => eventLabel(event, eventCatalog)).join(', ')
        : 'None selected',
    ],
  ]
  const disconnectButton = (
    <Button
      type="button"
      size="sm"
      variant="ghost"
      disabled={busy}
      onClick={() => setDisconnecting(true)}
    >
      Disconnect
    </Button>
  )
  return (
    <div className="grid gap-4">
      <div className="section-toolbar">
        <div className="hako-section-heading-title">
          <h2 className="inline-flex items-center gap-2">
            <SlackLogo className="size-5 shrink-0" />
            Connection
          </h2>
          <HeadingHelp title="Slack connection">
            Send selected Hakopod events to one Slack channel. Slack credentials stay on the server.
          </HeadingHelp>
        </div>
        {state.available && (
          <Button asChild size="sm" variant="primary">
            <Link to="/settings/integrations/slack" search={{ configure: true, connected: undefined }}>
              {state.configured ? 'Configure' : 'Set up Slack'}
            </Link>
          </Button>
        )}
      </div>
      {!state.available ? (
        <>
          <SlackUnavailable state={state} />
          {state.configured && (
            <Card className="grid gap-4 p-4">
              <InstallationReviewRows rows={connectionRows} />
              <div className="flex flex-wrap gap-2">{disconnectButton}</div>
            </Card>
          )}
        </>
      ) : !state.configured ? (
        <Card className="grid gap-3 p-4">
          <div>
            <strong className="inline-flex items-center gap-2">
              <SlackLogo className="size-5 shrink-0" />
              {state.mode === 'cloud' ? 'Hakopod Cloud Slack app' : 'Your Slack app'}
            </strong>
            <p className="muted-text m-0 mt-1 text-sm">
              {state.mode === 'cloud'
                ? 'Authorize the published Hakopod Cloud app in the Slack workspace that should receive notifications.'
                : 'Create an app from the generated manifest, save its credentials, then authorize its Slack workspace.'}
            </p>
          </div>
          {state.mode === 'cloud' && (
            <div>
              <Button
                type="button"
                size="sm"
                variant="primary"
                disabled={busy || !state.setup_available}
                onClick={() => void startConnect()}
              >
                {busy ? 'Opening Slack…' : 'Connect Slack'}
              </Button>
            </div>
          )}
          {!state.setup_available && <Note>Slack setup is not ready for this installation.</Note>}
        </Card>
      ) : (
        <Card className="grid gap-4 p-4">
          <InstallationReviewRows rows={connectionRows} />
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={busy || !state.channel}
              onClick={() => void test()}
            >
              {busy ? 'Requesting test…' : 'Send test'}
            </Button>
            {disconnectButton}
          </div>
          {testStatus && (
            <p role="status" className="m-0 text-sm">
              {testStatus}
            </p>
          )}
        </Card>
      )}
      {state.configured && deliveries.data && (
        <DeliveryList deliveries={deliveries.data.items} eventCatalog={eventCatalog} />
      )}
      {state.configured && deliveries.error && (
        <RequestError error={deliveries.error} remember="slack-deliveries" />
      )}
      {error && <RequestError error={error} />}
      <Dialog
        open={disconnecting}
        onOpenChange={(open) => {
          if (!busy) setDisconnecting(open)
        }}
        title="Disconnect Slack?"
        description="Future selected Hakopod events will no longer be sent to this Slack workspace."
      >
        {error && <RequestError error={error} />}
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={() => setDisconnecting(false)}>
            Keep connected
          </Button>
          <Button
            type="button"
            variant="danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.DELETE('/integrations/slack', {
                    body: { expected_revision: state.revision },
                  }),
                )
                setDisconnecting(false)
                refresh()
                await navigate({ to: '/settings/integrations' })
              } catch (cause) {
                setError(message(cause))
                if (cause instanceof APIError && (cause.status === 403 || cause.status === 409))
                  refresh()
              } finally {
                setBusy(false)
              }
            }}
          >
            <Icon name="trash" size={15} />
            {busy ? 'Disconnecting…' : 'Disconnect Slack'}
          </Button>
        </div>
      </Dialog>
    </div>
  )
}

function DeliveryList({
  deliveries,
  eventCatalog,
}: {
  deliveries: SlackDelivery[]
  eventCatalog: SlackEventCatalogEntry[]
}) {
  if (deliveries.length === 0) return null
  return (
    <section className="grid gap-3" aria-labelledby="slack-deliveries-heading">
      <div className="hako-section-heading-title">
        <h2 id="slack-deliveries-heading">Recent deliveries</h2>
        <HeadingHelp title="Recent deliveries">
          Recent delivery attempts are shown here. A queued test has not yet been received by Slack.
        </HeadingHelp>
      </div>
      <div
        className="table-container focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-[var(--navigation-active)]"
        tabIndex={0}
        role="region"
        aria-labelledby="slack-deliveries-heading"
      >
        <table>
          <thead>
            <tr>
              <th>Event</th>
              <th>Status</th>
              <th>Created</th>
              <th>Details</th>
            </tr>
          </thead>
          <tbody>
            {deliveries.map((delivery) => (
              <tr key={delivery.id}>
                <td>
                  {delivery.event === 'test' ? 'Test delivery' : eventLabel(delivery.event, eventCatalog)}
                </td>
                <td>
                  <Status value={delivery.status} small />
                </td>
                <td>{timestamp(delivery.created_at)}</td>
                <td className="break-words">
                  {delivery.last_error ||
                    `${delivery.attempts} attempt${delivery.attempts === 1 ? '' : 's'}`}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

export function SlackIntegrationPage() {
  const denied = AccessRequired()
  const query = useSlackIntegration()
  const cache = useQueryClient()
  const workspace = useEditionWorkspace()
  const location = useLocation()
  const search = location.search as { configure?: unknown; connected?: unknown }
  const connected = hasSearchMarker(search.connected)
  const refreshedConnection = useRef(false)
  useEffect(() => {
    if (!connected || refreshedConnection.current) return
    refreshedConnection.current = true
    void query.refetch()
    void cache.invalidateQueries({ queryKey: ['slack-deliveries', workspace.id || 'installation'] })
    if (workspace.id) void cache.invalidateQueries({ queryKey: ['cloud-workspace', workspace.id] })
    window.history.replaceState(window.history.state, '', window.location.pathname)
  }, [cache, connected, query, workspace.id])
  if (denied)
    return (
      <FormPage
        title="Slack notifications"
        brandMark={<SlackLogo className="size-6 shrink-0" />}
        breadcrumbs={[]}
      >
        {denied}
      </FormPage>
    )
  if (query.isPending) return <Loading />
  if (query.error || !query.data)
    return <ErrorState error={query.error} retry={() => void query.refetch()} />
  const state = query.data as SlackIntegration
  const configuring = hasSearchMarker(search.configure)
  return !state.available && !state.configured ? (
    <FormPage
      title="Slack notifications"
      brandMark={<SlackLogo className="size-6 shrink-0" />}
      breadcrumbs={[]}
    >
      <SlackUnavailable state={state} />
    </FormPage>
  ) : !state.configured || (state.available && configuring) ? (
    <SlackConfiguration state={state} />
  ) : (
    <FormPage
      title="Slack notifications"
      brandMark={<SlackLogo className="size-6 shrink-0" />}
      breadcrumbs={[]}
    >
      <SlackOverview state={state} />
    </FormPage>
  )
}

function SlackEventSelector({
  catalog,
  events,
  search,
  unknownEvents,
  onSearchChange,
  onToggle,
  onSelectEvents,
  onClearEvents,
}: {
  catalog: SlackEventCatalogEntry[]
  events: SlackEvent[]
  search: string
  unknownEvents: SlackEvent[]
  onSearchChange: (value: string) => void
  onToggle: (event: SlackEvent, checked: boolean) => void
  onSelectEvents: (events: SlackEvent[]) => void
  onClearEvents: (events: SlackEvent[]) => void
}) {
  const normalizedSearch = search.trim().toLowerCase()
  const visibleEvents = catalog.filter((event) => {
    if (!normalizedSearch) return true
    return [event.category, event.id, event.label, event.description].some((value) =>
      value.toLowerCase().includes(normalizedSearch),
    )
  })
  const groups = new Map<string, SlackEventCatalogEntry[]>()
  for (const event of visibleEvents) {
    const group = groups.get(event.category) || []
    group.push(event)
    groups.set(event.category, group)
  }
  return (
    <div className="grid gap-4">
      <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
        <label className="grid gap-1" htmlFor="slack-event-search">
          <span className="text-sm font-medium">Find an event</span>
          <Input
            id="slack-event-search"
            value={search}
            onChange={(event) => onSearchChange(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') event.preventDefault()
            }}
            placeholder="Search deployments, applications, or services"
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        {search && (
          <Button type="button" size="sm" variant="outline" onClick={() => onSearchChange('')}>
            Clear search
          </Button>
        )}
      </div>
      <p className="field-help m-0" role="status">
        {visibleEvents.length} of {catalog.length} events shown. {events.length} of 64 selected.
      </p>
      {unknownEvents.length > 0 && (
        <section className="grid gap-2" aria-labelledby="unavailable-saved-events-heading">
          <div>
            <h3 id="unavailable-saved-events-heading" className="m-0 text-sm font-medium">
              Unavailable saved events
            </h3>
            <p className="field-help m-0 mt-1">
              These saved events are not in the current catalog. Clear them before saving any
              Slack changes.
            </p>
          </div>
          <div className="grid gap-2">
            {unknownEvents.map((event, index) => (
              <label
                className="grid min-h-11 grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 rounded-sm px-1 py-2 focus-within:outline focus-within:outline-2 focus-within:outline-offset-2"
                key={`${event}-${index}`}
              >
                <Input
                  type="checkbox"
                  checked
                  onChange={(value) => onToggle(event, value.target.checked)}
                />
                <span className="min-w-0">
                  <span className="block break-words text-sm font-medium">{eventLabel(event)}</span>
                  <span className="muted-text mt-1 block break-words text-sm">{event}</span>
                </span>
              </label>
            ))}
          </div>
        </section>
      )}
      {groups.size === 0 ? (
        <p className="muted-text m-0 text-sm">No notification events match this search.</p>
      ) : (
        [...groups.entries()].map(([category, group]) => {
          const selected = group.filter((event) => events.includes(event.id)).length
          const groupIDs = group.map((event) => event.id)
          const filtered = Boolean(normalizedSearch)
          return (
            <section className="grid gap-2" key={category} aria-label={category}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h3 className="m-0 text-sm font-medium">{category}</h3>
                <div className="flex flex-wrap gap-2">
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={selected === group.length}
                    onClick={() => onSelectEvents(groupIDs)}
                  >
                    {filtered ? 'Select shown' : 'Select group'}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={selected === 0}
                    onClick={() => onClearEvents(groupIDs)}
                  >
                    {filtered ? 'Clear shown' : 'Clear group'}
                  </Button>
                </div>
              </div>
              <div className="grid gap-1">
                {group.map((event) => (
                  <label
                    className="grid min-h-11 grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 rounded-sm px-1 py-2 focus-within:outline focus-within:outline-2 focus-within:outline-offset-2"
                    key={event.id}
                  >
                    <Input
                      type="checkbox"
                      checked={events.includes(event.id)}
                      onChange={(value) => onToggle(event.id, value.target.checked)}
                    />
                    <span className="min-w-0">
                      <span className="block break-words text-sm font-medium">{event.label}</span>
                      <span className="muted-text mt-1 block break-words text-sm">
                        {event.description}
                      </span>
                    </span>
                  </label>
                ))}
              </div>
            </section>
          )
        })
      )}
    </div>
  )
}

function SelectedSlackEvents({ groups }: { groups: [string, SlackEventCatalogEntry[]][] }) {
  return (
    <div className="grid gap-3">
      <h3 className="m-0 text-sm font-medium">Selected notification events</h3>
      {groups.map(([category, events]) => (
        <section className="grid gap-2" key={category}>
          <h4 className="muted-text m-0 text-sm font-medium">{category}</h4>
          <ul className="m-0 grid list-none gap-2 p-0">
            {events.map((event) => (
              <li className="grid gap-1 text-sm" key={event.id}>
                <span className="break-words font-medium">{event.label}</span>
                <span className="muted-text break-words">{event.description}</span>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  )
}

function SlackConfiguration({ state }: { state: SlackIntegration }) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const workspace = useEditionWorkspace()
  const [clientID, setClientID] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [channelID, setChannelID] = useState(state.channel?.id || '')
  const [events, setEvents] = useState<SlackEvent[]>(state.events)
  const [eventSearch, setEventSearch] = useState('')
  const [review, setReview] = useState(false)
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [additionalChannelPages, setAdditionalChannelPages] = useState<SlackChannelPage[]>([])
  const [loadingMoreChannels, setLoadingMoreChannels] = useState(false)
  const [channelLoadError, setChannelLoadError] = useState('')
  const [manualChannelID, setManualChannelID] = useState('')
  const dirty = useRef(false)
  const initialized = useRef(false)
  const latestState = useRef(state)
  const formRef = useInstallationFormFocus(review)
  const channels = useQuery({
    queryKey: ['slack-channels', workspace.id || 'installation'],
    queryFn: ({ signal }) => unwrap(client.GET('/integrations/slack/channels', { signal })),
    enabled: state.configured,
    staleTime: 30000,
  })
  const listedChannels = useMemo(() => {
    const seen = new Set<string>()
    return [...(channels.data?.items || []), ...additionalChannelPages.flatMap((page) => page.items)].filter(
      (channel) => {
        if (seen.has(channel.id)) return false
        seen.add(channel.id)
        return true
      },
    )
  }, [additionalChannelPages, channels.data?.items])
  const channelPageCount = (channels.data ? 1 : 0) + additionalChannelPages.length
  const nextChannelBefore = additionalChannelPages.length
    ? additionalChannelPages.at(-1)?.next_before
    : channels.data?.next_before
  const canLoadMoreChannels = Boolean(nextChannelBefore) && channelPageCount < 10
  const loadMoreChannels = async () => {
    if (!nextChannelBefore || loadingMoreChannels || channelPageCount >= 10) return
    setLoadingMoreChannels(true)
    setChannelLoadError('')
    try {
      const page = await unwrap(
        client.GET('/integrations/slack/channels', {
          params: { query: { before: nextChannelBefore } },
        }),
      )
      setAdditionalChannelPages((pages) => [...pages, page])
    } catch (cause) {
      setChannelLoadError(message(cause))
    } finally {
      setLoadingMoreChannels(false)
    }
  }
  const eventCatalog = useMemo(() => {
    const entries = state.event_catalog || []
    const seen = new Set<string>()
    return entries.filter((entry) => {
      if (!entry.id || seen.has(entry.id)) return false
      seen.add(entry.id)
      return true
    })
  }, [state.event_catalog])
  const catalogIDs = useMemo(() => new Set(eventCatalog.map((event) => event.id)), [eventCatalog])
  const catalogAvailable = eventCatalog.length > 0
  const unknownEvents = useMemo(
    () => events.filter((event) => !catalogIDs.has(event)),
    [catalogIDs, events],
  )
  const selectedEventGroups = useMemo(() => {
    const groups = new Map<string, SlackEventCatalogEntry[]>()
    for (const event of events) {
      const entry = eventCatalog.find((candidate) => candidate.id === event)
      const category = entry?.category || 'Unavailable saved events'
      const items = groups.get(category) || []
      items.push(
        entry || {
          id: event,
          category,
          label: eventLabel(event),
          description:
            'This saved event is not available in the current catalog. Clear it before saving.',
        },
      )
      groups.set(category, items)
    }
    return [...groups.entries()]
  }, [eventCatalog, events])
  const chosenChannel = useMemo(
    () => listedChannels.find((item) => item.id === channelID),
    [channelID, listedChannels],
  )
  const channelOptions = useMemo(() => {
    const listed = listedChannels
    const selected = state.channel
    const fallback =
      channelID && !listed.some((item) => item.id === channelID)
        ? [
            {
              value: channelID,
              label:
                selected?.id === channelID
                  ? `Selected channel · #${selected.name}`
                  : `Channel ID · ${channelID}`,
            },
          ]
        : []
    return [
      { value: '', label: channels.isPending ? 'Loading channels…' : 'Choose a channel' },
      ...fallback,
      ...listed.map((channel) => ({
        value: channel.id,
        label: `${channel.is_private ? 'Private' : 'Channel'} · #${channel.name}`,
      })),
    ]
  }, [channelID, channels.isPending, listedChannels, state.channel])
  useEffect(() => {
    setAdditionalChannelPages([])
    setChannelLoadError('')
  }, [workspace.id])
  useEffect(() => {
    if (initialized.current && dirty.current) return
    latestState.current = state
    setChannelID(state.channel?.id || '')
    setEvents(state.events)
    initialized.current = true
  }, [state.channel?.id, state.events])
  const connect = async () => {
    const cloud = state.mode === 'cloud'
    if (busy || (!cloud && (!clientID.trim() || !clientSecret))) return
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const result = cloud
        ? await unwrap(client.POST('/integrations/slack/connect', { body: {} }))
        : await unwrap(
            client.POST('/integrations/slack/connect', {
              body: { client_id: clientID.trim(), client_secret: clientSecret },
            }),
          )
      const target = new URL(result.authorization_url)
      if (
        target.origin !== 'https://slack.com' ||
        target.pathname !== '/oauth/v2/authorize' ||
        target.username ||
        target.password
      )
        throw new Error('Slack returned an unsupported authorization address.')
      window.location.assign(target.href)
    } catch (cause) {
      setError(message(cause))
      setBusy(false)
    }
  }
  const save = async () => {
    if (busy || conflict || !channelID || events.length === 0 || !catalogAvailable) return
    if (unknownEvents.length > 0) {
      setError('Clear unavailable saved events before saving Slack notifications.')
      return
    }
    if (events.length > 64) {
      setError('You can select up to 64 notification events. Clear an event before saving.')
      return
    }
    setBusy(true)
    setError('')
    setNotice('')
    let saved: 'events' | 'channel' | undefined
    try {
      let current = latestState.current
      if (events.join(',') !== current.events.join(',')) {
        current = (await unwrap(
          client.PUT('/integrations/slack/events', {
            body: {
              events: events as components['schemas']['SlackEventsInput']['events'],
              expected_revision: current.revision,
            },
          }),
        )) as SlackIntegration
        latestState.current = current
        saved = 'events'
        cache.setQueryData(['slack-integration', workspace.id || 'installation'], current)
      }
      if (channelID !== current.channel?.id) {
        current = (await unwrap(
          client.PUT('/integrations/slack/channel', {
            body: { channel_id: channelID, expected_revision: current.revision },
          }),
        )) as SlackIntegration
      }
      latestState.current = current
      cache.setQueryData(['slack-integration', workspace.id || 'installation'], current)
      void cache.invalidateQueries({
        queryKey: ['slack-integration', workspace.id || 'installation'],
      })
      void cache.invalidateQueries({
        queryKey: ['slack-deliveries', workspace.id || 'installation'],
      })
      if (workspace.id)
        void cache.invalidateQueries({ queryKey: ['cloud-workspace', workspace.id] })
      await navigate({
        to: '/settings/integrations/slack',
        search: { configure: undefined, connected: undefined },
      })
    } catch (cause) {
      if (cause instanceof APIError && cause.status === 409) {
        setConflict(true)
        setError(
          saved
            ? `Notification events were saved, but another administrator changed Slack before the channel could be saved. Load the latest revision below to continue; your draft is still here.`
            : 'Another administrator changed Slack settings. Load the latest revision below to continue; your draft is still here.',
        )
      } else
        setError(
          saved === 'events'
            ? `Notification events were saved, but the Slack channel was not. ${message(cause)} You can retry without losing your choices.`
            : message(cause),
        )
    } finally {
      setBusy(false)
    }
  }
  const reloadLatestRevision = async () => {
    if (busy) return
    setBusy(true)
    try {
      const latest = (await unwrap(client.GET('/integrations/slack'))) as SlackIntegration
      latestState.current = latest
      cache.setQueryData(['slack-integration', workspace.id || 'installation'], latest)
      setConflict(false)
      setError('')
      setNotice('The latest Slack revision is loaded. Your channel and event choices are still here.')
    } catch (cause) {
      setNotice('')
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  const toggleEvent = (event: SlackEvent, checked: boolean) => {
    dirty.current = true
    setEvents((current) => {
      if (!checked) return current.filter((value) => value !== event)
      if (current.includes(event)) return current
      if (current.length >= 64) {
        setError('You can select up to 64 notification events. Clear an event before selecting more.')
        return current
      }
      return [...current, event]
    })
  }
  const selectEvents = (selected: SlackEvent[]) => {
    dirty.current = true
    setEvents((current) => {
      const additions = selected.filter((event) => !current.includes(event))
      if (current.length + additions.length > 64) {
        setError('You can select up to 64 notification events. Clear an event before selecting more.')
        return current
      }
      return [...current, ...additions]
    })
  }
  const clearEvents = (selected: SlackEvent[]) => {
    dirty.current = true
    const selectedIDs = new Set(selected)
    setEvents((current) => current.filter((event) => !selectedIDs.has(event)))
  }
  const useManualChannelID = () => {
    const value = manualChannelID.trim()
    if (!value) return
    dirty.current = true
    setChannelID(value)
    setManualChannelID('')
  }
  if (!state.configured) {
    const cloud = state.mode === 'cloud'
    return (
      <FormPage
        title="Connect Slack"
        brandMark={<SlackLogo className="size-6 shrink-0" />}
        description={
          cloud
            ? 'Authorize the Hakopod Cloud Slack app for this workspace.'
            : 'Create and authorize a Slack app for this installation.'
        }
        breadcrumbs={[]}
        keepFocusedControlsVisible
        help={
          !cloud ? (
            <FormHint title="Use the generated manifest">
              Create a Slack app from this manifest, then copy the app credentials into this form.
              The callback URL is included for this installation.
            </FormHint>
          ) : undefined
        }
      >
        <div className="grid gap-4">
          {!state.setup_available && <Note>Slack setup is not ready for this installation.</Note>}
          {!cloud && state.manifest && (
            <FormSection title="Slack app manifest">
              <div className="grid gap-2">
                <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words text-xs">
                  {JSON.stringify(state.manifest, null, 2)}
                </pre>
                <Copy value={JSON.stringify(state.manifest, null, 2)} label="Copy manifest" />
              </div>
            </FormSection>
          )}
          {cloud ? (
            <FormSection title="Authorize">
              <p className="m-0 text-sm">
                Continue to Slack to choose the workspace for the published Hakopod Cloud app.
              </p>
              <Button
                type="button"
                variant="primary"
                disabled={busy || !state.setup_available}
                onClick={() => void connect()}
              >
                {busy ? 'Opening Slack…' : 'Continue to Slack'}
              </Button>
            </FormSection>
          ) : (
            <FormSection title="Slack app credentials">
              <label>
                Client ID
                <Input
                  value={clientID}
                  required
                  maxLength={512}
                  onChange={(event) => setClientID(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                />
              </label>
              <label>
                Client secret
                <Input
                  type="password"
                  value={clientSecret}
                  required
                  maxLength={4096}
                  onChange={(event) => setClientSecret(event.target.value)}
                  autoComplete="new-password"
                  spellCheck={false}
                />
              </label>
              <p className="field-help">
                The secret is submitted only to start authorization and is never returned to the
                browser.
              </p>
              <Button
                type="button"
                variant="primary"
                disabled={busy || !state.setup_available || !clientID.trim() || !clientSecret}
                onClick={() => void connect()}
              >
                {busy ? 'Opening Slack…' : 'Continue to Slack'}
              </Button>
            </FormSection>
          )}
          {error && <RequestError error={error} />}
          <div className="form-footer">
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={() =>
                void navigate({
                  to: state.configured ? '/settings/integrations/slack' : '/settings/integrations',
                })
              }
            >
              Cancel
            </Button>
          </div>
        </div>
      </FormPage>
    )
  }
  return (
    <FormPage
      title="Configure Slack"
      brandMark={<SlackLogo className="size-6 shrink-0" />}
      breadcrumbs={[]}
      keepFocusedControlsVisible
      help={
        <FormHint title="Notification delivery">
          Choose one channel and the individual events it should receive. Event descriptions explain
          what Hakopod reports.
        </FormHint>
      }
    >
      <form
        ref={formRef}
        tabIndex={-1}
        aria-label={review ? 'Slack notification review' : 'Slack notification configuration'}
        className="grid gap-4"
        onSubmit={(event) => {
          event.preventDefault()
          if (review) {
            if (!catalogAvailable) setError('Notification events are unavailable. Refresh before saving.')
            else void save()
          } else if (!catalogAvailable)
            setError('Notification events are unavailable. Refresh before changing this configuration.')
          else if (unknownEvents.length > 0)
            setError('Clear unavailable saved events before reviewing Slack notifications.')
          else if (events.length > 64)
            setError('You can select up to 64 notification events. Clear an event before reviewing.')
          else if (!channelID || events.length === 0)
            setError('Choose a channel and at least one event.')
          else {
            setError('')
            setReview(true)
          }
        }}
      >
        {review ? (
          <FormSection title="Review Slack notifications">
            <InstallationReviewRows
              rows={[
                ['Workspace', state.team?.name || 'Connected workspace'],
                ['Channel', chosenChannel ? `#${chosenChannel.name}` : `Channel ${channelID}`],
                ['Events', `${events.length} selected`],
                ['Revision', String(latestState.current.revision)],
              ]}
            />
            <SelectedSlackEvents groups={selectedEventGroups} />
            {unknownEvents.length > 0 && (
              <Note>Clear unavailable saved events before Slack notifications can be saved.</Note>
            )}
          </FormSection>
        ) : (
          <>
            <FormSection title="Delivery channel">
              {channels.error && !channels.data ? (
                <ErrorState error={channels.error} retry={() => void channels.refetch()} />
              ) : (
                <div className="grid gap-3">
                  <SelectField
                    label="Slack channel"
                    value={channelID}
                    onValueChange={(value) => {
                      dirty.current = true
                      setChannelID(value)
                    }}
                    required
                    disabled={channels.isPending && !channels.data}
                    options={channelOptions}
                  />
                  {nextChannelBefore && (
                    <div className="flex flex-wrap items-center gap-2">
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={!canLoadMoreChannels || loadingMoreChannels}
                        onClick={() => void loadMoreChannels()}
                      >
                        {loadingMoreChannels ? 'Loading channels…' : 'Load more channels'}
                      </Button>
                      {channelPageCount >= 10 && (
                        <span className="field-help">More channel pages are not loaded below.</span>
                      )}
                    </div>
                  )}
                  {channelPageCount >= 10 && nextChannelBefore && (
                    <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
                      <label className="grid gap-1" htmlFor="slack-manual-channel-id">
                        <span className="text-sm font-medium">Slack channel ID</span>
                        <Input
                          id="slack-manual-channel-id"
                          value={manualChannelID}
                          onChange={(event) => setManualChannelID(event.target.value)}
                          onKeyDown={(event) => {
                            if (event.key === 'Enter') event.preventDefault()
                          }}
                          placeholder="C0123456789"
                          autoComplete="off"
                          spellCheck={false}
                          maxLength={64}
                        />
                      </label>
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={!manualChannelID.trim()}
                        onClick={useManualChannelID}
                      >
                        Use channel ID
                      </Button>
                      <p className="field-help m-0 sm:col-span-2">
                        Paste a channel ID when your channel is beyond the loaded pages. Hakopod
                        verifies it before saving.
                      </p>
                    </div>
                  )}
                  {channelLoadError && (
                    <div className="flex flex-wrap items-center gap-2">
                      <RequestError error={channelLoadError} />
                      {canLoadMoreChannels && (
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          disabled={loadingMoreChannels}
                          onClick={() => void loadMoreChannels()}
                        >
                          Retry load more
                        </Button>
                      )}
                    </div>
                  )}
                </div>
              )}
              <p className="field-help">
                Invite the Hakopod app to the Slack channel you want to use. Hakopod verifies the selected channel with Slack before saving it.
              </p>
            </FormSection>
            <FormSection title="Notification events">
              {!catalogAvailable ? (
                <RequestError error="Notification events are unavailable. Refresh before changing this configuration." />
              ) : (
                <SlackEventSelector
                  catalog={eventCatalog}
                  events={events}
                  search={eventSearch}
                  unknownEvents={unknownEvents}
                  onSearchChange={setEventSearch}
                  onToggle={toggleEvent}
                  onSelectEvents={selectEvents}
                  onClearEvents={clearEvents}
                />
              )}
            </FormSection>
          </>
        )}
        {error && <RequestError error={error} />}
        {notice && (
          <p className="m-0 text-sm" role="status">
            {notice}
          </p>
        )}
        {conflict && (
          <Note>
            Another administrator changed Slack settings. Your draft is kept on this page. Load the
            latest revision, then review and save your choices again.
            <div className="mt-2">
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => void reloadLatestRevision()}
              >
                Load latest revision
              </Button>
            </div>
          </Note>
        )}
        <div className="form-footer">
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() =>
              review
                ? setReview(false)
                : void navigate({
                    to: '/settings/integrations/slack',
                    search: { configure: undefined, connected: undefined },
                  })
            }
          >
            {review ? 'Back to edit' : 'Cancel'}
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={
              busy ||
              conflict ||
              !state.setup_available ||
              !catalogAvailable ||
              (review && unknownEvents.length > 0)
            }
          >
            {busy ? 'Saving…' : review ? 'Save notifications' : 'Review changes'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
