import { useEffect, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import {
  networkDraft,
  networkTOML,
  segmentDrafts,
  type SegmentDraft,
  type VirtualNetworkDetail,
  type VirtualNetworkPlan,
} from '../lib/virtual-networks'
import { FlowSteps, PendingButton, plural } from './create-flow'
import { FormError, FormPage } from './form-page'
import { Icon } from './icons'
import { HeadingHelp, Note, RequestError } from './shared'
import { Consequence, Consequences, ReviewFacts, ScopeLabel } from './task-review'
import { TOMLCode } from './toml-code'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'

// Mirrors the name rule in lib/virtual-networks so the first step can be checked on its own.
const validName = /^[a-z](?:[a-z0-9-]{0,38}[a-z0-9])?$/

const grantList = (value: string) =>
  value
    .split(/[,\n]/)
    .map((item) => item.trim())
    .filter(Boolean)

type Step = 'details' | 'segments' | 'review'

export function VirtualNetworkForm({
  project,
  environment,
  existing,
}: {
  project: string
  environment: string
  existing?: VirtualNetworkDetail
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [step, setStep] = useState<Step>('details')
  const [name, setName] = useState(existing?.spec.name || '')
  const [description, setDescription] = useState(existing?.spec.description || '')
  const [segments, setSegments] = useState(() => segmentDrafts(existing?.spec))
  const [mode, setMode] = useState<'form' | 'toml'>('form')
  const [toml, setTOML] = useState(existing?.toml || '')
  const [plan, setPlan] = useState<VirtualNetworkPlan | null>(null)
  const [nameIssue, setNameIssue] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [base, setBase] = useState(() => ({
    id: existing?.network.id || '',
    revision: existing?.network.revision || 0,
  }))
  const [baseStale, setBaseStale] = useState(false)
  const [baseNotice, setBaseNotice] = useState('')
  const request = useRef<AbortController | null>(null)
  useEffect(() => () => request.current?.abort(), [])
  // Applications already deployed here, offered as grants. Names not yet deployed can be typed.
  const applications = useQuery({
    queryKey: ['applications', project, environment, 'network-grants'],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications', { signal, params: { query: { project, environment } } })),
    enabled: mode === 'form',
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  const known = [...new Set((applications.data?.items || []).map((item) => item.name))].sort()
  const close = () =>
    existing
      ? void navigate({
          to: '/networks/$networkName',
          params: { networkName: existing.network.name },
          search: {},
        })
      : void navigate({ to: '/networks' })
  async function getPlan() {
    const controller = new AbortController()
    request.current = controller
    const result = await unwrap(
      client.POST('/virtual-networks/plan', {
        signal: controller.signal,
        body: {
          project,
          environment,
          expected_id: base.id,
          expected_revision: base.revision,
          ...(mode === 'toml' ? { toml } : { spec: networkDraft(name, description, segments) }),
        },
      }),
    )
    if (existing && result.spec.name !== existing.network.name)
      throw new Error('Keep the original network name when editing its configuration.')
    if (!existing && result.expected_revision !== 0)
      throw new Error(
        `A network named ${result.spec.name} already exists in ${project} / ${environment}. Choose another name, or open that network to change it.`,
      )
    if (existing && result.expected_revision === 0)
      throw new Error(
        'This network no longer exists. Your draft is kept; return to the network list to inspect it.',
      )
    return result
  }
  function failed(cause: unknown) {
    setError(message(cause))
    if (cause instanceof APIError && cause.status === 409) {
      setPlan(null)
      setStep(mode === 'toml' ? 'details' : 'segments')
      if (existing) setBaseStale(true)
    }
  }
  async function loadCurrent() {
    if (!existing) return
    setBusy(true)
    const controller = new AbortController()
    request.current = controller
    try {
      const current = await unwrap(
        client.GET('/virtual-networks/{name}', {
          signal: controller.signal,
          params: { path: { name: existing.network.name }, query: { project, environment } },
        }),
      )
      setBaseNotice(
        current.network.id === base.id
          ? `Loaded revision r${current.network.revision}. Your draft is kept for review.`
          : 'This name now refers to a recreated network. Your draft is kept; review its current grants before saving.',
      )
      setBase({ id: current.network.id, revision: current.network.revision })
      setBaseStale(false)
      setError('')
    } catch (cause) {
      if (!controller.signal.aborted) failed(cause)
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  async function changeMode(next: 'form' | 'toml') {
    if (busy || mode === next) return
    setError('')
    setBusy(true)
    try {
      if (next === 'toml') {
        const value = networkDraft(name || 'untitled', description, segments)
        value.name = name
        setTOML(networkTOML(value))
      } else {
        const parsed = await getPlan()
        setName(parsed.spec.name)
        setDescription(parsed.spec.description)
        setSegments(segmentDrafts(parsed.spec))
      }
      setMode(next)
      setStep('details')
    } catch (cause) {
      failed(cause)
    } finally {
      setBusy(false)
    }
  }
  function continueFromDetails() {
    if (mode === 'toml') return void review()
    const issue = existing
      ? ''
      : name === 'new'
        ? 'The name "new" is reserved. Choose another network name.'
        : !validName.test(name)
          ? 'Use 1–40 lowercase letters, numbers or hyphens. Start with a letter and end with a letter or number.'
          : ''
    setNameIssue(issue)
    if (!issue) setStep('segments')
  }
  async function review() {
    setBusy(true)
    setError('')
    try {
      setPlan(await getPlan())
      setStep('review')
    } catch (cause) {
      if (!request.current?.signal.aborted) failed(cause)
    } finally {
      if (!request.current?.signal.aborted) setBusy(false)
    }
  }
  async function save() {
    if (!plan) return
    setBusy(true)
    setError('')
    const controller = new AbortController()
    request.current = controller
    const body = {
      project,
      environment,
      spec: plan.spec,
      expected_revision: plan.expected_revision,
      expected_id: plan.expected_id,
    }
    try {
      const result = existing
        ? await unwrap(
            client.PUT('/virtual-networks/{name}', {
              signal: controller.signal,
              params: { path: { name: existing.network.name } },
              body,
            }),
          )
        : await unwrap(client.POST('/virtual-networks', { signal: controller.signal, body }))
      void cache.invalidateQueries({ queryKey: ['virtual-networks', project, environment] })
      void cache.invalidateQueries({
        queryKey: ['virtual-network', project, environment, result.name],
      })
      void cache.invalidateQueries({
        queryKey: ['virtual-network-candidates', project, environment, result.name],
      })
      void navigate({
        to: '/networks/$networkName',
        params: { networkName: result.name },
        search: {},
      })
    } catch (cause) {
      if (!controller.signal.aborted) failed(cause)
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  const updateSegment = (id: string, change: Partial<SegmentDraft>) =>
    setSegments((previous) =>
      previous.map((item) => (item.id === id ? { ...item, ...change } : item)),
    )
  const stepKeys: Step[] = mode === 'toml' ? ['details', 'review'] : ['details', 'segments', 'review']
  const steps =
    mode === 'toml'
      ? ['Configuration', 'Review']
      : [existing ? 'Description' : 'Name', 'Segments and grants', 'Review']
  const target = existing?.network.name || plan?.spec.name || name
  const primary =
    step === 'review'
      ? existing
        ? `Save ${target} r${(plan?.expected_revision || 0) + 1}`
        : `Create network ${target}`
      : step === 'details' && mode === 'form'
        ? 'Continue to segments'
        : 'Review network'
  return (
    <FormPage
      title={
        step === 'review'
          ? existing
            ? `Review changes to ${target}`
            : `Review ${target}`
          : existing
            ? `Configure ${existing.network.name}`
            : 'New network'
      }
      description="Virtual networks let services in different applications reach each other privately within one project and environment."
      icon="network"
      breadcrumbs={[
        { label: 'Virtual networks', to: '/networks' },
        { label: existing?.network.name || 'New network' },
      ]}
    >
      <div className="network-flow-context">
        <ScopeLabel project={project} environment={environment} />
        <FlowSteps
          steps={steps}
          current={stepKeys.indexOf(step)}
          disabled={busy}
          onSelect={(index) => {
            setPlan(null)
            setError('')
            setStep(stepKeys[index])
          }}
        />
      </div>
      <div className="form-body create-flow virtual-network-form">
        {step === 'review' && plan ? (
          <NetworkReview plan={plan} existing={Boolean(existing)} environment={environment} />
        ) : mode === 'toml' ? (
          <section className="create-section" aria-labelledby="network-toml-heading">
            <div className="create-section-heading">
              <h2 id="network-toml-heading">Network TOML</h2>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() => void changeMode('form')}
              >
                Use the guided form
              </Button>
            </div>
            <label>
              <span className="sr-only">Network TOML</span>
              <Textarea
                className="code-editor"
                value={toml}
                maxLength={32768}
                spellCheck={false}
                disabled={busy}
                onChange={(event) => setTOML(event.target.value)}
                aria-describedby="network-toml-help"
              />
              <span id="network-toml-help" className="field-help">
                One network with <code>schema_version</code>, <code>name</code> and{' '}
                <code>[segments.NAME]</code> tables. The review shows exactly what it grants.
              </span>
            </label>
          </section>
        ) : step === 'details' ? (
          <section className="create-section" aria-labelledby="network-name-heading">
            <div className="create-section-heading">
              <h2 id="network-name-heading">{existing ? 'Description' : 'Name the network'}</h2>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() => void changeMode('toml')}
              >
                <Icon name="code" size={14} />
                Paste TOML instead
              </Button>
            </div>
            <div className="create-fields network-name-fields">
              <label>
                Network name
                <Input
                  value={name}
                  maxLength={40}
                  autoComplete="off"
                  spellCheck={false}
                  autoFocus={!existing}
                  disabled={busy || Boolean(existing)}
                  error={nameIssue}
                  onChange={(event) => {
                    setName(event.target.value)
                    setNameIssue('')
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') continueFromDetails()
                  }}
                  aria-describedby="network-name-help"
                />
                <span id="network-name-help" className="field-help">
                  {existing
                    ? 'Application TOML refers to the network by this name, so it cannot change.'
                    : 'Lowercase letters, numbers and hyphens. Application TOML refers to the network by this name, and it cannot be changed later.'}
                </span>
              </label>
              <label>
                Description <span className="muted-text">(optional)</span>
                <Input
                  value={description}
                  maxLength={500}
                  disabled={busy}
                  onChange={(event) => setDescription(event.target.value)}
                  aria-describedby="network-description-help"
                />
                <span id="network-description-help" className="field-help">
                  Shown in the network list, for example “Storefront and its data services”.
                </span>
              </label>
            </div>
          </section>
        ) : (
          <section className="create-section" aria-labelledby="network-segments-heading">
            <div className="create-section-heading">
              <div className="hako-section-heading-title">
                <h2 id="network-segments-heading">Segments and grants</h2>
                <HeadingHelp title="Segments and grants">
                  A segment is a group of applications allowed to connect services to each other.
                  Granting an application lets it join; its services connect one at a time
                  afterwards.
                </HeadingHelp>
              </div>
              <span>{plural(segments.length, 'segment')} · up to 16</span>
            </div>
            <ol className="network-segment-editor">
              {segments.map((segment, index) => (
                <SegmentEditor
                  key={segment.id}
                  index={index}
                  segment={segment}
                  known={known}
                  knownPending={applications.isPending}
                  knownMore={Boolean(applications.data?.next_cursor)}
                  disabled={busy}
                  removable={segments.length > 1}
                  onChange={(change) => updateSegment(segment.id, change)}
                  onRemove={() =>
                    setSegments((previous) => previous.filter((item) => item.id !== segment.id))
                  }
                />
              ))}
            </ol>
            <Button
              size="sm"
              className="justify-self-start"
              disabled={busy || segments.length >= 16}
              onClick={() =>
                setSegments((previous) => [
                  ...previous,
                  { id: crypto.randomUUID(), name: '', applications: '' },
                ])
              }
            >
              <Icon name="plus" size={14} />
              Add segment
            </Button>
          </section>
        )}
        {baseStale && (
          <div className="network-base-conflict">
            <Note>
              The network changed after it was loaded. Load its current identity and revision, then
              review your retained draft again.
            </Note>
            <Button disabled={busy} onClick={() => void loadCurrent()}>
              Load current network
            </Button>
          </div>
        )}
        {baseNotice && !baseStale && <Note>{baseNotice}</Note>}
        {error && (step === 'review' ? <RequestError error={error} /> : <FormError className="create-issues">{error}</FormError>)}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {step === 'review'
            ? 'Only the reviewed grants are saved'
            : 'Nothing is saved until the review'}
        </span>
        <div className="deploy-footer-actions">
          <Button
            disabled={busy}
            onClick={() => {
              setError('')
              if (step === 'review') {
                setPlan(null)
                setStep(mode === 'toml' ? 'details' : 'segments')
              } else if (step === 'segments') setStep('details')
              else close()
            }}
          >
            {step === 'details' ? 'Cancel' : 'Back'}
          </Button>
          <PendingButton
            variant="primary"
            pending={busy}
            pendingLabel={
              step === 'review' ? (existing ? 'Saving…' : 'Creating…') : 'Checking…'
            }
            disabled={baseStale || (mode === 'toml' && step !== 'review' && !toml.trim())}
            onClick={() =>
              step === 'review'
                ? void save()
                : step === 'details'
                  ? continueFromDetails()
                  : void review()
            }
          >
            <span className="network-primary-label">{primary}</span>
            <Icon name="arrow" size={15} />
          </PendingButton>
        </div>
      </div>
    </FormPage>
  )
}

function SegmentEditor({
  index,
  segment,
  known,
  knownPending,
  knownMore,
  disabled,
  removable,
  onChange,
  onRemove,
}: {
  index: number
  segment: SegmentDraft
  known: string[]
  knownPending: boolean
  knownMore: boolean
  disabled: boolean
  removable: boolean
  onChange: (change: Partial<SegmentDraft>) => void
  onRemove: () => void
}) {
  const granted = grantList(segment.applications)
  const others = granted.filter((item) => !known.includes(item))
  // Keep the typed text (including a trailing new line) while it still lists the same names.
  const [text, setText] = useState(others.join('\n'))
  const shown = grantList(text).join('\n') === others.join('\n') ? text : others.join('\n')
  const label = segment.name || `segment ${index + 1}`
  const toggle = (application: string, on: boolean) =>
    onChange({
      applications: (on
        ? [...granted, application]
        : granted.filter((item) => item !== application)
      ).join('\n'),
    })
  return (
    <li className="network-segment-row">
      <header>
        <label>
          Segment name
          <Input
            value={segment.name}
            maxLength={40}
            autoComplete="off"
            spellCheck={false}
            disabled={disabled}
            placeholder="data"
            onChange={(event) => onChange({ name: event.target.value })}
          />
        </label>
        <Button
          size="icon"
          variant="ghost"
          aria-label={`Remove ${label}`}
          disabled={disabled || !removable}
          onClick={onRemove}
        >
          <Icon name="trash" size={15} />
        </Button>
      </header>
      <fieldset className="network-grant-picker" disabled={disabled}>
        <legend>
          Applications allowed to join <span>{plural(granted.length, 'application')}</span>
        </legend>
        {knownPending ? (
          <p className="field-help">Loading applications in this environment…</p>
        ) : known.length ? (
          <div className="network-grant-options">
            {known.map((application) => (
              <label key={application} className="checkbox-row">
                <input
                  type="checkbox"
                  checked={granted.includes(application)}
                  onChange={(event) => toggle(application, event.target.checked)}
                />
                <span>{application}</span>
              </label>
            ))}
          </div>
        ) : (
          <p className="field-help">No applications are deployed in this environment yet.</p>
        )}
        <label>
          {known.length ? 'Other application names' : 'Application names'}
          <Textarea
            value={shown}
            maxLength={4096}
            rows={2}
            spellCheck={false}
            onChange={(event) => {
              setText(event.target.value)
              onChange({
                applications: [
                  ...granted.filter((item) => known.includes(item)),
                  ...grantList(event.target.value),
                ].join('\n'),
              })
            }}
          />
          <span className="field-help">
            Exact names, one per line. You can grant an application before it is deployed.
            {knownMore ? ' Only the first page of applications is listed above.' : ''}
          </span>
        </label>
      </fieldset>
    </li>
  )
}

function NetworkReview({
  plan,
  existing,
  environment,
}: {
  plan: VirtualNetworkPlan
  existing: boolean
  environment: string
}) {
  const names = [
    ...new Set([...Object.keys(plan.previous?.segments || {}), ...Object.keys(plan.spec.segments)]),
  ].sort()
  const removedGrants = names.flatMap((segment) => {
    const after = plan.spec.segments[segment]?.applications || []
    return (plan.previous?.segments[segment]?.applications || [])
      .filter((application) => !after.includes(application))
      .map((application) => `${application} from ${segment}`)
  })
  const removedSegments = names.filter((segment) => !plan.spec.segments[segment])
  return (
    <>
      <ReviewFacts
        label="Network"
        items={[
          ['Network', <strong className="mono">{plan.spec.name}</strong>],
          [
            'Revision',
            <span className="mono">
              {plan.expected_revision ? `r${plan.expected_revision} → ` : 'New · '}r
              {plan.expected_revision + 1}
            </span>,
          ],
          ['Segments', String(Object.keys(plan.spec.segments).length)],
          [
            'Description',
            plan.spec.description || <span className="muted-text">No description</span>,
          ],
        ]}
      />
      <section className="create-section" aria-labelledby="network-review-grants">
        <div className="create-section-heading">
          <h2 id="network-review-grants">Who can join each segment</h2>
        </div>
        <ul className="network-review-segments">
          {names.map((segment) => {
            const before = plan.previous?.segments[segment]?.applications
            const after = plan.spec.segments[segment]?.applications
            return (
              <li key={segment} data-removed={!after || undefined}>
                <header>
                  <code>{segment}</code>
                  <span>
                    {!after ? 'Removed' : !before && existing ? 'New segment' : plural(after.length, 'application')}
                  </span>
                </header>
                {after?.length ? (
                  <ul className="network-chip-list" aria-label={`Applications allowed in ${segment}`}>
                    {after.map((application) => (
                      <li
                        key={application}
                        className="label-chip"
                        data-change={before && !before.includes(application) ? 'added' : undefined}
                      >
                        {application}
                        {before && !before.includes(application) && (
                          <span className="sr-only"> (added)</span>
                        )}
                      </li>
                    ))}
                  </ul>
                ) : (
                  after && <p className="muted-text">No application can join yet.</p>
                )}
                {before?.some((application) => !after?.includes(application)) && (
                  <p className="network-review-removed">
                    No longer allowed:{' '}
                    {before.filter((application) => !after?.includes(application)).join(', ')}
                  </p>
                )}
              </li>
            )
          })}
        </ul>
      </section>
      <Consequences>
        {existing ? (
          <Consequence>
            Granted applications can connect services after you save. Existing connections stay as
            they are.
          </Consequence>
        ) : (
          <Consequence>
            Creates the network in {environment} with no services connected. Each granted
            application connects its services afterwards; every connection deploys a new
            application revision.
          </Consequence>
        )}
        <Consequence>
          Services connected to the same segment can reach each other at private addresses, subject
          to each service’s peer restrictions. Nothing here opens public access.
        </Consequence>
        {(removedGrants.length > 0 || removedSegments.length > 0) && (
          <Consequence tone="warning">
            Removes {[...removedSegments.map((item) => `segment ${item}`), ...removedGrants].join(', ')}.
            Those applications can no longer connect services there.
          </Consequence>
        )}
      </Consequences>
      <details className="virtual-network-toml">
        <summary>Network TOML that will be saved</summary>
        <TOMLCode code={plan.toml} />
      </details>
    </>
  )
}
