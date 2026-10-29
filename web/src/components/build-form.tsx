import { useEditionFeatures } from '../lib/dashboard-edition'
import { validateVisibleFields } from '../lib/build-onboarding'
import { Icon } from './icons'
import { ComputeNotice } from './compute-notice'
import { fieldError, fieldGroupError, validationLines } from '../lib/form-errors'
import { useGitConnections, useGitProviderSetup } from '../lib/git-connections'
import { GitRepositoryField } from './git-repository-field'
import { saveEnvironment } from '../lib/save-environment'
import { EnvironmentFields } from './runtime-settings-fields'
import { environmentRows, isSecretEnvironment, splitEnvironment } from '../lib/service-environment'
import { formatProcessCommand, parseProcessCommand } from '../lib/process-command'
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
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { FormPage } from './form-page'
import { HeadingHelp, Note, RequestError } from './shared'
import { ChoiceCards, FlowSteps, PendingButton, nameFromImage, plural } from './create-flow'
import { ProviderMark, providerName, providerRunner } from './build-summary'

type Build = components['schemas']['BuildConfig']
type Source = 'build' | 'config' | 'image'

const steps = ['Source', 'Recipe', 'Service', 'Runtime', 'Review'] as const
const reviewStep = steps.length - 1

// API field paths, grouped by the step that owns the field. Only explicit paths move the
// user back; permissions, network and capacity failures stay on the current step.
const stepFields = [
  ['provider', 'connection_id', 'repository', 'branch', 'context_path', 'submodules'],
  ['mode', 'preset', 'architecture', 'dockerfile', 'build_args', 'build_secrets', 'framework'],
  ['name', 'application', 'service', 'port', 'size', 'registry_credential', 'reuse_services'],
  ['command', 'args', 'env', 'secrets'],
]
export function buildFlowErrorStep(error: string, service: string): number | undefined {
  for (let step = 0; step < stepFields.length; step++)
    for (const path of stepFields[step]) {
      if (fieldError(error, path, `services.${service}.${path}`)) return step
      if (
        validationLines(error).some(
          (line) =>
            (line.startsWith(`${path}.`) || line.startsWith(`services.${service}.${path}.`)) &&
            line.includes(':'),
        )
      )
        return step
    }
}

