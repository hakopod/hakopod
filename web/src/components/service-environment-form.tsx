import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application, Plan } from '../lib/types'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import {
  environmentChanges,
  environmentRows,
  mergeEnvironment,
  parseEnvironment,
  sameEnvironment,
  type Environment,
} from '../lib/service-environment'
import { FormHint, FormPage, FormSection } from './form-page'
import { DiffTable } from './deploy-dialog'
import { Icon } from './icons'
import { Note } from './shared'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'

export function ServiceEnvironmentForm({
  application,
  serviceName,
}: {
  application: Application
  serviceName: string
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const service = application.spec.services[serviceName]
  const [base] = useState(() => ({ ...service.env }))
  const [rows, setRows] = useState(() => environmentRows(service.env))
  const [reviewed, setReviewed] = useState<{ plan: Plan; before: Environment } | null>(null)
  const [conflict, setConflict] = useState<{ names: string[]; environment: Environment } | null>(
    null,
  )
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const request = useRef<AbortController | null>(null)
  const idempotency = useRef('')
  useEffect(() => () => request.current?.abort(), [])
  const close = () =>
    void navigate({
      to: '/applications/$applicationId',
      params: { applicationId: application.id },
      search: { service: serviceName, tab: 'environment' },
    })
  async function review(keepDraft = false) {
    setBusy(true)
    setError('')
    const controller = new AbortController()
    request.current = controller
    try {
      const current = await unwrap(
        client.GET('/applications/{id}', {
          signal: controller.signal,
          params: { path: { id: application.id } },
        }),
      )
      const currentService = current.spec.services[serviceName]
      if (!currentService)
        throw new Error(
          'This service was removed. Your draft is still here; return to the application to inspect its current revision.',
        )
      const draft = parseEnvironment(rows, Object.keys(currentService.secrets || {}))
      const before = currentService.env || {}
      const merged = mergeEnvironment(base, draft, before)
      if (
        merged.conflicts.length &&
        !(keepDraft && conflict && sameEnvironment(conflict.environment, before))
      ) {
        setConflict({ names: merged.conflicts, environment: before })
        return
      }
      const spec = structuredClone(current.spec)
      spec.services[serviceName].env = merged.environment
      const plan = await unwrap(
        client.POST('/plan', {
          signal: controller.signal,
          body: {
            project: current.project,
            environment: current.environment,
            service: serviceName,
            spec,
          },
        }),
      )
      if (plan.application_id !== application.id)
        throw new Error(
          'The reviewed plan belongs to a different application. Return to the application and try again.',
        )
      if (plan.expected_revision !== current.revision)
        throw new Error(
          'The application changed during review. Your draft is kept. Review again against the latest revision.',
        )
      setReviewed({ plan, before })
      setConflict(null)
      idempotency.current = crypto.randomUUID()
    } catch (cause) {
      if (!controller.signal.aborted) setError(message(cause))
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  async function deploy() {
    if (!reviewed) return
    setBusy(true)
    setError('')
    const controller = new AbortController()
    request.current = controller
    try {
      const result = await unwrap(
        client.POST('/deployments', {
          signal: controller.signal,
          body: {
            project: application.project,
            environment: application.environment,
            service: serviceName,
            spec: reviewed.plan.spec,
            expected_revision: reviewed.plan.expected_revision,
          },
          params: { header: { 'Idempotency-Key': idempotency.current } },
        }),
      )
      void cache.invalidateQueries({ queryKey: ['applications'] })
      void cache.invalidateQueries({ queryKey: ['application', application.id] })
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: result.id } })
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(message(cause))
        if (cause instanceof APIError && cause.status === 409) setReviewed(null)
      }
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  const changes = reviewed
    ? environmentChanges(reviewed.before, reviewed.plan.spec.services[serviceName].env)
    : []
  const secrets = Object.entries(service.secrets || {})
  return (
    <FormPage
      title={reviewed ? 'Review environment changes' : `${serviceName} environment`}
      description={`${application.name} · Plain variables passed to this service’s containers.`}
      icon="code"
      breadcrumbs={[
        { label: 'Applications', to: '/' },
        { label: application.name, to: `/applications/${application.id}` },
        { label: `${serviceName} environment` },
      ]}
      help={
        <>
          <FormHint title="Keep credentials in secrets">
            Plain variables are stored in application revisions. Use a secret reference for
            passwords, tokens, keys, and connection strings with credentials.
          </FormHint>
          <FormHint title="A reviewed deployment">
            Changes create an immutable revision and replace this service’s pods. Your draft stays
            here if validation or deployment fails.
          </FormHint>
        </>
      }
    >
      <div className="form-body service-environment-form">
        {reviewed ? (
          <>
            <div className="review-summary">
              <div>
                <span className="muted-text">SERVICE</span>
                <strong>{serviceName}</strong>
              </div>
              <div>
                <span className="muted-text">REVISION</span>
                <strong className="mono">
                  r{reviewed.plan.expected_revision} → r{reviewed.plan.expected_revision + 1}
                </strong>
              </div>
              <div>
                <span className="muted-text">VARIABLES CHANGED</span>
                <strong>{changes.length}</strong>
              </div>
            </div>
            {changes.length ? (
              <div className="table-container env-review-table">
                <table>
                  <thead>
                    <tr>
                      <th>Variable</th>
                      <th>Current value</th>
                      <th>After deployment</th>
                    </tr>
                  </thead>
                  <tbody>
                    {changes.map((change) => (
                      <tr key={change.name}>
                        <th scope="row">
                          <code>{change.name}</code>
                        </th>
                        <td>
                          <pre>
                            {change.before === undefined
                              ? 'Not set'
                              : change.before === ''
                                ? '(empty string)'
                                : change.before}
                          </pre>
                        </td>
                        <td>
                          <pre>
                            {change.after === undefined
                              ? 'Removed'
                              : change.after === ''
                                ? '(empty string)'
                                : change.after}
                          </pre>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <Note>No environment changes to deploy.</Note>
            )}
            {reviewed.plan.changes.some((change) => change.field !== 'env') && (
              <details className="env-additional-changes">
                <summary>Other changes recorded by the plan</summary>
                <DiffTable
                  changes={reviewed.plan.changes.filter((change) => change.field !== 'env')}
                />
              </details>
            )}
            {reviewed.plan.warnings?.map((warning, index) => (
              <Note key={index}>{warning}</Note>
            ))}
          </>
        ) : (
          <>
            <FormSection
              title="Plain variables"
              description={`${rows.length} / 128 variables. Empty values are allowed.`}
              icon="code"
            >
              {rows.length ? (
                <div className="env-variable-list">
                  {rows.map((row, index) => (
                    <div className="env-variable-row" key={row.id}>
                      <label>
                        Name
                        <Input
                          className="mono"
                          aria-label={`Variable ${index + 1} name`}
                          value={row.name}
                          maxLength={128}
                          autoComplete="off"
                          spellCheck={false}
                          disabled={busy}
                          onChange={(event) => {
                            setRows((previous) =>
                              previous.map((item) =>
                                item.id === row.id ? { ...item, name: event.target.value } : item,
                              ),
                            )
                            setConflict(null)
                          }}
                        />
                      </label>
                      <label>
                        Value
                        <Textarea
                          aria-label={`Variable ${index + 1} value`}
                          value={row.value}
                          maxLength={4096}
                          rows={2}
                          spellCheck={false}
                          disabled={busy}
                          onChange={(event) => {
                            setRows((previous) =>
                              previous.map((item) =>
                                item.id === row.id ? { ...item, value: event.target.value } : item,
                              ),
                            )
                            setConflict(null)
                          }}
                        />
                      </label>
                      <Button
                        size="icon"
                        variant="ghost"
                        aria-label={`Remove variable ${row.name || index + 1}`}
                        disabled={busy}
                        onClick={() => {
                          setRows((previous) => previous.filter((item) => item.id !== row.id))
                          setConflict(null)
                        }}
                      >
                        <Icon name="trash" size={15} />
                      </Button>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="field-help">This service has no plain variables yet.</p>
              )}
              <Button
                size="sm"
                disabled={busy || rows.length >= 128}
                onClick={() => {
                  setRows((previous) => [
                    ...previous,
                    { id: crypto.randomUUID(), name: '', value: '' },
                  ])
                  setConflict(null)
                }}
              >
                <Icon name="plus" size={14} />
                Add variable
              </Button>
            </FormSection>
            <FormSection
              title="Secret references"
              description="Managed separately from plain variables. Secret values are never shown here."
              icon="lock"
            >
              {secrets.length ? (
                <dl className="env-secret-references">
                  {secrets.map(([name, reference]) => (
                    <div key={name}>
                      <dt>
                        <code>{name}</code>
                      </dt>
                      <dd>
                        <Icon name="lock" size={13} />
                        <code>{reference.ref}</code>
                      </dd>
                    </div>
                  ))}
                </dl>
              ) : (
                <p className="field-help">No secret references attached to this service.</p>
              )}
              <Button asChild>
                <Link
                  to="/applications/$applicationId"
                  params={{ applicationId: application.id }}
                  search={{ service: serviceName, tab: 'secrets' }}
                >
                  Manage service secrets
                  <Icon name="arrow" size={14} />
                </Link>
              </Button>
            </FormSection>
            {conflict && (
              <div className="env-conflict" role="alert">
                <Note>
                  These variables changed since you opened the editor: {conflict.names.join(', ')}.
                  Your draft is kept. Continuing replaces these variables with your values in the
                  review.
                </Note>
                <Button disabled={busy} onClick={() => void review(true)}>
                  Keep my values and review
                </Button>
              </div>
            )}
          </>
        )}
        {error && (
          <div className="inline-error" role="alert">
            {error}
          </div>
        )}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {reviewed
            ? 'Only this service’s environment changes'
            : 'Nothing changes until you deploy'}
        </span>
        <div className="deploy-footer-actions">
          <Button disabled={busy} onClick={() => (reviewed ? setReviewed(null) : close())}>
            {reviewed ? 'Back to variables' : 'Cancel'}
          </Button>
          <Button
            variant="primary"
            disabled={busy || Boolean(reviewed && !changes.length)}
            onClick={() => void (reviewed ? deploy() : review())}
          >
            {busy
              ? reviewed
                ? 'Submitting…'
                : 'Validating…'
              : reviewed
                ? 'Deploy environment'
                : 'Review changes'}
            <Icon name="arrow" size={15} />
          </Button>
        </div>
      </div>
    </FormPage>
  )
}
