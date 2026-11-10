import { useId, useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import type { components } from '../lib/api.generated'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Dialog } from './ui/dialog'
import { SelectField } from './ui/select'
import { Icon } from './icons'
import { InspectorSheet } from './inspector-sheet'
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

      {draft && (
        <CapacitySheet
          draft={draft}
          settings={settings}
          busy={busy}
          error={error}
          conflict={conflict}
          onChange={setDraft}
          onClose={() => !busy && setDraft(null)}
          onSave={() => {
            const next = capacityOf(draft)
            const capacities = draft.editing
              ? settings.capacities.map((c) => (c.name === draft.editing ? next : c))
              : [...settings.capacities, next]
            void save(capacities, () => setDraft(null))
          }}
        />
      )}

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

// The role Hakopod's own EC2 instance runs as. It may only assume the actions role.
const controlPlanePolicy = (account: string) =>
  JSON.stringify(
    {
      Version: '2012-10-17',
      Statement: [
        {
          Effect: 'Allow',
          Action: 'sts:AssumeRole',
          Resource: `arn:aws:iam::${account}:role/hakopod-actions`,
        },
      ],
    },
    null,
    2,
  )

const imdsRole = `TOKEN=$(curl -s -X PUT http://169.254.169.254/latest/api/token -H 'X-aws-ec2-metadata-token-ttl-seconds: 60')
curl -s -H "X-aws-ec2-metadata-token: $TOKEN" http://169.254.169.254/latest/meta-data/iam/security-credentials/`

const trustPolicy = (externalID: string, account: string) =>
  JSON.stringify(
    {
      Version: '2012-10-17',
      Statement: [
        {
          Effect: 'Allow',
          Principal: { AWS: `arn:aws:iam::${account}:role/hakopod-control-plane` },
          Action: 'sts:AssumeRole',
          Condition: { StringEquals: { 'sts:ExternalId': externalID } },
        },
      ],
    },
    null,
    2,
  )

// Mirrors the policy in docs/managed-actions.md, with this installation's tag value filled in.
const permissionsPolicy = (installation: string) => {
  const own = { StringEquals: { 'ec2:ResourceTag/hakopod-installation': installation } }
  return JSON.stringify(
    {
      Version: '2012-10-17',
      Statement: [
        { Sid: 'Read', Effect: 'Allow', Action: 'ec2:Describe*', Resource: '*' },
        {
          Sid: 'Launch',
          Effect: 'Allow',
          Action: [
            'ec2:CreateLaunchTemplate',
            'ec2:DeleteLaunchTemplate',
            'ec2:CreateFleet',
            'ec2:RunInstances',
          ],
          Resource: '*',
        },
        {
          Sid: 'TagOnCreate',
          Effect: 'Allow',
          Action: 'ec2:CreateTags',
          Resource: '*',
          Condition: {
            StringEquals: {
              'ec2:CreateAction': [
                'CreateLaunchTemplate',
                'CreateFleet',
                'RunInstances',
                'CreateImage',
              ],
            },
          },
        },
        {
          Sid: 'OwnInstances',
          Effect: 'Allow',
          Action: [
            'ec2:TerminateInstances',
            'ec2:StartInstances',
            'ec2:ModifyInstanceAttribute',
            'ec2:GetConsoleOutput',
            'ec2:DeregisterImage',
            'ec2:DeleteSnapshot',
          ],
          Resource: '*',
          Condition: own,
        },
        {
          Sid: 'ClaimTags',
          Effect: 'Allow',
          Action: 'ec2:CreateTags',
          Resource: 'arn:aws:ec2:*:*:instance/*',
          Condition: {
            ...own,
            'ForAllValues:StringEquals': {
              'aws:TagKeys': ['hakopod-pool', 'hakopod-slot', 'hakopod-pool-state'],
            },
          },
        },
        {
          Sid: 'ImageFromOwnInstance',
          Effect: 'Allow',
          Action: 'ec2:CreateImage',
          Resource: 'arn:aws:ec2:*:*:instance/*',
          Condition: own,
        },
        {
          Sid: 'TaggedImage',
          Effect: 'Allow',
          Action: 'ec2:CreateImage',
          Resource: ['arn:aws:ec2:*::image/*', 'arn:aws:ec2:*::snapshot/*'],
          Condition: { StringEquals: { 'aws:RequestTag/hakopod-installation': installation } },
        },
        {
          Sid: 'UbuntuImage',
          Effect: 'Allow',
          Action: 'ssm:GetParameters',
          Resource: 'arn:aws:ssm:*::parameter/aws/service/canonical/*',
        },
        {
          Sid: 'FleetServiceRoles',
          Effect: 'Allow',
          Action: 'iam:CreateServiceLinkedRole',
          Resource: '*',
          Condition: {
            StringEquals: {
              'iam:AWSServiceName': ['ec2fleet.amazonaws.com', 'spot.amazonaws.com'],
            },
          },
        },
      ],
    },
    null,
    2,
  )
}

