import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Empty, ErrorState, Loading, Note, Status } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { ServiceIcon } from '../components/service-icon'
import { message, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { canAccess, useScope } from '../lib/scope'
import {
  availableManagedPlatformEntries,
  managedPlatformName,
  platformSearch,
  useManagedPlatformCatalog,
  type ManagedPlatformCatalog,
  type ManagedPlatformDefaults,
  type ManagedPlatformReviewResponse,
  type ManagedPlatform,
} from '../lib/managed-platforms'
import { projectRouteScopeMatches } from '../lib/projects'
import {
  completePlatformSpec,
  platformSecretKeys,
  platformSteps,
  type PlatformStage,
} from '../lib/platform-wizard'

export const Route = createFileRoute('/platforms/new')({
  validateSearch: platformSearch,
  component: Page,
})
const WizardPage = createContext<ReactNode>(null)
const rotatableSecrets = new Set([
  'auth-database-url',
  'database-owner-password',
  'database-role-bootstrap',
  'postgres-meta-database-password',
  'realtime-database-password',
  'rest-database-url',
  'storage-database-url',
  'supavisor-database-url',
  'database-tls-certificate',
  'gateway-tls-certificate',
  'envoy-runtime-config',
])
export function PlatformWizardStep() {
  return useContext(WizardPage)
}

function Page() {
  const scope = Route.useSearch()
  if (!scope.project || !scope.environment)
    return (
      <PlatformFormState>
        <Empty
          title="Choose a project and environment"
          description="Open Platforms from the project where this platform belongs."
        />
      </PlatformFormState>
    )
  return <CreatePlatform key={`${scope.project}/${scope.environment}`} {...scope} />
}

export function PlatformFormState({
  children,
  editing = false,
}: {
  children: ReactNode
  editing?: boolean
}) {
  return (
    <FormPage
      title={editing ? 'Configure platform' : 'New platform'}
      description="Configure the platform, then review the plan before applying it."
      breadcrumbs={[]}
    >
      {children}
    </FormPage>
  )
}

function CreatePlatform({ project, environment }: { project: string; environment: string }) {
  const workspace = useScope()
  const { identity } = workspace
  const scopeMatches = projectRouteScopeMatches(workspace, { project, environment })
  const canManage = !identity.application && canAccess(identity, project, 'deployments:write')
  const query = useManagedPlatformCatalog(project, environment, canManage && scopeMatches)
  if (!scopeMatches)
    return (
      <PlatformFormState>
        <Empty
          title="Workspace unavailable"
          description="Choose an accessible project and environment above."
        />
      </PlatformFormState>
    )
  if (identity.application || !canManage)
    return (
      <PlatformFormState>
        <ErrorState
          error={new Error('You do not have permission to create platforms in this project.')}
        />
      </PlatformFormState>
    )
  if (query.isPending)
    return (
      <PlatformFormState>
        <Loading />
      </PlatformFormState>
    )
  if (query.error)
    return (
      <PlatformFormState>
        <ErrorState error={query.error} />
      </PlatformFormState>
    )
  const items = availableManagedPlatformEntries(query.data)
  if (!items.length)
    return (
      <PlatformFormState>
        <Empty
          title="Platform creation unavailable"
          description="No managed platforms have completed the required checks on this server."
        />
      </PlatformFormState>
    )
  return <PlatformForm catalog={{ ...query.data, items }} />
}

export function PlatformForm({
  catalog,
  initial: initialInput,
}: {
  catalog: ManagedPlatformCatalog
  initial?: ManagedPlatform
}) {
  const initial = useRef(initialInput).current
  const resourcesOnly = initial?.spec.kind === 'neon'
  const pathname = useLocation().pathname
  const basePath = initial ? `/platforms/${initial.id}/configure` : '/platforms/new'
  const firstStep = resourcesOnly ? 'resources' : 'platform'
  const stepID = pathname === basePath ? firstStep : pathname.split('/').at(-1) || firstStep
  const [kind, setKind] = useState<'supabase' | 'neon'>(
    initial?.spec.kind || catalog.items[0]?.kind || 'supabase',
  )
  const [drafts, setDrafts] = useState<
    Partial<Record<'supabase' | 'neon', ManagedPlatformDefaults>>
  >(() =>
    Object.fromEntries(
      catalog.items.map((item) => [
        item.kind,
        initial?.spec.kind === item.kind ? initial.spec : item.default_spec,
      ]),
    ),
  )
  const [nodesByKind, setNodesByKind] = useState<{ supabase: string[]; neon: string[] }>(() => ({
    supabase: initial?.spec.kind === 'supabase' ? initial.spec.placement.node_names : [],
    neon: initial?.spec.kind === 'neon' ? initial.spec.placement.node_names : [],
  }))
  const [secretsByKind, setSecretsByKind] = useState<{
    supabase: Record<string, string>
    neon: Record<string, string>
  }>(() => {
    const selected = Object.fromEntries(
      Object.entries(initial?.spec.secrets || {}).map(([key, value]) => [
        key,
        `${value.name}@${value.revision}`,
      ]),
    )
    return {
      supabase: initial?.spec.kind === 'supabase' ? selected : {},
      neon: initial?.spec.kind === 'neon' ? selected : {},
    }
  })
  const [review, setReview] = useState<ManagedPlatformReviewResponse | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [, refreshExpiry] = useState(0)
  const [error, setError] = useState('')
  const idempotency = useRef('')
  const mutation = useRef(false)
  const heading = useRef<HTMLParagraphElement>(null)
  const cache = useQueryClient()
  const navigate = useNavigate()
  const scope = { project: catalog.project, environment: catalog.environment }
  const go = (step: string) =>
    initial
      ? navigate({
          to: '/platforms/$platformId/configure/$step',
          params: { platformId: initial.id, step },
          search: scope,
        })
      : navigate({ to: '/platforms/new/$step', params: { step }, search: scope })
  const entry = catalog.items.find((item) => item.kind === kind)
  const draft = drafts[kind]
  const requiredSecrets = platformSecretKeys(entry, initial?.spec)
  const steps = platformSteps(kind, requiredSecrets).filter(
    (step) => !resourcesOnly || step.stage === 'Resources' || step.stage === 'Review',
  )
  const resourceName = stepID.startsWith('resource-') ? stepID.slice('resource-'.length) : ''
  const resourceExists = Boolean(
    resourceName && draft && (resourceName in draft.resources || resourceName in draft.storage),
  )
  const index = resourceExists
    ? steps.findIndex((step) => step.id === 'resources')
    : steps.findIndex((step) => step.id === stepID)
  const current = steps[index]
  useEffect(() => {
    heading.current?.focus()
  }, [stepID])
  useEffect(() => {
    const deadline = Date.parse(review?.review?.expires_at || '')
    if (!Number.isFinite(deadline)) return
    const timer = window.setTimeout(
      () => {
        setConfirmed(false)
        refreshExpiry((value) => value + 1)
      },
      Math.min(Math.max(deadline - Date.now(), 0), 2_147_483_647),
    )
    return () => window.clearTimeout(timer)
  }, [review?.review?.expires_at])
  if (!entry || !draft)
    return (
      <PlatformFormState editing={Boolean(initial)}>
        <ErrorState
          error={new Error('The selected platform configuration is no longer available.')}
        />
      </PlatformFormState>
    )
  if (!current)
    return (
      <PlatformFormState editing={Boolean(initial)}>
        <Empty
          title="Step unavailable"
          description="Return to the first step to continue configuring this platform."
          action={<Button onClick={() => void go(firstStep)}>First step</Button>}
        />
      </PlatformFormState>
    )
  const nodes = nodesByKind[kind]
  const secrets = secretsByKind[kind]
  const expired = Boolean(
    review?.review &&
    (!Number.isFinite(Date.parse(review.review.expires_at)) ||
      Date.parse(review.review.expires_at) <= Date.now()),
  )
  const resetReview = () => {
    setReview(null)
    setConfirmed(false)
    setError('')
    idempotency.current = ''
  }
  const update = (next: ManagedPlatformDefaults) => {
    setDrafts((values) => ({ ...values, [next.kind]: next }))
    resetReview()
  }
  const chooseKind = (value: string) => {
    const next = catalog.items.find((item) => item.kind === value)
    if (next) {
      setKind(next.kind)
      resetReview()
    }
  }
  const toggleNode = (name: string) => {
    setNodesByKind((values) => ({
      ...values,
      [kind]: nodes.includes(name)
        ? nodes.filter((value) => value !== name)
        : nodes.length < entry.maximum_nodes
          ? [...nodes, name]
          : nodes,
    }))
    resetReview()
  }
  const chooseSecret = (key: string, value: string) => {
    setSecretsByKind((values) => ({ ...values, [kind]: { ...values[kind], [key]: value } }))
    resetReview()
  }
  const spec = resourcesOnly
    ? { ...initial.spec, resources: draft.resources }
    : completePlatformSpec(draft, nodes, requiredSecrets, secrets, catalog.secret_references)
  const stages: PlatformStage[] = [...new Set(steps.map((step) => step.stage))]
  const secretKeys = stepID.startsWith('secrets-')
    ? requiredSecrets.slice((Number(stepID.slice(8)) - 1) * 4, Number(stepID.slice(8)) * 4)
    : []
  async function submit() {
    if (mutation.current) return
    if (resourceExists) {
      await go('resources')
      return
    }
    if (
      stepID === 'placement' &&
      (nodes.length < entry!.minimum_nodes || nodes.length > entry!.maximum_nodes)
    ) {
      setError(
        `Choose ${entry!.minimum_nodes === entry!.maximum_nodes ? entry!.minimum_nodes : `${entry!.minimum_nodes} to ${entry!.maximum_nodes}`} nodes.`,
      )
      return
    }
    if (stepID !== 'review') {
      setError('')
      await go(steps[index + 1].id)
      return
    }
    if (
      !spec ||
      !draft!.name ||
      nodes.length < entry!.minimum_nodes ||
      nodes.length > entry!.maximum_nodes
    ) {
      setError(
        'Complete the platform name, placement and secret references before review. Your entries are saved while you visit earlier steps.',
      )
      return
    }
    mutation.current = true
    setBusy(true)
    setError('')
    const intent = {
      project: catalog.project,
      environment: catalog.environment,
      expected_revision: initial?.revision || 0,
      kind: initial ? ('update' as const) : ('create' as const),
      spec,
      ...(initial ? { id: initial.id, confirm_name: initial.spec.name } : {}),
    }
    try {
      if (!review || expired) {
        setReview(await unwrap(client.POST('/managed-platforms/reviews', { body: intent })))
        setConfirmed(false)
        return
      }
      if (!review.review || review.blocked)
        throw new Error('The server did not authorize this platform plan.')
      if (!confirmed) throw new Error('Confirm the reviewed plan before creating the platform.')
      if (!idempotency.current) idempotency.current = crypto.randomUUID()
      const operation = await unwrap(
        client.POST('/managed-platforms/operations', {
          params: { header: { 'Idempotency-Key': idempotency.current } },
          body: { ...intent, id: review.platform.id, review: review.review },
        }),
      )
      void cache.invalidateQueries({ queryKey: ['managed-platforms'] })
      await navigate({
        to: '/platforms/$platformId',
        params: { platformId: operation.platform_id },
        search: scope,
      })
    } catch (value) {
      setError(message(value))
    } finally {
      mutation.current = false
      setBusy(false)
    }
  }
  const content = (
    <FormPage
      title={initial ? 'Configure platform' : 'New platform'}
      description="Choose the platform, configure its resources, then review the plan before applying it."
      breadcrumbs={[]}
      keepFocusedControlsVisible
    >
      <nav
        aria-label="Platform setup"
        className="hako-page-steps mb-4 flex flex-wrap gap-x-4 gap-y-2 border-b border-border pb-3"
      >
        {stages.map((stage) => {
          const target = steps.find((step) => step.stage === stage)
          return (
            target && (
              <Button
                key={stage}
                type="button"
                variant="ghost"
                disabled={busy}
                aria-current={current.stage === stage ? 'step' : undefined}
                className={`bg-transparent! border-transparent! ${current.stage === stage ? 'text-[var(--navigation-active)]!' : 'text-muted-foreground'}`}
                onClick={() => void go(target.id)}
              >
                {stage}
              </Button>
            )
          )
        })}
      </nav>
      <form
        onSubmit={(event) => {
          event.preventDefault()
          void submit()
        }}
      >
        <p
          ref={heading}
          tabIndex={-1}
          aria-label={`${resourceExists ? humanName(resourceName) : current.title}. ${resourceExists ? 'Optional settings' : `Step ${index + 1} of ${steps.length}`}`}
          className="mb-3 text-xs text-muted-foreground outline-offset-4 focus-visible:outline-2"
        >
          {resourceExists ? 'Optional settings' : `Step ${index + 1} of ${steps.length}`}
        </p>
        {stepID === 'platform' && (
          <FormSection title="Platform">
            <div className="mb-4 grid gap-3 sm:grid-cols-2">
              {catalog.items.map((item) => (
                <button
                  key={item.kind}
                  type="button"
                  aria-pressed={kind === item.kind}
                  disabled={busy || Boolean(initial)}
                  className={`flex min-w-0 items-start gap-3 rounded-md border p-4 text-left outline-offset-2 focus-visible:outline-2 focus-visible:outline-primary ${kind === item.kind ? 'border-primary' : 'border-border'}`}
                  onClick={() => chooseKind(item.kind)}
                >
                  <ServiceIcon name={item.kind} size={28} loading="eager" />
                  <span className="min-w-0">
                    <strong className="block text-sm">{managedPlatformName(item.kind)}</strong>
                    <span className="mt-1 block text-xs text-muted-foreground">
                      {item.kind === 'supabase'
                        ? 'Postgres, authentication, storage and APIs'
                        : 'Postgres compute with separate storage'}
                    </span>
                    <span className="mt-2 block font-mono text-xs">
                      {item.kind === 'neon' ? item.version.slice(0, 12) : item.version}
                    </span>
                  </span>
                </button>
              ))}
            </div>
            <label>
              Name
              <Input
                required
                maxLength={40}
                pattern="[a-z][a-z0-9\-]*"
                autoComplete="off"
                value={draft.name}
                disabled={busy || Boolean(initial)}
                onChange={(event) => update({ ...draft, name: event.target.value })}
              />
              <span className="field-help">
                Start with a lowercase letter. Use letters, numbers and hyphens.
              </span>
            </label>
          </FormSection>
        )}
        {current.stage === 'Settings' && (
          <PlatformSettings
            step={stepID}
            draft={draft}
            busy={busy}
            update={update}
          />
        )}
        {stepID === 'placement' && (
          <FormSection
            title="Placement"
            description="Choose from the capacity nodes configured for this environment. Node labels do not establish availability across physical zones."
          >
            {!catalog.nodes.length ? (
              <Note>No capacity nodes are configured for this environment.</Note>
            ) : (
              <div className="grid gap-2 sm:grid-cols-2">
                {catalog.nodes.map((node) => (
                  <label
                    key={node.uid}
                    className="flex min-w-0 items-start gap-2 rounded border border-border p-3"
                  >
                    <input
                      type="checkbox"
                      className="mt-1"
                      checked={nodes.includes(node.name)}
                      disabled={
                        busy ||
                        (!nodes.includes(node.name) &&
                          (nodes.length >= entry.maximum_nodes ||
                            node.architecture !== 'amd64' ||
                            node.operating_system !== 'linux'))
                      }
                      onChange={() => toggleNode(node.name)}
                    />
                    <span className="min-w-0">
                      <span className="block break-all">{node.name}</span>
                      {(node.architecture !== 'amd64' || node.operating_system !== 'linux') && (
                        <span className="field-help block">Requires a Linux AMD64 node.</span>
                      )}
                    </span>
                  </label>
                ))}
              </div>
            )}
            <p className="field-help mt-3">
              Choose{' '}
              {entry.minimum_nodes === entry.maximum_nodes
                ? entry.minimum_nodes
                : `${entry.minimum_nodes} to ${entry.maximum_nodes}`}{' '}
              node{entry.maximum_nodes === 1 ? '' : 's'}.
            </p>
            {draft.kind === 'neon' && (
              <div className="mt-4">
                <label>
                  Member separation
                  <SelectField
                    required
                    label="Member separation"
                    value={draft.placement.spread || ''}
                    disabled={busy}
                    options={[
                      { value: 'nodes', label: 'One member per node' },
                      { value: 'zones', label: 'One member per zone' },
                    ]}
                    onValueChange={(spread) =>
                      update({
                        ...draft,
                        placement: { ...draft.placement, spread: spread as 'nodes' | 'zones' },
                      })
                    }
                  />
                </label>
              </div>
            )}
          </FormSection>
        )}
        {current.stage === 'Secrets' && (
          <FormSection
            title={current.title}
            description="Choose existing secret revisions. Secret values stay hidden."
          >
            {initial && (
              <Note>
                Change all eight database credential references together. Database TLS can rotate
                separately; gateway TLS must change with its Envoy configuration. Other secret
                rotations are unavailable.
              </Note>
            )}
            {!catalog.secret_references.length ? (
              <Note>No secret references are configured for this scope.</Note>
            ) : (
              <div className="grid gap-4 sm:grid-cols-2">
                {secretKeys.map((key) => (
                  <label key={key}>
                    {humanName(key)}
                    <SelectField
                      required
                      label={humanName(key)}
                      value={secrets[key] || ''}
                      disabled={busy || Boolean(initial && !rotatableSecrets.has(key))}
                      onValueChange={(value) => chooseSecret(key, value)}
                      options={[
                        { value: '', label: 'Choose a secret reference' },
                        ...catalog.secret_references.map((reference) => ({
                          value: `${reference.name}@${reference.revision}`,
                          label: `${reference.name} · revision ${reference.revision}`,
                        })),
                      ]}
                    />
                  </label>
                ))}
              </div>
            )}
          </FormSection>
        )}
        {stepID === 'resources' && (
          <FormSection
            title="Resource allocation"
            description="The server checks these requests against the available capacity before creation. Open a component to change its allocation."
          >
            {resourcesOnly && (
              <Note>Change CPU and memory here. Storage, nodes, topology and secret references stay fixed.</Note>
            )}
            <ul className="divide-y divide-border">
              {[...new Set([...Object.keys(draft.resources), ...Object.keys(draft.storage)])].map(
                (name) => (
                  <li
                    key={name}
                    className="flex min-w-0 flex-wrap items-center justify-between gap-3 py-3"
                  >
                    <div className="min-w-0">
                      <h3 className="break-all text-sm font-medium">{humanName(name)}</h3>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {draft.resources[name] &&
                          `${draft.resources[name].cpu} CPU · ${draft.resources[name].memory} memory`}
                        {draft.storage[name] ? ` · ${draft.storage[name]} GiB storage` : ''}
                      </p>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      onClick={() => void go(`resource-${name}`)}
                      aria-label={`Edit ${humanName(name)} resources`}
                    >
                      Edit
                    </Button>
                  </li>
                ),
              )}
            </ul>
          </FormSection>
        )}
        {resourceExists && (
          <ResourceFields name={resourceName} draft={draft} busy={busy} update={update} lockStorage={resourcesOnly} />
        )}
        {stepID === 'review' && (
          <FormSection title="Plan">
            {initial && (
              <Note>
                Applying changes can restart platform services and interrupt connections. Review the
                resources and secret references before continuing.
              </Note>
            )}
            <dl className="grid gap-3 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-muted-foreground">Platform</dt>
                <dd className="break-all">
                  {draft.name || 'Name required'} · {managedPlatformName(draft.kind)}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Scope</dt>
                <dd className="break-all">
                  {catalog.project} / {catalog.environment}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Nodes</dt>
                <dd className="break-all">{nodes.join(', ') || 'Choose nodes'}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Secret references</dt>
                <dd>
                  {requiredSecrets.filter((key) => Boolean(secrets[key])).length} of{' '}
                  {requiredSecrets.length} selected
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">TLS certificates</dt>
                <dd>
                  {draft.tls_mode === 'managed'
                    ? 'Issued and renewed by Hakopod'
                    : 'Supplied by your operator'}
                </dd>
              </div>
            </dl>
            {review && (
              <div className="mt-4 grid gap-3 border-t border-border pt-4 text-sm">
                <p className="break-all">Namespace: {review.plan.namespace}</p>
                <p>
                  {review.plan.components.length} components · Storage class:{' '}
                  {review.plan.storage_class}
                </p>
                <p>Public service: {review.plan.public_service || 'None'}</p>
                <div className="flex flex-wrap items-center gap-2">
                  <Status value={review.plan.capability.available ? 'available' : 'blocked'} />
                  <span>{review.plan.capability.reason}</span>
                </div>
                {review.review && (
                  <p>
                    Review expires {timestamp(review.review.expires_at)}.
                    {expired ? ' Refresh the review to continue.' : ''}
                  </p>
                )}
                {review.review?.blocked_reasons.map((reason) => (
                  <Note key={reason}>{reason}</Note>
                ))}
                {review.blocked && !review.review?.blocked_reasons.length && (
                  <Note>Creation is unavailable for this plan.</Note>
                )}
                {!review.blocked && review.review && (
                  <label className="flex items-start gap-2">
                    <input
                      type="checkbox"
                      className="mt-1"
                      checked={confirmed}
                      disabled={busy || expired}
                      onChange={(event) => setConfirmed(event.target.checked)}
                    />
                    <span>
                      {initial
                        ? 'Apply these changes to the platform.'
                        : 'Create this platform with the reviewed resources.'}
                    </span>
                  </label>
                )}
                <div>
                  <Button type="button" disabled={busy} onClick={resetReview}>
                    Refresh review
                  </Button>
                </div>
              </div>
            )}
          </FormSection>
        )}
        {error && <FormError>{error}</FormError>}
        <div className="form-footer">
          <Button asChild>
            {initial ? (
              <Link
                to="/platforms/$platformId"
                params={{ platformId: initial.id }}
                search={{ ...scope, tab: 'configuration' }}
              >
                Cancel
              </Link>
            ) : (
              <Link to="/platforms" search={scope}>
                Cancel
              </Link>
            )}
          </Button>
          <div className="flex flex-wrap gap-2">
            {(index > 0 || resourceExists) && (
              <Button
                type="button"
                disabled={busy}
                onClick={() => void go(resourceExists ? 'resources' : steps[index - 1].id)}
              >
                Back
              </Button>
            )}
            <Button
              type="submit"
              variant="primary"
              disabled={
                busy ||
                (current.stage === 'Secrets' && !catalog.secret_references.length) ||
                Boolean(
                  stepID === 'review' &&
                  review &&
                  !expired &&
                  (review.blocked || !review.review || !confirmed),
                )
              }
            >
              {busy
                ? 'Working…'
                : resourceExists
                  ? 'Save allocation'
                  : stepID === 'review'
                    ? review && !expired
                      ? initial
                        ? 'Apply changes'
                        : 'Create platform'
                      : 'Review plan'
                    : 'Continue'}
            </Button>
          </div>
        </div>
      </form>
    </FormPage>
  )
  return (
    <WizardPage.Provider value={content}>
      {pathname === basePath ? content : <Outlet />}
    </WizardPage.Provider>
  )
}

function humanName(value: string) {
  return value
    .replaceAll('_', ' ')
    .replaceAll('-', ' ')
    .replace(/^./, (first) => first.toUpperCase())
}

type SettingsProps = {
  draft: ManagedPlatformDefaults
  busy: boolean
  update: (value: ManagedPlatformDefaults) => void
}
function PlatformSettings({
  step,
  draft,
  busy,
  update,
}: SettingsProps & { step: string }) {
  if (draft.kind === 'supabase') {
    const config = draft.supabase
    const set = (fields: Partial<typeof config>) =>
      update({ ...draft, supabase: { ...config, ...fields } })
    return (
      <FormSection
        title={
          step === 'connections'
            ? 'Application URLs'
            : step === 'database'
              ? 'Database and API'
              : 'Connections and signup'
        }
      >
        <div className="grid gap-4 sm:grid-cols-2">
          {step === 'connections' && (
            <>
              <label>
                Public HTTPS origin
                <Input
                  required
                  type="url"
                  value={config.public_url}
                  disabled={busy}
                  onChange={(e) => set({ public_url: e.target.value })}
                />
              </label>
              <label>
                Site HTTPS origin
                <Input
                  required
                  type="url"
                  value={config.site_url}
                  disabled={busy}
                  onChange={(e) => set({ site_url: e.target.value })}
                />
              </label>
              <label className="sm:col-span-2">
                Allowed redirect origins
                <Input
                  value={(config.redirect_urls || []).join(', ')}
                  disabled={busy}
                  onChange={(e) =>
                    set({
                      redirect_urls: e.target.value
                        .split(',')
                        .map((v) => v.trim())
                        .filter(Boolean),
                    })
                  }
                />
                <span className="field-help">
                  Comma-separated HTTPS origins. Use exact addresses.
                </span>
              </label>
            </>
          )}
          {step === 'database' && (
            <>
              <label>
                Database name
                <Input
                  required
                  value={config.database_name}
                  disabled
                />
                <span className="field-help">This Supabase release uses the postgres database.</span>
              </label>
              <NumberField
                label="JWT expiry (seconds)"
                value={config.jwt_expiry_seconds}
                min={300}
                max={86400}
                busy={busy}
                set={(value) => set({ jwt_expiry_seconds: value })}
              />
              <NumberField
                label="REST maximum rows"
                value={config.rest_max_rows}
                min={1}
                max={10000}
                busy={busy}
                set={(value) => set({ rest_max_rows: value })}
              />
              <NumberField
                label="Storage file limit (bytes)"
                value={config.storage_file_limit_bytes}
                min={1048576}
                max={5368709120}
                busy={busy}
                set={(value) => set({ storage_file_limit_bytes: value })}
              />
            </>
          )}
          {step === 'pool' && (
            <>
              <NumberField
                label="Pool size"
                value={config.pool_size}
                min={1}
                max={100}
                busy={busy}
                set={(value) => set({ pool_size: value })}
              />
              <NumberField
                label="Maximum pool clients"
                value={config.pool_max_clients}
                min={config.pool_size}
                max={1000}
                busy={busy}
                set={(value) => set({ pool_max_clients: value })}
              />
              <label className="flex items-start gap-2 sm:col-span-2">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={Boolean(config.anonymous_signup)}
                  disabled={busy}
                  onChange={(e) => set({ anonymous_signup: e.target.checked })}
                />
                <span>Allow anonymous signup</span>
              </label>
              <div className="sm:col-span-2">
                <Note>Email signup is unavailable.</Note>
              </div>
            </>
          )}
        </div>
      </FormSection>
    )
  }
  const config = draft.neon
  const set = (fields: Partial<typeof config>) =>
    update({ ...draft, neon: { ...config, ...fields } })
  return (
    <FormSection title={step === 'topology' ? 'Compute and storage nodes' : 'Object storage'}>
      <div className="grid gap-4 sm:grid-cols-2">
        {step === 'topology' ? (
          <>
            <NumberField
              label="Compute nodes"
              value={config.compute_replicas}
              min={1}
              max={6}
              busy={busy}
              describedBy="neon-compute-help"
              set={(value) => set({ compute_replicas: value })}
            />
            <NumberField
              label="Pageservers"
              value={config.pageservers}
              min={2}
              max={8}
              busy={busy}
              set={(value) => set({ pageservers: value })}
            />
            <p id="neon-compute-help" className="field-help self-center">One compute accepts writes. Additional computes are read-only replicas, with 64 total connections per compute.</p>
            <p className="field-help self-center">This release manages one branch per platform. Three safekeepers store the write-ahead log.</p>
          </>
        ) : (
          <>
            <label>
              Object storage HTTPS origin
              <Input
                required
                type="url"
                value={config.object_storage_url}
                disabled={busy}
                onChange={(e) => set({ object_storage_url: e.target.value })}
              />
            </label>
            <label>
              Bucket
              <Input
                required
                value={config.object_storage_bucket}
                disabled={busy}
                onChange={(e) => set({ object_storage_bucket: e.target.value })}
              />
            </label>
            <label>
              Region
              <Input
                required
                value={config.object_storage_region}
                disabled={busy}
                onChange={(e) => set({ object_storage_region: e.target.value })}
              />
            </label>
            <label>
              Resource prefix
              <Input
                required
                value={config.object_storage_prefix}
                disabled={busy}
                onChange={(e) => set({ object_storage_prefix: e.target.value })}
              />
            </label>
          </>
        )}
      </div>
    </FormSection>
  )
}

function ResourceFields({ name, draft, busy, update, lockStorage = false }: SettingsProps & { name: string; lockStorage?: boolean }) {
  const resource = draft.resources[name]
  return (
    <FormSection title={humanName(name)}>
      <div className="grid gap-4 sm:grid-cols-2">
        {resource && (
          <>
            <label>
              CPU
              <Input
                required
                value={resource.cpu}
                disabled={busy}
                onChange={(e) =>
                  update({
                    ...draft,
                    resources: { ...draft.resources, [name]: { ...resource, cpu: e.target.value } },
                  })
                }
              />
              <span className="field-help">For example, 500m is half a CPU.</span>
            </label>
            <label>
              Memory
              <Input
                required
                value={resource.memory}
                disabled={busy}
                onChange={(e) =>
                  update({
                    ...draft,
                    resources: {
                      ...draft.resources,
                      [name]: { ...resource, memory: e.target.value },
                    },
                  })
                }
              />
              <span className="field-help">For example, 512Mi or 2Gi.</span>
            </label>
          </>
        )}
        {name in draft.storage && (
          <NumberField
            label="Storage (GiB)"
            value={draft.storage[name]}
            min={1}
            max={1024}
            busy={busy || lockStorage}
            set={(value) => update({ ...draft, storage: { ...draft.storage, [name]: value } })}
          />
        )}
      </div>
    </FormSection>
  )
}
function NumberField({
  label,
  value,
  min,
  max,
  busy,
  set,
  describedBy,
}: {
  label: string
  value: number
  min: number
  max: number
  busy: boolean
  set: (value: number) => void
  describedBy?: string
}) {
  return (
    <label>
      {label}
      <Input
        required
        type="number"
        min={min}
        max={max}
        value={value}
        disabled={busy}
        aria-describedby={describedBy}
        onChange={(event) => set(Number(event.target.value))}
      />
    </label>
  )
}
