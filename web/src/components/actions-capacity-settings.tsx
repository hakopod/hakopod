import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import type { components } from '../lib/api.generated'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Dialog } from './ui/dialog'
import { SelectField } from './ui/select'
import { Icon } from './icons'
import { Copy, ErrorState, Loading, Note, RequestError } from './shared'
import {
  SettingsEmpty,
  SettingsRow,
  SettingsRows,
  SettingsSection,
  SettingsState,
} from './settings-section'

type Capacity = components['schemas']['ActionsCapacity']
type Settings = components['schemas']['ActionsCapacitySettings']
type Image = components['schemas']['ActionsCapacityImage']
type Test = components['schemas']['ActionsCapacityTest']
type ListKey = 'zones' | 'subnet_ids' | 'security_group_ids' | 'instance_types' | 'environments'
type Draft = Omit<Capacity, ListKey | 'disk_gib' | 'max_instances' | 'warm_pool'> &
  Record<ListKey | 'disk_gib' | 'max_instances' | 'warm_pool', string> & { editing: string }

const help =
  'Named AWS or Google Cloud accounts where GitHub runner pools launch one single-use VM per job instead of a runner pod. Point a pool at one with capacity = "name". Hakopod reaches AWS by assuming role_arn and Google Cloud by impersonating service_account; no keys are stored.'

const list = (value: string) => value.split(/[\s,]+/).filter(Boolean)
const blank = (cloud: Capacity['cloud']): Draft => ({
  editing: '',
  name: '',
  cloud,
  region: '',
  role_arn: '',
  external_id: '',
  project: '',
  zones: '',
  service_account: '',
  subnet_ids: '',
  security_group_ids: '',
  instance_types:
    cloud === 'aws' ? 'c7i.xlarge, c6i.xlarge, m7i.xlarge' : 'n2d-standard-4, e2-standard-4',
  architecture: 'amd64',
  spot: true,
  disk_gib: '100',
  max_instances: '10',
  environments: '*',
  image: cloud === 'aws' ? 'runs-on-full' : 'hakopod-minimal',
  warm_pool: '0',
})
const draftOf = (c: Capacity): Draft => ({
  ...blank(c.cloud),
  ...c,
  editing: c.name,
  role_arn: c.role_arn || '',
  external_id: c.external_id || '',
  project: c.project || '',
  service_account: c.service_account || '',
  image: c.image || '',
  zones: (c.zones || []).join(', '),
  subnet_ids: c.subnet_ids.join(', '),
  security_group_ids: (c.security_group_ids || []).join(', '),
  instance_types: c.instance_types.join(', '),
  environments: c.environments.join(', '),
  disk_gib: String(c.disk_gib ?? 100),
  max_instances: String(c.max_instances),
  warm_pool: String(c.warm_pool ?? 0),
})
// Only the active cloud's fields are sent; the API refuses the other cloud's.
const capacityOf = (d: Draft): Capacity => {
  const common = {
    name: d.name.trim(),
    cloud: d.cloud,
    region: d.region.trim(),
    subnet_ids: list(d.subnet_ids),
    instance_types: list(d.instance_types),
    architecture: d.architecture,
    spot: d.spot,
    disk_gib: Number(d.disk_gib),
    max_instances: Number(d.max_instances),
    environments: list(d.environments),
    warm_pool: Number(d.warm_pool),
    ...(d.image?.trim() ? { image: d.image.trim() } : {}),
  }
  return d.cloud === 'aws'
    ? {
        ...common,
        role_arn: d.role_arn?.trim(),
        security_group_ids: list(d.security_group_ids),
        ...(d.external_id?.trim() ? { external_id: d.external_id.trim() } : {}),
      }
    : {
        ...common,
        project: d.project?.trim(),
        zones: list(d.zones),
        service_account: d.service_account?.trim(),
      }
}

function imageState(image?: Image) {
  if (!image) return null
  if (image.state === 'available')
    return <SettingsState tone="ok">Image {image.image_id || 'ready'}</SettingsState>
  if (image.state === 'building') return <SettingsState tone="warn">Building image…</SettingsState>
  return <SettingsState tone="danger">Image build failed</SettingsState>
}

