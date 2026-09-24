import { useEditionFeatures } from '../lib/dashboard-edition'
import { buildSteps, buildErrorStep, validateVisibleFields } from '../lib/build-onboarding'
import { Icon } from './icons'
import { ComputeNotice } from './compute-notice'
import { fieldError, fieldGroupError } from '../lib/form-errors'
import { useGitConnections, useGitProviderSetup } from '../lib/git-connections'
import { GitRepositoryField } from './git-repository-field'
import { saveEnvironment } from '../lib/save-environment'
import { EnvironmentFields } from './runtime-settings-fields'
import { environmentRows, isSecretEnvironment, splitEnvironment } from '../lib/service-environment'
import { formatProcessCommand, parseProcessCommand } from '../lib/process-command'
import { GitDeploymentPaths } from './git-deployment-paths'
import {
  FrameworkBuildFields,
  defaultFrameworkPlan,
  frameworkLabel,
} from './framework-build-fields'
import { formatBuildSecrets, parseBuildSecrets } from '../lib/build-secrets'
import { GitConnectionField } from './git-connection-field'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { formatBuildArgs, parseBuildArgs } from '../lib/build-args'
import { SelectField } from './ui/select'
import { useEffect, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { FormPage, FormHint, FormSection } from './form-page'
import { Note, RequestError } from './shared'

type Build = components['schemas']['BuildConfig']
export default function BuildForm({
  build,
  application,
  onClose,
}: {
  build?: Build
  application?: Application
  onClose: () => void
}) {
  const scope = useScope()
  const features = useEditionFeatures()
  const [step, setStep] = useState(0)
  const [furthest, setFurthest] = useState(0)
  const stepHeading = useRef<HTMLDivElement>(null)
  const formRef = useRef<HTMLFormElement>(null)
  const showStep = (next: number) => {
    setStep(next)
    setFurthest((previous) => Math.max(previous, next))
  }
  const gitSetup = useGitProviderSetup()
  const gitConnections = useGitConnections()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const project = build?.project || application?.project || scope.project
  const environment = build?.environment || application?.environment || scope.environment
  const [name, setName] = useState(build?.name || application?.name || '')
  const [service, setService] = useState(
    build?.service || Object.keys(application?.spec.services || {})[0] || 'web',
  )
  const [reuseServices, setReuseServices] = useState<string[]>(build?.reuse_services || [])
  const [provider, setProvider] = useState<'github' | 'gitlab'>(build?.provider || 'github')
  const [connectionId, setConnectionId] = useState(build?.connection_id || '')
  const [repository, setRepository] = useState(build?.repository || '')
  const [branch, setBranch] = useState(build?.branch || 'main')
  const [mode, setMode] = useState<Build['mode']>(build?.mode || 'framework')
  const [framework, setFramework] = useState(build?.framework || defaultFrameworkPlan)
  const [buildSecrets, setBuildSecrets] = useState(() => formatBuildSecrets(build?.build_secrets))
  const boundApplicationId = application?.id || build?.application_id
  const linkedApp = useQuery({
    queryKey: ['application', boundApplicationId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}', { signal, params: { path: { id: boundApplicationId! } } }),
      ),
    enabled: Boolean(boundApplicationId),
    initialData: application,
  })
  const reuseApplication = linkedApp.data || application
  const [detecting, setDetecting] = useState(false)
  const [detectionError, setDetectionError] = useState('')
  const [suggestion, setSuggestion] = useState<{
    source: string
    result: components['schemas']['BuildDetection']
  } | null>(null)
  const [preset, setPreset] = useState<Build['preset']>(build?.preset || 'auto')
  const [architecture, setArchitecture] = useState<Build['architecture'] | ''>(
    build?.architecture || (features.hostedCompute ? 'amd64' : ''),
  )
  const [context, setContext] = useState(build?.context_path || '.')
  const [buildArgs, setBuildArgs] = useState(() => formatBuildArgs(build?.build_args))
  const [runtimeMode, setRuntimeMode] = useState<'preserve' | 'default' | 'override'>(() => {
    if (build?.command?.length || build?.args?.length) return 'override'
    if (build?.command !== undefined || build?.args !== undefined) return 'default'
    return boundApplicationId ? 'preserve' : 'default'
  })
  const [runtimeCommand, setRuntimeCommand] = useState(() => formatProcessCommand(build?.command))
  const [runtimeEnvEnabled, setRuntimeEnvEnabled] = useState(
    build?.env !== undefined || !boundApplicationId,
  )
  const [runtimeEnv, setRuntimeEnv] = useState(() => environmentRows(build?.env))
  const [runtimeArgs, setRuntimeArgs] = useState(() => formatProcessCommand(build?.args))
  const [dockerfile, setDockerfile] = useState(build?.dockerfile || 'Dockerfile')
  const [port, setPort] = useState(build?.port || defaultFrameworkPlan.port)
  const [isPublic, setPublic] = useState(build?.public || false)
  const [size, setSize] = useState(build?.size || 'small')
  const [registry, setRegistry] = useState(build?.registry_credential || '')
  const [automatic, setAutomatic] = useState(build?.auto_build || false)
  const [autoDeploy, setAutoDeploy] = useState(build?.auto_deploy || false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const registries = useQuery({
    queryKey: ['registries', project, environment],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/registries', { signal, params: { query: { project, environment } } })),
    gcTime: 0,
  })
  const managedRegistryAvailable =
    provider === 'github' &&
    gitSetup.data?.managed_registry_available &&
    gitConnections.data?.items.some(
      (item) =>
        item.id === connectionId &&
        item.auth_kind === 'github_app' &&
        item.enabled &&
        item.configured,
    )
  const linked = Boolean(build?.application_id || application)
  const sourceFingerprint = JSON.stringify([
    boundApplicationId,
    project,
    environment,
    name,
    service,
    provider,
    connectionId,
    repository,
    branch,
    context,
  ])
  const currentSource = useRef(sourceFingerprint)
  currentSource.current = sourceFingerprint
  const recipeEdited = useRef(Boolean(build))
  const lastDetectedSource = useRef('')
  const detection = suggestion?.source === sourceFingerprint ? suggestion.result : undefined
  const canDetect = Boolean(
    name &&
    repository &&
    branch &&
    (scope.identity.admin || scope.identity.can_manage_git || boundApplicationId),
  )
  const applyDetection = (result: components['schemas']['BuildDetection']) => {
    setMode(result.mode)
    if (result.framework) {
      setFramework({ ...result.framework })
      setPort(result.framework.port)
    }
    if (result.dockerfile) setDockerfile(result.dockerfile)
    recipeEdited.current = true
  }
  const detectRepository = async (applyInitial = false) => {
    if (!canDetect) return
    const source = sourceFingerprint
    setDetecting(true)
    setDetectionError('')
    setSuggestion(null)
    try {
      const result = await unwrap(
        client.POST('/builds/detect', {
          body: {
            project,
            environment,
            name,
            service,
            provider,
            connection_id: connectionId,
            repository,
            branch,
            context_path: context,
            application_id: boundApplicationId,
            mode: 'dockerfile',
          },
        }),
      )
      if (source !== currentSource.current) return
      lastDetectedSource.current = source
      setSuggestion({ source, result })
      if (applyInitial && !recipeEdited.current) applyDetection(result)
    } catch (cause) {
      if (source === currentSource.current) setDetectionError(message(cause))
    } finally {
      setDetecting(false)
    }
  }
  useEffect(() => {
    if (error) {
      const invalid = formRef.current?.querySelector<HTMLElement>(
        '[aria-invalid="true"]:not(:disabled)',
      )
      if (invalid) {
        let details = invalid.closest('details')
        while (details) {
          details.open = true
          details = details.parentElement?.closest('details') || null
        }
        invalid.focus()
      }
    } else if (!build) stepHeading.current?.focus()
  }, [step, build, error])
  const fail = (cause: unknown) => {
    const text = message(cause)
    if (!build) {
      const fieldStep = buildErrorStep(text, service)
      if (fieldStep !== undefined) showStep(fieldStep)
    }
    setError(text)
  }
  const parseField = <T,>(path: string, read: () => T): T => {
    try {
      return read()
    } catch (cause) {
      throw new Error(`${path}: ${message(cause)}`)
    }
  }
  const validateDraft = () => {
    parseField('build_args', () => parseBuildArgs(buildArgs))
    parseField('build_secrets', () => parseBuildSecrets(buildSecrets))
    if (mode === 'buildpacks' && buildSecrets.trim())
      throw new Error(
        'build_secrets: Buildpacks do not support secret mounts. Remove the references or choose another build method.',
      )
    if (features.hostedCompute && architecture && architecture !== 'amd64')
      throw new Error(
        'architecture: Hosted Free runs AMD64 images. Choose AMD64 or connect your own server.',
      )
    if (features.hostedFree && !linked && size !== 'small')
      throw new Error(
        'size: Hosted Free supports the small profile. Choose small or connect your own server.',
      )
    if (runtimeMode === 'override') {
      parseField('command', () => parseProcessCommand(runtimeCommand))
      parseField('args', () => parseProcessCommand(runtimeArgs))
    }
    if (runtimeEnvEnabled) parseField('env', () => splitEnvironment(runtimeEnv))
  }
  const runtimeVariables = runtimeEnv.filter(
    (row) => (row.name || row.value) && !isSecretEnvironment(row),
  ).length
  const runtimeSecrets = runtimeEnv.filter(
    (row) => (row.name || row.value) && isSecretEnvironment(row),
  ).length
  return (
    <FormPage
      breadcrumbs={[
        { label: 'Source builds', to: '/builds' },
        ...(build ? [{ label: build.name, to: `/builds/${build.id}` }] : []),
        { label: build ? 'Edit build' : 'New source build' },
      ]}
      icon="branch"
      help={
        <>
          {(build || step > 0) && (
            <FormHint title="Build summary">
              <dl className="grid min-w-0 gap-3 text-sm [&_dd]:[overflow-wrap:anywhere] [&_dt]:text-[var(--muted)]">
                <div>
                  <dt>Repository</dt>
                  <dd>
                    {repository} · {branch}
                  </dd>
                </div>
                <div>
                  <dt>Build directory</dt>
                  <dd>
                    <code>{context}</code>
                  </dd>
                </div>
                <div>
                  <dt>Recipe</dt>
                  <dd>
                    {mode === 'framework'
                      ? frameworkLabel(framework.framework)
                      : mode === 'dockerfile'
                        ? 'Dockerfile'
                        : 'Cloud Native Buildpacks'}
                  </dd>
                </div>
                <div>
                  <dt>Build location</dt>
                  <dd>{provider === 'github' ? 'GitHub Actions' : 'GitLab CI'}</dd>
                </div>
                <div>
                  <dt>Deployment</dt>
                  <dd>
                    {linked
                      ? 'Keep linked service resources and networking'
                      : 'Deploy a verified build to create the application'}
                  </dd>
                </div>
              </dl>
            </FormHint>
          )}
          <FormHint title="Keep builds in Git">
            The reviewed workflow runs in your provider account. Hakopod deploys its verified image
            digest.
          </FormHint>
          <FormHint title="Private images">
            {managedRegistryAvailable
              ? 'Hakopod can supply the private registry and scoped pull credentials. Leave Registry on its automatic option to use it.'
              : 'For private images in an external registry, save a credential with pull access or select an existing credential.'}
          </FormHint>
        </>
      }
      title={build ? 'Edit source build' : 'Build an application from source'}
      description={`${project} / ${environment} · Build with a framework recipe, Dockerfile or Cloud Native Buildpacks.`}
    >
      {!build && (scope.identity.admin || scope.identity.can_manage_git) && (
        <GitDeploymentPaths active="build" />
      )}
      <ComputeNotice creatingApplication={!linked} />
      {!build && (
        <nav aria-label="Source build steps" className="flex flex-wrap gap-2 py-3">
          {buildSteps.map((label, index) => (
            <Button
              key={label}
              type="button"
              size="sm"
              variant="ghost"
              aria-current={step === index ? 'step' : undefined}
              disabled={busy || detecting || index > furthest}
              className={`bg-transparent! border-transparent! ${step === index ? 'text-[var(--navigation-active)]!' : ''}`}
              onClick={() => {
                if (index > step && formRef.current && !validateVisibleFields(formRef.current))
                  return
                setError('')
                showStep(index)
              }}
            >
              {index + 1}. {label}
            </Button>
          ))}
        </nav>
      )}
      <form
        ref={formRef}
        noValidate
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy || detecting) return
          if (!validateVisibleFields(e.currentTarget)) return
          setError('')
          try {
            if (!build && step < 3) {
              if (step === 0 && lastDetectedSource.current !== sourceFingerprint)
                await detectRepository(true)
              if (step === 1) {
                parseField('build_args', () => parseBuildArgs(buildArgs))
                parseField('build_secrets', () => parseBuildSecrets(buildSecrets))
              }
              if (step === 2) validateDraft()
              showStep(step + 1)
              return
            }
            validateDraft()
            setBusy(true)
            const savedRuntime = runtimeEnvEnabled
              ? await saveEnvironment(
                  runtimeEnv,
                  { project, environment, application: application?.name || name },
                  build?.secrets || application?.spec.services[service]?.secrets,
                )
              : undefined
            const body = {
              project,
              environment,
              name,
              service,
              reuse_services: reuseServices.filter((name) => name !== service),
              provider,
              connection_id: connectionId,
              repository,
              branch,
              mode,
              preset,
              context_path: context,
              architecture: architecture || undefined,
              dockerfile,
              command:
                runtimeMode === 'override'
                  ? parseProcessCommand(runtimeCommand)
                  : runtimeMode === 'default'
                    ? []
                    : undefined,
              args:
                runtimeMode === 'override'
                  ? parseProcessCommand(runtimeArgs)
                  : runtimeMode === 'default'
                    ? []
                    : undefined,
              env: savedRuntime?.env,
              secrets: savedRuntime?.secrets,
              build_args: parseBuildArgs(buildArgs),
              framework:
                mode === 'framework'
                  ? { ...framework, port: framework.runtime === 'static' ? 8080 : port }
                  : undefined,
              build_secrets: parseBuildSecrets(buildSecrets),
              port: mode === 'framework' && framework.runtime === 'static' ? 8080 : port,
              public: isPublic,
              size,
              registry_credential: registry,
              auto_build: automatic,
              auto_deploy: autoDeploy,
              application_id: build?.application_id || application?.id || '',
              ...(build ? { expected_config_revision: build.revision } : {}),
            }
            const result = build
              ? await unwrap(
                  client.PUT('/builds/{id}', { params: { path: { id: build.id } }, body }),
                )
              : await unwrap(client.POST('/builds', { body }))
            void cache.invalidateQueries({ queryKey: ['builds'] })
            void cache.invalidateQueries({ queryKey: ['build', result.id] })
            onClose()
            void navigate({
              to: '/builds/$buildId',
              params: { buildId: result.id },
              search: { review: 'workflow' },
            })
          } catch (err) {
            fail(err)
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="form-body auth-form">
          {!build && (
            <div ref={stepHeading} tabIndex={-1} className="sr-only" role="status">
              Step {step + 1} of {buildSteps.length}: {buildSteps[step]}
            </div>
          )}
          {error && <RequestError error={error} />}
          <fieldset
            disabled={busy || detecting || (!build && step !== 0)}
            className={build || step === 0 ? 'grid min-w-0 gap-4' : 'hidden'}
          >
            <FormSection
              title="Repository"
              description="Select the application, provider, and source branch."
              icon="branch"
            >
              <div className="grid gap-4 sm:grid-cols-2">
                <label>
                  Application name
                  <Input
                    value={name}
                    error={fieldError(error, 'name', 'application')}
                    readOnly={Boolean(build || application)}
                    onChange={(e) => setName(e.target.value)}
                    pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?"
                    data-build-source
                    title="Use up to 40 lowercase letters, numbers or hyphens. Start with a letter and end with a letter or number."
                    maxLength={40}
                    required
                  />
                </label>
                <label>
                  Service
                  {application ? (
                    <SelectField
                      label="Service"
                      value={service}
                      error={fieldError(error, 'service', `services.${service}.service`)}
                      onValueChange={(value) => setService(value)}
                      options={
                        Object.keys(application.spec.services).map((item) => ({
                          value: item,
                          label: item,
                        })) ?? []
                      }
                    />
                  ) : (
                    <Input
                      value={service}
                      error={fieldError(error, 'service')}
                      readOnly={Boolean(build)}
                      onChange={(e) => setService(e.target.value)}
                      pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?"
                      maxLength={40}
                      required
                    />
                  )}
                </label>
              </div>
              <label>
                Git provider
                <SelectField
                  label="Git provider"
                  value={provider}
                  error={fieldError(error, 'provider')}
                  onValueChange={(value) => {
                    setProvider(value as 'github' | 'gitlab')
                    setConnectionId('')
                  }}
                  options={[
                    {
                      value: 'github',
                      label: 'GitHub Actions',
                    },
                    {
                      value: 'gitlab',
                      label: 'GitLab CI + GitLab Container Registry',
                    },
                  ]}
                />
              </label>
              <GitConnectionField
                provider={provider}
                value={connectionId}
                onValueChange={setConnectionId}
                builds
                error={fieldError(error, 'connection_id')}
              />
              <div className="grid items-start gap-4 sm:grid-cols-2">
                <GitRepositoryField
                  provider={provider}
                  connectionId={connectionId}
                  value={repository}
                  onChange={setRepository}
                  onBranchChange={setBranch}
                  error={fieldError(error, 'repository')}
                />
                <label>
                  Source branch
                  <Input
                    value={branch}
                    error={fieldError(error, 'branch', `services.${service}.branch`)}
                    onChange={(e) => setBranch(e.target.value)}
                    maxLength={200}
                    required
                  />
                  <span className="field-help">
                    Build this branch. Use the same branch when installing the repository workflow.
                  </span>
                </label>
              </div>
              <details className="form-disclosure" open={context !== '.' || undefined}>
                <summary>
                  Repository subdirectory <span className="muted-text">Optional</span>
                </summary>
                <label>
                  Build context
                  <Input
                    value={context}
                    onChange={(e) => setContext(e.target.value)}
                    maxLength={200}
                    required
                    error={fieldError(error, 'context_path')}
                  />
                  <span className="field-help">
                    Use . for the repository root, or the directory containing this app in a
                    monorepo.
                  </span>
                </label>
              </details>
            </FormSection>
          </fieldset>
          <fieldset
            disabled={busy || (!build && step !== 1)}
            className={build || step === 1 ? 'grid min-w-0 gap-4' : 'hidden'}
          >
            <div className="flex min-w-0 flex-wrap items-center justify-between gap-3 border-b border-[var(--border)] pb-4">
              <div className="grid min-w-0 gap-1 text-sm" aria-live="polite">
                <strong className="break-all">{repository || 'Choose a repository'}</strong>
                <span className="break-all text-[var(--muted)]">
                  {branch} · {context} · {provider === 'github' ? 'GitHub Actions' : 'GitLab CI'}
                </span>
                {detecting ? (
                  <span>Reading repository…</span>
                ) : detection ? (
                  <span>
                    Detected{' '}
                    {detection.framework
                      ? frameworkLabel(detection.framework.framework)
                      : 'Dockerfile'}{' '}
                    at commit <code>{detection.commit_sha.slice(0, 12)}</code>
                  </span>
                ) : suggestion ? (
                  <span>Source changed. Detect again to refresh the recipe.</span>
                ) : (
                  <span>Choose a recipe or detect settings from your repository.</span>
                )}
              </div>
              <div className="flex flex-wrap gap-2">
                {detection && (mode !== 'framework' || !detection.framework) && (
                  <Button
                    type="button"
                    disabled={busy || detecting}
                    onClick={() => applyDetection(detection)}
                  >
                    Use detected settings
                  </Button>
                )}
                <Button
                  type="button"
                  disabled={busy || detecting || !canDetect}
                  onClick={() => void detectRepository()}
                >
                  {detecting ? 'Detecting…' : detection ? 'Detect again' : 'Detect framework'}
                </Button>
              </div>
            </div>
            {!(scope.identity.admin || scope.identity.can_manage_git) && (
              <p className="field-help">
                Detection uses this application's approved source repository. Ask your workspace
                owner to approve a new repository, or enter a reviewed recipe.
              </p>
            )}
            {detectionError && (
              <div role="status">
                <RequestError error={detectionError} />
                <p className="field-help">
                  You can retry detection or configure the build recipe manually.
                </p>
              </div>
            )}
            {detection && detection.warnings.length > 0 && (
              <details className="form-disclosure" open>
                <summary>Review detection notes ({detection.warnings.length})</summary>
                <ul className="grid gap-2 text-sm">
                  {detection.warnings.map((note) => (
                    <li key={note}>{note}</li>
                  ))}
                </ul>
              </details>
            )}
            <FormSection
              title="Build recipe"
              description="Paths are relative to the repository root."
              icon="code"
            >
              <div className="grid gap-4 sm:grid-cols-2">
                <label>
                  Build method
                  <SelectField
                    label="Build method"
                    value={mode}
                    error={fieldError(error, 'mode', `services.${service}.mode`)}
                    onValueChange={(value) => {
                      recipeEdited.current = true
                      setMode(value as Build['mode'])
                      if (value === 'framework') setPort(framework.port)
                    }}
                    options={[
                      {
                        value: 'dockerfile',
                        label: 'Dockerfile',
                      },
                      {
                        value: 'buildpacks',
                        label: 'Cloud Native Buildpacks',
                      },
                      { value: 'framework', label: 'Framework recipe' },
                    ]}
                  />
                </label>
              </div>
              {mode === 'framework' && (
                <FrameworkBuildFields
                  key={sourceFingerprint}
                  value={{ ...framework, port: framework.runtime === 'static' ? 8080 : port }}
                  detected={detection?.framework}
                  error={error}
                  onChange={(next) => {
                    recipeEdited.current = true
                    setFramework(next)
                    setPort(next.port)
                  }}
                />
              )}
              {linked &&
                mode === 'framework' &&
                Boolean(reuseApplication?.spec.services[service]?.port) &&
                reuseApplication?.spec.services[service]?.port !==
                  (framework.runtime === 'static' ? 8080 : port) && (
                  <Note>
                    The linked service routes traffic to port{' '}
                    {reuseApplication?.spec.services[service]?.port}, but this recipe listens on
                    port {framework.runtime === 'static' ? 8080 : port}. Match the server port or
                    update the service networking before deploying.
                  </Note>
                )}
              {mode === 'dockerfile' ? (
                <label>
                  Dockerfile path
                  <Input
                    value={dockerfile}
                    error={fieldError(error, 'dockerfile', `services.${service}.dockerfile`)}
                    onChange={(e) => setDockerfile(e.target.value)}
                    maxLength={200}
                    required
                  />
                </label>
              ) : mode === 'buildpacks' ? (
                <label>
                  Buildpack preset
                  <SelectField
                    label="Buildpack preset"
                    value={preset}
                    error={fieldError(error, 'preset', `services.${service}.preset`)}
                    onValueChange={(value) => setPreset(value as Build['preset'])}
                    options={
                      ['auto', 'nodejs', 'python', 'go', 'java', 'dotnet', 'ruby', 'static'].map(
                        (value) => ({
                          value: value,
                          label: value,
                        }),
                      ) ?? []
                    }
                  />
                </label>
              ) : null}
              <details
                className="form-disclosure"
                open={Boolean(fieldError(error, 'architecture')) || undefined}
              >
                <summary>
                  Build architecture{' '}
                  <span className="muted-text">{architecture || 'Infer from cluster'}</span>
                </summary>
                <label>
                  Target architecture
                  <SelectField
                    label="Target architecture"
                    value={architecture}
                    error={fieldError(error, 'architecture', `services.${service}.architecture`)}
                    onValueChange={(value) => setArchitecture(value as Build['architecture'] | '')}
                    options={[
                      {
                        value: '',
                        label: 'Infer from a uniform cluster',
                      },
                      {
                        value: 'amd64',
                        label: 'Linux AMD64',
                      },
                      {
                        value: 'arm64',
                        label: features.hostedCompute
                          ? 'Linux ARM64 · Requires your own server'
                          : 'Linux ARM64',
                        disabled: features.hostedCompute,
                      },
                    ]}
                  />
                </label>
              </details>
              <details
                className="form-disclosure"
                open={
                  Boolean(buildArgs) || Boolean(fieldGroupError(error, 'build_args')) || undefined
                }
              >
                <summary>
                  Public build values <span className="muted-text">Optional</span>
                </summary>
                <label>
                  Public build values
                  <Textarea
                    value={buildArgs}
                    error={fieldGroupError(error, 'build_args')}
                    onChange={(event) => setBuildArgs(event.target.value)}
                    rows={3}
                    placeholder="NEXT_PUBLIC_API_URL=https://api.example.com"
                    aria-describedby="build-args-help"
                    spellCheck={false}
                  />
                  <span id="build-args-help" className="field-help">
                    One NAME=value per line. Public values are baked into the image; never enter
                    secrets. Save changes, then review and reinstall the workflow before building.
                  </span>
                </label>
              </details>
            </FormSection>
            <details
              className="form-disclosure"
              open={
                Boolean(buildSecrets) || Boolean(fieldError(error, 'build_secrets')) || undefined
              }
            >
              <summary>
                Build secrets{' '}
                <span className="muted-text">Optional · private dependencies and build tools</span>
              </summary>
              <FormSection
                title="Build secrets"
                description="Reference secrets already configured in your repository's GitHub Actions settings or GitLab CI variables."
                icon="lock"
              >
                <label>
                  Secret references
                  <Textarea
                    rows={3}
                    value={buildSecrets}
                    error={fieldError(error, 'build_secrets', `services.${service}.build_secrets`)}
                    onChange={(e) => setBuildSecrets(e.target.value)}
                    placeholder="npm_token=NPM_TOKEN"
                    aria-describedby="build-secrets-help"
                  />
                </label>
                <p id="build-secrets-help" className="muted-text">
                  Configure secret values in GitHub Actions secrets or GitLab CI variables first.
                  Enter mount_id=CI_SECRET_NAME, never a secret value. Dockerfile builds read
                  /run/secrets/mount_id; framework build steps also receive the named variable.
                  Buildpacks do not support secret mounts.
                </p>
              </FormSection>
            </details>
          </fieldset>
          <fieldset
            disabled={busy || (!build && step !== 2)}
            className={build || step === 2 ? 'grid min-w-0 gap-4' : 'hidden'}
          >
            <FormSection
              title="Runtime"
              description="Choose resource and image-pull settings."
              icon="box"
            >
              <SelectField
                label="Runtime command"
                value={runtimeMode}
                onValueChange={(value) => setRuntimeMode(value as typeof runtimeMode)}
                options={[
                  ...(linked ? [{ value: 'preserve', label: 'Keep service settings' }] : []),
                  { value: 'default', label: 'Use image defaults' },
                  { value: 'override', label: 'Override runtime command' },
                ]}
              />
              {runtimeMode === 'preserve' && (
                <p className="field-help">
                  The current service command and arguments are retained when this build is
                  deployed.
                </p>
              )}
              {runtimeMode === 'default' && (
                <p className="field-help">
                  Use the image ENTRYPOINT and CMD. Deploying this build clears any existing service
                  command override.
                </p>
              )}
              {runtimeMode === 'override' && (
                <div className="grid gap-4">
                  <label>
                    Command
                    <Input
                      value={runtimeCommand}
                      error={fieldError(error, 'command', `services.${service}.command`)}
                      onChange={(event) => setRuntimeCommand(event.target.value)}
                      placeholder="uvicorn"
                      maxLength={8192}
                    />
                  </label>
                  <label>
                    Arguments
                    <Input
                      value={runtimeArgs}
                      error={fieldError(error, 'args', `services.${service}.args`)}
                      onChange={(event) => setRuntimeArgs(event.target.value)}
                      placeholder="main:app --host 0.0.0.0 --port 8000"
                      maxLength={16384}
                    />
                  </label>
                  <p className="field-help">
                    Command replaces the image ENTRYPOINT; arguments replace CMD. Leave either blank
                    to use that image default. Quotes group arguments; use an explicit shell for
                    shell expressions. The override is applied when this build is deployed.
                  </p>
                </div>
              )}
              {!linked && (
                <div className="grid gap-4 sm:grid-cols-2">
                  <label>
                    Service port
                    <Input
                      type="number"
                      min={1}
                      max={65535}
                      value={mode === 'framework' && framework.runtime === 'static' ? 8080 : port}
                      error={fieldError(error, 'port', `services.${service}.port`)}
                      readOnly={mode === 'framework' && framework.runtime === 'static'}
                      onChange={(e) => setPort(Number(e.target.value))}
                      required
                    />
                  </label>
                  <label>
                    Resource profile
                    <SelectField
                      label="Resource profile"
                      value={size}
                      error={fieldError(error, 'size', `services.${service}.size`)}
                      onValueChange={(value) => setSize(value)}
                      options={
                        ['small', 'medium', 'large'].map((value) => ({
                          value: value,
                          label:
                            features.hostedFree && value !== 'small'
                              ? `${value} · Requires your own server`
                              : value,
                          disabled: features.hostedFree && value !== 'small',
                        })) ?? []
                      }
                    />
                  </label>
                </div>
              )}
              {!linked && (
                <label className="checkbox-row">
                  <Input
                    type="checkbox"
                    checked={isPublic}
                    onChange={(e) => setPublic(e.target.checked)}
                  />
                  Expose the service publicly
                </label>
              )}
              {boundApplicationId && (
                <label className="checkbox-label">
                  <Input
                    type="checkbox"
                    checked={runtimeEnvEnabled}
                    onChange={(event) => setRuntimeEnvEnabled(event.target.checked)}
                  />
                  Replace service environment variables on deployment
                </label>
              )}
              {runtimeEnvEnabled ? (
                <EnvironmentFields
                  rows={runtimeEnv}
                  onBusyChange={setBusy}
                  onChange={setRuntimeEnv}
                  label="Runtime"
                  error={
                    fieldGroupError(error, 'env') ||
                    fieldGroupError(error, `services.${service}.env`)
                  }
                  disabled={busy}
                />
              ) : (
                <p className="field-help">Existing service environment variables are retained.</p>
              )}
              <details
                className="form-disclosure"
                open={
                  Boolean(registry) ||
                  Boolean(fieldError(error, 'registry_credential')) ||
                  undefined
                }
              >
                <summary>
                  Image registry <span className="muted-text">Automatic unless changed</span>
                </summary>
                <label>
                  Runtime registry credential
                  <SelectField
                    label="Runtime registry credential"
                    value={registry}
                    error={fieldError(
                      error,
                      'registry_credential',
                      `services.${service}.registry_credential`,
                    )}
                    onValueChange={(value) => setRegistry(value)}
                    options={[
                      {
                        value: '',
                        label: managedRegistryAvailable
                          ? 'Automatic · Hakopod private registry'
                          : 'Automatic · public or matching saved credential',
                      },
                      ...(registry && !registries.data?.items.some((item) => item.name === registry)
                        ? [{ value: registry, label: registry }]
                        : []),
                      ...(registries.data?.items.map((item) => ({
                        value: item.name,
                        label: item.name + ' · ' + item.registry,
                      })) ?? []),
                    ]}
                  />
                  <span className="field-help">
                    {managedRegistryAvailable
                      ? 'GitHub App builds use the Hakopod private registry automatically. Worker pull credentials are configured for you. Choosing a saved credential uses your own registry instead.'
                      : 'Matching saved registry credentials are tried automatically for private images. You can choose which credential to try first.'}
                  </span>
                </label>
                <Button type="button" asChild>
                  <a
                    href="/infrastructure/registries/new"
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    Create registry credential <Icon name="external" size={14} />
                  </a>
                </Button>
                <span className="field-help">
                  Opens in a new tab so your build draft stays here. Return and refresh the
                  credential list after saving.
                </span>
                <Button
                  type="button"
                  disabled={registries.isFetching}
                  onClick={() => void registries.refetch()}
                >
                  Refresh credentials
                </Button>
              </details>
            </FormSection>
            {boundApplicationId && !features.hostedCompute && (
              <FormSection
                title="Reuse this image"
                description="Build once for your web process, workers and migrations."
                icon="box"
              >
                <p className="field-help">
                  Selected services receive the same verified image on deployment. Each keeps its
                  own run command, variables, ports and volumes.
                </p>
                {linkedApp.error && (
                  <Note>
                    The application services could not be loaded. Your saved selections are
                    retained; reload before changing them.
                  </Note>
                )}
                {Object.keys(reuseApplication?.spec.services || {})
                  .filter((name) => name !== service)
                  .map((name) => (
                    <label key={name} className="checkbox-label">
                      <Input
                        type="checkbox"
                        checked={reuseServices.includes(name)}
                        disabled={busy}
                        onChange={(event) =>
                          setReuseServices((current) =>
                            event.target.checked
                              ? [...current, name]
                              : current.filter((value) => value !== name),
                          )
                        }
                      />
                      <span>{name}</span>
                    </label>
                  ))}
                {reuseApplication && (
                  <Button type="button" asChild>
                    <a
                      href={`/applications/${reuseApplication.id}/configure`}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      Add another service <Icon name="external" size={14} />
                    </a>
                  </Button>
                )}
                <Button
                  type="button"
                  disabled={linkedApp.isFetching}
                  onClick={() => void linkedApp.refetch()}
                >
                  Refresh services
                </Button>
              </FormSection>
            )}
            <FormSection
              title="Automation"
              description="Choose how verified commits become deployments."
              icon="refresh"
            >
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={automatic}
                  onChange={(e) => {
                    setAutomatic(e.target.checked)
                    if (!e.target.checked) setAutoDeploy(false)
                  }}
                />
                Build automatically on pushes to this source branch
              </label>
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={autoDeploy}
                  disabled={!automatic}
                  onChange={(e) => setAutoDeploy(e.target.checked)}
                />
                Deploy successful verified builds automatically
              </label>
              <Note>
                {linked
                  ? 'A successful build replaces the selected service image. Existing service resources and networking remain controlled by its application configuration.'
                  : 'The application is created when a verified build image is deployed. No container image is needed now.'}{' '}
                Saving build settings prepares a workflow preview. A repository manager explicitly
                installs the reviewed workflow in {provider === 'gitlab' ? 'GitLab' : 'GitHub'}.
              </Note>
              {provider === 'gitlab' && (
                <Note>
                  Hakopod manages one .gitlab-ci.yml entrypoint per repository. Installation refuses
                  an existing unowned CI file or a file owned by another build. Configure Pipeline
                  events on the project webhook for automatic deployment.
                </Note>
              )}
              {automatic && (
                <Note>
                  Automatic builds require the installed workflow. Automatic deployment additionally
                  needs the{' '}
                  {provider === 'gitlab'
                    ? 'authenticated GitLab Pipeline Hook'
                    : 'signed GitHub workflow event'}{' '}
                  integration and your continuing project authority.
                </Note>
              )}
            </FormSection>
          </fieldset>
          {!build && step === 3 && (
            <FormSection title="Review source build">
              <dl className="grid min-w-0 gap-3 text-sm sm:grid-cols-2 [&_dd]:break-words [&_dt]:text-[var(--muted)]">
                <div>
                  <dt>Application / service</dt>
                  <dd>
                    {name} / {service}
                  </dd>
                </div>
                <div>
                  <dt>Repository</dt>
                  <dd>
                    {repository} · {branch}
                  </dd>
                </div>
                <div>
                  <dt>Build method</dt>
                  <dd>
                    {mode === 'framework'
                      ? frameworkLabel(framework.framework)
                      : mode === 'buildpacks'
                        ? 'Cloud Native Buildpacks'
                        : 'Dockerfile'}{' '}
                    · {context}
                  </dd>
                </div>
                <div>
                  <dt>Architecture</dt>
                  <dd>{architecture || 'Infer from cluster'}</dd>
                </div>
                <div>
                  <dt>Recipe</dt>
                  <dd>
                    {mode === 'dockerfile'
                      ? dockerfile
                      : mode === 'buildpacks'
                        ? preset
                        : `${framework.package_manager} · ${framework.runtime}`}
                  </dd>
                </div>
                {mode === 'framework' && (
                  <div className="sm:col-span-2">
                    <dt>Build commands</dt>
                    <dd className="grid gap-1 break-all">
                      <code>{framework.install_command}</code>
                      <code>{framework.build_command}</code>
                      <code>
                        {framework.runtime === 'static'
                          ? `Serve ${framework.output_directory} on port 8080`
                          : `${framework.start_command} · port ${port}`}
                      </code>
                    </dd>
                  </div>
                )}
                <div>
                  <dt>Image registry</dt>
                  <dd>
                    {registry ||
                      (managedRegistryAvailable
                        ? 'Hakopod private registry'
                        : 'Automatic matching credentials')}
                  </dd>
                </div>
                <div>
                  <dt>Resources and access</dt>
                  <dd>
                    {linked
                      ? 'Keep existing service resources and networking'
                      : `${size} · ${isPublic ? 'Public HTTP' : 'Private'} · port ${mode === 'framework' && framework.runtime === 'static' ? 8080 : port}`}
                  </dd>
                </div>
                <div>
                  <dt>Run command</dt>
                  <dd>
                    {runtimeMode === 'preserve' ? (
                      'Keep service settings'
                    ) : runtimeMode === 'default' ? (
                      'Use image defaults'
                    ) : (
                      <code className="whitespace-pre-wrap break-all">
                        {runtimeCommand || '(image command)'} {runtimeArgs || '(image arguments)'}
                      </code>
                    )}
                  </dd>
                </div>
                <div>
                  <dt>Variables and secrets</dt>
                  <dd>
                    {runtimeEnvEnabled
                      ? `${runtimeVariables} ${runtimeVariables === 1 ? 'variable' : 'variables'} · ${runtimeSecrets} ${runtimeSecrets === 1 ? 'secret' : 'secrets'} · values hidden`
                      : 'Keep existing variables and secrets'}
                  </dd>
                </div>
                <div>
                  <dt>Automation</dt>
                  <dd>
                    {automatic
                      ? autoDeploy
                        ? 'Build and deploy verified commits'
                        : 'Build on pushes; deploy manually'
                      : 'Manual builds and deployments'}
                  </dd>
                </div>
                {reuseServices.length > 0 && (
                  <div>
                    <dt>Also update these services</dt>
                    <dd>{reuseServices.join(', ')}</dd>
                  </div>
                )}
              </dl>
              <Note>
                Saving prepares a workflow for review. Next, install it in your repository to start
                building. This does not deploy your service yet.
              </Note>
            </FormSection>
          )}
        </div>
        <div className="form-footer">
          <Button type="button" disabled={busy || detecting} onClick={onClose}>
            Cancel
          </Button>
          {!build && step > 0 && (
            <Button
              type="button"
              disabled={busy || detecting}
              onClick={() => {
                setError('')
                showStep(step - 1)
              }}
            >
              Back
            </Button>
          )}
          <Button type="submit" variant="primary" disabled={busy || detecting}>
            {busy
              ? 'Saving…'
              : build
                ? 'Save build configuration'
                : step === 3
                  ? 'Save and review workflow'
                  : `Continue to ${buildSteps[step + 1].toLowerCase()}`}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