const methodLabel = (mode: Build['mode']) =>
  mode === 'framework'
    ? 'Framework recipe'
    : mode === 'buildpacks'
      ? 'Cloud Native Buildpacks'
      : 'Dockerfile'

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
  const [source, setSource] = useState<Source>('build')
  const stepHeading = useRef<HTMLDivElement>(null)
  const firstStep = useRef(true)
  const formRef = useRef<HTMLFormElement>(null)
  const gitSetup = useGitProviderSetup()
  const gitConnections = useGitConnections()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const canManageGit = scope.identity.admin || scope.identity.can_manage_git
  const project = build?.project || application?.project || scope.project
  const environment = build?.environment || application?.environment || scope.environment
  const [name, setName] = useState(build?.name || application?.name || '')
  const nameEdited = useRef(Boolean(build || application))
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
  const [submodules, setSubmodules] = useState(build ? Boolean(build.submodules) : true)
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
  // Only an administrator sees connections; without one that can run builds, say so up front.
  const noBuildConnection =
    canManageGit &&
    Boolean(gitConnections.data) &&
    !gitConnections.data?.items.some(
      (item) => item.provider === provider && item.enabled && item.capabilities.builds,
    )
  // Detection reads the repository; the application and service names do not change its result.
  const sourceFingerprint = JSON.stringify([
    boundApplicationId,
    project,
    environment,
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
  const canDetect = Boolean(name && repository && branch && (canManageGit || boundApplicationId))
  const servicePort = mode === 'framework' && framework.runtime === 'static' ? 8080 : port
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
    } else if (!firstStep.current) {
      // The status line announces the step; focus moves to the visible page heading.
      const heading = formRef.current?.closest('.form-page')?.querySelector<HTMLElement>('h1')
      if (heading) {
        heading.tabIndex = -1
        heading.focus()
      }
    }
    firstStep.current = false
  }, [step, error])
  const fail = (cause: unknown) => {
    const text = message(cause)
    const fieldStep = buildFlowErrorStep(text, service)
    if (fieldStep !== undefined) setStep(fieldStep)
    setError(text)
  }
  const parseField = <T,>(path: string, read: () => T): T => {
    try {
      return read()
    } catch (cause) {
      throw new Error(`${path}: ${message(cause)}`)
    }
  }
  const validateRecipe = () => {
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
  }
  const validateService = () => {
    if (features.hostedFree && !linked && size !== 'small')
      throw new Error(
        'size: Hosted Free supports the small profile. Choose small or connect your own server.',
      )
  }
  const validateRuntime = () => {
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
  const runner = providerRunner(provider)
  const leaving = step === 0 && !build && source !== 'build'
  const primaryLabel = leaving
    ? source === 'config'
      ? 'Import repository configuration'
      : 'Configure image services'
    : step < reviewStep
      ? step === 3
        ? 'Review build'
        : `Continue to ${steps[step + 1].toLowerCase()}`
      : build
        ? 'Save build changes'
        : 'Create build'
  const pendingLabel = detecting
    ? 'Reading repository…'
    : build
      ? 'Saving changes…'
      : 'Creating build…'
  const showStep = (next: number) => {
    setError('')
    setStep(next)
  }
  const edit = (target: number) => (
    <Button
      type="button"
      size="sm"
      variant="ghost"
      disabled={busy}
      onClick={() => showStep(target)}
    >
      Edit
      <span className="sr-only"> {steps[target].toLowerCase()}</span>
    </Button>
  )
  return (
    <FormPage
      breadcrumbs={[
        { label: 'Source builds', to: '/builds' },
        ...(build ? [{ label: build.name, to: `/builds/${build.id}` }] : []),
        { label: build ? 'Edit build' : 'New source build' },
      ]}
      title={
        build
          ? `Edit ${build.name} / ${build.service} build`
          : step === reviewStep
            ? `Review ${name} / ${service} build`
            : 'New source build'
      }
      description={`${project} / ${environment}. Builds run in your ${runner} account; Hakopod deploys the verified image digest.`}
    >
      <FlowSteps
        steps={steps}
        current={step}
        disabled={busy || detecting}
        onSelect={(index) => showStep(index)}
      />
      <form
        ref={formRef}
        className="build-flow"
        noValidate
        aria-busy={busy || detecting || undefined}
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy || detecting) return
          if (leaving) {
            void navigate(
              source === 'config'
                ? { to: '/applications/import' }
                : { to: '/applications/new', search: { mode: 'form' } },
            )
            return
          }
          if (!validateVisibleFields(e.currentTarget)) return
          setError('')
          try {
            if (step < reviewStep) {
              if (step === 0 && !build && lastDetectedSource.current !== sourceFingerprint)
                await detectRepository(true)
              if (step === 1) validateRecipe()
              if (step === 2) validateService()
              if (step === 3) validateRuntime()
              setStep(step + 1)
              return
            }
            validateRecipe()
            validateService()
            validateRuntime()
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
              submodules,
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
              framework: mode === 'framework' ? { ...framework, port: servicePort } : undefined,
              build_secrets: parseBuildSecrets(buildSecrets),
              port: servicePort,
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
        <div className="form-body create-flow build-flow-body">
          <div ref={stepHeading} className="sr-only" role="status">
            Step {step + 1} of {steps.length}: {steps[step]}
          </div>
          <ComputeNotice creatingApplication={!linked} />
          {error && <RequestError error={error} />}

          {/* Source: what to deploy, then only that source's fields. */}
          <fieldset
            disabled={busy || detecting || step !== 0}
            className={step === 0 ? 'build-flow-step' : 'hidden'}
          >
            {!build && canManageGit && (
              <FlowSection title="What do you want to deploy?">
                <ChoiceCards
                  label="What do you want to deploy?"
                  value={source}
                  onChange={setSource}
                  choices={[
                    {
                      value: 'build',
                      title: 'Source code',
                      description:
                        'Build an image from a repository branch with a framework recipe, Dockerfile or Buildpacks.',
                      icon: <Icon name="branch" size={18} />,
                    },
                    {
                      value: 'config',
                      title: 'Repository with hakopod.toml',
                      description: 'Deploy the Hakopod configuration committed in a repository.',
                      icon: <Icon name="code" size={18} />,
                    },
                    {
                      value: 'image',
                      title: 'Existing container image',
                      description: 'Run an image you already publish, pinned by digest.',
                      icon: <Icon name="box" size={18} />,
                    },
                  ]}
                />
              </FlowSection>
            )}
            {source !== 'build' && !build ? (
              <div className="create-source-detail">
                <p>
                  {source === 'config'
                    ? 'Choose the repository and branch that contain hakopod.toml. Hakopod reads it and reviews the services before anything is created.'
                    : 'Add one or more services and choose each image, placement and variables. Nothing is created until the final review.'}
                </p>
              </div>
            ) : (
              <>
                <FlowSection
                  title="Where builds run"
                  help="The workflow runs in your provider account and uses its runner minutes and container registry."
                >
                  <ChoiceCards
                    compact
                    label="Where builds run"
                    value={provider}
                    onChange={(value) => {
                      setProvider(value)
                      setConnectionId('')
                    }}
                    choices={[
                      {
                        value: 'github',
                        title: 'GitHub Actions',
                        description: 'A workflow file in your GitHub repository.',
                        icon: <ProviderMark provider="github" size={18} />,
                      },
                      {
                        value: 'gitlab',
                        title: 'GitLab CI',
                        description: 'A pipeline that pushes to GitLab Container Registry.',
                        icon: <ProviderMark provider="gitlab" size={18} />,
                      },
                    ]}
                  />
                  {fieldError(error, 'provider') && (
                    <p className="field-error" role="alert">
                      {fieldError(error, 'provider')}
                    </p>
                  )}
                </FlowSection>
                <FlowSection title="Repository">
                  {noBuildConnection && (
                    <div className="create-source-detail" role="status">
                      <p>
                        No {providerName(provider)} connection can run builds yet. Connect{' '}
                        {providerName(provider)} with build access, then return here.
                      </p>
                      <Button asChild size="sm" className="justify-self-start">
                        <Link to="/settings/git/connections/new" search={{ provider }}>
                          <ProviderMark provider={provider} />
                          Connect {providerName(provider)}
                        </Link>
                      </Button>
                    </div>
                  )}
                  <GitConnectionField
                    provider={provider}
                    value={connectionId}
                    onValueChange={setConnectionId}
                    builds
                    error={fieldError(error, 'connection_id')}
                  />
                  <GitRepositoryField
                    provider={provider}
                    connectionId={connectionId}
                    value={repository}
                    onChange={(value) => {
                      setRepository(value)
                      if (!nameEdited.current) setName(nameFromImage(value))
                    }}
                    onBranchChange={setBranch}
                    error={fieldError(error, 'repository')}
                  />
                  <label>
                    Branch
                    <Input
                      value={branch}
                      error={fieldError(error, 'branch', `services.${service}.branch`)}
                      onChange={(e) => setBranch(e.target.value)}
                      maxLength={200}
                      required
                    />
                    <span className="field-help">
                      Builds use this branch. Install the workflow on the same branch.
                    </span>
                  </label>
                  <details
                    className="form-disclosure"
                    open={context !== '.' || !submodules || undefined}
                  >
                    <summary>
                      Repository options{' '}
                      <span className="muted-text">
                        {context === '.' ? 'Repository root' : context} ·{' '}
                        {submodules ? 'Submodules included' : 'No submodules'}
                      </span>
                    </summary>
                    <div className="create-fields">
                      <label>
                        Build directory
                        <Input
                          value={context}
                          onChange={(e) => setContext(e.target.value)}
                          maxLength={200}
                          required
                          error={fieldError(error, 'context_path')}
                        />
                        <span className="field-help">
                          Use . for the repository root, or the directory that contains this app in
                          a monorepo.
                        </span>
                      </label>
                      <div className="flex min-w-0 flex-wrap items-center gap-1">
                        <label className="checkbox-row">
                          <Input
                            type="checkbox"
                            checked={submodules}
                            onChange={(e) => setSubmodules(e.target.checked)}
                          />
                          Check out Git submodules recursively
                        </label>
                        <HeadingHelp title="Git submodules">
                          The workflow token is scoped to the repository that runs it, so a private
                          submodule in another repository is not checked out.
                        </HeadingHelp>
                      </div>
                    </div>
                  </details>
                </FlowSection>
              </>
            )}
          </fieldset>

          {/* Recipe: how the image is built. */}
          <fieldset
            disabled={busy || step !== 1}
            className={step === 1 ? 'build-flow-step' : 'hidden'}
          >
            <div className="build-detection" aria-live="polite">
              <div className="build-detection-source">
                <ProviderMark provider={provider} size={16} />
                <div>
                  <strong>{repository || 'No repository chosen'}</strong>
                  <span>
                    {branch} · {context === '.' ? 'repository root' : context} · {runner}
                  </span>
                </div>
              </div>
              <p className="build-detection-result">
                {detecting ? (
                  'Reading repository…'
                ) : detectionError ? (
                  <span className="build-detection-error" role="alert">
                    Detection failed: {detectionError.replace(/\.?\s*$/, '.')} Retry, or choose the
                    build method yourself.
                  </span>
                ) : detection ? (
                  <>
                    Detected{' '}
                    <strong>
                      {detection.framework
                        ? frameworkLabel(detection.framework.framework)
                        : methodLabel(detection.mode)}
                    </strong>{' '}
                    at commit <code>{detection.commit_sha.slice(0, 12)}</code>
                  </>
                ) : suggestion ? (
                  'The source changed after detection. Detect again to refresh the suggestion.'
                ) : canDetect ? (
                  'Not detected yet. Choose a method or detect it from the repository.'
                ) : (
                  'Detection needs an approved repository. Choose the build method yourself.'
                )}
              </p>
              <div className="build-detection-actions">
                {detection && (mode !== 'framework' || !detection.framework) && (
                  <Button
                    type="button"
                    size="sm"
                    disabled={busy || detecting}
                    onClick={() => applyDetection(detection)}
                  >
                    Use detected settings
                  </Button>
                )}
                <PendingButton
                  type="button"
                  size="sm"
                  pending={detecting}
                  pendingLabel="Detecting…"
                  disabled={busy || !canDetect}
                  onClick={() => void detectRepository()}
                >
                  {detectionError
                    ? 'Retry detection'
                    : detection
                      ? 'Detect again'
                      : 'Detect from repository'}
                </PendingButton>
              </div>
            </div>
            {detection && detection.warnings.length > 0 && (
              <Note>
                <ul className="build-detection-notes">
                  {detection.warnings.map((note) => (
                    <li key={note}>{note}</li>
                  ))}
                </ul>
              </Note>
            )}
            <FlowSection title="Build method">
              <ChoiceCards
                label="Build method"
                value={mode}
                onChange={(value) => {
                  recipeEdited.current = true
                  setMode(value)
                  if (value === 'framework') setPort(framework.port)
                }}
                choices={[
                  {
                    value: 'framework',
                    title: 'Framework recipe',
                    description: 'Generated install, build and start steps for a known framework.',
                    icon: <Icon name="grid" size={18} />,
                  },
                  {
                    value: 'dockerfile',
                    title: 'Dockerfile',
                    description: 'Build the Dockerfile committed in your repository.',
                    icon: <Icon name="code" size={18} />,
                  },
                  {
                    value: 'buildpacks',
                    title: 'Cloud Native Buildpacks',
                    description: 'Detect the language and build without a Dockerfile.',
                    icon: <Icon name="box" size={18} />,
                  },
                ]}
              />
              {fieldError(error, 'mode', `services.${service}.mode`) && (
                <p className="field-error" role="alert">
                  {fieldError(error, 'mode', `services.${service}.mode`)}
                </p>
              )}
            </FlowSection>
            {mode === 'framework' && (
              <FlowSection title="Framework">
                <FrameworkBuildFields
                  key={sourceFingerprint}
                  value={{ ...framework, port: servicePort }}
                  detected={detection?.framework}
                  error={error}
                  onChange={(next) => {
                    recipeEdited.current = true
                    setFramework(next)
                    setPort(next.port)
                  }}
                />
                {linked &&
                  Boolean(reuseApplication?.spec.services[service]?.port) &&
                  reuseApplication?.spec.services[service]?.port !== servicePort && (
                    <Note>
                      The linked service routes traffic to port{' '}
                      {reuseApplication?.spec.services[service]?.port}, but this recipe listens on
                      port {servicePort}. Match the server port or update the service networking
                      before deploying.
                    </Note>
                  )}
              </FlowSection>
            )}
            {mode === 'dockerfile' && (
              <label>
                Dockerfile path
                <Input
                  value={dockerfile}
                  error={fieldError(error, 'dockerfile', `services.${service}.dockerfile`)}
                  onChange={(e) => setDockerfile(e.target.value)}
                  maxLength={200}
                  required
                />
                <span className="field-help">Relative to the repository root.</span>
              </label>
            )}
            {mode === 'buildpacks' && (
              <label>
                Buildpack preset
                <SelectField
                  label="Buildpack preset"
                  value={preset}
                  error={fieldError(error, 'preset', `services.${service}.preset`)}
                  onValueChange={(value) => setPreset(value as Build['preset'])}
                  options={[
                    'auto',
                    'nodejs',
                    'python',
                    'go',
                    'java',
                    'dotnet',
                    'ruby',
                    'static',
                  ].map((value) => ({
                    value,
                    label: value === 'auto' ? 'Detect automatically' : value,
                  }))}
                />
              </label>
            )}
            <details
              className="form-disclosure"
              open={
                Boolean(
                  buildArgs ||
                  buildSecrets ||
                  fieldError(error, 'architecture') ||
                  fieldGroupError(error, 'build_args') ||
                  fieldError(error, 'build_secrets'),
                ) || undefined
              }
            >
              <summary>
                Advanced build options{' '}
                <span className="muted-text">
                  {architecture ? `Linux ${architecture.toUpperCase()}` : 'Architecture inferred'}
                  {buildArgs.trim() && ' · public values'}
                  {buildSecrets.trim() && ' · secret references'}
                </span>
              </summary>
              <div className="create-fields">
                <label>
                  Target architecture
                  <SelectField
                    label="Target architecture"
                    value={architecture}
                    error={fieldError(error, 'architecture', `services.${service}.architecture`)}
                    onValueChange={(value) => setArchitecture(value as Build['architecture'] | '')}
                    options={[
                      { value: '', label: 'Infer from a uniform cluster' },
                      { value: 'amd64', label: 'Linux AMD64' },
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
                    One NAME=value per line. These values are baked into the image, so never enter
                    secrets here.
                  </span>
                </label>
                <label>
                  Build secret references
                  <Textarea
                    rows={3}
                    value={buildSecrets}
                    error={fieldError(error, 'build_secrets', `services.${service}.build_secrets`)}
                    onChange={(e) => setBuildSecrets(e.target.value)}
                    placeholder="npm_token=NPM_TOKEN"
                    aria-describedby="build-secrets-help"
                    spellCheck={false}
                  />
                  <span id="build-secrets-help" className="field-help">
                    One mount_id=CI_SECRET_NAME per line, naming a secret already saved in{' '}
                    {provider === 'gitlab' ? 'GitLab CI variables' : 'GitHub Actions secrets'}.
                    Never enter a secret value. Dockerfile builds read /run/secrets/mount_id;
                    framework steps also receive the named variable. Buildpacks do not support
                    secret mounts.
                  </span>
                </label>
              </div>
            </details>
          </fieldset>

          {/* Service: where the image runs. */}
          <fieldset
            disabled={busy || step !== 2}
            className={step === 2 ? 'build-flow-step' : 'hidden'}
          >
            <FlowSection
              title={linked ? 'Service that receives the image' : 'New application'}
              help={
                linked
                  ? 'A successful build replaces this service’s image. Its resources and networking stay controlled by the application configuration.'
                  : 'The application is created when you deploy the first verified build. No container image is needed now.'
              }
            >
              <div className="create-fields">
                <label>
                  Application name
                  <Input
                    value={name}
                    error={fieldError(error, 'name', 'application')}
                    readOnly={Boolean(build || application)}
                    onChange={(e) => {
                      nameEdited.current = true
                      setName(e.target.value)
                    }}
                    pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?"
                    title="Use up to 40 lowercase letters, numbers or hyphens. Start with a letter and end with a letter or number."
                    maxLength={40}
                    required
                  />
                  {!build && !application && (
                    <span className="field-help">Lowercase letters, numbers and hyphens.</span>
                  )}
                </label>
                {application ? (
                  <label>
                    Service
                    <SelectField
                      label="Service"
                      value={service}
                      error={fieldError(error, 'service', `services.${service}.service`)}
                      onValueChange={setService}
                      options={Object.keys(application.spec.services).map((item) => ({
                        value: item,
                        label: item,
                      }))}
                    />
                  </label>
                ) : (
                  <label>
                    Service
                    <Input
                      value={service}
                      error={fieldError(error, 'service')}
                      readOnly={Boolean(build)}
                      onChange={(e) => setService(e.target.value)}
                      pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?"
                      maxLength={40}
                      required
                    />
                  </label>
                )}
              </div>
              {!linked && (
                <div className="create-fields">
                  <label>
                    Service port
                    <Input
                      type="number"
                      min={1}
                      max={65535}
                      value={servicePort}
                      error={fieldError(error, 'port', `services.${service}.port`)}
                      readOnly={mode === 'framework' && framework.runtime === 'static'}
                      onChange={(e) => setPort(Number(e.target.value))}
                      required
                    />
                    {mode === 'framework' && (
                      <span className="field-help">
                        {framework.runtime === 'static'
                          ? 'Static files are served on 8080.'
                          : 'Matches the recipe’s server port.'}
                      </span>
                    )}
                  </label>
                  <label>
                    Resource profile
                    <SelectField
                      label="Resource profile"
                      value={size}
                      error={fieldError(error, 'size', `services.${service}.size`)}
                      onValueChange={setSize}
                      options={['small', 'medium', 'large'].map((value) => ({
                        value,
                        label:
                          features.hostedFree && value !== 'small'
                            ? `${value} · Requires your own server`
                            : value,
                        disabled: features.hostedFree && value !== 'small',
                      }))}
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
                  Expose the service publicly over HTTP
                </label>
              )}
            </FlowSection>
            {boundApplicationId && !features.hostedCompute && (
              <FlowSection
                title="Reuse this image"
                help="Selected services receive the same verified image on deployment. Each keeps its own run command, variables, ports and volumes."
              >
                {linkedApp.error && (
                  <Note>
                    The application services could not be loaded. Your saved selections are kept;
                    reload before changing them.
                  </Note>
                )}
                {Object.keys(reuseApplication?.spec.services || {}).filter(
                  (item) => item !== service,
                ).length === 0 ? (
                  <p className="field-help">This application has no other services.</p>
                ) : (
                  <div className="build-checks">
                    {Object.keys(reuseApplication?.spec.services || {})
                      .filter((item) => item !== service)
                      .map((item) => (
                        <label key={item} className="checkbox-row">
                          <Input
                            type="checkbox"
                            checked={reuseServices.includes(item)}
                            onChange={(event) =>
                              setReuseServices((current) =>
                                event.target.checked
                                  ? [...current, item]
                                  : current.filter((value) => value !== item),
                              )
                            }
                          />
                          <code>{item}</code>
                        </label>
                      ))}
                  </div>
                )}
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  className="justify-self-start"
                  disabled={linkedApp.isFetching}
                  onClick={() => void linkedApp.refetch()}
                >
                  <Icon name="refresh" size={14} />
                  Refresh services
                </Button>
              </FlowSection>
            )}
            <details
              className="form-disclosure"
              open={Boolean(registry || fieldError(error, 'registry_credential')) || undefined}
            >
              <summary>
                Image registry{' '}
                <span className="muted-text">
                  {registry ||
                    (managedRegistryAvailable ? 'Hakopod private registry' : 'Automatic')}
                </span>
              </summary>
              <div className="create-fields">
                <label>
                  Pull credential
                  <SelectField
                    label="Pull credential"
                    value={registry}
                    error={fieldError(
                      error,
                      'registry_credential',
                      `services.${service}.registry_credential`,
                    )}
                    onValueChange={setRegistry}
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
                        label: `${item.name} · ${item.registry}`,
                      })) ?? []),
                    ]}
                  />
                </label>
                <p className="field-help">
                  {managedRegistryAvailable
                    ? 'GitHub App builds push to the Hakopod private registry and pull credentials are set up for you. Choosing a saved credential uses your own registry instead.'
                    : 'Saved credentials that match the image registry are tried automatically. Choose one to try it first.'}
                </p>
                <div className="flex flex-wrap gap-2">
                  <Button type="button" size="sm" asChild>
                    <a
                      href="/infrastructure/registries/new"
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      Add registry credential
                      <Icon name="external" size={13} />
                      <span className="sr-only"> (opens in a new tab)</span>
                    </a>
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    disabled={registries.isFetching}
                    onClick={() => void registries.refetch()}
                  >
                    <Icon name="refresh" size={14} />
                    Refresh credentials
                  </Button>
                </div>
              </div>
            </details>
          </fieldset>

          {/* Runtime: how the deployed container starts, and what happens on push. */}
          <fieldset
            disabled={busy || step !== 3}
            className={step === 3 ? 'build-flow-step' : 'hidden'}
          >
            <FlowSection title="Run command">
              <ChoiceCards
                compact
                label="Run command"
                value={runtimeMode}
                onChange={setRuntimeMode}
                choices={[
                  ...(linked
                    ? [
                        {
                          value: 'preserve' as const,
                          title: 'Keep service settings',
                          description: 'The service keeps its current command and arguments.',
                        },
                      ]
                    : []),
                  {
                    value: 'default' as const,
                    title: 'Image defaults',
                    description: linked
                      ? 'Use ENTRYPOINT and CMD; clears the service override.'
                      : 'Use the image ENTRYPOINT and CMD.',
                  },
                  {
                    value: 'override' as const,
                    title: 'Custom command',
                    description: 'Replace the entrypoint, the arguments, or both.',
                  },
                ]}
              />
              {runtimeMode === 'override' && (
                <div className="create-fields">
                  <label>
                    Command
                    <Input
                      value={runtimeCommand}
                      error={fieldError(error, 'command', `services.${service}.command`)}
                      onChange={(event) => setRuntimeCommand(event.target.value)}
                      placeholder="uvicorn"
                      maxLength={8192}
                    />
                    <span className="field-help">Replaces ENTRYPOINT. Leave blank to keep it.</span>
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
                    <span className="field-help">
                      Replaces CMD. Quotes group arguments; use an explicit shell for shell
                      expressions.
                    </span>
                  </label>
                </div>
              )}
            </FlowSection>
            <FlowSection title="Variables and secrets">
              {boundApplicationId && (
                <label className="checkbox-row">
                  <Input
                    type="checkbox"
                    checked={runtimeEnvEnabled}
                    onChange={(event) => setRuntimeEnvEnabled(event.target.checked)}
                  />
                  Replace the service’s variables when this build is deployed
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
                <p className="field-help">The service keeps its current variables and secrets.</p>
              )}
            </FlowSection>
            <FlowSection title="Automation">
              <div className="build-checks">
                <label className="checkbox-row">
                  <Input
                    type="checkbox"
                    checked={automatic}
                    onChange={(e) => {
                      setAutomatic(e.target.checked)
                      if (!e.target.checked) setAutoDeploy(false)
                    }}
                  />
                  Build on every push to {branch || 'the branch'}
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
              </div>
              {automatic && (
                <p className="field-help">
                  Push builds need the installed workflow. Automatic deployment also needs the{' '}
                  {provider === 'gitlab'
                    ? 'authenticated GitLab Pipeline Hook'
                    : 'signed GitHub workflow event'}{' '}
                  and your continuing project access.
                </p>
              )}
            </FlowSection>
          </fieldset>

          {step === reviewStep && (
            <div className="build-flow-step">
              <div className="review-services build-review">
                <ReviewCard
                  title="Source"
                  icon={<ProviderMark provider={provider} />}
                  edit={edit(0)}
                >
                  <Fact label="Repository">
                    <code>{repository}</code>
                  </Fact>
                  <Fact label="Branch">
                    <code>{branch}</code>
                  </Fact>
                  <Fact label="Build directory">
                    {context === '.' ? 'Repository root' : <code>{context}</code>}
                    {submodules ? ' · with submodules' : ' · no submodules'}
                  </Fact>
                  <Fact label="Runs in">{runner}</Fact>
                </ReviewCard>
                <ReviewCard title="Build" icon={<Icon name="code" size={16} />} edit={edit(1)}>
                  <Fact label="Method">
                    {mode === 'framework'
                      ? `${frameworkLabel(framework.framework)} · ${framework.package_manager}${!build && !detection && !recipeEdited.current ? ' · default recipe, not detected' : ''}`
                      : mode === 'buildpacks'
                        ? `Buildpacks · ${preset}`
                        : 'Dockerfile'}
                  </Fact>
                  {mode === 'dockerfile' && (
                    <Fact label="Dockerfile">
                      <code>{dockerfile}</code>
                    </Fact>
                  )}
                  {mode === 'framework' && (
                    <Fact label="Commands">
                      <span className="build-review-commands">
                        <code>{framework.install_command}</code>
                        <code>{framework.build_command}</code>
                        <code>
                          {framework.runtime === 'static'
                            ? `serve ${framework.output_directory}`
                            : framework.start_command}
                        </code>
                      </span>
                    </Fact>
                  )}
                  <Fact label="Architecture">
                    {architecture ? `Linux ${architecture.toUpperCase()}` : 'Inferred from cluster'}
                  </Fact>
                  {(buildArgs.trim() || buildSecrets.trim()) && (
                    <Fact label="Build inputs">
                      {plural(
                        buildArgs.split('\n').filter((line) => line.trim()).length,
                        'public value',
                      )}{' '}
                      ·{' '}
                      {plural(
                        buildSecrets.split('\n').filter((line) => line.trim()).length,
                        'secret reference',
                      )}
                    </Fact>
                  )}
                </ReviewCard>
                <ReviewCard title="Service" icon={<Icon name="box" size={16} />} edit={edit(2)}>
                  <Fact label="Deploys to">
                    {project} / {environment} / <strong>{name}</strong> / {service}
                  </Fact>
                  <Fact label="Resources">
                    {linked
                      ? 'Kept from the application'
                      : `${size} · port ${servicePort} · ${isPublic ? 'public HTTP' : 'private'}`}
                  </Fact>
                  <Fact label="Pull credential">
                    {registry ||
                      (managedRegistryAvailable ? 'Hakopod private registry' : 'Automatic')}
                  </Fact>
                  {reuseServices.length > 0 && (
                    <Fact label="Also updates">{reuseServices.join(', ')}</Fact>
                  )}
                </ReviewCard>
                <ReviewCard
                  title="Runtime"
                  icon={<Icon name="terminal" size={16} />}
                  edit={edit(3)}
                >
                  <Fact label="Run command">
                    {runtimeMode === 'preserve' ? (
                      'Keep service settings'
                    ) : runtimeMode === 'default' ? (
                      'Image defaults'
                    ) : (
                      <code>
                        {runtimeCommand || '(image entrypoint)'} {runtimeArgs || '(image command)'}
                      </code>
                    )}
                  </Fact>
                  <Fact label="Variables">
                    {runtimeEnvEnabled
                      ? `${plural(runtimeVariables, 'variable')} · ${plural(runtimeSecrets, 'secret')}`
                      : 'Keep current variables and secrets'}
                  </Fact>
                  <Fact label="Automation">
                    {automatic
                      ? autoDeploy
                        ? 'Build and deploy on push'
                        : 'Build on push, deploy after review'
                      : 'Manual builds and deployments'}
                  </Fact>
                </ReviewCard>
              </div>
              <section className="build-next" aria-labelledby="build-next-heading">
                <h2 id="build-next-heading">What happens next</h2>
                <ol>
                  <li>
                    {build
                      ? 'Hakopod saves a new configuration revision. Nothing is deployed.'
                      : 'Hakopod saves the build configuration. Nothing is deployed.'}
                  </li>
                  <li>
                    You review the generated {runner} workflow, and a repository manager commits it
                    to <code>{repository}</code>
                    {provider === 'gitlab' ? ' as the managed .gitlab-ci.yml.' : '.'}
                  </li>
                  <li>
                    {automatic
                      ? `Each push to ${branch} builds an image. Hakopod verifies its digest${autoDeploy ? ' and deploys it.' : '; you review the deployment.'}`
                      : 'You run a build, then review deploying its verified image.'}
                  </li>
                </ol>
                {provider === 'gitlab' && (
                  <p className="field-help">
                    Installation refuses an existing .gitlab-ci.yml that Hakopod does not manage, or
                    one owned by another build. Configure Pipeline events on the project webhook for
                    automatic deployment.
                  </p>
                )}
              </section>
            </div>
          )}
        </div>
        <div className="form-footer deploy-footer">
          <span className="dialog-footer-note">
            <Icon name="lock" size={13} />
            {step === reviewStep
              ? 'Only the reviewed configuration is saved'
              : 'Nothing is saved until the review'}
          </span>
          <div className="deploy-footer-actions">
            <Button
              type="button"
              disabled={busy || detecting}
              onClick={() => (step > 0 ? showStep(step - 1) : onClose())}
            >
              {step > 0 ? 'Back' : 'Cancel'}
            </Button>
            <PendingButton
              type="submit"
              variant="primary"
              pending={busy || detecting}
              pendingLabel={pendingLabel}
              disabled={step === 0 && !build && !leaving && noBuildConnection}
            >
              {primaryLabel}
              {step < reviewStep && <Icon name="arrow" size={15} />}
            </PendingButton>
          </div>
        </div>
      </form>
    </FormPage>
  )
}

function FlowSection({
  title,
  help,
  children,
}: {
  title: string
  help?: string
  children: ReactNode
}) {
  return (
    <section className="create-section build-flow-section">
      <div className="create-section-heading">
        <div className="hako-section-heading-title">
          <h2>{title}</h2>
          {help && <HeadingHelp title={title}>{help}</HeadingHelp>}
        </div>
      </div>
      {children}
    </section>
  )
}

function ReviewCard({
  title,
  icon,
  edit,
  children,
}: {
  title: string
  icon: ReactNode
  edit: ReactNode
  children: ReactNode
}) {
  return (
    <section className="review-service" aria-label={title}>
      <header>
        <div className="build-review-icon" aria-hidden="true">
          {icon}
        </div>
        <strong>{title}</strong>
        <span>{edit}</span>
      </header>
      <dl>{children}</dl>
    </section>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}