const gcloudSetup = (project: string) => `PROJECT=${project || 'my-project'}
HAKOPOD_IDENTITY=hakopod@my-project.iam.gserviceaccount.com   # the identity Hakopod runs as
gcloud services enable compute.googleapis.com iamcredentials.googleapis.com --project "$PROJECT"
gcloud iam service-accounts create hakopod-actions --project "$PROJECT"
gcloud iam roles create hakopodActions --project "$PROJECT" --title "Hakopod Actions runners" --permissions \\
compute.instances.create,compute.instances.get,compute.instances.list,compute.instances.delete,\\
compute.instances.start,compute.instances.setLabels,compute.instances.setMetadata,\\
compute.instances.getSerialPortOutput,compute.disks.create,compute.disks.setLabels,compute.disks.useReadOnly,\\
compute.subnetworks.get,compute.subnetworks.use,compute.subnetworks.useExternalIp,\\
compute.zoneOperations.get,compute.zoneOperations.list,compute.globalOperations.get,\\
compute.images.create,compute.images.get,compute.images.list,compute.images.delete,\\
compute.images.useReadOnly,compute.images.getFromFamily,compute.firewalls.list
gcloud projects add-iam-policy-binding "$PROJECT" \\
  --member "serviceAccount:hakopod-actions@$PROJECT.iam.gserviceaccount.com" \\
  --role "projects/$PROJECT/roles/hakopodActions"
gcloud iam service-accounts add-iam-policy-binding "hakopod-actions@$PROJECT.iam.gserviceaccount.com" \\
  --project "$PROJECT" --member "serviceAccount:$HAKOPOD_IDENTITY" \\
  --role roles/iam.serviceAccountTokenCreator`

function Snippet({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid min-w-0 gap-1">
      <div className="flex items-center justify-between gap-2 text-xs muted-text">
        <span>{label}</span>
        <Copy value={value} label={`Copy ${label.toLowerCase()}`} />
      </div>
      <pre
        className="max-h-56 min-w-0 overflow-auto rounded-[var(--radius-control)] border border-[var(--hairline)] p-3 text-xs"
        aria-label={label}
      >
        <code>{value}</code>
      </pre>
    </div>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="grid min-w-0 gap-2">
      <strong className="text-sm">
        {n}. {title}
      </strong>
      <div className="grid min-w-0 gap-2 text-sm">{children}</div>
    </li>
  )
}

