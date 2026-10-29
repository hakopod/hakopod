import { DeploymentSecrets } from './deployment-secrets'
import { ServiceExecutionFields, FunctionEditor } from './service-execution-fields'
import { ResourceFields } from './resource-fields'
import { serviceResources } from '../lib/service-resources'
import { EnvironmentFiles, environmentFilePayload, type EnvironmentFile } from './environment-files'
import { prepareEnvironment, saveEnvironment } from '../lib/save-environment'
import { EnvironmentFields, RunCommandFields } from './runtime-settings-fields'
import { environmentRows, type EnvironmentRow } from '../lib/service-environment'
import { formatProcessCommand, parseProcessCommand } from '../lib/process-command'
import { ComposeImport, type ComposeDraft } from './compose-import'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { useGitConnections } from '../lib/git-connections'
import { withoutService } from '../lib/remove-service'
import { Input } from './ui/input'
import { TOMLEditor, type TOMLEditorHandle } from './toml-editor'
import { SelectField } from './ui/select'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { useEffect, useId, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application, Plan, Service, Spec } from '../lib/types'
import { APIError, message } from '../lib/api'
import { draftKey } from '../lib/drafts'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { specToTOML } from '../lib/toml'
import { actionsNeedTOML, actionsProviderName } from '../lib/actions-provider'
import { FormError, FormPage } from './form-page'
import { ComputeNotice } from './compute-notice'
import { hostedComputeIssues } from '../lib/compute-limits'
import { fieldError } from '../lib/form-errors'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { Note, RequestError } from './shared'
import { ServiceIcon } from './service-icon'
import {
  ChoiceCards,
  FlowSteps,
  IssueCount,
  nameFromImage,
  PendingButton,
  plural,
  serviceIssues,
  serviceKind,
  serviceKindLabel,
  servicePresets,
  type ServiceKind,
} from './create-flow'

const serviceSections = {
  source: 'Image and runtime',
  compute: 'Placement and capacity',
  variables: 'Variables',
  access: 'Access',
} as const
type ServiceSection = keyof typeof serviceSections

type Source = 'repository' | 'image' | 'file'

const kindIcon: Record<ServiceKind, string> = {
  web: 'globe',
  private: 'lock',
  worker: 'terminal',
  job: 'clock',
  function: 'activity',
}

const applicationNamePattern = /^[a-z](?:[a-z0-9-]{0,38}[a-z0-9])?$/

type RuntimeDraft = { command: string; args: string; variables: EnvironmentRow[] }
const runtimeDraft = (service: Service): RuntimeDraft => ({
  command: formatProcessCommand(service.command),
  args: formatProcessCommand(service.args),
  variables: environmentRows(service.env),
})

const newSpec = (): Spec => ({
  schema_version: 1,
  name: '',
  services: { web: { ...servicePresets.web } },
})

