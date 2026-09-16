import { useEffect, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import {
  networkDraft,
  networkTOML,
  segmentDrafts,
  type VirtualNetworkDetail,
  type VirtualNetworkPlan,
} from '../lib/virtual-networks'
import { FormHint, FormPage, FormSection } from './form-page'
import { Icon } from './icons'
import { Note, RequestError } from './shared'
import { TOMLCode } from './toml-code'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'

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
  const [name, setName] = useState(existing?.spec.name || '')
  const [description, setDescription] = useState(existing?.spec.description || '')
  const [segments, setSegments] = useState(() => segmentDrafts(existing?.spec))
  const [mode, setMode] = useState<'form' | 'toml'>('form')
  const [toml, setTOML] = useState(existing?.toml || '')
  const [plan, setPlan] = useState<VirtualNetworkPlan | null>(null)
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
        'A network with this name already exists. Use a different name or open that network to edit it.',
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
    } catch (cause) {
      failed(cause)
    } finally {
      setBusy(false)
    }
  }
  async function review() {
    setBusy(true)
    setError('')
    try {
      setPlan(await getPlan())
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
      if (!controller.signal.aborted) {
        failed(cause)
      }
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  const names = plan
    ? [
        ...new Set([
          ...Object.keys(plan.previous?.segments || {}),
          ...Object.keys(plan.spec.segments),
        ]),
      ].sort()
    : []
  return (
    <FormPage
      title={
        plan
          ? 'Review network configuration'
          : existing
            ? `Configure ${existing.network.name}`
            : 'Create a virtual network'
      }
      description={`${project} / ${environment} · Grant application names access to private segments.`}
      icon="network"
      breadcrumbs={[
        { label: 'Virtual networks', to: '/networks' },
        { label: existing?.network.name || 'New network' },
      ]}
      help={
        <>
          <FormHint title="Small groups of services">
            Use a segment for applications that need to communicate, such as an API and its
            database. Names are scoped to this project and environment.
          </FormHint>
          <FormHint title="Grants and connections">
            A grant lets an application join. Connect each service separately after saving.
            Disconnect services before removing their grant or segment.
          </FormHint>
        </>
      }
    >
      <div className="form-body virtual-network-form">
        {plan ? (
          <>
            <div className="review-summary">
              <div>
                <span className="muted-text">NETWORK</span>
                <strong>{plan.spec.name}</strong>
              </div>
              <div>
                <span className="muted-text">REVISION</span>
                <strong className="mono">
                  {plan.expected_revision
                    ? `r${plan.expected_revision} → r${plan.expected_revision + 1}`
                    : 'New network · r1'}
                </strong>
              </div>
              <div>
                <span className="muted-text">SEGMENTS</span>
                <strong>{Object.keys(plan.spec.segments).length}</strong>
              </div>
            </div>
            {plan.previous?.description !== plan.spec.description && (
              <div className="virtual-network-description-review">
                <strong>Description</strong>
                <p>{plan.spec.description || 'No description'}</p>
                {plan.previous?.description && (
                  <small>Previously: {plan.previous.description}</small>
                )}
              </div>
            )}
            <div className="table-container network-grant-review">
              <table>
                <thead>
                  <tr>
                    <th>Segment</th>
                    <th>Currently allowed</th>
                    <th>Allowed after saving</th>
                  </tr>
                </thead>
                <tbody>
                  {names.map((segment) => (
                    <tr key={segment}>
                      <th scope="row">
                        <code>{segment}</code>
                      </th>
                      <td>
                        {plan.previous?.segments[segment]
                          ? plan.previous.segments[segment].applications.join(', ') ||
                            'No applications'
                          : 'New segment'}
                      </td>
                      <td>
                        {plan.spec.segments[segment]
                          ? plan.spec.segments[segment].applications.join(', ') || 'No applications'
                          : 'Removed'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <details className="virtual-network-toml">
              <summary>Canonical network TOML</summary>
              <TOMLCode code={plan.toml} />
            </details>
            <Note>
              Saving grants access to these application names. Services join only after their
              application configuration is deployed.
            </Note>
          </>
        ) : (
          <>
            <div className="segmented-control">
              <button
                type="button"
                className={mode === 'form' ? 'selected' : ''}
                disabled={busy}
                onClick={() => void changeMode('form')}
              >
                <Icon name="network" size={14} />
                Guided setup
              </button>
              <button
                type="button"
                className={mode === 'toml' ? 'selected' : ''}
                disabled={busy}
                onClick={() => void changeMode('toml')}
              >
                <Icon name="code" size={14} />
                TOML
              </button>
            </div>
            {mode === 'toml' ? (
              <label className="field-stack">
                Network configuration
                <Textarea
                  aria-label="Network TOML"
                  className="code-editor"
                  value={toml}
                  maxLength={32768}
                  spellCheck={false}
                  disabled={busy}
                  onChange={(event) => setTOML(event.target.value)}
                />
              </label>
            ) : (
              <>
                <FormSection title="Network" icon="network">
                  <label className="field-stack">
                    Name
                    <Input
                      value={name}
                      maxLength={40}
                      autoComplete="off"
                      spellCheck={false}
                      disabled={busy || Boolean(existing)}
                      onChange={(event) => setName(event.target.value)}
                      placeholder="shared-backend"
                    />
                  </label>
                  <label className="field-stack">
                    Description
                    <Textarea
                      value={description}
                      maxLength={500}
                      rows={2}
                      disabled={busy}
                      onChange={(event) => setDescription(event.target.value)}
                      placeholder="Services shared by our applications"
                    />
                  </label>
                </FormSection>
                <FormSection
                  title="Segments and grants"
                  description="Use exact application names, one per line or separated by commas. You can grant a name before deploying it."
                  icon="lock"
                >
                  <div className="network-segment-editor">
                    {segments.map((segment, index) => (
                      <div className="network-segment-row" key={segment.id}>
                        <label className="field-stack">
                          Segment name
                          <Input
                            aria-label={`Segment ${index + 1} name`}
                            value={segment.name}
                            maxLength={40}
                            autoComplete="off"
                            spellCheck={false}
                            disabled={busy}
                            onChange={(event) =>
                              setSegments((previous) =>
                                previous.map((item) =>
                                  item.id === segment.id
                                    ? { ...item, name: event.target.value }
                                    : item,
                                ),
                              )
                            }
                          />
                        </label>
                        <label className="field-stack">
                          Allowed applications
                          <Textarea
                            aria-label={`Segment ${index + 1} allowed applications`}
                            value={segment.applications}
                            maxLength={4096}
                            rows={3}
                            spellCheck={false}
                            disabled={busy}
                            onChange={(event) =>
                              setSegments((previous) =>
                                previous.map((item) =>
                                  item.id === segment.id
                                    ? { ...item, applications: event.target.value }
                                    : item,
                                ),
                              )
                            }
                            placeholder={'orders\ndatabase'}
                          />
                        </label>
                        <Button
                          size="icon"
                          variant="ghost"
                          aria-label={`Remove segment ${segment.name || index + 1}`}
                          disabled={busy || segments.length === 1}
                          onClick={() =>
                            setSegments((previous) =>
                              previous.filter((item) => item.id !== segment.id),
                            )
                          }
                        >
                          <Icon name="trash" size={15} />
                        </Button>
                      </div>
                    ))}
                  </div>
                  <Button
                    size="sm"
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
                </FormSection>
              </>
            )}
          </>
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
        {error && <RequestError error={error} />}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {plan ? 'Only reviewed grants will be saved' : 'Nothing changes until you save'}
        </span>
        <div className="deploy-footer-actions">
          <Button disabled={busy} onClick={() => (plan ? setPlan(null) : close())}>
            {plan ? 'Back to configuration' : 'Cancel'}
          </Button>
          <Button
            variant="primary"
            disabled={busy || baseStale || (mode === 'toml' && !toml.trim())}
            onClick={() => void (plan ? save() : review())}
          >
            {busy
              ? plan
                ? 'Saving…'
                : 'Validating…'
              : plan
                ? existing
                  ? 'Save network'
                  : 'Create network'
                : 'Review changes'}
            <Icon name="arrow" size={15} />
          </Button>
        </div>
      </div>
    </FormPage>
  )
}