function SetupGuide({ draft, settings }: { draft: Draft; settings: Settings }) {
  const externalID = draft.external_id || settings.external_id
  // Fill the account in once IAM role ARN names it.
  const account = /^arn:aws:iam::(\d{12}):/.exec(draft.role_arn || '')?.[1] || 'ACCOUNT_ID'
  return (
    <details className="capacity-guide grid gap-3" open={!draft.editing}>
      <summary className="cursor-pointer text-sm font-medium">
        Set up {draft.cloud === 'aws' ? 'your AWS account' : 'your Google Cloud project'} first
      </summary>
      {draft.cloud === 'aws' ? (
        <ol className="mt-3 grid gap-4">
          <Step n={1} title="Give Hakopod its own AWS identity">
            <p className="muted-text">
              Hakopod calls AWS as the identity it runs as. On EC2 that is the instance role; no
              access keys are stored. Run this on the Hakopod host: a role name means one is
              attached, a 404 means none is.
            </p>
            <Snippet label="Check the instance role" value={imdsRole} />
            <p className="muted-text">
              Without one, in the AWS console open <b>IAM → Roles → Create role</b>, choose{' '}
              <b>AWS service</b> and <b>EC2</b>, and name it <code>hakopod-control-plane</code>.
              Open the role, choose <b>Add permissions → Create inline policy → JSON</b> and paste
              this. Then attach it under{' '}
              <b>EC2 → Instances → the Hakopod host → Actions → Security → Modify IAM role</b>. If
              Hakopod runs in a container on that host, set the instance metadata hop limit to 2.
              ACCOUNT_ID is your 12-digit AWS account; the snippets fill it in once IAM role ARN
              below is set.
            </p>
            <Snippet label="Control plane inline policy" value={controlPlanePolicy(account)} />
            <p className="muted-text">
              On EKS, use the pod role instead; elsewhere, Hakopod's <code>AWS_ACCESS_KEY_ID</code>{' '}
              user. Put that ARN in the trust policy in step 3.
            </p>
          </Step>
          <Step n={2} title="Create the permissions policy">
            <p className="muted-text">
              Open <b>IAM → Policies → Create policy → JSON</b>, paste this and name it{' '}
              <code>hakopod-actions</code>. Hakopod can launch tagged instances, and start, change,
              image or terminate only instances tagged{' '}
              <code>hakopod-installation={settings.installation_id}</code>. Runners get no instance
              profile, so no <code>iam:PassRole</code>.
            </p>
            <Snippet
              label="Permissions policy"
              value={permissionsPolicy(settings.installation_id)}
            />
          </Step>
          <Step n={3} title="Create the role Hakopod assumes">
            <p className="muted-text">
              Open <b>IAM → Roles → Create role</b>, choose <b>Custom trust policy</b> and paste
              this. It has a Principal, so it does not go in the policy editor of step 2. Attach the{' '}
              <code>hakopod-actions</code> policy, name the role <code>hakopod-actions</code> and
              put its ARN in IAM role ARN below. Create the step 1 role first: AWS rejects a trust
              policy whose principal does not exist.
            </p>
            <Snippet label="Trust policy" value={trustPolicy(externalID, account)} />
          </Step>
          <Step n={4} title="Pick the network">
            <p className="muted-text">
              Use subnets with a route to the internet: private subnets behind a NAT gateway, or
              public subnets that assign public IPs. Create a security group with no inbound rules
              and outbound allowed; runners only call GitHub over HTTPS.
            </p>
          </Step>
          <Step n={5} title="Fill in the form below, save, then Test">
            <p className="muted-text">
              Test assumes the role and reads the subnets and security groups. Test with launch also
              starts and terminates one t3.micro or t4g.micro.
            </p>
          </Step>
        </ol>
      ) : (
        <ol className="mt-3 grid gap-4">
          <Step n={1} title="Create a service account and a custom role">
            <p className="muted-text">
              Hakopod impersonates this service account from its own Google identity, so no key is
              stored. Set <code>HAKOPOD_IDENTITY</code> to the identity Hakopod runs as: the GCE
              VM's or GKE workload's service account, or the account of the key file in{' '}
              <code>GOOGLE_APPLICATION_CREDENTIALS</code>.
            </p>
            <Snippet label="gcloud commands" value={gcloudSetup(draft.project || '')} />
          </Step>
          <Step n={2} title="Pick the network">
            <p className="muted-text">
              One subnetwork in the region. Runners get an ephemeral external IP and need nothing
              inbound; Test warns about firewall rules that open every VM to the internet, such as
              the default network's <code>default-allow-ssh</code>.
            </p>
          </Step>
          <Step n={3} title="Fill in the form below, save, then Test">
            <p className="muted-text">
              Test impersonates the service account, reads the subnetwork and lists firewall rules.
              Test with launch also starts and deletes one e2-micro or t2a-standard-1. The
              hakopod-minimal image builds itself in your project on first use, in about ten
              minutes.
            </p>
          </Step>
        </ol>
      )}
    </details>
  )
}

