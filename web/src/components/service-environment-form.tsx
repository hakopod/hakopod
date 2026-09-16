import { Input } from './ui/input'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application, Plan, Service } from '../lib/types'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { saveEnvironment } from '../lib/save-environment'
import { secretReference } from '../lib/secret-reference'
import {
  environmentChanges,
  environmentRows,
  mergeEnvironment,
  splitEnvironment,
  sameEnvironment,
  type Environment,
} from '../lib/service-environment'
import { FormHint, FormPage, FormSection } from './form-page'
import { DiffTable } from './deploy-dialog'
import { Icon } from './icons'
import { Note, RequestError } from './shared'
import { Button } from './ui/button'
import { EnvironmentFields } from './runtime-settings-fields'

export function ServiceEnvironmentForm({
  application,
  serviceName,
}: {
  application: Application
  serviceName?: string
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const service: Pick<Service, 'env' | 'secrets'> = serviceName
    ? application.spec.services[serviceName]
    : { env: application.spec.env, secrets: application.spec.secrets }
  const label = serviceName || 'Application'
  const [injectEnv, setInjectEnv] = useState(application.spec.inject_env ?? false)
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
      search: { service: serviceName, tab: serviceName ? 'environment' : 'services' },
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
      const currentService = serviceName
        ? current.spec.services[serviceName]
        : { env: current.spec.env, secrets: current.spec.secrets }
      if (!currentService)
        throw new Error(
          'This service was removed. Your draft is still here; return to the application to inspect its current revision.',
        )
      const draft = splitEnvironment(rows, Object.keys(currentService.secrets || {})).env
      const before = currentService.env || {}
      const merged = mergeEnvironment(base, draft, before)
      if (
        merged.conflicts.length &&
        !(keepDraft && conflict && sameEnvironment(conflict.environment, before))
      ) {
        setConflict({ names: merged.conflicts, environment: before })
        return
      }
      const saved = await saveEnvironment(
        rows,
        { project: current.project, environment: current.environment, application: current.name },
        currentService.secrets,
        controller.signal,
      )
      const spec = structuredClone(current.spec)
      if (serviceName) {
        spec.services[serviceName].env = merged.environment
        spec.services[serviceName].secrets = saved.secrets
      } else {
        spec.inject_env =
          injectEnv === Boolean(application.spec.inject_env)
            ? Boolean(current.spec.inject_env)
            : injectEnv
        spec.env = merged.environment
        spec.secrets = saved.secrets
      }
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
    ? environmentChanges(
        reviewed.before,
        serviceName ? reviewed.plan.spec.services[serviceName].env : reviewed.plan.spec.env,
      )
    : []
  const secretChanges =
    reviewed?.plan.changes.filter(
      (change) => change.field === 'secrets' || change.field === 'inject_env',
    ) || []
  const secrets = Object.entries(service.secrets || {})
  return (
    <FormPage
      title={reviewed ? 'Review environment changes' : `${label} environment`}
      description={`${application.name} · Application defaults are inherited by services; service values take precedence.`}
      icon="code"
      breadcrumbs={[
        { label: 'Applications', to: `/projects/${encodeURIComponent(application.project)}` },
        { label: application.name, to: `/applications/${application.id}` },
        { label: `${label} environment` },
      ]}
      help={
        <>
          <FormHint title="Keep credentials in secrets">
            Plain variables are stored in application revisions. Imported passwords, tokens and
            credential URLs are stored as secret references when you review, before deployment.
          </FormHint>
          <FormHint title="A reviewed deployment">
            Changes create an immutable revision. Application defaults update all inheriting
            services; service overrides update that service. Your draft stays here if validation or
            deployment fails.
          </FormHint>
        </>
      }
    >
      <div className="form-body service-environment-form">
        {reviewed ? (
          <>
            <div className="review-summary">
              <div>
                <span className="muted-text">SCOPE</span>
                <strong>{label}</strong>
              </div>
              <div>
                <span className="muted-text">REVISION</span>
                <strong className="mono">
                  r{reviewed.plan.expected_revision} → r{reviewed.plan.expected_revision + 1}
                </strong>
              </div>
              <div>
                <span className="muted-text">VARIABLES CHANGED</span>
                <strong>{changes.length + secretChanges.length}</strong>
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
              <Note>
                {secretChanges.length
                  ? 'Environment settings are ready to deploy. Secret values are hidden.'
                  : 'No environment changes to deploy.'}
              </Note>
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
              title="Variables"
              description={`${rows.length} / 128 variables. Empty values are allowed.`}
              icon="code"
            >
              {!serviceName && (
                <label className="checkbox-label mb-4">
                  <Input
                    type="checkbox"
                    checked={injectEnv}
                    onChange={(event) => setInjectEnv(event.target.checked)}
                    disabled={busy}
                  />
                  <span>
                    Inject application variables into every service <code>inject_env = true</code>
                  </span>
                </label>
              )}
              <EnvironmentFields
                rows={rows}
                onChange={(next) => {
                  setRows(next)
                  setConflict(null)
                }}
                label={label}
                disabled={busy}
              />
              {serviceName &&
                Object.keys({
                  ...(application.spec.inject_env ? application.spec.env : {}),
                  ...application.spec.secrets,
                }).length > 0 && (
                  <div className="mt-4 grid gap-2">
                    <strong className="text-sm">Inherited from application</strong>
                    <p className="field-help">
                      Add a service variable with the same name to override a default. Empty strings
                      also override defaults.
                    </p>
                    {Object.keys({
                      ...(application.spec.inject_env ? application.spec.env : {}),
                      ...application.spec.secrets,
                    })
                      .sort()
                      .map((name) => (
                        <code key={name}>
                          {name}
                          {rows.some((row) => row.name === name) ||
                          Object.hasOwn(service.secrets || {}, name)
                            ? ' · overridden'
                            : ''}
                        </code>
                      ))}
                    <Button asChild>
                      <Link
                        to="/applications/$applicationId/environment"
                        params={{ applicationId: application.id }}
                      >
                        Edit application variables
                      </Link>
                    </Button>
                  </div>
                )}
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
                        <code>{secretReference(reference)}</code>
                      </dd>
                    </div>
                  ))}
                </dl>
              ) : (
                <p className="field-help">No secret references attached at this scope.</p>
              )}
              <Button asChild>
                <Link
                  to="/applications/$applicationId"
                  params={{ applicationId: application.id }}
                  search={{ service: serviceName, tab: 'secrets' }}
                >
                  Manage application secrets
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
        {error && <RequestError error={error} />}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {reviewed
            ? serviceName
              ? 'Only this service’s environment changes'
              : 'Application defaults update all inheriting services'
            : 'Values are saved securely at review; containers change on deployment'}
        </span>
        <div className="deploy-footer-actions">
          <Button disabled={busy} onClick={() => (reviewed ? setReviewed(null) : close())}>
            {reviewed ? 'Back to variables' : 'Cancel'}
          </Button>
          <Button
            variant="primary"
            disabled={busy || Boolean(reviewed && !changes.length && !secretChanges.length)}
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
