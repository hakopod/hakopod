import { saveEnvironment } from '../lib/save-environment'
import { EnvironmentFields, RunCommandFields } from './runtime-settings-fields'
import { environmentRows, type EnvironmentRow } from '../lib/service-environment'
import { formatProcessCommand, parseProcessCommand } from '../lib/process-command'
import { ComposeImport, type ComposeDraft } from './compose-import'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { withoutService } from '../lib/remove-service'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { SelectField } from './ui/select'
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
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { Note } from './shared'
import { ServiceIcon } from './service-icon'

type RuntimeDraft = { command: string; args: string; variables: EnvironmentRow[] }
const runtimeDraft = (service: Service): RuntimeDraft => ({
  command: formatProcessCommand(service.command),
  args: formatProcessCommand(service.args),
  variables: environmentRows(service.env),
})

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
  removeService,
}: {
  onClose: () => void
  application?: Application
  initialMode?: 'form' | 'toml' | 'compose'
  serviceName?: string
  removeService?: string
}) {
  const features = useEditionFeatures()
  const scope = useScope()
  const canBuildFromGit =
    !application && (scope.identity.admin || scope.identity.can_manage_git) && features.git
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
  const [runtime, setRuntime] = useState<Record<string, RuntimeDraft>>({})
  const [toml, setToml] = useState('')
  const [mode, setMode] = useState<'form' | 'toml' | 'compose'>('form')
  const [composeDraft, setComposeDraft] = useState<ComposeDraft | null>(null)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [editorExpanded, setEditorExpanded] = useState(false)
  const expandedEditor = useRef<HTMLTextAreaElement>(null)
  const tomlEditor = useRef<HTMLTextAreaElement>(null)
  const focusGeneratedTOML = useRef(false)
  const requestKey = useRef('')
  useEffect(() => {
    if (mode === 'toml' && focusGeneratedTOML.current) {
      focusGeneratedTOML.current = false
      tomlEditor.current?.focus()
    }
  }, [mode])
  useEffect(() => {
    {
      const initial = application
        ? removeService
          ? withoutService(application.spec, removeService)
          : structuredClone(application.spec)
        : newSpec()
      setSpec(initial)
      setRuntime(
        Object.fromEntries(
          Object.entries(initial.services).map(([name, service]) => [name, runtimeDraft(service)]),
        ),
      )
      setPlan(null)
      setComposeDraft(null)
      setError('')
      setToml(application ? specToTOML(initial) : '')
      setMode(initialMode)
    }
  }, [application?.id, initialMode, serviceName, removeService])
  const formSpec = async (): Promise<Spec> => {
    const next = structuredClone(spec)
    for (const [name, service] of Object.entries(next.services)) {
      const draft = runtime[name]
      if (!draft) continue
      const saved = await saveEnvironment(
        draft.variables,
        { project, environment, application: next.name },
        service.secrets,
      )
      next.services[name] = {
        ...service,
        command: parseProcessCommand(draft.command),
        args: parseProcessCommand(draft.args),
        ...saved,
      }
    }
    return next
  }
  const payload = async () => ({
    project,
    environment,
    ...(serviceName ? { service: serviceName } : {}),
    ...(mode === 'form' ? { spec: await formSpec() } : { toml }),
  })

  async function changeMode(next: 'form' | 'toml' | 'compose') {
    if (busy || mode === next) return
    setError('')
    setPlan(null)
    setBusy(true)
    try {
      if (mode === 'form' && (next === 'toml' || next === 'compose')) {
        const nextSpec = await formSpec()
        setSpec(nextSpec)
        setToml(specToTOML(nextSpec))
        setMode(next)
        return
      }
      if (next === 'compose' || mode === 'compose' || !toml.trim()) {
        setMode(next)
        return
      }
      const result = await unwrap(client.POST('/plan', { body: await payload() }))
      if (application && result.expected_revision !== application.revision)
        throw new Error(
          'This application changed while you were editing. Your draft is kept; reload the application before applying it.',
        )
      if (
        composeDraft &&
        (result.expected_revision !== composeDraft.expected_revision ||
          result.application_id !== composeDraft.application_id)
      )
        throw new Error(
          'The application changed after conversion. Reload it and import Compose again before reviewing.',
        )
      if (application && result.application_id !== application.id)
        throw new Error('Keep the original application name when editing this application.')
      setSpec(result.spec)
      setRuntime(
        Object.fromEntries(
          Object.entries(result.spec.services).map(([name, service]) => [
            name,
            runtimeDraft(service),
          ]),
        ),
      )
      setMode(next)
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  async function review() {
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(client.POST('/plan', { body: await payload() }))
      if (application && result.expected_revision !== application.revision)
        throw new Error(
          'This application changed while you were editing. Your draft is kept; reload the application before applying it.',
        )
      if (
        composeDraft &&
        (result.expected_revision !== composeDraft.expected_revision ||
          result.application_id !== composeDraft.application_id)
      )
        throw new Error(
          'The application changed after conversion. Reload it and import Compose again before reviewing.',
        )
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
        { label: 'Applications', to: `/projects/${encodeURIComponent(project)}` },
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
            application networks and volumes retain their accepted configuration.
          </Note>
        )}
        {composeDraft &&
          mode !== 'compose' &&
          composeDraft.warnings.map((warning, index) => (
            <Note key={`compose-${index}`}>{warning}</Note>
          ))}
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
            {Object.keys(plan.spec.services).length === 0 && (
              <Note>
                This removes every service and stops all application traffic. Persistent volumes and
                backups are retained. Delete the empty application only after this deployment
                succeeds.
              </Note>
            )}
            <div className="review-resources">
              {Object.entries(plan.spec.services).map(([name, service]) => {
                const profile = plan.resource_profiles?.[service.size || 'small']
                return profile ? (
                  <div key={name}>
                    <strong>{name}</strong>
                    <span>
                      {service.job?.schedule
                        ? 'Scheduled job'
                        : service.job
                          ? 'Deployment job'
                          : `${service.replicas ?? 1} replica`}{' '}
                      · {profile.CPURequest} CPU / {profile.MemoryRequest} memory requested
                    </span>
                    <small>
                      Limits: {profile.CPULimit} CPU / {profile.MemoryLimit} memory per{' '}
                      {service.job ? 'attempt' : 'replica'}
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
          <fieldset disabled={busy} className="m-0 min-w-0 border-0 p-0">
            <div
              className="segmented-control deployment-methods"
              data-method-count={canBuildFromGit ? (serviceName ? 4 : 5) : serviceName ? 2 : 3}
            >
              <button
                disabled={busy}
                className={mode === 'form' ? 'selected' : ''}
                onClick={() => void changeMode('form')}
              >
                <Icon name="box" size={15} />
                Container images
              </button>
              <button
                disabled={busy}
                className={mode === 'toml' ? 'selected' : ''}
                onClick={() => void changeMode('toml')}
              >
                <Icon name="code" size={15} />
                Import TOML
              </button>
              {!serviceName && (
                <button
                  disabled={busy}
                  className={mode === 'compose' ? 'selected' : ''}
                  onClick={() => void changeMode('compose')}
                >
                  <Icon name="code" size={15} />
                  Import Compose
                </button>
              )}
              {canBuildFromGit && (
                <>
                  <button onClick={() => void navigate({ to: '/builds/new' })}>
                    <ServiceIcon name="github" size={15} />
                    Build from Git
                  </button>
                  <button onClick={() => void navigate({ to: '/applications/import' })}>
                    <Icon name="code" size={15} />
                    Import Git configuration
                  </button>
                </>
              )}
            </div>
            {mode === 'compose' ? (
              <ComposeImport
                application={application}
                project={project}
                environment={environment}
                name={spec.name}
                onUse={(draft) => {
                  setComposeDraft(draft)
                  setSpec(draft.spec)
                  setRuntime(
                    Object.fromEntries(
                      Object.entries(draft.spec.services).map(([name, service]) => [
                        name,
                        runtimeDraft(service),
                      ]),
                    ),
                  )
                  setToml(draft.toml)
                  focusGeneratedTOML.current = true
                  setMode('toml')
                  setPlan(null)
                  setError('')
                }}
              />
            ) : mode === 'toml' ? (
              <div className="field-stack deploy-toml-field">
                <div className="toml-editor-heading">
                  <label htmlFor="toml-import">hakopod.toml</label>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    aria-haspopup="dialog"
                    onClick={() => setEditorExpanded(true)}
                  >
                    <Icon name="external" size={14} />
                    Expand editor
                  </Button>
                </div>
                <Textarea
                  id="toml-import"
                  ref={tomlEditor}
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
                  Configure networks, private ports, peer access, mounts and filesystem permissions
                  here alongside images, health checks and secret references. Switching to the form
                  validates and keeps these settings.
                </p>
              </div>
            ) : (
              <div className="field-stack">
                <label>
                  Application name
                  <Input
                    placeholder="my-application"
                    value={spec.name}
                    disabled={Boolean(application)}
                    onChange={(event) =>
                      setSpec((previous) => ({ ...previous, name: event.target.value }))
                    }
                    pattern="[a-z][a-z0-9\-]*"
                    maxLength={63}
                  />
                </label>
                <label>
                  Automatic release recovery
                  <SelectField
                    disabled={busy}
                    label="Automatic release recovery"
                    aria-describedby="release-recovery-help"
                    value={spec.recovery?.on_failure || 'safe'}
                    onValueChange={(value) =>
                      setSpec((previous) => ({
                        ...previous,
                        recovery: { on_failure: value as 'safe' | 'disabled' },
                      }))
                    }
                    options={[
                      { value: 'safe', label: 'Restore the last successful stateless release' },
                      { value: 'disabled', label: 'Disabled' },
                    ]}
                  />
                </label>
                <p id="release-recovery-help" className="field-help">
                  Automatic recovery skips jobs, volumes and service additions or removals. It
                  restores workload configuration only; external data and secret values are
                  unchanged.
                </p>
                {application && Object.keys(spec.services).length === 0 && (
                  <Note>
                    Deploying this revision removes every service and stops application traffic.
                    Persistent volumes and backups are retained. Once deployment succeeds, you can
                    delete the empty application.
                  </Note>
                )}
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
                        {!serviceName &&
                          (Boolean(application) || Object.keys(spec.services).length > 1) && (
                            <Button
                              size="icon"
                              variant="ghost"
                              aria-label={`Remove service ${name}`}
                              disabled={busy}
                              onClick={() => setSpec((previous) => withoutService(previous, name))}
                            >
                              <Icon name="trash" size={14} />
                            </Button>
                          )}
                      </div>
                      <label>
                        Container image
                        <Input
                          placeholder="nginx:1.29-alpine"
                          value={service.image}
                          onChange={(event) => updateService(name, { image: event.target.value })}
                        />
                      </label>
                      <label>
                        Registry credential
                        <SelectField
                          disabled={busy}
                          label="Registry credential"
                          value={service.registry_credential || ''}
                          onValueChange={(value) =>
                            updateService(name, {
                              registry_credential: value || undefined,
                            })
                          }
                          options={[
                            {
                              value: '',
                              label: 'Automatic · public or matching saved credential',
                            },
                            ...(service.registry_credential &&
                            !registries.data?.items.some(
                              (item) => item.name === service.registry_credential,
                            )
                              ? [
                                  {
                                    value: service.registry_credential,
                                    label: `${service.registry_credential} (saved reference)`,
                                  },
                                ]
                              : []),
                            ...(registries.data?.items.map((item) => ({
                              value: item.name,
                              label:
                                item.name +
                                ' · ' +
                                item.registry +
                                (item.synchronized ? '' : ' · pending sync'),
                            })) ?? []),
                          ]}
                        />
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
                          <Input
                            type="number"
                            min={0}
                            max={65535}
                            placeholder="No port"
                            disabled={Boolean(service.job)}
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
                          <SelectField
                            disabled={busy}
                            label="Size"
                            value={service.size || 'small'}
                            onValueChange={(value) => updateService(name, { size: value })}
                            options={[
                              {
                                value: 'small',
                                label: 'Small',
                              },
                              {
                                value: 'medium',
                                label: 'Medium',
                              },
                              {
                                value: 'large',
                                label: 'Large',
                              },
                            ]}
                          />
                        </label>
                        <label>
                          Replicas
                          <Input
                            type="number"
                            min={1}
                            max={20}
                            value={service.replicas ?? 1}
                            disabled={Boolean(service.job)}
                            onChange={(event) =>
                              updateService(name, { replicas: Number(event.target.value) })
                            }
                          />
                        </label>
                      </div>
                      <RunCommandFields
                        label={name}
                        disabled={busy}
                        command={(runtime[name] || runtimeDraft(service)).command}
                        args={(runtime[name] || runtimeDraft(service)).args}
                        onChange={(value) =>
                          setRuntime((previous) => ({
                            ...previous,
                            [name]: { ...(previous[name] || runtimeDraft(service)), ...value },
                          }))
                        }
                      />
                      <EnvironmentFields
                        label={name}
                        disabled={busy}
                        rows={(runtime[name] || runtimeDraft(service)).variables}
                        onChange={(variables) =>
                          setRuntime((previous) => ({
                            ...previous,
                            [name]: { ...(previous[name] || runtimeDraft(service)), variables },
                          }))
                        }
                      />
                      <div className="service-exposure">
                        <label className="checkbox-label">
                          <Input
                            type="checkbox"
                            checked={service.public || false}
                            disabled={Boolean(service.job) || !service.port}
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
                              : service.job
                                ? service.job.schedule
                                  ? `Schedule: ${service.job.schedule.cron} · ${service.job.schedule.timezone || 'UTC'}`
                                  : 'Deployment job · configure timeout and retries in TOML'
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
                      setRuntime((previous) => {
                        const next = { ...previous }
                        delete next[name]
                        return next
                      })
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
          </fieldset>
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
          {plan
            ? 'Only reviewed changes will be submitted'
            : 'Imported secrets are saved at review; containers change on deployment'}
        </span>
        <div className="deploy-footer-actions">
          <Button disabled={busy} onClick={() => (plan ? setPlan(null) : onClose())}>
            {plan ? 'Back to configuration' : 'Cancel'}
          </Button>
          <Button
            variant="primary"
            disabled={
              busy ||
              mode === 'compose' ||
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
      </div>
      <Dialog
        open={editorExpanded}
        onOpenChange={setEditorExpanded}
        className="toml-editor-dialog"
        title="Edit hakopod.toml"
        description="Changes stay in this form until you review and deploy."
        onOpenAutoFocus={(event) => {
          event.preventDefault()
          expandedEditor.current?.focus({ preventScroll: true })
        }}
      >
        <div className="toml-editor-body">
          <Textarea
            ref={expandedEditor}
            aria-label="Expanded TOML configuration"
            className="code-editor"
            value={toml}
            onChange={(event) => setToml(event.target.value)}
            spellCheck={false}
            maxLength={262144}
          />
        </div>
        <div className="dialog-footer toml-editor-footer">
          <span>Escape closes the editor and keeps your changes.</span>
          <Button onClick={() => setEditorExpanded(false)}>Done editing</Button>
        </div>
      </Dialog>
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