export default function ActionsCapacitySettings() {
  const cache = useQueryClient()
  const query = useQuery({
    queryKey: ['actions-capacity'],
    queryFn: ({ signal }) => unwrap(client.GET('/settings/actions-capacity', { signal })),
    gcTime: 0,
    // An image build takes several minutes; follow it while one is running.
    refetchInterval: (q) =>
      Object.values(q.state.data?.images || {}).some((i) => i.state === 'building') ? 15000 : false,
  })
  const [draft, setDraft] = useState<Draft | null>(null)
  const [removing, setRemoving] = useState<string | null>(null)
  const [testing, setTesting] = useState<{ name: string; launch: boolean } | null>(null)
  const [result, setResult] = useState<Test | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const refresh = () => void cache.invalidateQueries({ queryKey: ['actions-capacity'] })

  if (query.isPending || query.error || !query.data)
    return (
      <SettingsSection title="Actions VM capacity" help={help}>
        {query.isPending ? (
          <Loading />
        ) : (
          <ErrorState error={query.error} retry={() => void query.refetch()} />
        )}
      </SettingsSection>
    )
  const settings: Settings = query.data

  const save = async (capacities: Capacity[], done: () => void) => {
    setBusy(true)
    setError('')
    setConflict(false)
    try {
      cache.setQueryData(
        ['actions-capacity'],
        await unwrap(
          client.PUT('/settings/actions-capacity', {
            body: { capacities, expected_revision: settings.revision },
          }),
        ),
      )
      done()
    } catch (cause) {
      if (cause instanceof APIError && cause.status === 409) {
        setConflict(true)
        refresh()
      } else setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true)
    setError('')
    try {
      await action()
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  const field = (key: keyof Draft, label: string, hint?: string, required = true) => (
    <label key={key}>
      {label}
      <Input
        required={required}
        value={String(draft?.[key] ?? '')}
        disabled={key === 'name' && Boolean(draft?.editing)}
        placeholder={hint}
        inputMode={['disk_gib', 'max_instances', 'warm_pool'].includes(key) ? 'numeric' : undefined}
        onChange={(event) => draft && setDraft({ ...draft, [key]: event.target.value })}
        autoComplete="off"
      />
    </label>
  )

  return (
    <SettingsSection
      title="Actions VM capacity"
      help={help}
      state={
        settings.capacities.length
          ? `${settings.capacities.length} ${settings.capacities.length === 1 ? 'capacity' : 'capacities'}`
          : 'Off · pools run as pods'
      }
      actions={
        <Button
          variant="primary"
          onClick={() => {
            setDraft(blank('aws'))
            setError('')
            setConflict(false)
          }}
        >
          <Icon name="plus" size={15} />
          Add capacity
        </Button>
      }
    >
      <div className="field-stack">
        <Note>
          AWS roles trust this installation with external ID <code>{settings.external_id}</code>
          <Copy value={settings.external_id} label="Copy external ID" iconOnly /> and condition
          TerminateInstances on the hakopod-installation tag <code>{settings.installation_id}</code>
          <Copy value={settings.installation_id} label="Copy installation ID" iconOnly />.
        </Note>
        {conflict && (
          <Note>
            Someone else changed VM capacity. The list is reloaded; review and save again.
          </Note>
        )}
        {error && !draft && !removing && !testing && <RequestError error={error} />}
        {settings.capacities.length ? (
          <SettingsRows label="VM capacities">
            {settings.capacities.map((c) => (
              <SettingsRow
                key={c.name}
                mark={<Icon name="server" size={16} />}
                name={c.name}
                detail={`${c.cloud === 'aws' ? 'AWS' : 'Google Cloud'} ${c.cloud === 'gcp' ? `${c.project} · ` : ''}${c.region} · ${c.instance_types.join(', ')}`}
                status={
                  imageState(settings.images[c.name]) || (
                    <SettingsState tone={c.spot ? 'ok' : 'muted'}>
                      {c.spot ? 'Spot' : 'On-demand'}
                    </SettingsState>
                  )
                }
                meta={`Up to ${c.max_instances}${c.warm_pool ? ` · ${c.warm_pool} warm` : ''} · ${c.environments.join(', ')}`}
                actions={
                  <div className="flex shrink-0 items-center gap-2">
                    <Button
                      size="sm"
                      onClick={() => {
                        setTesting({ name: c.name, launch: false })
                        setResult(null)
                        setError('')
                      }}
                    >
                      Test
                    </Button>
                    {(c.image || (c.cloud === 'gcp' ? 'hakopod-minimal' : '')) ===
                      'hakopod-minimal' && (
                      <Button
                        size="sm"
                        disabled={busy || settings.images[c.name]?.state === 'building'}
                        title={settings.images[c.name]?.message}
                        onClick={() =>
                          void run(async () => {
                            await unwrap(
                              client.POST('/settings/actions-capacity/{name}/build-image', {
                                params: { path: { name: c.name } },
                              }),
                            )
                            refresh()
                          })
                        }
                      >
                        {settings.images[c.name] ? 'Rebuild image' : 'Build image'}
                      </Button>
                    )}
                    <Button
                      size="sm"
                      onClick={() => {
                        setDraft(draftOf(c))
                        setError('')
                        setConflict(false)
                      }}
                    >
                      Edit
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label={`Remove ${c.name}`}
                      onClick={() => {
                        setRemoving(c.name)
                        setError('')
                      }}
                    >
                      <Icon name="trash" size={15} />
                    </Button>
                  </div>
                }
              />
            ))}
          </SettingsRows>
        ) : (
          <SettingsEmpty title="No VM capacity">
            Runner pools run as pods on this installation's nodes until a capacity is added.
          </SettingsEmpty>
        )}
      </div>

      <Dialog
        open={Boolean(draft)}
        wide
        onOpenChange={(open) => !busy && !open && setDraft(null)}
        title={draft?.editing ? `Edit ${draft.editing}` : 'Add VM capacity'}
        description="Runners need outbound HTTPS to GitHub and nothing inbound. max_instances is a hard ceiling across every pool, standbys included."
      >
        {draft && (
          <form
            onSubmit={(event) => {
              event.preventDefault()
              if (busy) return
              const next = capacityOf(draft)
              const capacities = draft.editing
                ? settings.capacities.map((c) => (c.name === draft.editing ? next : c))
                : [...settings.capacities, next]
              void save(capacities, () => setDraft(null))
            }}
          >
            <div className="dialog-body field-stack">
              <div className="form-grid-two">
                {field('name', 'Name', 'aws-spot')}
                <label>
                  Cloud
                  <SelectField
                    label="Cloud"
                    value={draft.cloud}
                    disabled={Boolean(draft.editing)}
                    onValueChange={(value) =>
                      setDraft({ ...blank(value as Capacity['cloud']), name: draft.name })
                    }
                    options={[
                      { value: 'aws', label: 'AWS EC2' },
                      { value: 'gcp', label: 'Google Compute Engine' },
                    ]}
                  />
                </label>
              </div>
              {draft.cloud === 'aws' ? (
                <>
                  <div className="form-grid-two">
                    {field('region', 'Region', 'eu-central-1')}
                    {field('external_id', 'External ID (optional)', settings.external_id, false)}
                  </div>
                  {field(
                    'role_arn',
                    'IAM role ARN',
                    'arn:aws:iam::123456789012:role/hakopod-actions',
                  )}
                  <div className="form-grid-two">
                    {field('subnet_ids', 'Subnets', 'subnet-0abc1234')}
                    {field('security_group_ids', 'Security groups', 'sg-0abc1234')}
                  </div>
                </>
              ) : (
                <>
                  <div className="form-grid-two">
                    {field('project', 'Project', 'my-project')}
                    {field('region', 'Region', 'us-central1')}
                  </div>
                  {field(
                    'service_account',
                    'Service account',
                    'hakopod-actions@my-project.iam.gserviceaccount.com',
                  )}
                  <div className="form-grid-two">
                    {field('zones', 'Zones', 'us-central1-a, us-central1-b')}
                    {field('subnet_ids', 'Subnetwork', 'default')}
                  </div>
                </>
              )}
              {field(
                'instance_types',
                draft.cloud === 'aws' ? 'Instance types, in order' : 'Machine types, in order',
              )}
              <div className="form-grid-two">
                <label>
                  Architecture
                  <SelectField
                    label="Architecture"
                    value={draft.architecture}
                    onValueChange={(value) =>
                      setDraft({ ...draft, architecture: value as Capacity['architecture'] })
                    }
                    options={[
                      { value: 'amd64', label: 'amd64' },
                      {
                        value: 'arm64',
                        label:
                          draft.cloud === 'aws' ? 'arm64 (Graviton)' : 'arm64 (Tau T2A, Axion)',
                      },
                    ]}
                  />
                </label>
                {field(
                  'image',
                  'Image',
                  draft.cloud === 'aws'
                    ? 'runs-on-full, hakopod-minimal, ami-…'
                    : 'hakopod-minimal or projects/…/global/images/…',
                  false,
                )}
              </div>
              <div className="form-grid-two">
                {field('max_instances', 'Max instances')}
                {field('warm_pool', 'Warm standbys')}
              </div>
              <div className="form-grid-two">
                {field('disk_gib', 'Disk (GiB)')}
                {field('environments', 'Environments', 'shop/production or *')}
              </div>
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={draft.spot}
                  onChange={(e) => setDraft({ ...draft, spot: e.target.checked })}
                />
                Launch on spot capacity (on-demand when off)
              </label>
              {conflict && <Note>Someone else changed VM capacity. Review and save again.</Note>}
              {error && <RequestError error={error} />}
            </div>
            <div className="dialog-footer">
              <Button type="button" disabled={busy} onClick={() => setDraft(null)}>
                Cancel
              </Button>
              <Button type="submit" variant="primary" disabled={busy}>
                {busy ? 'Saving…' : 'Save capacity'}
              </Button>
            </div>
          </form>
        )}
      </Dialog>

      <Dialog
        open={Boolean(removing)}
        onOpenChange={(open) => !busy && !open && setRemoving(null)}
        title={`Remove ${removing || 'capacity'}?`}
        description="A capacity that a pool still uses cannot be removed; move the pool off it first."
      >
        <div className="dialog-body">{error && <RequestError error={error} />}</div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={() => setRemoving(null)}>
            Keep
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={() =>
              removing &&
              void save(
                settings.capacities.filter((c) => c.name !== removing),
                () => setRemoving(null),
              )
            }
          >
            <Icon name="trash" size={15} />
            {busy ? 'Removing…' : 'Remove'}
          </Button>
        </div>
      </Dialog>

      <Dialog
        open={Boolean(testing)}
        onOpenChange={(open) => !busy && !open && setTesting(null)}
        title={`Test ${testing?.name || 'capacity'}`}
        description="Checks that Hakopod can reach the account and reads the network settings. A launch test also starts and terminates the smallest on-demand instance."
      >
        {testing && (
          <>
            <div className="dialog-body field-stack">
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={testing.launch}
                  disabled={busy}
                  onChange={(e) => setTesting({ ...testing, launch: e.target.checked })}
                />
                Also launch and terminate one instance
              </label>
              {result && (
                <div className="grid gap-1 text-sm">
                  <SettingsState tone={result.warnings.length ? 'warn' : 'ok'}>
                    {result.warnings.length ? 'Reachable, with warnings' : 'Ready'}
                  </SettingsState>
                  {result.instance_id && (
                    <span>
                      Launched and terminated {result.instance_type} {result.instance_id}
                    </span>
                  )}
                  {result.warnings.map((w) => (
                    <span key={w} className="muted-text">
                      {w}
                    </span>
                  ))}
                </div>
              )}
              {error && <RequestError error={error} />}
            </div>
            <div className="dialog-footer">
              <Button type="button" disabled={busy} onClick={() => setTesting(null)}>
                Close
              </Button>
              <Button
                variant="primary"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    setResult(null)
                    setResult(
                      await unwrap(
                        client.POST('/settings/actions-capacity/{name}/test', {
                          params: { path: { name: testing.name } },
                          body: { launch: testing.launch },
                        }),
                      ),
                    )
                  })
                }
              >
                {busy ? 'Testing…' : 'Run test'}
              </Button>
            </div>
          </>
        )}
      </Dialog>
    </SettingsSection>
  )
}
