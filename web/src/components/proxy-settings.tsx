import { Textarea } from './ui/textarea'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { HeadingHelp, ErrorState, Loading, Note, Status } from './shared'

type ProxyStatus = components['schemas']['ProxyStatus']
export default function ProxySettings() {
  const [editing, setEditing] = useState<ProxyStatus | null>(null)
  const proxy = useQuery({
    queryKey: ['haproxy-settings'],
    queryFn: ({ signal }) => unwrap(client.GET('/settings/haproxy', { signal })),
    gcTime: 0,
    refetchInterval: (query) => (query.state.data?.change.status === 'queued' ? 2500 : 30000),
    refetchIntervalInBackground: false,
  })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>HAProxy configuration</h2>
            <HeadingHelp title="HAProxy configuration">
              Review supported controller settings before applying a durable change.
            </HeadingHelp>
          </div>
        </div>
        <Button
          variant="primary"
          disabled={!proxy.data || proxy.data.change.status === 'queued'}
          onClick={() => proxy.data && setEditing(structuredClone(proxy.data))}
        >
          Edit settings
        </Button>
      </div>
      {proxy.isPending ? (
        <Loading />
      ) : proxy.error ? (
        <ErrorState error={proxy.error} retry={() => void proxy.refetch()} />
      ) : (
        proxy.data && (
          <>
            <section className="panel service-summary-panel">
              <div className="section-toolbar">
                <h3>
                  {proxy.data.observed.namespace} / {proxy.data.observed.name}
                </h3>
                <Status value={proxy.data.change.status || 'observed'} />
              </div>
              <p className="field-help">
                Database revision {proxy.data.revision} · Kubernetes resource version{' '}
                {proxy.data.observed.resource_version}
              </p>
              {proxy.data.change.error && (
                <div className="inline-error" role="alert">
                  {proxy.data.change.error}
                </div>
              )}
              {proxy.data.drift && (
                <Note>
                  Observed configuration differs from the last applied dashboard change. Review the
                  latest observed values before editing.
                </Note>
              )}
              <dl className="service-definition-list">
                {Object.entries(proxy.data.observed.settings).map(([key, value]) => (
                  <div key={key}>
                    <dt>
                      <code>{key}</code>
                    </dt>
                    <dd className="mono break-text">{value || 'Controller default'}</dd>
                  </div>
                ))}
              </dl>
            </section>
            <details className="panel service-summary-panel">
              <summary>Supported fields</summary>
              <dl className="service-definition-list">
                {proxy.data.observed.fields.map((field) => (
                  <div key={field.name}>
                    <dt>
                      <code>{field.name}</code>
                    </dt>
                    <dd>
                      {field.description}
                      <p className="field-help">Example: {field.example}</p>
                    </dd>
                  </div>
                ))}
              </dl>
            </details>
          </>
        )
      )}
      <Note>
        Settings apply to installation ingress. Services and ingresses can override backend
        defaults. A change can reload HAProxy; existing connections drain until the configured
        deadline. Only reviewed fields change, and unrelated controller configuration is preserved.
      </Note>
      {editing && (
        <ProxyEditor
          snapshot={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void proxy.refetch()
          }}
        />
      )}
    </>
  )
}

function ProxyEditor({
  snapshot,
  onClose,
  onSaved,
}: {
  snapshot: ProxyStatus
  onClose: () => void
  onSaved: () => void
}) {
  const initial = Object.fromEntries(
    snapshot.observed.fields
      .filter((field) => field.name in snapshot.observed.settings)
      .map((field) => [field.name, snapshot.observed.settings[field.name]]),
  )
  const [text, setText] = useState(JSON.stringify(initial, null, 2))
  const [review, setReview] = useState<Record<string, string> | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const changed = review
    ? Object.entries(review).filter(
        ([key, value]) => value !== (snapshot.observed.settings[key] || ''),
      )
    : []
  return (
    <Dialog
      open
      wide
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={review ? 'Review HAProxy changes' : 'Edit HAProxy settings'}
      description="An empty string explicitly resets a field to its controller default."
    >
      <div className="dialog-body auth-form">
        {review ? (
          <>
            {changed.map(([key, value]) => (
              <div className="diff-row" key={key}>
                <div className="diff-field">
                  <strong>{key}</strong>
                </div>
                <div className="diff-value diff-before">
                  <code>{snapshot.observed.settings[key] || 'Controller default'}</code>
                </div>
                <div className="diff-value diff-after">
                  <code>{value || 'Reset to controller default'}</code>
                </div>
              </div>
            ))}
            {!changed.length && <Note>No settings change.</Note>}
          </>
        ) : (
          <>
            <label>
              Settings object (JSON)
              <Textarea
                value={text}
                onChange={(e) => setText(e.target.value)}
                className="toml-editor"
                rows={15}
                maxLength={32768}
                spellCheck={false}
              />
            </label>
            <p className="field-help">
              Write every value as a string, including numbers and true or false. Durations use one
              integer with ms, s, m or h. Omitted and unchanged fields remain untouched.
            </p>
            <details>
              <summary>Setting reference and limits</summary>
              <dl className="service-definition-list">
                {snapshot.observed.fields.map((field) => (
                  <div key={field.name}>
                    <dt>
                      <code>{field.name}</code>
                    </dt>
                    <dd>
                      {field.description}
                      <p className="field-help">Example: &quot;{field.example}&quot;</p>
                    </dd>
                  </div>
                ))}
              </dl>
            </details>
          </>
        )}
        {error && (
          <div className="inline-error" role="alert">
            {error}
          </div>
        )}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={() => (review ? setReview(null) : onClose())}>
          {review ? 'Back to editor' : 'Cancel'}
        </Button>
        <Button
          variant="primary"
          disabled={busy || Boolean(review && !changed.length)}
          onClick={async () => {
            setError('')
            if (!review) {
              try {
                const parsed = JSON.parse(text)
                if (
                  !parsed ||
                  Array.isArray(parsed) ||
                  typeof parsed !== 'object' ||
                  Object.values(parsed).some((value) => typeof value !== 'string')
                )
                  throw new Error('Use a JSON object containing string values.')
                if (
                  Object.keys(parsed).some(
                    (key) => !snapshot.observed.fields.some((field) => field.name === key),
                  )
                )
                  throw new Error('Use only the supported setting names shown below the editor.')
                setReview(parsed)
              } catch (err) {
                setError(message(err))
              }
              return
            }
            setBusy(true)
            try {
              await unwrap(
                client.PATCH('/settings/haproxy', {
                  body: {
                    settings: Object.fromEntries(changed),
                    expected_revision: snapshot.revision,
                    expected_resource_version: snapshot.observed.resource_version,
                  },
                }),
              )
              onSaved()
            } catch (err) {
              setError(message(err))
            } finally {
              setBusy(false)
            }
          }}
        >
          {busy ? 'Submitting…' : review ? 'Apply reviewed settings' : 'Review changes'}
        </Button>
      </div>
    </Dialog>
  )
}
