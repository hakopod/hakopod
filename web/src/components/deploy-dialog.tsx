import { useEffect, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application, Plan, Service, Spec } from '../lib/types'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { specToTOML } from '../lib/toml'
import { FormPage, FormHint } from './form-page'
import { Button } from './ui/button'
import { Icon } from './icons'
import { Note } from './shared'
import { ServiceIcon } from './service-icon'

const newSpec = (): Spec => ({
  schema_version: 1,
  name: '',
  services: { web: { image: '', port: 80, public: true, size: 'small', replicas: 1 } },
})

export function DeploymentForm({
  onClose,
  application,
  initialMode = 'form',
  serviceName,
}: {
  onClose: () => void
  application?: Application
  initialMode?: 'form' | 'toml'
  serviceName?: string
}) {
  const scope = useScope()
  const project = application?.project || scope.project
  const environment = application?.environment || scope.environment
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const registries = useQuery({
    queryKey: ['registries', project, environment],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/registries', { signal, params: { query: { project, environment } } })),
    enabled: Boolean(project && environment),
    gcTime: 0,
  })
  const [spec, setSpec] = useState<Spec>(newSpec)
  const [toml, setToml] = useState('')
  const [mode, setMode] = useState<'form' | 'toml'>('form')
  const [plan, setPlan] = useState<Plan | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const requestKey = useRef('')
  useEffect(() => {
    {
      setSpec(application ? structuredClone(application.spec) : newSpec())
      setPlan(null)
      setError('')
      setToml(application ? specToTOML(application.spec) : '')
      setMode(initialMode)
    }
  }, [application?.id, initialMode, serviceName])
  const payload = () => ({
    project,
    environment,
    ...(serviceName ? { service: serviceName } : {}),
    ...(mode === 'form' ? { spec } : { toml }),
  })
  async function review() {
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(client.POST('/plan', { body: payload() }))
      if (application && result.application_id !== application.id)
        throw new Error(
          'The imported application name does not match this application. Keep its original name to update it.',
        )
      setPlan(result)
      requestKey.current = crypto.randomUUID()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  async function deploy() {
    if (!plan) return
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.POST('/deployments', {
          body: {
            project,
            environment,
            spec: plan.spec,
            expected_revision: plan.expected_revision,
            ...(serviceName ? { service: serviceName } : {}),
          },
          params: { header: { 'Idempotency-Key': requestKey.current } },
        }),
      )
      void queryClient.invalidateQueries({ queryKey: ['applications'] })
      void queryClient.invalidateQueries({ queryKey: ['application', result.application_id] })
      onClose()
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: result.id } })
    } catch (err) {
      setError(message(err))
      if (err instanceof APIError && err.status === 409) setPlan(null)
    } finally {
      setBusy(false)
    }
  }
  const updateService = (name: string, changes: Partial<Service>) =>
    setSpec((previous) => ({
      ...previous,
      services: { ...previous.services, [name]: { ...previous.services[name], ...changes } },
    }))
  return (
    <FormPage
      breadcrumbs={[
        { label: 'Applications', to: '/' },
        ...(application
          ? [{ label: application.name, to: `/applications/${application.id}` }]
          : []),
        { label: application ? 'Configure' : 'New application' },
      ]}
      icon="box"
      help={
        <>
          <FormHint title="Start small">
            Choose the smallest resource profile that fits. You can review a larger profile before
            applying it later.
          </FormHint>
          <FormHint title="Review before deploy">
            The next step shows the server-validated revision and every configuration change.
          </FormHint>
        </>
      }
      title={
        plan
          ? 'Review your deployment'
          : application
            ? `Configure ${serviceName ? `${application.name} / ${serviceName}` : application.name}`
            : 'Deploy an application'
      }
      description={
        plan
          ? 'Review the exact changes before creating a new application revision.'
          : `Deploy container images to ${project} / ${environment}.`
      }
    >
      <div className="deploy-steps">
        <span className={!plan ? 'step-current' : ''}>
          <b>{plan ? <Icon name="check" size={12} /> : '1'}</b>Configure
        </span>
        <i />
        <span className={plan ? 'step-current' : ''}>
          <b>2</b>Review & deploy
        </span>
      </div>
      <div className="form-body deploy-body">
        {serviceName && (
          <Note>
            This revision stages changes to <strong>{serviceName}</strong> only. Other services and
            application networks retain their accepted configuration.
          </Note>
        )}
        {plan ? (
          <>
            <div className="review-summary">
              <div>
                <span className="muted-text">APPLICATION</span>
                <strong>{plan.spec.name}</strong>
              </div>
              <div>
                <span className="muted-text">REVISION</span>
                <strong className="mono">
                  {plan.expected_revision === 0 ? 'New application' : `r${plan.expected_revision}`}{' '}
                  <Icon name="arrow" size={13} /> r{plan.expected_revision + 1}
                </strong>
              </div>
              <div>
                <span className="muted-text">SERVICES</span>
                <strong>{Object.keys(plan.spec.services).length}</strong>
              </div>
            </div>
            <div className="review-resources">
              {Object.entries(plan.spec.services).map(([name, service]) => {
                const profile = plan.resource_profiles?.[service.size || 'small']
                return profile ? (
                  <div key={name}>
                    <strong>{name}</strong>
                    <span>
                      {service.replicas || 1} replica · {profile.CPURequest} CPU /{' '}
                      {profile.MemoryRequest} memory requested
                    </span>
                    <small>
                      Limits: {profile.CPULimit} CPU / {profile.MemoryLimit} memory per replica
                    </small>
                  </div>
                ) : null
              })}
            </div>
            <div className="section-caption">
              CONFIGURATION CHANGES <span>{plan.changes.length}</span>
            </div>
            <DiffTable changes={plan.changes} />
            {plan.warnings?.map((warning, i) => (
              <Note key={i}>{warning}</Note>
            ))}
            <Note>
              Readiness gates traffic to new replicas. Multi-service rollouts are not atomic;
              deployment details report each service’s result.
            </Note>
          </>
        ) : (
          <>
            <div className="segmented-control">
              <button className={mode === 'form' ? 'selected' : ''} onClick={() => setMode('form')}>
                <Icon name="box" size={15} />
                Container images
              </button>
              <button className={mode === 'toml' ? 'selected' : ''} onClick={() => setMode('toml')}>
                <Icon name="code" size={15} />
                Import TOML
              </button>
              {!application && scope.identity.admin && (
                <button onClick={() => void navigate({ to: '/applications/import' })}>
                  <ServiceIcon name="github" size={15} />
                  <ServiceIcon name="gitlab" size={15} />
                  Git repository
                </button>
              )}
            </div>
            {mode === 'toml' ? (
              <div className="field-stack">
                <label htmlFor="toml-import">hakopod.toml</label>
                <textarea
                  id="toml-import"
                  className="code-editor"
                  value={toml}
                  onChange={(event) => setToml(event.target.value)}
                  spellCheck={false}
                  placeholder={
                    'schema_version = 1\nname = "my-app"\n\n[services.web]\nimage = "nginx:1.29-alpine"\nport = 80\npublic = true'
                  }
                  maxLength={262144}
                />
                <p className="field-help">
                  TOML supports advanced networking, environment variables, dependencies, and health
                  checks, persistent storage, GPU requests, and saved secret bindings.
                </p>
              </div>
            ) : (
              <div className="field-stack">
                <label>
                  Application name
                  <input
                    placeholder="my-application"
                    value={spec.name}
                    disabled={Boolean(application)}
                    onChange={(event) =>
                      setSpec((previous) => ({ ...previous, name: event.target.value }))
                    }
                    pattern="[a-z0-9][a-z0-9-]*"
                    maxLength={63}
                  />
                </label>
                <div className="form-section-heading">
                  <span>Services</span>
                  <span className="muted-text">Private network included</span>
                </div>
                {Object.entries(spec.services)
                  .filter(([name]) => !serviceName || name === serviceName)
                  .map(([name, service]) => (
                    <div className="service-form" key={name}>
                      <div className="service-form-heading">
                        <div className="service-mini-icon">
                          <Icon
                            name={service.public ? 'globe' : service.port ? 'lock' : 'terminal'}
                            size={16}
                          />
                        </div>
                        <strong className="mono">{name}</strong>
                        <span className="form-spacer" />
                        {!serviceName && Object.keys(spec.services).length > 1 && (
                          <Button
                            size="icon"
                            variant="ghost"
                            aria-label={`Remove ${name}`}
                            onClick={() =>
                              setSpec((previous) => ({
                                ...previous,
                                services: Object.fromEntries(
                                  Object.entries(previous.services).filter(([key]) => key !== name),
                                ),
                              }))
                            }
                          >
                            <Icon name="x" size={14} />
                          </Button>
                        )}
                      </div>
                      <label>
                        Container image
                        <input
                          placeholder="nginx:1.29-alpine"
                          value={service.image}
                          onChange={(event) => updateService(name, { image: event.target.value })}
                        />
                      </label>
                      <label>
                        Registry credential
                        <select
                          value={service.registry_credential || ''}
                          onChange={(event) =>
                            updateService(name, {
                              registry_credential: event.target.value || undefined,
                            })
                          }
                        >
                          <option value="">Public image / no credential</option>
                          {service.registry_credential &&
                            !registries.data?.items.some(
                              (item) => item.name === service.registry_credential,
                            ) && (
                              <option value={service.registry_credential}>
                                {service.registry_credential} (saved reference)
                              </option>
                            )}
                          {registries.data?.items.map((item) => (
                            <option key={item.name} value={item.name}>
                              {item.name} · {item.registry}
                              {item.synchronized ? '' : ' · pending sync'}
                            </option>
                          ))}
                        </select>
                        {registries.error && (
                          <span className="field-help">
                            Registry credentials could not be loaded. Existing references are
                            preserved.
                          </span>
                        )}
                      </label>
                      <div className="form-grid-three">
                        <label>
                          Port
                          <input
                            type="number"
                            min={0}
                            max={65535}
                            placeholder="No port"
                            value={service.port || ''}
                            onChange={(event) =>
                              updateService(name, {
                                port: Number(event.target.value) || 0,
                                ...(!Number(event.target.value) ? { public: false } : {}),
                              })
                            }
                          />
                        </label>
                        <label>
                          Size
                          <select
                            value={service.size || 'small'}
                            onChange={(event) => updateService(name, { size: event.target.value })}
                          >
                            <option value="small">Small</option>
                            <option value="medium">Medium</option>
                            <option value="large">Large</option>
                          </select>
                        </label>
                        <label>
                          Replicas
                          <input
                            type="number"
                            min={1}
                            max={20}
                            value={service.replicas || 1}
                            onChange={(event) =>
                              updateService(name, { replicas: Number(event.target.value) })
                            }
                          />
                        </label>
                      </div>
                      <div className="service-exposure">
                        <label className="checkbox-label">
                          <input
                            type="checkbox"
                            checked={service.public || false}
                            disabled={!service.port}
                            onChange={(event) =>
                              updateService(name, { public: event.target.checked })
                            }
                          />
                          <span>Public HTTP endpoint</span>
                        </label>
                        <span>
                          {service.public
                            ? 'Gets a generated URL'
                            : service.port
                              ? 'Private to this application'
                              : 'Background worker'}
                        </span>
                      </div>
                    </div>
                  ))}
                {!serviceName && (
                  <Button
                    className="add-service-button"
                    variant="ghost"
                    disabled={Object.keys(spec.services).length >= 20}
                    onClick={() => {
                      let name = 'api'
                      let n = 2
                      while (spec.services[name]) name = `service-${n++}`
                      setSpec((previous) => ({
                        ...previous,
                        services: {
                          ...previous.services,
                          [name]: {
                            image: '',
                            port: 8080,
                            public: false,
                            size: 'small',
                            replicas: 1,
                          },
                        },
                      }))
                    }}
                  >
                    <Icon name="plus" size={16} />
                    Add service
                  </Button>
                )}
                <Note>
                  Private services discover each other by name, such as <code>http://api:8080</code>
                  . Your public web server can proxy browser requests to the private API.
                </Note>
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
      <div className="form-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {plan ? 'Only reviewed changes will be submitted' : 'Nothing changes until you deploy'}
        </span>
        <Button disabled={busy} onClick={() => (plan ? setPlan(null) : onClose())}>
          {plan ? 'Back to configuration' : 'Cancel'}
        </Button>
        <Button
          variant="primary"
          disabled={
            busy ||
            (!plan &&
              mode === 'form' &&
              (!spec.name || Object.values(spec.services).some((service) => !service.image))) ||
            (!plan && mode === 'toml' && !toml.trim())
          }
          onClick={() => void (plan ? deploy() : review())}
        >
          {busy
            ? plan
              ? 'Submitting…'
              : 'Validating…'
            : plan
              ? 'Deploy changes'
              : 'Review changes'}
          <Icon name="arrow" size={15} />
        </Button>
      </div>
    </FormPage>
  )
}

export function DiffTable({ changes }: { changes: Plan['changes'] }) {
  const [page, setPage] = useState(0)
  const pages = Math.ceil(changes.length / 50)
  const current = Math.min(page, Math.max(0, pages - 1))
  if (!changes.length)
    return (
      <div className="no-changes">
        <Icon name="check" size={18} />
        No configuration changes detected.
      </div>
    )
  const display = (value: unknown, sensitive: boolean) =>
    sensitive
      ? '[redacted]'
      : value === null || value === undefined || value === ''
        ? '—'
        : typeof value === 'object'
          ? JSON.stringify(value)
          : String(value)
  return (
    <div className="diff-table">
      {changes.slice(current * 50, (current + 1) * 50).map((change, i) => (
        <div className="diff-row" key={i}>
          <div className="diff-field">
            <span>{change.service || 'application'}</span>
            <strong>{change.field}</strong>
            {change.sensitive && <Icon name="lock" size={12} />}
          </div>
          <div className="diff-value diff-before">
            <span>−</span>
            <code>{display(change.before, change.sensitive)}</code>
          </div>
          <div className="diff-value diff-after">
            <span>+</span>
            <code>{display(change.after, change.sensitive)}</code>
          </div>
        </div>
      ))}
      {pages > 1 && (
        <div className="diff-pagination">
          <span>
            Changes {current * 50 + 1}–{Math.min((current + 1) * 50, changes.length)} of{' '}
            {changes.length}
          </span>
          <Button size="sm" disabled={current === 0} onClick={() => setPage(current - 1)}>
            Previous
          </Button>
          <Button size="sm" disabled={current + 1 >= pages} onClick={() => setPage(current + 1)}>
            Next
          </Button>
        </div>
      )}
    </div>
  )
}
