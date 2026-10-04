import { useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope, canAccess } from '../lib/scope'
import { message, timestamp } from '../lib/api'
import {
  databaseSummary,
  type DatabaseSpec,
  type ManagedDatabase,
  type DatabaseResizeReview,
} from '../lib/databases'
import { FormError, FormPage, FormSection } from './form-page'
import { Note } from './shared'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { databaseVersions } from '../lib/database-create'
import { DatabaseCreate } from './database-create'
import { databaseRequestedCapacity, routerInstances, votingDatabase, databaseStorageGiB, engineName } from '../lib/database-view'

const initial: DatabaseSpec = {
  schema_version: 1,
  name: '',
  engine: 'postgresql',
  version: '17',
  mode: 'standalone',
  replicas: 0,
  shards: 1,
  cpu: '250m',
  memory: '512Mi',
  storage_gib: 5,
  tls: { mode: 'required' },
}

const resourcePresets = [
  {
    name: 'Starter',
    cpu: '250m',
    memory: '512Mi',
    detail: 'Small development and low-traffic workloads',
  },
  { name: 'Standard', cpu: '500m', memory: '1Gi', detail: 'Steady application traffic' },
  { name: 'Performance', cpu: '1', memory: '2Gi', detail: 'Heavier queries and cache workloads' },
] as const
export function DatabaseForm(props: { project: string; environment: string; database?: ManagedDatabase }) {
  return props.database ? <DatabaseAllocationForm {...props} /> : <DatabaseCreate project={props.project} environment={props.environment} />
}
function DatabaseAllocationForm({
  project,
  environment,
  database,
}: {
  project: string
  environment: string
  database?: ManagedDatabase
}) {
  const [spec, setSpec] = useState<DatabaseSpec>(database?.spec || initial)
  const capacity = databaseRequestedCapacity(spec)
  const [review, setReview] = useState<{ id: string; plan?: DatabaseResizeReview } | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [showCustomResources, setShowCustomResources] = useState(
    () =>
      !resourcePresets.some((preset) => preset.cpu === spec.cpu && preset.memory === spec.memory),
  )
  const key = useRef('')
  const navigate = useNavigate()
  const cache = useQueryClient()
  const { identity } = useScope()
  const canManage = !identity.application && canAccess(identity, project, 'deployments:write')
  const replicaOnly = Boolean(database && votingDatabase(spec.engine))
  const unchangedReplicas = replicaOnly && spec.replicas === database?.spec.replicas
  const expired = Boolean(review?.plan && Date.parse(review.plan.expires_at) <= Date.now())
  const update = (fields: Partial<DatabaseSpec>) => {
    setSpec((v) => ({ ...v, ...fields }))
    setReview(null)
    setConfirmed(false)
    key.current = ''
  }
  if (!canManage)
    return (
      <Note>
        Database changes require project deployment permission without an application-only scope.
      </Note>
    )
  if (database && (['clickhouse', 'oracle', 'vitess'].includes(spec.engine) || (replicaOnly && spec.mode === 'standalone'))) return <FormPage title="Database capacity" description="Review the current engine's capacity policy." breadcrumbs={[]}>
    <Note>{replicaOnly ? `${engineName(spec.engine)} member resources and storage` : spec.engine === 'vitess' ? 'Vitess capacity and table routing' : spec.engine === 'clickhouse' ? 'ClickHouse topology and resources' : 'Oracle edition and resources'} are fixed at creation. Create a separate compatible database and recover into it to change capacity.</Note>
    {spec.engine === 'vitess' && <>
      <Note>Creation is unavailable while native replication and recovery acceptance remain incomplete. Vitess requires a dedicated operator-approved native backup destination.</Note>
      <FormSection title="Vitess configuration"><dl className="db-create-facts"><div><dt>Table routing</dt><dd>{spec.shards === 1 ? 'Single shard; no sharding columns' : (spec.vitess?.tables || []).map((table) => `${table.name} / ${table.sharding_column}`).join(', ') || 'Not configured'}</dd></div><div><dt>Native backup destination</dt><dd>{spec.vitess?.backup_destination_id || 'Not configured'}{spec.vitess && ` · r${spec.vitess.backup_destination_revision}`}</dd></div></dl></FormSection>
    </>}
    <Button asChild><Link to="/databases/$databaseId" params={{ databaseId: database.id }} search={{ project: database.project, environment: database.environment }}>Back to database</Link></Button>
  </FormPage>
  return (
    <FormPage
      title={replicaOnly ? 'Change replicas' : database ? 'Resize database' : 'New database'}
      description="Database resources are independent of application replicas. Review the allocation before creating or resizing."
      breadcrumbs={[]}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy) return
          if (unchangedReplicas) {
            setError('Choose a different replica count before requesting a review.')
            return
          }
          setBusy(true)
          setError('')
          try {
            if (!review) {
              setReview(
                database
                  ? await unwrap(
                      client.POST('/databases/{id}/resize-plan', {
                        params: { path: { id: database.id } },
                        body: { spec },
                      }),
                    )
                  : { id: 'create' },
              )
              return
            }
            if (!key.current) key.current = crypto.randomUUID()
            const op = database
              ? await unwrap(
                  client.POST('/databases/{id}/resize', {
                    params: {
                      path: { id: database.id },
                      header: { 'Idempotency-Key': key.current },
                    },
                    body: { spec, review_id: review.id, expected_revision: database.revision },
                  }),
                )
              : await unwrap(
                  client.POST('/databases', {
                    params: { header: { 'Idempotency-Key': key.current } },
                    body: { project, environment, spec },
                  }),
                )
            void cache.invalidateQueries({ queryKey: ['managed-databases'] })
            void cache.invalidateQueries({ queryKey: ['managed-database', op.database_id] })
            void navigate({
              to: '/databases/$databaseId',
              params: { databaseId: op.database_id },
              search: { project, environment },
            })
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <FormSection title="Database and allocation">
          {spec.engine === 'mysql' && <Note>MySQL member resources and storage are fixed at creation. Recover into a new database to change per-member resources. Clusters support 2, 4 or 6 voting replicas.</Note>}
          {spec.engine === 'mongodb' && <Note>MongoDB member resources and storage are fixed at creation. Recover into a new database to change per-member resources. Clusters support 2, 4 or 6 voting replicas. Removing replicas retains their volumes until the database is deleted.</Note>}
          <div className="grid gap-4 sm:grid-cols-2">
            <label>
              Name
              <Input
                required
                value={spec.name}
                disabled={Boolean(database) || busy}
                maxLength={40}
                pattern="[a-z][a-z0-9-]*"
                onChange={(e) => update({ name: e.target.value })}
              />
            </label>
            <label>
              Engine
              <SelectField
                label="Engine"
                value={spec.engine}
                disabled={Boolean(database) || busy}
                options={[
                  { value: 'postgresql', label: 'PostgreSQL' },
                  { value: 'redis', label: 'Redis' },
                  { value: 'mysql', label: 'MySQL' },
                  { value: 'mongodb', label: 'MongoDB', disabled: !database },
                ]}
                onValueChange={(v) =>
                  update({
                    engine: v as DatabaseSpec['engine'],
                    version: databaseVersions(v)[0],
                    shards: v === 'redis' && spec.mode === 'cluster' ? 3 : 1,
                  })
                }
              />
            </label>
            <label>
              Version
              <SelectField
                label="Version"
                value={spec.version}
                disabled={Boolean(database) || busy}
                options={databaseVersions(spec.engine).map((value) => ({
                  value,
                  label: value,
                }))}
                onValueChange={(version) => update({ version })}
              />
            </label>
            <label>
              Layout
              <SelectField
                label="Layout"
                value={spec.mode}
                disabled={Boolean(database) || busy}
                options={[
                  { value: 'standalone', label: 'Standalone' },
                  { value: 'cluster', label: 'Cluster' },
                ]}
                onValueChange={(v) =>
                  update({
                    mode: v as DatabaseSpec['mode'],
                    replicas: v === 'cluster' ? votingDatabase(spec.engine) ? 2 : 1 : 0,
                    shards: v === 'cluster' && spec.engine === 'redis' ? 3 : 1,
                    placement: { ...spec.placement, spread: v === 'cluster' ? 'nodes' : '' },
                  })
                }
              />
            </label>
            {spec.mode === 'cluster' && (
              <label>
                {spec.engine === 'redis' ? 'Replicas per shard' : 'Replicas'}
                <Input
                  type="number"
                  min={votingDatabase(spec.engine) ? 2 : 1}
                  step={votingDatabase(spec.engine) ? 2 : 1}
                  max={spec.engine === 'redis' ? 2 : 6}
                  required
                  value={spec.replicas}
                  disabled={busy}
                  onChange={(e) => update({ replicas: Number(e.target.value) })}
                />
                {replicaOnly && database && <span className="field-help">Current: {database.spec.replicas} voting replicas. Choose a different count to review a change.</span>}
              </label>
            )}
            {spec.mode === 'cluster' && spec.engine === 'redis' && (
              <label>
                Shards
                <Input
                  type="number"
                  min={3}
                  max={16}
                  required
                  value={spec.shards}
                  disabled={busy}
                  onChange={(e) => update({ shards: Number(e.target.value) })}
                />
              </label>
            )}
            <div className="sm:col-span-2 grid gap-2">
              <span className="text-sm font-medium">Resources per member</span>
              <div className="grid min-w-0 gap-2 sm:grid-cols-[repeat(3,minmax(0,1fr))]">
                {resourcePresets.map((preset) => {
                  const selected = spec.cpu === preset.cpu && spec.memory === preset.memory
                  return (
                    <Button
                      key={preset.name}
                      type="button"
                      variant={selected && !showCustomResources ? 'primary' : 'outline'}
                      aria-pressed={selected && !showCustomResources}
                      disabled={busy || votingDatabase(spec.engine)}
                      className="h-auto! min-h-11 min-w-0 justify-start! whitespace-normal! px-3 py-2 text-left normal-case! font-sans!"
                      onClick={() => {
                        setShowCustomResources(false)
                        update({ cpu: preset.cpu, memory: preset.memory })
                      }}
                    >
                      <span className="grid min-w-0 gap-0.5 whitespace-normal">
                        <span className="break-words">{preset.name}</span>
                        <span className="break-words text-xs font-normal opacity-80">
                          {preset.cpu} CPU · {preset.memory} · {preset.detail}
                        </span>
                      </span>
                    </Button>
                  )
                })}
              </div>
              <details
                open={showCustomResources}
                onToggle={(event) => setShowCustomResources(event.currentTarget.open)}
              >
                <summary>Custom CPU and memory</summary>
                <div className="mt-3 grid gap-4 sm:grid-cols-2">
                  <label>
                    CPU per member
                    <Input
                      required
                      value={spec.cpu}
                      disabled={busy || votingDatabase(spec.engine)}
                      onChange={(e) => {
                        setShowCustomResources(true)
                        update({ cpu: e.target.value })
                      }}
                    />
                    <span className="field-help">100m to 16 cores; 1000m is one core.</span>
                  </label>
                  <label>
                    Memory per member
                    <Input
                      required
                      value={spec.memory}
                      disabled={busy || votingDatabase(spec.engine)}
                      onChange={(e) => {
                        setShowCustomResources(true)
                        update({ memory: e.target.value })
                      }}
                    />
                    <span className="field-help">{votingDatabase(spec.engine) ? '1Gi' : '128Mi'} to 64Gi.</span>
                  </label>
                </div>
              </details>
            </div>
            <label>
              Storage per member (GiB)
              <Input
                type="number"
                required
                min={database?.spec.storage_gib || 1}
                max={1024}
                value={spec.storage_gib}
                disabled={busy || votingDatabase(spec.engine)}
                onChange={(e) => update({ storage_gib: Number(e.target.value) })}
              />
            </label>
          </div>
          {spec.mode === 'cluster' && spec.engine === 'redis' && (
            <Note>
              Redis Cluster requires a cluster-aware client. Changing shard count requires healthy
              slot ownership and a verified backup captured within the last hour.
            </Note>
          )}
        </FormSection>
        <FormSection title="Placement">
          <div className="grid gap-4 sm:grid-cols-2">
            {spec.mode === 'cluster' && (
              <label>
                Member separation
                <SelectField
                  label="Member separation"
                  value={spec.placement?.spread || ''}
                  disabled={Boolean(database) || busy}
                  options={[
                    { value: 'nodes', label: 'One member per node' },
                    { value: 'zones', label: 'One member per zone' },
                    { value: '', label: 'Scheduler defaults' },
                  ]}
                  onValueChange={(spread) =>
                    update({
                      placement: { ...spec.placement, spread: spread as '' | 'nodes' | 'zones' },
                    })
                  }
                />
                <span className="field-help">
                  {spec.placement?.spread
                    ? `Strict separation needs ${spec.shards * (1 + spec.replicas)} distinct ${spec.placement.spread === 'zones' ? 'zones' : 'nodes'}. Members wait if a suitable location is unavailable.`
                    : 'The scheduler may place multiple members on one node.'}
                </span>
              </label>
            )}
            <div className="grid gap-2"><span className="text-sm">Eligible nodes</span><div className="flex flex-wrap gap-2">{spec.placement?.node_names?.length ? spec.placement.node_names.map((name) => <span key={name} className="max-w-full break-all rounded border border-border px-3 py-2 text-xs">{name}</span>) : <span className="text-sm text-muted-foreground">All approved nodes</span>}</div><p className="field-help">Node selection is fixed at creation. This resize preserves the saved placement.</p></div>
          </div>
          <Note>
            Placement is fixed when the database is created. Nodes may belong to different providers
            within one connected cluster. Independent clusters are not supported. Zone separation
            alone does not guarantee failover or zero data loss.
          </Note>
        </FormSection>
        {review && (
          <FormSection title="Review">
            {database && (
              <p>
                Current: {databaseSummary(database.spec)}; {database.spec.cpu} CPU,{' '}
                {database.spec.memory}, {database.spec.storage_gib} GiB per member.
              </p>
            )}
            {review.plan && (
              <p>
                Review expires {timestamp(review.plan.expires_at)}.
                {expired ? ' Refresh this review before proceeding.' : ''}
              </p>
            )}
            <Button
              type="button"
              onClick={() => {
                setReview(null)
                setConfirmed(false)
                key.current = ''
              }}
            >
              Edit or refresh review
            </Button>
            <p>
              {project} / {environment} · {spec.name}
            </p>
            <p>{database ? 'Proposed: ' : ''}{databaseSummary(spec)}</p>
            {replicaOnly && database && <p>Voting replicas change from {database.spec.replicas} to {spec.replicas}. Per-member CPU, memory and storage stay the same.</p>}
            <p>
              {spec.placement?.spread === 'zones'
                ? 'One member per zone'
                : spec.placement?.spread === 'nodes'
                  ? 'One member per node'
                  : 'Scheduler defaults'}{' '}
              · {spec.placement?.node_names?.join(', ') || 'All authorized nodes'}
            </p>
            <p>
              {spec.shards * (1 + spec.replicas)} members, each with {spec.cpu} CPU, {spec.memory}{' '}
              memory and {spec.storage_gib} GiB storage.
            </p>
            {spec.engine === 'mysql' && capacity && <>
              <p>Each member also has a 100m CPU / 256Mi sidecar. {routerInstances(spec)} Routers each use 100m CPU / 128Mi.</p>
              <p>Total requested: {capacity.cpu.toLocaleString(undefined, { maximumFractionDigits: 3 })} CPU cores · {(capacity.memoryMiB / 1024).toLocaleString(undefined, { maximumFractionDigits: 3 })} GiB memory · {spec.storage_gib * spec.shards * (1 + spec.replicas)} GiB storage. Additional capacity is reserved for replacement and recovery operations.</p>
            </>}
            {spec.engine === 'mongodb' && capacity && <>
              <p>Each member also has a 100m CPU / 256Mi agent and a separate 1 GiB log volume.</p>
              <p>Total requested: {capacity.cpu.toLocaleString()} CPU cores · {(capacity.memoryMiB / 1024).toLocaleString()} GiB memory · {databaseStorageGiB(spec)} GiB storage. Additional capacity is reserved for replacement and recovery operations.</p>
            </>}
            {replicaOnly && database && spec.replicas < database.spec.replicas && <Note>Reducing replicas keeps the previous CPU and storage reservations until the database is deleted. Memory reservation follows the completed layout.</Note>}
            {review.plan?.backup && (
              <p>Verified backup captured {timestamp(review.plan.backup.captured_at)}.</p>
            )}
            {review.plan?.warnings.map((text) => (
              <Note key={text}>{text}</Note>
            ))}
            {review.plan?.blocked_reasons.map((text) => (
              <p key={text} role="alert" className="text-destructive">
                {text}
              </p>
            ))}
            {!review.plan?.blocked_reasons.length && (
              <label className="flex min-h-11 items-center gap-2">
                <input
                  type="checkbox"
                  checked={confirmed}
                  onChange={(e) => setConfirmed(e.target.checked)}
                />
                {replicaOnly ? 'I have reviewed the replica change and its total resource allocation.' : 'I have reviewed the resources and this change.'}
              </label>
            )}
          </FormSection>
        )}
        {error && (
          <FormError>{error}</FormError>
        )}
        <div className="form-footer">
          <Button asChild>
            <Link to="/databases" search={{ project, environment }}>
              Cancel
            </Link>
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={
              busy || unchangedReplicas ||
              expired ||
              Boolean(review && (!confirmed || review.plan?.blocked_reasons.length))
            }
          >
            {busy ? 'Working…' : !review ? 'Review' : replicaOnly ? 'Apply replica change' : database ? 'Apply resize' : 'Create database'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