export function DeploymentForm({
  onClose,
  application: incomingApplication,
  initialMode = 'form',
  chooseSource = false,
  serviceName,
  removeService,
  addedService,
  draftRevision,
}: {
  onClose: () => void
  application?: Application
  /**
   * The staged draft's revision for an existing application (0 when none is staged).
   * When set, a reviewed change is added to the draft by default; deploying now stays available.
   */
  draftRevision?: number
  initialMode?: 'form' | 'toml' | 'compose'
  // New applications start by choosing where the release comes from.
  chooseSource?: boolean
  serviceName?: string
  removeService?: string
  addedService?: { name: string; service: Service }
}) {
  const applicationSnapshot = useRef(incomingApplication)
  if (applicationSnapshot.current?.id !== incomingApplication?.id)
    applicationSnapshot.current = incomingApplication
  const application = applicationSnapshot.current
  const nativeActions = Object.entries(application?.spec.services || {})
    .filter(([name]) => !serviceName || serviceName === name)
    .map(([, service]) => service.actions)
    .find((actions) => actions && actionsNeedTOML(actions))
  const tomlOnlyReason = nativeActions
    ? `${actionsProviderName(nativeActions)} runner settings are preserved in TOML. Use the runner pool setup for guided provider configuration.`
    : undefined
  const features = useEditionFeatures()
  const scope = useScope()
  const canBuildFromGit =
    !application && (scope.identity.admin || scope.identity.can_manage_git) && features.git
  const connections = useGitConnections()
  const buildConnection = connections.data?.items.some(
    (item) => item.enabled && item.capabilities.builds,
  )
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
  const [toml, setToml] = useState(() => (application ? specToTOML(application.spec) : ''))
  const [envFiles, setEnvFiles] = useState<EnvironmentFile[]>([])
  const [mode, setMode] = useState<'form' | 'toml' | 'compose'>(
    tomlOnlyReason ? 'toml' : initialMode,
  )
  const [step, setStep] = useState<'source' | 'configure'>(
    chooseSource && !application ? 'source' : 'configure',
  )
  const [source, setSource] = useState<Source | ''>('')
  const [fileFormat, setFileFormat] = useState<'compose' | 'toml'>('compose')
  const [nameEdited, setNameEdited] = useState(false)
  const [showIssues, setShowIssues] = useState(false)
  const [issueAttempt, setIssueAttempt] = useState(0)
  const [composeDraft, setComposeDraft] = useState<ComposeDraft | null>(null)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [activeService, setActiveService] = useState(serviceName || addedService?.name || '')
  const [serviceSection, setServiceSection] = useState<ServiceSection>('source')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [editorExpanded, setEditorExpanded] = useState(false)
  const expandedEditor = useRef<TOMLEditorHandle>(null)
  const tomlEditor = useRef<TOMLEditorHandle>(null)
  const focusGeneratedTOML = useRef(false)
  const requestKey = useRef('')
  const editorBody = useRef<HTMLDivElement>(null)
  const applicationHeading = useId()
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
      if (addedService) initial.services[addedService.name] = structuredClone(addedService.service)
      setSpec(initial)
      setActiveService(serviceName || addedService?.name || Object.keys(initial.services)[0] || '')
      setServiceSection('source')
      setRuntime(
        Object.fromEntries(
          Object.entries(initial.services).map(([name, service]) => [name, runtimeDraft(service)]),
        ),
      )
      setPlan(null)
      setEnvFiles([])
      setComposeDraft(null)
      setError('')
      setShowIssues(false)
      setToml(application ? specToTOML(initial) : '')
      setMode(tomlOnlyReason ? 'toml' : initialMode)
    }
  }, [application?.id, initialMode, serviceName, removeService, addedService, tomlOnlyReason])
  useEffect(() => {
    if (!error || mode !== 'form') return
    const match = error.match(/services\.([a-z][a-z0-9-]*)\.([a-z_]+)/)
    if (!match || !spec.services[match[1]]) return
    setActiveService(match[1])
    const field = match[2]
    setServiceSection(
      ['env', 'secrets'].includes(field)
        ? 'variables'
        : ['public', 'port'].includes(field)
          ? 'access'
          : ['image', 'registry_credential', 'command', 'args', 'function'].includes(field)
            ? 'source'
            : 'compute',
    )
  }, [error, mode, spec.services])
  const focusInvalid = () =>
    requestAnimationFrame(() =>
      Array.from(editorBody.current?.querySelectorAll<HTMLElement>('[aria-invalid="true"]') || [])
        .find((control) => control.getClientRects().length > 0)
        ?.focus(),
    )
  useEffect(() => {
    if (!error || mode !== 'form') return
    const frame = focusInvalid()
    return () => cancelAnimationFrame(frame)
  }, [error, mode, activeService, serviceSection])
  // A failed step boundary focuses its first problem once; later service switches do not.
  useEffect(() => {
    if (!issueAttempt) return
    const frame = focusInvalid()
    return () => cancelAnimationFrame(frame)
  }, [issueAttempt])
  const limitIssues = features.hostedCompute
    ? hostedComputeIssues(spec, features.hostedFree, features.hostedStorageGiB)
    : []
  const serviceNames = Object.keys(spec.services)
  const selectedService =
    serviceName || (spec.services[activeService] ? activeService : serviceNames[0])
  const draftOf = (name: string) => runtime[name] || runtimeDraft(spec.services[name])
  const issues = Object.fromEntries(
    serviceNames
      .filter((name) => !serviceName || name === serviceName)
      .map((name) => [name, serviceIssues(spec.services[name], draftOf(name))]),
  )
  const nameIssue =
    !application && !applicationNamePattern.test(spec.name)
      ? spec.name
        ? 'Use 1–40 lowercase letters, numbers and hyphens, starting with a letter.'
        : 'Enter an application name.'
      : ''
  const issueList = [
    ...Object.entries(issues).flatMap(([name, found]) => [
      ...found.source.map((text) => `${name}: ${text}`),
      ...(found.variables
        ? [`${name}: ${plural(found.variables, 'variable needs', 'variables need')} attention.`]
        : []),
    ]),
    ...(nameIssue ? [`Application: ${nameIssue}`] : []),
  ]
  const formSpec = async (): Promise<Spec> => {
    const next = structuredClone(spec)
    // Validate every environment and command draft before writing private
    // values, including fields belonging to later services in the form.
    for (const [name, service] of Object.entries(next.services)) {
      if (serviceName && name !== serviceName) continue
      const draft = runtime[name]
      if (!draft) continue
      try {
        parseProcessCommand(draft.command)
        parseProcessCommand(draft.args)
      } catch (error) {
        setActiveService(name)
        setServiceSection('source')
        throw error
      }
      try {
        prepareEnvironment(
          draft.variables,
          { project, environment, application: next.name },
          service.secrets,
        )
      } catch (error) {
        setActiveService(name)
        setServiceSection('variables')
        throw error
      }
    }
    for (const [name, service] of Object.entries(next.services)) {
      if (serviceName && name !== serviceName) continue
      const draft = runtime[name]
      if (!draft) continue
      const saved = draft.variables.some((row) => row.name || row.value)
        ? await saveEnvironment(
            draft.variables,
            { project, environment, application: next.name },
            service.secrets,
          )
        : { env: {}, secrets: service.secrets }
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
    ...(mode === 'form'
      ? { spec: await formSpec() }
      : { toml, env_files: environmentFilePayload(envFiles) }),
  })

  async function changeMode(next: 'form' | 'toml' | 'compose') {
    if (busy || mode === next || (tomlOnlyReason && next !== 'toml')) return
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
      setEnvFiles([])
      setToml(specToTOML(result.spec))
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
    if (mode === 'form' && issueList.length) {
      // Stop at the step boundary and open the first service task with a problem.
      setShowIssues(true)
      setIssueAttempt((count) => count + 1)
      const [name, found] = Object.entries(issues).find(([, found]) => found.total > 0) || []
      if (name && found) {
        setActiveService(name)
        setServiceSection(found.source.length ? 'source' : 'variables')
      }
      return
    }
    setBusy(true)
    setError('')
    try {
      if (mode === 'form' && limitIssues.length) throw new Error(limitIssues.join('\n'))
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
  // Values that cannot be stored (uploaded env files) must be deployed directly.
  const canStage =
    Boolean(application) && draftRevision !== undefined && !(mode === 'toml' && envFiles.length)
  async function stage() {
    if (!plan || !application || draftRevision === undefined) return
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.PUT('/applications/{id}/draft', {
          params: { path: { id: application.id } },
          // Always the whole configuration: it already starts from the staged draft, while a
          // service-targeted plan merges into the live revision and would drop other staged edits.
          body: {
            ...(mode === 'form' ? { spec: await formSpec() } : { toml }),
            expected_draft_revision: draftRevision,
          },
        }),
      )
      void queryClient.invalidateQueries({ queryKey: draftKey(application.id) })
      onClose()
      void navigate({
        to: '/applications/$applicationId',
        params: { applicationId: application.id },
      })
    } catch (err) {
      setError(
        err instanceof APIError && err.status === 409
          ? 'Someone else changed the staged changes. Go back to the application to review them, then edit again.'
          : message(err),
      )
    } finally {
      setBusy(false)
    }
  }
  async function deploy() {
    if (!plan || plan.missing_secrets?.length) return
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
    setSpec((previous) => {
      const next = {
        ...previous,
        services: { ...previous.services, [name]: { ...previous.services[name], ...changes } },
      }
      // Suggest a name from the first image until the user types their own.
      if (!application && !nameEdited && changes.image !== undefined)
        next.name = nameFromImage(Object.values(next.services).find((s) => s.image)?.image || '')
      return next
    })
  function addService(preset: keyof typeof servicePresets) {
    let name = preset === 'web' ? 'web' : preset === 'private' ? 'api' : 'worker'
    let n = 2
    while (spec.services[name]) name = `${preset === 'private' ? 'api' : preset}-${n++}`
    setActiveService(name)
    setServiceSection('source')
    setRuntime((previous) => {
      const next = { ...previous }
      delete next[name]
      return next
    })
    setSpec((previous) => ({
      ...previous,
      services: { ...previous.services, [name]: { ...servicePresets[preset] } },
    }))
  }

  const isNew = !application
  const reviewCount = plan ? Object.keys(plan.spec.services).length : 0
  const draftCount = serviceName ? 1 : serviceNames.length
  const chosenSource: Source | '' =
    source ||
    (connections.isPending && canBuildFromGit ? '' : buildConnection ? 'repository' : 'image')
  const steps = isNew ? ['Source', 'Services', 'Review'] : ['Configure', 'Review']
  const currentStep = (isNew ? 1 : 0) + (step === 'source' ? -1 : plan ? 1 : 0)
  const maxServices = features.hostedFree ? 1 : features.hostedCompute ? 10 : 20
  const sizeError = selectedService
    ? fieldError(error || limitIssues.join('\n'), `services.${selectedService}.size`)
    : ''
  function continueFromSource() {
    if (chosenSource === 'repository') {
      void navigate({ to: buildConnection ? '/builds/new' : '/applications/import' })
      return
    }
    setMode(chosenSource === 'file' ? fileFormat : 'form')
    setStep('configure')
    setError('')
  }
  const primaryLabel =
    step === 'source'
      ? chosenSource === 'repository'
        ? buildConnection
          ? 'Choose repository'
          : 'Import repository configuration'
        : chosenSource === 'file'
          ? fileFormat === 'compose'
            ? 'Import Compose file'
            : 'Write hakopod.toml'
          : 'Configure services'
      : plan
        ? isNew
          ? `Create ${plural(reviewCount, 'service')}`
          : `Deploy ${serviceName || application.name}`
        : mode === 'form'
          ? `Review ${plural(draftCount, 'service')}`
          : 'Review configuration'
  const pendingLabel = plan
    ? canStage
      ? 'Adding to changes…'
      : isNew
        ? `Creating ${plural(reviewCount, 'service')}…`
        : 'Deploying…'
    : mode === 'form'
      ? `Checking ${plural(draftCount, 'service')}…`
      : 'Checking configuration…'
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
      title={
        plan
          ? isNew
            ? `Review ${plan.spec.name}`
            : 'Review your deployment'
          : application
            ? `Configure ${serviceName ? `${application.name} / ${serviceName}` : application.name}`
            : step === 'source'
              ? 'New application'
              : mode === 'form'
                ? 'Configure services'
                : 'Import configuration'
      }
      description={
        plan
          ? 'Review the exact changes before creating a new application revision.'
          : `${application ? 'Change' : 'Create'} an application in ${project} / ${environment}.`
      }
    >
      <FlowSteps
        steps={steps}
        current={currentStep}
        disabled={busy}
        onSelect={(index) => {
          if (isNew && index === 0) {
            setPlan(null)
            setStep('source')
          } else setPlan(null)
        }}
      />
      <div ref={editorBody} className="form-body deploy-body create-flow">
        <ComputeNotice creatingApplication={!application} />
        {tomlOnlyReason && <Note>{tomlOnlyReason}</Note>}
        {serviceName && !plan && (
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
        {step === 'source' ? (
          <SourceStep
            source={chosenSource}
            onSource={setSource}
            fileFormat={fileFormat}
            onFileFormat={setFileFormat}
            repository={
              !canBuildFromGit
                ? features.git
                  ? 'Needs Git management access. Ask an administrator, or deploy a container image.'
                  : 'Git repositories are not available in this dashboard.'
                : ''
            }
            buildConnection={
              connections.isPending
                ? undefined
                : connections.error
                  ? null
                  : Boolean(buildConnection)
            }
          />
        ) : plan ? (
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
                <strong>{reviewCount}</strong>
              </div>
            </div>
            {reviewCount === 0 && (
              <Note>
                This removes every service and stops all application traffic. Persistent volumes and
                backups are retained. Delete the empty application only after this deployment
                succeeds.
              </Note>
            )}
            <ReviewServices plan={plan} only={serviceName} />
            <div className="section-caption">
              CONFIGURATION CHANGES <span>{plan.changes.length}</span>
            </div>
            <DeploymentSecrets
              plan={plan}
              project={project}
              environment={environment}
              busy={busy}
              onBusy={setBusy}
              onChange={(missing) =>
                setPlan((current) => (current ? { ...current, missing_secrets: missing } : current))
              }
            />
            <DiffTable changes={plan.changes} />
            {plan.warnings?.map((warning, i) => (
              <Note key={i}>{warning}</Note>
            ))}
            <Note>
              {isNew ? 'Nothing is created until you choose Create. ' : ''}
              Readiness gates traffic to new replicas. Multi-service rollouts are not atomic;
              deployment details report each service’s result.
            </Note>
          </>
        ) : (
          <fieldset disabled={busy} className="m-0 min-w-0 border-0 p-0">
            {!tomlOnlyReason && (
              <div
                className="segmented-control deployment-methods"
                data-method-count={serviceName ? 2 : 3}
                role="group"
                aria-label="Edit as"
              >
                <button
                  type="button"
                  disabled={busy}
                  aria-pressed={mode === 'form'}
                  className={mode === 'form' ? 'selected' : ''}
                  onClick={() => void changeMode('form')}
                >
                  <Icon name="box" size={15} />
                  Container images
                </button>
                <button
                  type="button"
                  disabled={busy}
                  aria-pressed={mode === 'toml'}
                  className={mode === 'toml' ? 'selected' : ''}
                  onClick={() => void changeMode('toml')}
                >
                  <Icon name="code" size={15} />
                  TOML
                </button>
                {!serviceName && (
                  <button
                    type="button"
                    disabled={busy}
                    aria-pressed={mode === 'compose'}
                    className={mode === 'compose' ? 'selected' : ''}
                    onClick={() => void changeMode('compose')}
                  >
                    <Icon name="code" size={15} />
                    Compose import
                  </button>
                )}
              </div>
            )}
            {mode === 'compose' ? (
              <ComposeImport
                onBusyChange={setBusy}
                application={application}
                project={project}
                environment={environment}
                name={spec.name}
                onUse={(draft) => {
                  setEnvFiles([])
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
                  <span>hakopod.toml</span>
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
                <TOMLEditor
                  ref={tomlEditor}
                  label="TOML configuration"
                  value={toml}
                  onChange={setToml}
                  disabled={busy}
                />
                <p className="field-help">
                  {tomlOnlyReason
                    ? 'Review the saved provider, target, image and secret references here.'
                    : 'Configure networks, private ports, peer access, mounts and filesystem permissions here alongside images, health checks and secret references. Switching to the form validates and keeps these settings.'}
                </p>
                <p className="field-help">
                  Use env_file = ".env" above the service tables to share a file across services, or
                  inside a service table for that service.
                </p>
                <EnvironmentFiles
                  files={envFiles}
                  onChange={(files) => {
                    setEnvFiles(files)
                    setPlan(null)
                  }}
                  disabled={busy}
                  onBusyChange={setBusy}
                />
              </div>
            ) : (
              <div className="field-stack">
                {application && serviceNames.length === 0 && (
                  <Note>
                    Deploying this revision removes every service and stops application traffic.
                    Persistent volumes and backups are retained. Once deployment succeeds, you can
                    delete the empty application.
                  </Note>
                )}
                {limitIssues.length > 0 && (
                  <div role="alert" className="field-error">
                    <p>
                      This configuration exceeds your hosted compute allocation. Edit the settings
                      or connect your own server. No values have been changed.
                    </p>
                    <ul>
                      {limitIssues.map((issue) => (
                        <li key={issue}>{issue}</li>
                      ))}
                    </ul>
                  </div>
                )}
                {!serviceName && (
                  <section className="create-section" aria-label="Services in this draft">
                    <div className="create-section-heading">
                      <h2>Services</h2>
                      <span>
                        {plural(serviceNames.length, 'service')} · private network included
                      </span>
                    </div>
                    <div className="service-picker">
                      {Object.entries(spec.services).map(([name, service]) => {
                        const kind = serviceKind(service)
                        return (
                          <button
                            key={name}
                            type="button"
                            className="service-card interactive"
                            aria-pressed={selectedService === name}
                            onClick={() => {
                              setActiveService(name)
                              setServiceSection('source')
                            }}
                          >
                            <Brackets />
                            <span className="service-card-icon">
                              <Icon name={kindIcon[kind]} size={16} />
                            </span>
                            <span className="service-card-text">
                              <strong>{name}</strong>
                              <span>{serviceKindLabel[kind]}</span>
                              <small>
                                {service.image ? service.image.split('@')[0] : 'Image needed'} ·{' '}
                                {service.size || 'small'} ·{' '}
                                {service.serverless
                                  ? '0–1 replicas'
                                  : plural(service.replicas ?? 1, 'replica')}
                              </small>
                            </span>
                            <IssueCount count={issues[name]?.total ?? 0} />
                          </button>
                        )
                      })}
                    </div>
                    <div className="service-presets" role="group" aria-label="Add service">
                      {(
                        [
                          ['web', 'Web service'],
                          ['private', 'Private service'],
                          ['worker', 'Worker'],
                        ] as const
                      ).map(([preset, label]) => (
                        <Button
                          key={preset}
                          variant="ghost"
                          size="sm"
                          disabled={busy || serviceNames.length >= maxServices}
                          onClick={() => addService(preset)}
                        >
                          <Icon name="plus" size={14} />
                          {label}
                        </Button>
                      ))}
                    </div>
                    {features.hostedFree && (
                      <p className="field-help">
                        Hosted Free allows one small service with one replica, using an AMD64 image.
                        More services and larger sizes require your own server.
                      </p>
                    )}
                  </section>
                )}
                {selectedService && spec.services[selectedService] && (
                  <ServiceEditor
                    key={selectedService}
                    name={selectedService}
                    service={spec.services[selectedService]}
                    draft={draftOf(selectedService)}
                    issues={issues[selectedService]}
                    showIssues={showIssues}
                    section={serviceSection}
                    onSection={setServiceSection}
                    removable={!serviceName && (Boolean(application) || serviceNames.length > 1)}
                    onRemove={() => {
                      const next = withoutService(spec, selectedService)
                      setSpec(next)
                      setActiveService(Object.keys(next.services)[0] || '')
                      setServiceSection('source')
                    }}
                    busy={busy}
                    error={error}
                    sizeError={sizeError}
                    application={spec.name}
                    project={project}
                    environment={environment}
                    registries={registries}
                    onChange={(changes) => updateService(selectedService, changes)}
                    onDraft={(changes) =>
                      setRuntime((previous) => ({
                        ...previous,
                        [selectedService]: { ...draftOf(selectedService), ...changes },
                      }))
                    }
                    onStarter={(value) => {
                      updateService(selectedService, value)
                      setRuntime((previous) => ({
                        ...previous,
                        [selectedService]: runtimeDraft({
                          ...spec.services[selectedService],
                          ...value,
                        }),
                      }))
                    }}
                  />
                )}
                {!serviceName && (
                  <section className="create-section" aria-labelledby={applicationHeading}>
                    <div className="create-section-heading">
                      <h2 id={applicationHeading}>Application</h2>
                      <span>Shared by every service</span>
                    </div>
                    <div className="create-fields">
                      {isNew && (
                        <label>
                          Application name
                          <Input
                            value={spec.name}
                            error={
                              fieldError(error, 'name') || (showIssues ? nameIssue : undefined)
                            }
                            onChange={(event) => {
                              setNameEdited(true)
                              setSpec((previous) => ({ ...previous, name: event.target.value }))
                            }}
                            pattern="[a-z][a-z0-9\-]*"
                            maxLength={40}
                            autoComplete="off"
                            spellCheck={false}
                            aria-describedby="application-name-help"
                          />
                          <span id="application-name-help" className="field-help">
                            Lowercase letters, numbers and hyphens. Suggested from the first image
                            until you edit it.
                          </span>
                        </label>
                      )}
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
                            {
                              value: 'safe',
                              label: 'Restore the last successful stateless release',
                            },
                            { value: 'disabled', label: 'Disabled' },
                          ]}
                        />
                        <span id="release-recovery-help" className="field-help">
                          Skips jobs, volumes and service additions or removals. Restores workload
                          configuration only; external data and secret values are unchanged.
                        </span>
                      </label>
                    </div>
                  </section>
                )}
              </div>
            )}
          </fieldset>
        )}
        {showIssues && mode === 'form' && step === 'configure' && !plan && issueList.length > 0 && (
          <FormError className="create-issues">
            <span>Resolve {plural(issueList.length, 'issue')} before review:</span>
            <ul>
              {issueList.map((issue) => (
                <li key={issue}>{issue}</li>
              ))}
            </ul>
          </FormError>
        )}
        {error && <RequestError error={error} />}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {step === 'source'
            ? 'Nothing is created until the final review'
            : plan
              ? 'Only reviewed changes will be submitted'
              : tomlOnlyReason
                ? 'Review validates the configuration before deployment'
                : 'Secrets are saved at review or when switching to TOML; containers change on deployment'}
        </span>
        <div className="deploy-footer-actions">
          <Button
            disabled={busy}
            onClick={() =>
              plan ? setPlan(null) : isNew && step === 'configure' ? setStep('source') : onClose()
            }
          >
            {plan
              ? 'Back to configuration'
              : isNew && step === 'configure'
                ? 'Back to source'
                : 'Cancel'}
          </Button>
          {plan && canStage && (
            <Button
              disabled={busy || Boolean(plan.missing_secrets?.length)}
              onClick={() => void deploy()}
            >
              Deploy now
            </Button>
          )}
          <PendingButton
            variant="primary"
            pending={busy && step === 'configure'}
            pendingLabel={step === 'source' ? primaryLabel : pendingLabel}
            disabled={
              busy ||
              (step === 'source' && !chosenSource) ||
              (step === 'configure' &&
                ((Boolean(plan?.missing_secrets?.length) && !canStage) ||
                  mode === 'compose' ||
                  (!plan && mode === 'toml' && !toml.trim())))
            }
            onClick={() =>
              step === 'source'
                ? continueFromSource()
                : void (plan ? (canStage ? stage() : deploy()) : review())
            }
          >
            {plan && canStage ? 'Add to changes' : primaryLabel}
            <Icon name="arrow" size={15} />
          </PendingButton>
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
          expandedEditor.current?.focus()
        }}
      >
        <div className="toml-editor-body">
          <TOMLEditor
            ref={expandedEditor}
            label="Expanded TOML configuration"
            value={toml}
            onChange={setToml}
            expanded
            disabled={busy}
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

function SourceStep({
  source,
  onSource,
  fileFormat,
  onFileFormat,
  repository,
  buildConnection,
}: {
  source: Source | ''
  onSource: (source: Source) => void
  fileFormat: 'compose' | 'toml'
  onFileFormat: (format: 'compose' | 'toml') => void
  // Why repositories are unavailable to this person; empty when they are available.
  repository: string
  // undefined while loading, null when connections could not be loaded.
  buildConnection: boolean | null | undefined
}) {
  return (
    <div className="create-source">
      <ChoiceCards
        label="Where does this application come from?"
        value={source}
        onChange={onSource}
        choices={[
          {
            value: 'repository',
            title: 'Git repository',
            description: 'Build an image from your code, or deploy the hakopod.toml it contains.',
            icon: <Icon name="branch" size={18} />,
            disabled: Boolean(repository),
            unavailable: repository,
          },
          {
            value: 'image',
            title: 'Container image',
            description:
              'Run one or more existing images, pinned by digest for repeatable releases.',
            icon: <Icon name="box" size={18} />,
          },
          {
            value: 'file',
            title: 'Compose or TOML file',
            description: 'Convert a Compose file, or paste a Hakopod configuration.',
            icon: <Icon name="code" size={18} />,
          },
        ]}
      />
      {source === 'repository' && (
        <div className="create-source-detail" aria-live="polite">
          {buildConnection === undefined ? (
            <p className="field-help" role="status">
              Checking Git connections…
            </p>
          ) : buildConnection ? (
            <>
              <p>
                Pick a repository and branch next. Hakopod detects the framework, or builds your
                Dockerfile, then reviews the release.
              </p>
              <Button asChild variant="ghost" size="sm" className="justify-self-start">
                <Link to="/applications/import">Use a committed hakopod.toml instead</Link>
              </Button>
            </>
          ) : (
            <>
              <p>
                {buildConnection === null
                  ? 'Git connections could not be loaded, so builds are unavailable right now.'
                  : 'No Git connection can run builds yet.'}{' '}
                You can still import a committed hakopod.toml, or connect GitHub or GitLab first.
              </p>
              <Button asChild variant="ghost" size="sm" className="justify-self-start">
                <Link to="/settings/git/connections/new">
                  <ServiceIcon name="github" size={15} />
                  Connect a repository
                </Link>
              </Button>
            </>
          )}
        </div>
      )}
      {source === 'image' && (
        <div className="create-source-detail">
          <p>
            Next you add services and choose each image, placement and variables. Use a digest such
            as <code>nginx@sha256:…</code> so every release runs the same bytes; tags are resolved
            to a digest at deployment.
          </p>
        </div>
      )}
      {source === 'file' && (
        <div className="create-source-detail">
          <ChoiceCards
            compact
            label="File format"
            value={fileFormat}
            onChange={onFileFormat}
            choices={[
              {
                value: 'compose',
                title: 'Docker Compose',
                description: 'Converted to services you can review. Unsupported keys are reported.',
              },
              {
                value: 'toml',
                title: 'hakopod.toml',
                description: 'Networks, mounts and ports beyond the guided form.',
              },
            ]}
          />
        </div>
      )}
    </div>
  )
}

function ServiceEditor({
  name,
  service,
  draft,
  issues,
  showIssues,
  section,
  onSection,
  removable,
  onRemove,
  busy,
  error,
  sizeError,
  application,
  project,
  environment,
  registries,
  onChange,
  onDraft,
  onStarter,
}: {
  name: string
  service: Service
  draft: RuntimeDraft
  issues?: ReturnType<typeof serviceIssues>
  showIssues: boolean
  section: ServiceSection
  onSection: (section: ServiceSection) => void
  removable: boolean
  onRemove: () => void
  busy: boolean
  error: string
  sizeError?: string
  application: string
  project: string
  environment: string
  registries: {
    data?: { items: { name: string; registry: string; synchronized: boolean }[] }
    error: unknown
  }
  onChange: (changes: Partial<Service>) => void
  onDraft: (changes: Partial<RuntimeDraft>) => void
  onStarter: (value: Service) => void
}) {
  const features = useEditionFeatures()
  const kind = serviceKind(service)
  const panel = useId()
  const sectionIssues: Partial<Record<ServiceSection, number>> = {
    source: issues?.source.length,
    variables: issues?.variables,
  }
  return (
    <section className="service-form create-service-editor" aria-label={`${name} settings`}>
      <div className="service-form-heading">
        <div className="service-mini-icon">
          <Icon name={kindIcon[kind]} size={16} />
        </div>
        <span className="min-w-0">
          <strong className="mono">{name}</strong>
          <small className="block text-muted-foreground">{serviceKindLabel[kind]}</small>
        </span>
        <span className="form-spacer" />
        {removable && (
          <Button
            size="icon"
            variant="ghost"
            aria-label={`Remove service ${name}`}
            disabled={busy}
            onClick={onRemove}
          >
            <Icon name="trash" size={14} />
          </Button>
        )}
      </div>
      <div className="service-tasks" role="group" aria-label={`${name} tasks`}>
        {(Object.keys(serviceSections) as ServiceSection[]).map((key) => (
          <button
            key={key}
            type="button"
            aria-pressed={section === key}
            aria-controls={panel}
            onClick={() => onSection(key)}
          >
            {serviceSections[key]}
            <IssueCount count={sectionIssues[key] ?? 0} />
          </button>
        ))}
      </div>
      <div id={panel} className="create-fields">
        {section === 'source' && (
          <>
            <FunctionEditor service={service} disabled={busy} onChange={onChange} />
            <label>
              Container image
              <Input
                placeholder="nginx@sha256:… or nginx:1.29-alpine"
                value={service.image}
                autoComplete="off"
                spellCheck={false}
                error={
                  fieldError(error, `services.${name}.image`) ||
                  (showIssues && !service.image.trim() ? 'Enter a container image.' : undefined)
                }
                onChange={(event) => onChange({ image: event.target.value })}
              />
            </label>
            <label>
              Registry credential
              <SelectField
                disabled={busy}
                label="Registry credential"
                value={service.registry_credential || ''}
                error={fieldError(error, `services.${name}.registry_credential`)}
                onValueChange={(value) => onChange({ registry_credential: value || undefined })}
                options={[
                  { value: '', label: 'Automatic · public or matching saved credential' },
                  ...(service.registry_credential &&
                  !registries.data?.items.some((item) => item.name === service.registry_credential)
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
              {Boolean(registries.error) && (
                <span className="field-help">
                  Registry credentials could not be loaded. Existing references are preserved.
                </span>
              )}
            </label>
            <RunCommandFields
              label={name}
              disabled={busy}
              command={draft.command}
              args={draft.args}
              error={error}
              fieldPath={`services.${name}`}
              onChange={onDraft}
            />
          </>
        )}
        {section === 'compute' && (
          <>
            <div className="grid gap-1.5">
              <span className="create-label">Size</span>
              <ChoiceCards
                compact
                label={`${name} size`}
                value={service.size || 'small'}
                disabled={busy}
                onChange={(size) => onChange({ size })}
                choices={[
                  { value: 'small', title: 'Small', description: 'Default profile' },
                  {
                    value: 'medium',
                    title: 'Medium',
                    description: 'Larger profile',
                    disabled: features.hostedFree,
                    unavailable: 'Requires your own server',
                  },
                  {
                    value: 'large',
                    title: 'Large',
                    description: 'Largest profile',
                    disabled: features.hostedFree,
                    unavailable: 'Requires your own server',
                  },
                  ...(service.size && !['small', 'medium', 'large'].includes(service.size)
                    ? [{ value: service.size, title: service.size, description: 'Saved profile' }]
                    : []),
                ]}
              />
              {sizeError ? (
                <p className="field-error" role="alert">
                  {sizeError}
                </p>
              ) : (
                <p className="field-help">
                  Profiles are defined by this installation. The review shows the exact CPU and
                  memory each one requests.
                </p>
              )}
            </div>
            <label>
              Replicas
              <Input
                type="number"
                min={1}
                max={features.hostedFree ? 1 : features.hostedCompute ? 3 : 20}
                value={service.replicas ?? 1}
                error={fieldError(error, `services.${name}.replicas`)}
                disabled={Boolean(service.job || service.serverless)}
                onChange={(event) => onChange({ replicas: Number(event.target.value) })}
              />
            </label>
            <ServiceExecutionFields
              name={name}
              service={service}
              project={project}
              environment={environment}
              application={application}
              disabled={busy}
              error={error}
              onChange={onChange}
              onStarter={onStarter}
            />
            <ResourceFields
              name={name}
              resources={service.resources}
              disabled={busy}
              hostedFree={features.hostedFree}
              error={error}
              onChange={(resources) => onChange({ resources })}
            />
          </>
        )}
        {section === 'variables' && (
          <EnvironmentFields
            label={name}
            error={fieldError(error, `services.${name}.env`)}
            disabled={busy}
            showIssues={showIssues}
            rows={draft.variables}
            onChange={(variables) => onDraft({ variables })}
          />
        )}
        {section === 'access' && (
          <>
            <label>
              Port
              <Input
                type="number"
                min={0}
                max={65535}
                placeholder="No port"
                disabled={Boolean(service.job)}
                value={service.port || ''}
                error={fieldError(error, `services.${name}.port`)}
                aria-describedby={`${panel}-port`}
                onChange={(event) =>
                  onChange({
                    port: Number(event.target.value) || 0,
                    ...(!Number(event.target.value) ? { public: false } : {}),
                  })
                }
              />
              <span id={`${panel}-port`} className="field-help">
                The port your server listens on. Leave empty for a background worker.
              </span>
            </label>
            <div className="service-exposure">
              <label className="checkbox-label">
                <Input
                  type="checkbox"
                  checked={service.public || false}
                  disabled={Boolean(service.job) || !service.port}
                  onChange={(event) => onChange({ public: event.target.checked })}
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
            <p className="field-help">
              Private services discover each other by name, such as <code>http://api:8080</code>. A
              public web service can proxy browser requests to a private API.
            </p>
          </>
        )}
      </div>
    </section>
  )
}

function ReviewServices({ plan, only }: { plan: Plan; only?: string }) {
  return (
    <div className="review-services">
      {Object.entries(plan.spec.services)
        .filter(([name]) => !only || name === only)
        .map(([name, service]) => {
          const profile = serviceResources(service, plan.resource_profiles)
          const kind = serviceKind(service)
          const secrets = Object.keys(service.secrets || {}).length
          const variables = Object.keys(service.env || {}).length
          return (
            <section key={name} className="review-service" aria-label={`${name} review`}>
              <header>
                <Icon name={kindIcon[kind]} size={15} />
                <strong className="min-w-0 break-words">{name}</strong>
                <span>{serviceKindLabel[kind]}</span>
              </header>
              <dl>
                <div>
                  <dt>Placement</dt>
                  <dd>{service.node_name || 'Automatic · any eligible node'}</dd>
                </div>
                <div>
                  <dt>Capacity</dt>
                  <dd>
                    {service.job?.schedule
                      ? 'Scheduled job'
                      : service.job
                        ? 'Deployment job'
                        : service.serverless
                          ? service.serverless.min_replicas === 1
                            ? '1 always-warm replica'
                            : '0–1 replicas'
                          : plural(service.replicas ?? 1, 'replica')}
                    {profile &&
                      ` · ${profile.CPURequest} CPU / ${profile.MemoryRequest} memory requested · limits ${profile.CPULimit} CPU / ${profile.MemoryLimit} per ${service.job ? 'attempt' : 'replica'}`}
                  </dd>
                </div>
                <div>
                  <dt>External access</dt>
                  <dd>
                    {service.public
                      ? `Public HTTP · generated URL · port ${service.port}`
                      : service.port
                        ? `Private · port ${service.port}`
                        : 'None'}
                    {service.public_tcp?.length
                      ? ` · ${plural(service.public_tcp.length, 'public TCP port')}`
                      : ''}
                  </dd>
                </div>
                <div>
                  <dt>Bindings</dt>
                  <dd>
                    {plural(variables, 'variable')} · {plural(secrets, 'secret reference')}
                  </dd>
                </div>
                <div>
                  <dt>Startup</dt>
                  <dd>
                    {service.serverless
                      ? `Serverless HTTP · ${service.serverless.min_replicas === 1 ? 'always warm' : `sleeps after ${service.serverless.idle_seconds ?? 300}s idle`} · ${service.serverless.max_concurrency ?? 16} concurrent requests · startup wait ${service.serverless.startup_timeout_seconds ?? 60}s · request limit ${service.serverless.request_timeout_seconds ?? 60}s`
                      : service.startup_timeout_seconds
                        ? `Startup wait ${service.startup_timeout_seconds}s`
                        : 'Installation startup wait'}
                  </dd>
                </div>
              </dl>
            </section>
          )
        })}
    </div>
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
