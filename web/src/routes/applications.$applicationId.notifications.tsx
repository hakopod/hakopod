import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope, useResourceScope } from '../lib/scope'
import type { components } from '../lib/api.generated'
import { FormPage, FormSection } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { Dialog } from '../components/ui/dialog'
import { Icon } from '../components/icons'
import { Empty, ErrorState, Loading, Note, RequestError } from '../components/shared'

type Target = components['schemas']['NotificationTarget']
type Kind = Target['kind']
type Event = Target['events'][number]
const labels: Record<Kind, string> = {
  email: 'Email',
  slack: 'Slack',
  discord: 'Discord',
  webhook: 'Webhook',
}
const events: [Event, string][] = [
  ['succeeded', 'Deployment succeeds'],
  ['failed', 'Deployment fails'],
  ['cancelled', 'Deployment is cancelled'],
]

export const Route = createFileRoute('/applications/$applicationId/notifications')({
  component: Notifications,
})
function Notifications() {
  const { applicationId } = Route.useParams()
  const scope = useScope()
  const cache = useQueryClient()
  const app = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    gcTime: 0,
  })
  const query = useQuery({
    queryKey: ['deployment-notifications', applicationId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/notifications', {
          signal,
          params: { path: { id: applicationId } },
        }),
      ),
    refetchInterval: 5000,
    gcTime: 0,
  })
  useResourceScope(app.data)
  const [editing, setEditing] = useState<Target | null>(null)
  const [name, setName] = useState('')
  const [kind, setKind] = useState<Kind>('email')
  const [destination, setDestination] = useState('')
  const [secret, setSecret] = useState('')
  const [selected, setSelected] = useState<Event[]>(['succeeded', 'failed'])
  const [enabled, setEnabled] = useState(true)
  const [review, setReview] = useState(false)
  const [removing, setRemoving] = useState<Target | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  if (app.isPending || query.isPending) return <Loading />
  if (app.error || query.error || !app.data || !query.data)
    return <ErrorState error={app.error || query.error} />
  const data = query.data
  const writable = scope.can('deployments:write')
  function reset(target: Target | null) {
    setEditing(target)
    setName(target?.name || '')
    setKind(target?.kind || 'email')
    setDestination('')
    setSecret('')
    setSelected(target?.events || ['succeeded', 'failed'])
    setEnabled(target?.enabled ?? true)
    setReview(false)
    setError('')
    setNotice('')
    document.getElementById('notification-name')?.focus()
  }
  const params = { path: { id: applicationId } }
  async function refresh() {
    await cache.invalidateQueries({ queryKey: ['deployment-notifications', applicationId] })
  }
  async function save() {
    setBusy(true)
    setError('')
    const body = {
      name,
      kind,
      enabled,
      events: selected,
      destination,
      signing_secret: secret,
      expected_revision: editing?.revision || 0,
    }
    try {
      if (editing)
        await unwrap(
          client.PUT('/applications/{id}/notifications/{target}', {
            params: { path: { id: applicationId, target: editing.id } },
            body,
          }),
        )
      else await unwrap(client.POST('/applications/{id}/notifications', { params, body }))
      reset(null)
      setNotice('Destination saved. Future matching deployments will send notifications.')
      await refresh()
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  async function test(target: Target) {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await unwrap(
        client.POST('/applications/{id}/notifications/{target}/test', {
          params: { path: { id: applicationId, target: target.id } },
          body: { expected_revision: target.revision },
        }),
      )
      setNotice('Test notification queued. Check delivery history below for the result.')
      await refresh()
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  async function remove() {
    if (!removing) return
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.DELETE('/applications/{id}/notifications/{target}', {
          params: { path: { id: applicationId, target: removing.id } },
          body: { expected_revision: removing.revision },
        }),
      )
      if (editing?.id === removing.id) reset(null)
      setRemoving(null)
      setNotice('Destination removed.')
      await refresh()
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  const destinationRequired = !editing || editing.kind !== kind
  let destinationSummary = 'Saved destination unchanged'
  if (destination.trim()) {
    if (kind === 'email') destinationSummary = destination.trim()
    else {
      try {
        destinationSummary = new URL(destination).hostname + ' (new webhook URL)'
      } catch {
        destinationSummary = 'New webhook URL; the server will validate it before saving'
      }
    }
  }
  return (
    <FormPage
      title="Deployment notifications"
      description="Choose where this application sends deployment results. Delivery failures never block a deployment."
      breadcrumbs={[]}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span>{app.data.display_name || app.data.name}</span>
        <Button asChild variant="outline">
          <Link
            to="/applications/$applicationId"
            params={{ applicationId }}
            search={{ tab: 'deployments' }}
          >
            Back to deployments
          </Link>
        </Button>
      </div>
      {error && <RequestError error={error} />}
      {notice && (
        <p role="status" className="text-sm">
          {notice}
        </p>
      )}
      <FormSection
        title="Destinations"
        description="Up to five destinations per application. Addresses and webhook credentials are encrypted and never returned after saving."
      >
        {data.items.length === 0 ? (
          <Empty
            title="No notification destinations"
            description="Add email, Slack, Discord or a signed webhook to receive deployment results."
            icon="bell"
          />
        ) : (
          <div className="flex flex-col divide-y divide-border">
            {data.items.map((target) => (
              <article
                key={target.id}
                className="flex flex-col items-stretch justify-between gap-3 py-3 sm:flex-row sm:items-center"
              >
                <div className="min-w-0 flex-1">
                  <h3 className="break-words">{target.name}</h3>
                  <p className="text-sm text-muted-foreground">
                    {labels[target.kind]} · {target.enabled ? 'Enabled' : 'Paused'} ·{' '}
                    {target.events
                      .map((e) =>
                        e === 'succeeded' ? 'Success' : e === 'failed' ? 'Failure' : 'Cancellation',
                      )
                      .join(', ')}
                  </p>
                </div>
                {writable && (
                  <div className="flex flex-wrap gap-2 self-start sm:self-auto">
                    <Button
                      variant="outline"
                      disabled={busy || !target.enabled}
                      onClick={() => void test(target)}
                    >
                      Send test
                    </Button>
                    <Button variant="outline" disabled={busy} onClick={() => reset(target)}>
                      Edit
                    </Button>
                    <Button
                      variant="outline"
                      aria-label={'Delete ' + target.name}
                      disabled={busy}
                      onClick={() => setRemoving(target)}
                    >
                      <Icon name="trash" size={14} />
                    </Button>
                  </div>
                )}
              </article>
            ))}
          </div>
        )}
      </FormSection>
      {writable ? (
        <FormSection title={editing ? 'Edit destination' : 'Add destination'}>
          {!data.encryption_ready && (
            <Note>
              The installation encryption key must be configured before saving destinations.
            </Note>
          )}
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              setError('')
              setNotice('')
              if (!selected.length) {
                setError('Select at least one deployment event.')
                return
              }
              setReview(true)
            }}
          >
            <label className="flex flex-col gap-1" htmlFor="notification-name">
              Name
              <Input
                id="notification-name"
                value={name}
                maxLength={80}
                required
                onChange={(e) => {
                  setName(e.target.value)
                  setReview(false)
                }}
                placeholder="Engineering deployments"
              />
            </label>
            <label className="flex flex-col gap-1">
              Channel
              <SelectField
                label="Notification channel"
                value={kind}
                onValueChange={(v) => {
                  setKind(v as Kind)
                  setDestination('')
                  setSecret('')
                  setReview(false)
                }}
                options={Object.entries(labels).map(([value, label]) => ({ value, label }))}
              />
            </label>
            <label className="flex flex-col gap-1" htmlFor="notification-destination">
              {kind === 'email' ? 'Recipient email' : 'Webhook URL'}
              <Input
                id="notification-destination"
                type={kind === 'email' ? 'email' : 'password'}
                autoComplete="off"
                value={destination}
                maxLength={2048}
                required={destinationRequired}
                onChange={(e) => {
                  setDestination(e.target.value)
                  setReview(false)
                }}
                aria-describedby="notification-destination-help"
                placeholder={
                  destinationRequired
                    ? kind === 'email'
                      ? 'team@example.com'
                      : 'https://…'
                    : 'Leave blank to keep the saved destination'
                }
              />
            </label>
            <p id="notification-destination-help" className="text-sm text-muted-foreground">
              {kind === 'email'
                ? data.email_available
                  ? 'Uses the installation SMTP configuration. Enter one recipient.'
                  : 'SMTP is not configured. Ask the installation administrator to enable email delivery, or save this destination paused.'
                : kind === 'slack'
                  ? 'Paste the incoming webhook URL from your Slack app. It starts with https://hooks.slack.com/services/.'
                  : kind === 'discord'
                    ? 'Paste a channel webhook from Discord Server Settings → Integrations → Webhooks.'
                    : 'Use a public HTTPS endpoint on port 443. Private addresses and redirects are not supported.'}
            </p>
            {kind === 'webhook' && (
              <label className="flex flex-col gap-1" htmlFor="notification-secret">
                Signing secret
                <Input
                  id="notification-secret"
                  type="password"
                  autoComplete="new-password"
                  value={secret}
                  minLength={32}
                  maxLength={256}
                  required={!editing || editing.kind !== 'webhook'}
                  onChange={(e) => {
                    setSecret(e.target.value)
                    setReview(false)
                  }}
                  placeholder={
                    editing?.kind === 'webhook'
                      ? 'Leave blank to keep the saved secret'
                      : 'At least 32 characters'
                  }
                />
                <span className="text-sm text-muted-foreground">
                  Your receiver uses this secret to verify the X-Hakopod-Signature header.
                </span>
              </label>
            )}
            <fieldset className="flex flex-col gap-2">
              <legend className="mb-2">Notify when</legend>
              {events.map(([value, label]) => (
                <label key={value} className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={selected.includes(value)}
                    onChange={(e) => {
                      setSelected(
                        e.target.checked
                          ? [...selected, value]
                          : selected.filter((x) => x !== value),
                      )
                      setReview(false)
                    }}
                  />
                  {label}
                </label>
              ))}
            </fieldset>
            <label className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => {
                  setEnabled(e.target.checked)
                  setReview(false)
                }}
              />
              Enable this destination
            </label>
            {review && (
              <div
                className="flex flex-col gap-2 rounded-md border border-border p-3"
                role="region"
                aria-label="Review notification destination"
              >
                <h3>Review destination</h3>
                <p className="break-words text-sm">Destination: {destinationSummary}</p>
                <p>
                  {name} · {labels[kind]} · {enabled ? 'Enabled' : 'Paused'}
                </p>
                <p className="text-sm">
                  {selected.map((e) => events.find(([value]) => e === value)?.[1]).join(', ')}.
                </p>
                <p className="text-sm">
                  {enabled
                    ? 'Saving enables future matching notifications.'
                    : 'This destination will remain paused until enabled.'}{' '}
                  Existing deployments are not replayed. Editing a destination skips queued messages
                  created with its previous settings.
                </p>
                <Button type="button" disabled={busy} onClick={() => void save()}>
                  {busy ? 'Saving…' : 'Save destination'}
                </Button>
              </div>
            )}
            <div className="flex flex-wrap justify-end gap-2">
              {editing && (
                <Button type="button" variant="outline" disabled={busy} onClick={() => reset(null)}>
                  Cancel edit
                </Button>
              )}
              <Button
                type="submit"
                disabled={
                  busy ||
                  !data.encryption_ready ||
                  (!editing && data.items.length >= 5) ||
                  (enabled && kind === 'email' && !data.email_available)
                }
              >
                Review destination
              </Button>
            </div>
          </form>
        </FormSection>
      ) : (
        <Note>You need deployment write access to manage notification destinations.</Note>
      )}
      <FormSection
        title="Delivery history"
        description="Latest 25 deliveries. Transient failures retry up to five attempts over 24 hours; delivery is at least once. Receivers should deduplicate by event ID."
      >
        {data.deliveries.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No deliveries yet. Save a destination and send a test, or wait for a deployment to
            finish.
          </p>
        ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr>
                  <th>Destination</th>
                  <th>Event</th>
                  <th>Status</th>
                  <th>Attempts</th>
                  <th>Created</th>
                  <th>Details</th>
                </tr>
              </thead>
              <tbody>
                {data.deliveries.map((d) => (
                  <tr key={d.id}>
                    <td>
                      {data.items.find((t) => t.id === d.target_id)?.name || 'Removed destination'}
                    </td>
                    <td>
                      {d.payload.status === 'test' ? 'Test' : d.payload.status}{' '}
                      {d.payload.status !== 'test' && '· r' + d.payload.revision}
                    </td>
                    <td>{d.status}</td>
                    <td>{d.attempts}</td>
                    <td>{new Date(d.created_at).toLocaleString()}</td>
                    <td className="max-w-64 whitespace-normal break-words">
                      {d.last_error || '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </FormSection>
      <Dialog
        open={!!removing}
        onOpenChange={(open) => {
          if (!busy && !open) setRemoving(null)
        }}
        title="Delete notification destination?"
        description={`Remove ${removing?.name || 'this destination'} and its delivery history. Pending notifications are discarded; an in-flight delivery may still finish.`}
      >
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="outline" disabled={busy} onClick={() => setRemoving(null)}>
            Cancel
          </Button>
          <Button variant="destructive" disabled={busy} onClick={() => void remove()}>
            <Icon name="trash" size={14} />
            Delete destination
          </Button>
        </div>
      </Dialog>
    </FormPage>
  )
}
