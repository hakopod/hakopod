import { useId, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import {
  edgeDraft,
  editableProxySettings,
  prepareEdgePolicy,
  prepareProxySettings,
  sameEdgePolicy,
  type EdgePolicy,
  type ProxyStatus,
} from '../lib/proxy-settings'
import { EdgePolicyFields, EdgePolicySummary } from './edge-policy-fields'
import { FormPage, FormSection } from './form-page'
import { InstallationAccess } from './installation-access'
import { InstallationReviewRows, useInstallationFormFocus } from './installation-form-fields'
import { Button } from './ui/button'
import { Textarea } from './ui/textarea'
import { HeadingHelp, ErrorState, Loading, Note, Status, RequestError } from './shared'

const queryKey = ['haproxy-settings']
const managedDescription = 'Installation traffic settings are managed by the Cloud service.'

function useProxySettings() {
  return useQuery({
    queryKey,
    queryFn: ({ signal }) => unwrap(client.GET('/settings/haproxy', { signal })),
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false,
    refetchInterval: (query) => (query.state.data?.change.status === 'queued' ? 2500 : 30000),
    refetchIntervalInBackground: false,
  })
}

export default function ProxySettings() {
  return (
    <InstallationAccess managedDescription={managedDescription}>
      <ProxyOverview />
    </InstallationAccess>
  )
}

function ProxyOverview() {
  const proxy = useProxySettings()
  const current = proxy.data
  return (
    <div className="grid min-w-0 gap-4">
      <div className="section-toolbar">
        <div className="hako-section-heading-title">
          <h2>Hakopod Edge</h2>
          <HeadingHelp title="Hakopod Edge">
            Manage traffic protection and supported HAProxy settings for this installation.
          </HeadingHelp>
        </div>
        {!current || current.change.status === 'queued' ? (
          <Button variant="primary" disabled>
            Edit configuration
          </Button>
        ) : (
          <Button variant="primary" asChild>
            <Link to="/settings/edge">Edit configuration</Link>
          </Button>
        )}
      </div>
      {proxy.isPending ? (
        <Loading />
      ) : proxy.error ? (
        <ErrorState error={proxy.error} retry={() => void proxy.refetch()} />
      ) : (
        current && (
          <>
            <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-2 text-sm">
              <Status value={current.change.status || 'observed'} />
              <span>Revision {current.revision}</span>
              <span className="break-all text-muted-foreground">
                HAProxy · {current.observed.namespace} / {current.observed.name}
              </span>
            </div>
            {current.change.status === 'queued' && (
              <Note>
                The change is queued. Wait for HAProxy to report the new configuration before
                treating it as applied.
              </Note>
            )}
            {current.change.error && (
              <ErrorState
                error={current.change.error}
                title="The configuration change needs attention"
              />
            )}
            {current.drift && (
              <Note>
                Observed configuration differs from the last applied change. Review the current
                values before editing.
              </Note>
            )}
            <section className="grid min-w-0 gap-4" aria-labelledby="edge-observed-heading">
              <h3 id="edge-observed-heading" className="m-0 text-sm font-medium">
                Saved traffic configuration
              </h3>
              <EdgePolicySummary policy={current.observed.edge} compact />
            </section>
            <details className="border-t border-border pt-4">
              <summary>HAProxy controller settings</summary>
              <dl className="service-definition-list mt-3">
                {Object.entries(current.observed.settings).map(([key, value]) => (
                  <div key={key}>
                    <dt>
                      <code>{key}</code>
                    </dt>
                    <dd className="mono break-text">{value || 'Controller default'}</dd>
                  </div>
                ))}
              </dl>
              {!Object.keys(current.observed.settings).length && (
                <p className="field-help">Controller defaults.</p>
              )}
              <p className="field-help">
                Kubernetes resource version {current.observed.resource_version}
              </p>
            </details>
            <Note>
              Rules apply to HTTP traffic at this installation. Request limits are local to each
              HAProxy process and listener. A configuration change can reload HAProxy; existing
              connections drain until their configured deadline.
            </Note>
          </>
        )
      )}
    </div>
  )
}

export function ProxyEditor() {
  return (
    <InstallationAccess managedDescription={managedDescription}>
      <ProxyEditorLoader />
    </InstallationAccess>
  )
}

function ProxyEditorLoader() {
  const proxy = useProxySettings()
  if (!proxy.data) {
    if (proxy.isPending) return <Loading />
    return <ErrorState error={proxy.error} retry={() => void proxy.refetch()} />
  }
  return (
    <>
      {proxy.error && (
        <Note>
          The latest configuration could not be refreshed. Your draft is kept; saving still checks
          the reviewed revision.
        </Note>
      )}
      <ProxyForm current={proxy.data} />
    </>
  )
}

type ProxyReview = { settings: Record<string, string>; edge?: EdgePolicy }

function ProxyForm({ current }: { current: ProxyStatus }) {
  const [snapshot, setSnapshot] = useState(() => structuredClone(current))
  const [draft, setDraft] = useState(() => edgeDraft(current.observed.edge))
  const [text, setText] = useState(() => JSON.stringify(editableProxySettings(current), null, 2))
  const [review, setReview] = useState<ProxyReview | null>(null)
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const [compared, setCompared] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState('')
  const formRef = useInstallationFormFocus(Boolean(review))
  const navigate = useNavigate()
  const cache = useQueryClient()
  const queued = current.change.status === 'queued'
  const changed = Boolean(review && (review.edge || Object.keys(review.settings).length))

  function prepare(): ProxyReview {
    const settings = prepareProxySettings(text, snapshot)
    const policy = prepareEdgePolicy(draft)
    return {
      settings,
      ...(!sameEdgePolicy(policy, snapshot.observed.edge) ? { edge: policy } : {}),
    }
  }

  async function save() {
    if (!review || busy || conflict || queued || submitted || !changed) return
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.PATCH('/settings/haproxy', {
          body: {
            ...(Object.keys(review.settings).length ? { settings: review.settings } : {}),
            ...(review.edge ? { edge: review.edge } : {}),
            expected_revision: snapshot.revision,
            expected_resource_version: snapshot.observed.resource_version,
          },
        }),
      )
      setSubmitted(true)
      cache.setQueryData<ProxyStatus>(queryKey, {
        ...current,
        revision: result.revision,
        change: { ...review, status: result.status, error: '' },
      })
      void cache.invalidateQueries({ queryKey })
      await navigate({ to: '/settings', search: { tab: 'edge' } })
    } catch (cause) {
      setError(message(cause))
      if (cause instanceof APIError && cause.status === 409) setConflict(true)
    } finally {
      setBusy(false)
    }
  }

  async function compareLatest() {
    if (busy || submitted) return
    setError('')
    let changes: ProxyReview
    try {
      changes = prepare()
    } catch (cause) {
      setError(message(cause))
      return
    }
    setBusy(true)
    try {
      const latest = await unwrap(client.GET('/settings/haproxy'))
      if (latest.change.status === 'queued')
        throw new Error(
          'Another change is still queued. Your draft is kept; compare again after it finishes.',
        )
      setText(JSON.stringify({ ...editableProxySettings(latest), ...changes.settings }, null, 2))
      if (!changes.edge) setDraft(edgeDraft(latest.observed.edge))
      setSnapshot(structuredClone(latest))
      cache.setQueryData(queryKey, latest)
      setReview(null)
      setConflict(false)
      setCompared(true)
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <FormPage
      title="Configure Hakopod Edge"
      description="Review HTTP traffic rules and HAProxy controller settings before applying an installation change."
      breadcrumbs={[]}
    >
      <form
        ref={formRef}
        tabIndex={-1}
        aria-label={review ? 'Hakopod Edge review' : 'Hakopod Edge configuration'}
        className="grid min-w-0 gap-4"
        onSubmit={(event) => {
          event.preventDefault()
          if (busy || submitted) return
          if (review) {
            void save()
            return
          }
          try {
            setReview(prepare())
            setError('')
          } catch (cause) {
            setError(message(cause))
          }
        }}
      >
        {queued && (
          <Note>A configuration change is queued. Your draft is kept while it finishes.</Note>
        )}
        {submitted && (
          <Note>
            Your change was accepted and is queued for HAProxy. Return to Settings to follow its
            status.
          </Note>
        )}
        {compared && (
          <Note>
            The latest configuration is loaded for comparison. Your edits are kept. Review the whole
            traffic policy before applying; it replaces the current policy.
          </Note>
        )}
        {review ? (
          <>
            <FormSection title="Review traffic protection">
              {review.edge ? (
                <>
                  <Note>
                    The proposed policy replaces all current traffic rules. The first matching rule
                    wins; later rules do not add restrictions.
                  </Note>
                  {review.edge.enabled && review.edge.client_ip_source === 'trusted_proxy' && (
                    <Note>
                      Protected routes will reject direct or untrusted connections and missing
                      client headers. Country rules require a valid country header from your trusted
                      proxy.
                    </Note>
                  )}
                  {!review.edge.enabled && snapshot.observed.edge.enabled && (
                    <Note>
                      Applying this change disables traffic protection. Rules are retained but will
                      not restrict traffic.
                    </Note>
                  )}
                  <div className="grid min-w-0 gap-6 lg:grid-cols-2">
                    <section
                      className="grid min-w-0 content-start gap-4"
                      aria-labelledby="edge-review-before"
                    >
                      <h3 id="edge-review-before" className="m-0 text-sm font-medium">
                        Current policy
                      </h3>
                      <EdgePolicySummary policy={snapshot.observed.edge} />
                    </section>
                    <section
                      className="grid min-w-0 content-start gap-4"
                      aria-labelledby="edge-review-after"
                    >
                      <h3 id="edge-review-after" className="m-0 text-sm font-medium">
                        Proposed policy
                      </h3>
                      <EdgePolicySummary policy={review.edge} />
                    </section>
                  </div>
                </>
              ) : (
                <p className="m-0 text-sm text-muted-foreground">
                  Traffic protection is unchanged.
                </p>
              )}
            </FormSection>
            <FormSection title="Review HAProxy settings">
              {Object.keys(review.settings).length ? (
                Object.entries(review.settings).map(([key, value]) => (
                  <div
                    key={key}
                    className="grid min-w-0 gap-2 border-b border-border pb-3 last:border-0 last:pb-0"
                  >
                    <strong className="text-sm font-medium">
                      <code>{key}</code>
                    </strong>
                    <InstallationReviewRows
                      rows={[
                        ['Current', snapshot.observed.settings[key] || 'Controller default'],
                        ['Proposed', value || 'Reset to controller default'],
                      ]}
                    />
                  </div>
                ))
              ) : (
                <p className="m-0 text-sm text-muted-foreground">
                  Controller settings are unchanged.
                </p>
              )}
              <p className="field-help">
                Reviewing revision {snapshot.revision}, Kubernetes resource version{' '}
                {snapshot.observed.resource_version}. Applying can reload HAProxy.
              </p>
            </FormSection>
            {!changed && <Note>No configuration changes to apply.</Note>}
          </>
        ) : (
          <fieldset disabled={busy || submitted} className="m-0 grid min-w-0 gap-4 border-0 p-0">
            <EdgePolicyFields draft={draft} onChange={setDraft} />
            <ControllerFields snapshot={snapshot} text={text} onChange={setText} />
          </fieldset>
        )}
        {error && <RequestError error={error} />}
        {conflict && (
          <Note>
            <p className="m-0">
              Configuration changed after review. Your draft is kept. Compare with the latest
              settings before applying again.
            </p>
            <Button
              type="button"
              size="sm"
              className="mt-3"
              disabled={busy}
              onClick={() => void compareLatest()}
            >
              {busy ? 'Loading comparison…' : 'Compare latest settings'}
            </Button>
          </Note>
        )}
        <div className="form-footer">
          <Button
            type="button"
            disabled={busy}
            onClick={() =>
              review && !submitted
                ? setReview(null)
                : void navigate({ to: '/settings', search: { tab: 'edge' } })
            }
          >
            {submitted ? 'Return to Settings' : review ? 'Back to editor' : 'Cancel'}
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={busy || submitted || Boolean(review && (!changed || conflict || queued))}
          >
            {busy ? 'Saving…' : review ? 'Apply configuration' : 'Review changes'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}

function ControllerFields({
  snapshot,
  text,
  onChange,
}: {
  snapshot: ProxyStatus
  text: string
  onChange: (value: string) => void
}) {
  const help = useId()
  return (
    <FormSection
      title="HAProxy configuration editor"
      description="Edit only the supported controller fields. Service and ingress settings can override backend defaults."
    >
      <label className="grid gap-2">
        Controller settings (JSON)
        <Textarea
          value={text}
          onChange={(event) => onChange(event.target.value)}
          className="toml-editor"
          rows={10}
          maxLength={32768}
          spellCheck={false}
          aria-describedby={help}
        />
      </label>
      <p id={help} className="field-help">
        Write values as strings, including numbers and true or false. An empty string resets a field
        to its controller default. Omitted and unchanged fields stay untouched. Durations use one
        integer with ms, s, m or h.
      </p>
      <details>
        <summary>Supported settings and limits</summary>
        <dl className="service-definition-list mt-3">
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
    </FormSection>
  )
}