function CapacitySheet({
  draft,
  settings,
  busy,
  error,
  conflict,
  onChange,
  onClose,
  onSave,
}: {
  draft: Draft
  settings: Settings
  busy: boolean
  error: string
  conflict: boolean
  onChange: (draft: Draft) => void
  onClose: () => void
  onSave: () => void
}) {
  const formId = useId()
  const aws = draft.cloud === 'aws'
  const field = (
    key: keyof Draft,
    label: string,
    example: string,
    hint: ReactNode,
    required = true,
  ) => (
    <label key={key} className="grid min-w-0 gap-1">
      {label}
      <Input
        required={required}
        value={String(draft[key] ?? '')}
        disabled={key === 'name' && Boolean(draft.editing)}
        placeholder={example && `e.g. ${example}`}
        inputMode={['disk_gib', 'max_instances', 'warm_pool'].includes(key) ? 'numeric' : undefined}
        onChange={(event) => onChange({ ...draft, [key]: event.target.value })}
        autoComplete="off"
      />
      <small className="text-xs muted-text">{hint}</small>
    </label>
  )
  return (
    <InspectorSheet
      open
      wide
      kind="capacity"
      onOpenChange={(open) => !open && onClose()}
      title={draft.editing ? `Edit ${draft.editing}` : 'Add VM capacity'}
      description="Runner pools that use this capacity launch one single-use VM per job in your cloud account."
      footer={
        <>
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save capacity'}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className="grid min-w-0 gap-6"
        onSubmit={(event) => {
          event.preventDefault()
          if (!busy) onSave()
        }}
      >
        <div className="form-grid-two">
          {field(
            'name',
            'Name',
            aws ? 'aws-spot' : 'gcp-spot',
            'Pools refer to it as capacity = "name". Lowercase letters, digits and hyphens.',
          )}
          <label className="grid min-w-0 gap-1">
            Cloud
            <SelectField
              label="Cloud"
              value={draft.cloud}
              disabled={Boolean(draft.editing)}
              onValueChange={(value) =>
                onChange({ ...blank(value as Capacity['cloud']), name: draft.name })
              }
              options={[
                { value: 'aws', label: 'AWS EC2' },
                { value: 'gcp', label: 'Google Compute Engine' },
              ]}
            />
            <small className="text-xs muted-text">Cannot be changed after saving.</small>
          </label>
        </div>

        <SetupGuide draft={draft} settings={settings} />

        <fieldset className="grid min-w-0 gap-4">
          <legend className="mb-2 text-sm font-medium">Account and network</legend>
          {aws ? (
            <>
              {field(
                'role_arn',
                'IAM role ARN',
                'arn:aws:iam::123456789012:role/hakopod-actions',
                'The role from step 1. Hakopod assumes it for every call.',
              )}
              <div className="form-grid-two">
                {field('region', 'Region', 'eu-central-1', 'Where runners launch.')}
                {field(
                  'external_id',
                  'External ID',
                  settings.external_id,
                  "Optional. Leave empty to use this installation's.",
                  false,
                )}
              </div>
              <div className="form-grid-two">
                {field(
                  'subnet_ids',
                  'Subnets',
                  'subnet-0abc1234, subnet-0def5678',
                  'Comma-separated, 1–10. Spread across zones for more spot capacity.',
                )}
                {field(
                  'security_group_ids',
                  'Security groups',
                  'sg-0abc1234',
                  'Comma-separated, 1–5. Outbound only.',
                )}
              </div>
            </>
          ) : (
            <>
              <div className="form-grid-two">
                {field('project', 'Project ID', 'my-project', 'Runners launch in this project.')}
                {field('region', 'Region', 'us-central1', 'Where runners launch.')}
              </div>
              {field(
                'service_account',
                'Service account',
                'hakopod-actions@my-project.iam.gserviceaccount.com',
                'The account from step 1. Runner VMs themselves get no service account.',
              )}
              <div className="form-grid-two">
                {field(
                  'zones',
                  'Zones',
                  'us-central1-a, us-central1-b',
                  'Comma-separated zones of the region, tried in order.',
                )}
                {field('subnet_ids', 'Subnetwork', 'default', 'One subnetwork name in the region.')}
              </div>
            </>
          )}
        </fieldset>

        <fieldset className="grid min-w-0 gap-4">
          <legend className="mb-2 text-sm font-medium">Machines</legend>
          {field(
            'instance_types',
            aws ? 'Instance types' : 'Machine types',
            aws ? 'c7i.xlarge, c6i.xlarge, m7i.xlarge' : 'n2d-standard-4, e2-standard-4',
            'Comma-separated, tried in order. List several so a launch finds spot capacity. All must match the architecture.',
          )}
          <div className="form-grid-two">
            <label className="grid min-w-0 gap-1">
              Architecture
              <SelectField
                label="Architecture"
                value={draft.architecture}
                onValueChange={(value) =>
                  onChange({ ...draft, architecture: value as Capacity['architecture'] })
                }
                options={[
                  { value: 'amd64', label: 'amd64 (x86)' },
                  { value: 'arm64', label: aws ? 'arm64 (Graviton)' : 'arm64 (Tau T2A, Axion)' },
                ]}
              />
              <small className="text-xs muted-text">
                Workflows target it with the runner labels of their pool.
              </small>
            </label>
            {field(
              'image',
              'Image',
              aws ? 'runs-on-full' : 'hakopod-minimal',
              aws
                ? 'runs-on-full: GitHub-compatible Ubuntu 24.04 image from RunsOn. hakopod-minimal: built in your account. Or an AMI ID.'
                : 'hakopod-minimal is built in your project. Or projects/…/global/images/[family/]NAME.',
              false,
            )}
          </div>
          <div className="form-grid-two">
            {field(
              'disk_gib',
              'Disk (GiB)',
              '100',
              'Encrypted root volume. runs-on-full needs 30 or more.',
            )}
            <label className="grid min-w-0 gap-1">
              Pricing
              <span className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={draft.spot}
                  onChange={(event) => onChange({ ...draft, spot: event.target.checked })}
                />
                Spot
              </span>
              <small className="text-xs muted-text">
                Spot is up to 90% cheaper but can be reclaimed; interrupted runs are rerun
                automatically. Off launches on-demand.
              </small>
            </label>
          </div>
        </fieldset>

        <fieldset className="grid min-w-0 gap-4">
          <legend className="mb-2 text-sm font-medium">Limits and access</legend>
          <div className="form-grid-two">
            {field(
              'max_instances',
              'Max instances',
              '10',
              'Hard ceiling across every pool on this capacity, standbys included. Pools wait at the limit.',
            )}
            {field(
              'warm_pool',
              'Warm standbys',
              '0',
              'Stopped on-demand VMs kept ready so jobs start faster. You pay for their disks.',
            )}
          </div>
          {field(
            'environments',
            'Allowed environments',
            'shop/production, shop/staging',
            'project/environment pairs that may use it, comma-separated, or * for all.',
          )}
        </fieldset>

        {conflict && <Note>Someone else changed VM capacity. Review and save again.</Note>}
        {error && <RequestError error={error} />}
      </form>
    </InspectorSheet>
  )
}
