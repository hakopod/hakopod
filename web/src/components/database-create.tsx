import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import {
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCheck,
  ChevronRight,
  Cpu,
  HardDrive,
  Layers3,
  LockKeyhole,
  MemoryStick,
  Network,
  Server,
  ShieldCheck,
  Trash2,
} from 'lucide-react'
import { client, unwrap } from '../lib/client'
import { canAccess, useScope } from '../lib/scope'
import { message } from '../lib/api'
import { useDatabasePlacementNodes, type DatabaseSpec } from '../lib/databases'
import { useBackupDestinations } from '../lib/backups'
import { databaseEligibleNodes, databasePlacementIssue } from '../lib/database-placement'
import { DatabasePlacementPicker } from './database-placement-picker'
import {
  databaseRequestedCapacity,
  poolerInstances,
  routerInstances,
  keeperInstances,
  placementDomains,
  shardedDatabase,
  databaseLayoutSummary,
  oracleFreeQuotaGiB,
  databaseCapacity,
  engineName,
  expectedMembers,
  votingDatabase,
  databaseStorageGiB,
} from '../lib/database-view'
import {
  databaseCreateIssue,
  databaseCreateSteps,
  databaseVersions,
  databaseEngines,
  initialDatabaseSpec,
  databaseEngineDefaults,
  databaseMinimum,
  maximumReplicas,
} from '../lib/database-create'
import { FormError, FormPage, FormSection } from './form-page'
import { ServiceIcon } from './service-icon'
import { HeadingHelp, Note } from './shared'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'

const sizes = [
  { name: 'Small', cpu: '250m', memory: '512Mi', icon: Server },
  { name: 'Medium', cpu: '1', memory: '2Gi', icon: Layers3 },
  { name: 'Large', cpu: '2', memory: '4Gi', icon: Cpu },
]

export function DatabaseCreate({ project, environment }: { project: string; environment: string }) {
  const [spec, setSpec] = useState<DatabaseSpec>(initialDatabaseSpec)
  const [specificNodes, setSpecificNodes] = useState(false)
  const [step, setStep] = useState(0)
  const [visited, setVisited] = useState(0)
  const [confirmed, setConfirmed] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [attempted, setAttempted] = useState(false)
  const key = useRef('')
  const title = useRef<HTMLDivElement>(null)
  const previousStep = useRef(step)
  const navigate = useNavigate()
  const cache = useQueryClient()
  const { identity } = useScope()
  const members = expectedMembers(spec)
  const domains = placementDomains(spec)
  const minimum = databaseMinimum(spec.engine)
  const total = databaseRequestedCapacity(spec)
  const canManage = !identity.application && canAccess(identity, project, 'deployments:write')
  const nodeInventory = useDatabasePlacementNodes(project, environment, canManage)
  const backupDestinations = useBackupDestinations(canManage && spec.engine === 'vitess')
  const vitessDestinations = (backupDestinations.data?.items || []).filter(
    (destination) => destination.project === project && destination.environment === environment,
  )
  const eligibleNodes = databaseEligibleNodes(spec.engine, nodeInventory.data?.items || [])
  const placementIssue = nodeInventory.error
    ? 'Approved nodes could not be refreshed. Your selection is preserved; retry before continuing.'
    : nodeInventory.isPending
      ? 'Loading approved database nodes…'
      : databasePlacementIssue(spec, nodeInventory.data?.items || [], specificNodes)
  const issue = databaseCreateIssue(spec, step) || (step >= 1 ? placementIssue : undefined)

  useEffect(() => {
    if (previousStep.current !== step) {
      previousStep.current = step
      title.current?.focus()
    }
  }, [step])

  const update = (fields: Partial<DatabaseSpec>) => {
    setSpec((previous) => ({ ...previous, ...fields }))
    setConfirmed(false)
    setError('')
    setVisited(step)
    key.current = ''
  }
  const move = (next: number) => {
    setStep(next)
    setAttempted(false)
    setError('')
  }
  const chooseEngine = (engine: DatabaseSpec['engine']) => {
    if (engine !== spec.engine) update(databaseEngineDefaults(spec, engine))
  }
  const chooseMode = (mode: DatabaseSpec['mode']) => {
    if (mode === spec.mode) return
    update({
      mode,
      replicas: mode === 'cluster' ? (votingDatabase(spec.engine) ? 2 : 1) : 0,
      pooling: spec.pooling
        ? { ...spec.pooling, read_only: mode === 'cluster' && spec.pooling.read_only }
        : undefined,
      shards: mode === 'cluster' && spec.engine === 'redis' ? 3 : 1,
      vitess: spec.vitess
        ? { ...spec.vitess, tables: mode === 'standalone' ? [] : spec.vitess.tables }
        : undefined,
      placement: { ...spec.placement, spread: mode === 'cluster' ? 'nodes' : '' },
    })
  }
  const placement =
    spec.placement?.spread === 'zones'
      ? 'One member per zone'
      : spec.placement?.spread === 'nodes'
        ? 'One member per node'
        : 'Scheduler defaults'

  if (!canManage)
    return (
      <Note>
        Database changes require project deployment permission without an application-only scope.
      </Note>
    )

  return (
    <FormPage
      title="Create database"
      description="Choose a database, review its topology and resources, then create it in the selected project."
      breadcrumbs={[]}
    >
      <form
        className="db-create"
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy) return
          setAttempted(true)
          if (issue) return
          if (step < 4) {
            setVisited(Math.max(visited, step + 1))
            move(step + 1)
            return
          }
          for (let i = 0; i < 4; i++) {
            if (databaseCreateIssue(spec, i)) {
              move(i)
              setAttempted(true)
              return
            }
          }
          if (!confirmed) return
          setBusy(true)
          setError('')
          try {
            if (!key.current) key.current = crypto.randomUUID()
            const operation = await unwrap(
              client.POST('/databases', {
                params: { header: { 'Idempotency-Key': key.current } },
                body: { project, environment, spec },
              }),
            )
            void cache.invalidateQueries({ queryKey: ['managed-databases'] })
            void cache.invalidateQueries({ queryKey: ['managed-database', operation.database_id] })
            void navigate({
              to: '/databases/$databaseId',
              params: { databaseId: operation.database_id },
              search: { project, environment },
            })
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <nav aria-label="Database creation steps" className="db-create-steps">
          <ol>
            {databaseCreateSteps.map((label, index) => (
              <li key={label}>
                <button
                  type="button"
                  aria-current={step === index ? 'step' : undefined}
                  disabled={busy || index > visited}
                  onClick={() => move(index)}
                >
                  <span className="db-step-index">
                    {index < step ? (
                      <Check size={14} aria-hidden="true" />
                    ) : (
                      String(index + 1).padStart(2, '0')
                    )}
                  </span>
                  <span>{label}</span>
                </button>
                {index < 4 && <ChevronRight size={14} aria-hidden="true" />}
              </li>
            ))}
          </ol>
        </nav>

        <div className="db-create-layout">
          <div className="min-w-0">
            <div className="db-create-stage" ref={title} tabIndex={-1}>
              <span>Step {step + 1} of 5</span>
              <h2>
                {
                  [
                    'Choose your database',
                    'Design the topology',
                    'Allocate resources',
                    'Secure your connections',
                    'Review deployment',
                  ][step]
                }
              </h2>
            </div>

            {step === 0 && (
              <div className="grid gap-5">
                <fieldset className="min-w-0">
                  <legend className="sr-only">Database engine</legend>
                  <div className="db-engine-grid">
                    {databaseEngines.map((engine) => (
                      <button
                        key={engine.id}
                        type="button"
                        className="db-engine-card"
                        aria-pressed={engine.id === spec.engine}
                        disabled={busy || !engine.enabled}
                        onClick={() => chooseEngine(engine.id as DatabaseSpec['engine'])}
                      >
                        <div className="db-engine-card-top">
                          <span className="db-engine-mark">
                            <ServiceIcon name={engine.id} size={30} loading="eager" />
                          </span>
                          <span className="db-choice-indicator">
                            {engine.id === spec.engine && <Check size={14} aria-hidden="true" />}
                          </span>
                        </div>
                        <strong>{engine.name}</strong>
                        <span className="db-engine-description">{engine.description}</span>
                        <span className="db-engine-tags">
                          <span>{engine.category}</span>
                          {!engine.enabled ? (
                            <span>In development</span>
                          ) : (
                            ['clickhouse', 'oracle'].includes(engine.id) && (
                              <span>Development preview</span>
                            )
                          )}
                        </span>
                      </button>
                    ))}
                  </div>
                  <p className="field-help mt-3">
                    Engines marked “In development” cannot be created yet.
                  </p>
                </fieldset>
                <FormSection title="Database identity">
                  <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,180px)]">
                    <label htmlFor="db-create-name">
                      Database name
                      <Input
                        id="db-create-name"
                        required
                        autoComplete="off"
                        spellCheck={false}
                        placeholder="orders-db"
                        maxLength={40}
                        pattern="[a-z]([a-z0-9\-]*[a-z0-9])?"
                        value={spec.name}
                        disabled={busy}
                        aria-describedby="db-name-help"
                        onChange={(event) => update({ name: event.target.value })}
                      />
                      <span id="db-name-help" className="field-help">
                        Lowercase letters, numbers and hyphens. Choose a name that describes its
                        purpose.
                      </span>
                    </label>
                    <label>
                      Version
                      <SelectField
                        label="Database version"
                        value={spec.version}
                        disabled={busy}
                        options={databaseVersions(spec.engine).map((value) => ({
                          value,
                          label: value,
                        }))}
                        onValueChange={(version) => update({ version })}
                      />
                    </label>
                  </div>
                </FormSection>
                {spec.engine === 'oracle' && (
                  <FormSection title="Oracle edition">
                    <div className="grid gap-3 sm:grid-cols-2">
                      <div className="db-layout-card">
                        <strong>Oracle Database Free</strong>
                        <span>
                          Free to use under Oracle's terms. Proprietary software, not open source.
                        </span>
                        <span className="db-engine-tags">
                          <span>Standalone</span>
                          <span>26ai</span>
                        </span>
                      </div>
                      <div className="db-layout-card">
                        <strong>Customer-licensed Enterprise</strong>
                        <span>
                          Bring an eligible license, a container image pinned by digest and a scoped
                          registry reference. Deployment and Data Guard are still in development.
                        </span>
                        <span className="db-engine-tags">
                          <span>Not available yet</span>
                        </span>
                      </div>
                    </div>
                    <p className="field-help">
                      Free is limited to 2 database CPUs, 2 GB of database memory and 12 GB of user
                      data. Container resources also cover Oracle's supporting processes.
                    </p>
                  </FormSection>
                )}
                {['clickhouse', 'oracle'].includes(spec.engine) && (
                  <Note>
                    Development preview. Native acceptance is in progress; this is not a production
                    availability guarantee.
                  </Note>
                )}
              </div>
            )}

            {step === 1 && (
              <div className="grid gap-4">
                <fieldset>
                  <legend className="sr-only">Deployment layout</legend>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <button
                      type="button"
                      className="db-layout-card"
                      aria-pressed={spec.mode === 'standalone'}
                      disabled={busy}
                      onClick={() => chooseMode('standalone')}
                    >
                      <Server size={22} aria-hidden="true" />
                      <strong>Standalone</strong>
                      <span>One database member. Simple allocation, with no replica failover.</span>
                      <span className="db-engine-tags">
                        <span>1 member</span>
                      </span>
                    </button>
                    <button
                      type="button"
                      className="db-layout-card"
                      aria-pressed={spec.mode === 'cluster'}
                      disabled={busy || spec.engine === 'oracle'}
                      onClick={() => chooseMode('cluster')}
                    >
                      <Network size={22} aria-hidden="true" />
                      <strong>Cluster</strong>
                      <span>
                        {spec.engine === 'oracle'
                          ? 'Data Guard requires customer-licensed Enterprise support, which is still in development.'
                          : spec.engine === 'vitess'
                            ? 'Route through vtgate to a primary tablet in each shard, with replicas and three topology members.'
                          : spec.engine === 'clickhouse'
                            ? 'Replicate each shard across data members, with three Keeper members for coordination.'
                            : spec.engine === 'redis'
                              ? 'Distribute data across primary shards, each with its own replicas.'
                              : votingDatabase(spec.engine)
                                ? 'One write primary, voting replicas and a majority quorum for failover.'
                                : 'One write primary with streaming replicas and controller-managed failover.'}
                      </span>
                      <span className="db-engine-tags">
                        <span>
                          {spec.engine === 'oracle'
                            ? 'Enterprise required'
                            : shardedDatabase(spec.engine)
                              ? 'Shards + replicas'
                              : 'Primary + replicas'}
                        </span>
                      </span>
                    </button>
                  </div>
                </fieldset>
                {spec.mode === 'cluster' && (
                  <FormSection title="Cluster members">
                    <div className="grid gap-4 sm:grid-cols-2">
                      <label htmlFor="db-replicas">
                        {shardedDatabase(spec.engine)
                          ? 'Additional replicas per shard'
                          : votingDatabase(spec.engine)
                            ? 'Voting replicas'
                            : 'Read replicas'}
                        <Input
                          id="db-replicas"
                          type="number"
                          required
                          min={votingDatabase(spec.engine) ? 2 : 1}
                          step={votingDatabase(spec.engine) ? 2 : 1}
                          max={maximumReplicas(spec.engine)}
                          value={spec.replicas}
                          disabled={busy}
                          onChange={(event) => update({ replicas: Number(event.target.value) })}
                        />
                      </label>
                      {shardedDatabase(spec.engine) && (
                        <label htmlFor="db-shards">
                          Shards
                          <Input
                            id="db-shards"
                            type="number"
                            required
                            min={spec.engine === 'redis' ? 3 : 1}
                            max={spec.engine === 'redis' ? 16 : 8}
                            value={spec.shards}
                            disabled={busy}
                            onChange={(event) => {
                              const shards = Number(event.target.value)
                              update({
                                shards,
                                vitess: spec.vitess && spec.engine === 'vitess'
                                  ? { ...spec.vitess, tables: shards === 1 ? [] : spec.vitess.tables }
                                  : spec.vitess,
                              })
                            }}
                          />
                        </label>
                      )}
                    </div>
                    {spec.engine === 'mysql' && (
                      <p className="field-help">
                        Choose 2, 4 or 6 replicas. A majority of voting members must remain
                        connected for writes. Every database includes {routerInstances(spec)} Router
                        instances.
                      </p>
                    )}
                    {spec.engine === 'mongodb' && (
                      <p className="field-help">
                        Choose 2, 4 or 6 voting replicas. Majority writes need a connected majority.
                        Applications use replica-set discovery to find the primary.
                      </p>
                    )}
                    {spec.engine === 'redis' && (
                      <p className="field-help">
                        Applications need a Redis Cluster client that can discover and reach every
                        member.
                      </p>
                    )}
                    {spec.engine === 'vitess' && (
                      <p className="field-help">
                        Choose 1, 2, 4 or 8 shards and 1–5 additional tablets per shard. Applications
                        connect through redundant vtgate gateways; three etcd members hold routing metadata.
                      </p>
                    )}
                  </FormSection>
                )}
                <FormSection
                  title="Placement"
                  description="Choose eligible locations inside this connected cluster. Placement is fixed when the database is created."
                >
                  <div className="grid gap-4 sm:grid-cols-2">
                    {spec.mode === 'cluster' && (
                      <label>
                        Member separation
                        <SelectField
                          label="Member separation"
                          value={spec.placement?.spread || ''}
                          disabled={busy}
                          options={[
                            { value: 'nodes', label: 'One member per node' },
                            { value: 'zones', label: 'One member per zone' },
                            { value: '', label: 'Scheduler defaults' },
                          ]}
                          onValueChange={(spread) =>
                            update({
                              placement: {
                                ...spec.placement,
                                spread: spread as '' | 'nodes' | 'zones',
                              },
                            })
                          }
                        />
                        <span className="field-help">
                          {spec.placement?.spread
                            ? `Requires ${domains} distinct ${spec.placement.spread}. Members wait if a suitable location is unavailable.`
                            : 'Multiple members may share a node.'}
                          {keeperInstances(spec) > 0 &&
                            ' Data members and Keeper members use separate spreading groups and may share a node.'}
                        </span>
                      </label>
                    )}
                  </div>
                  {nodeInventory.error && <FormError focus={false}>{placementIssue}</FormError>}
                  <DatabasePlacementPicker
                    nodes={eligibleNodes}
                    selected={spec.placement?.node_names || []}
                    specific={specificNodes}
                    disabled={busy || nodeInventory.isPending}
                    onSpecific={(specific) => {
                      setSpecificNodes(specific)
                      update({ placement: { ...spec.placement, node_names: [] } })
                    }}
                    onSelection={(node_names) =>
                      update({ placement: { ...spec.placement, node_names } })
                    }
                  />
                  <Button
                    className="justify-self-start"
                    disabled={busy || nodeInventory.isFetching}
                    onClick={() => {
                      void nodeInventory.refetch()
                    }}
                  >
                    {nodeInventory.isFetching ? 'Refreshing nodes…' : 'Refresh nodes'}
                  </Button>
                  <Note>
                    Nodes can span zones or providers within one connected cluster. Placement labels
                    alone do not prove outage tolerance or guarantee zero data loss.
                  </Note>
                </FormSection>
              </div>
            )}

            {step === 2 && (
              <div className="grid gap-4">
                <fieldset>
                  <legend className="mb-2 text-sm">Starting allocation per member</legend>
                  <div className="grid gap-3 sm:grid-cols-3">
                    {sizes
                      .filter((size) => {
                        const capacity = databaseCapacity(
                          { ...spec, cpu: size.cpu, memory: size.memory },
                          1,
                        )
                        return (
                          capacity &&
                          capacity.cpu >= minimum.cpu &&
                          capacity.memoryMiB >= minimum.memoryMiB
                        )
                      })
                      .map((size) => (
                        <button
                          type="button"
                          key={size.name}
                          className="db-layout-card"
                          aria-pressed={spec.cpu === size.cpu && spec.memory === size.memory}
                          disabled={busy}
                          onClick={() => update({ cpu: size.cpu, memory: size.memory })}
                        >
                          <size.icon size={20} aria-hidden="true" />
                          <strong>{size.name}</strong>
                          <span>
                            {size.cpu} CPU · {size.memory} memory
                          </span>
                        </button>
                      ))}
                  </div>
                </fieldset>
                <FormSection title="Customize allocation">
                  <div className="grid gap-4 sm:grid-cols-2">
                    <label htmlFor="db-cpu">
                      CPU per member
                      <Input
                        id="db-cpu"
                        required
                        value={spec.cpu}
                        disabled={busy}
                        aria-describedby="db-cpu-help"
                        onChange={(event) => update({ cpu: event.target.value })}
                      />
                      <span id="db-cpu-help" className="field-help">
                        {minimum.cpu < 1 ? `${minimum.cpu * 1000}m` : minimum.cpu} to 16 cores.
                        1000m equals one core.
                      </span>
                    </label>
                    <label htmlFor="db-memory">
                      Memory per member
                      <Input
                        id="db-memory"
                        required
                        value={spec.memory}
                        disabled={busy}
                        aria-describedby="db-memory-help"
                        onChange={(event) => update({ memory: event.target.value })}
                      />
                      <span id="db-memory-help" className="field-help">
                        {minimum.memoryMiB < 1024
                          ? `${minimum.memoryMiB}Mi`
                          : `${minimum.memoryMiB / 1024}Gi`}{' '}
                        to 64Gi.
                      </span>
                    </label>
                    <label htmlFor="db-storage">
                      Storage per member (GiB)
                      <Input
                        id="db-storage"
                        type="number"
                        required
                        min={minimum.storageGiB}
                        max={1024}
                        value={spec.storage_gib}
                        disabled={busy}
                        aria-describedby="db-storage-help"
                        onChange={(event) => update({ storage_gib: Number(event.target.value) })}
                      />
                      <span id="db-storage-help" className="field-help">
                        {!['postgresql', 'redis'].includes(spec.engine)
                          ? 'Member resources are fixed at creation. Recover into a new database to change capacity.'
                          : 'Persistent storage for each member. Storage can grow after creation.'}
                      </span>
                    </label>
                  </div>
                </FormSection>
                {spec.engine === 'mongodb' && (
                  <Note>
                    Includes a 100m CPU / 256Mi agent and 1 GiB log volume per member. Requires
                    amd64 nodes. Separate replacement and recovery headroom is reserved.
                  </Note>
                )}
                {spec.engine === 'mysql' && (
                  <Note>
                    Includes a 100m CPU / 256Mi sidecar per member and {routerInstances(spec)}{' '}
                    Router instances at 100m CPU / 128Mi each. Requires amd64 nodes. Separate
                    replacement and recovery headroom is reserved.
                  </Note>
                )}
                {spec.engine === 'clickhouse' && (
                  <Note>
                    Each data member includes a backup-staging volume equal to its data volume.
                    {keeperInstances(spec) > 0 &&
                      ' The three Keeper members each reserve 250m CPU, 256Mi memory and 1 GiB storage.'}{' '}
                    Ordinary local tables contain one shard; use a Distributed table for queries
                    across shards.
                  </Note>
                )}
                {spec.engine === 'oracle' && (
                  <Note>
                    Includes {spec.storage_gib} GiB of backup staging. The APP schema quota is{' '}
                    {oracleFreeQuotaGiB(spec.storage_gib)} GiB, reserving space for system data,
                    undo and temporary work. Free's upstream limits still apply.
                  </Note>
                )}
                {spec.engine === 'vitess' && (
                  <Note>
                    Each MySQL member includes a 100m CPU / 256Mi vttablet. The allocation also
                    includes redundant vtgate gateways, three etcd topology members, control services,
                    and bounded native-backup recovery headroom. Storage includes one data volume per tablet
                    and 1 GiB for each topology member.
                  </Note>
                )}
                <div className="db-allocation-total">
                  <Cpu size={18} aria-hidden="true" />
                  <span>
                    <strong>Total requested allocation</strong>
                    <span>
                      {total
                        ? `${total.cpu.toLocaleString()} CPU cores · ${(total.memoryMiB / 1024).toLocaleString()} GiB memory`
                        : 'Enter valid CPU and memory values'}{' '}
                      · {databaseStorageGiB(spec)} GiB storage including support volumes · {members}{' '}
                      data {members === 1 ? 'member' : 'members'}
                    </span>
                  </span>
                </div>
              </div>
            )}

            {step === 3 && (
              <div className="grid gap-4">
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="db-security-card">
                    <ShieldCheck size={24} aria-hidden="true" />
                    <strong>Verified TLS</strong>
                    <span>
                      Required for new databases. Clients verify the database hostname and
                      certificate issuer.
                    </span>
                    <span className="db-engine-tags">
                      <span>Required</span>
                      <span>TLS 1.2+</span>
                    </span>
                  </div>
                  <div className="db-security-card">
                    <LockKeyhole size={24} aria-hidden="true" />
                    <strong>Private networking</strong>
                    <span>
                      Database endpoints stay inside the cluster. Connect authorized applications
                      using database bindings.
                    </span>
                    <span className="db-engine-tags">
                      <span>Private endpoint</span>
                    </span>
                  </div>
                </div>
                <FormSection title="Connection policy">
                  <dl className="db-create-facts">
                    <div>
                      <dt>Encryption</dt>
                      <dd>Native database TLS; plaintext connections rejected</dd>
                    </div>
                    <div>
                      <dt>Client trust</dt>
                      <dd>
                        CA certificate available after deployment; scoped trust for application
                        bindings
                      </dd>
                    </div>
                    <div>
                      <dt>Credentials</dt>
                      <dd>Generated for this database; disclosed only to authorized users</dd>
                    </div>
                    <div>
                      <dt>Public access</dt>
                      <dd>
                        Off ·{' '}
                        {spec.engine === 'postgresql'
                          ? 'review and configure public endpoints after creation'
                          : 'dedicated public endpoints are not available for this engine yet'}
                      </dd>
                    </div>
                  </dl>
                </FormSection>
                {spec.engine === 'postgresql' && (
                  <FormSection
                    title="Connection pooling"
                    description="PgBouncer reuses database connections. Applications select write or replica endpoints explicitly; pooling does not inspect SQL to choose a route."
                  >
                    <fieldset>
                      <legend className="sr-only">Connection pooling</legend>
                      <div className="grid gap-3 sm:grid-cols-2">
                        <button
                          type="button"
                          className="db-layout-card"
                          aria-pressed={!spec.pooling}
                          disabled={busy}
                          onClick={() => update({ pooling: undefined })}
                        >
                          <Server size={20} aria-hidden="true" />
                          <strong>Direct connections</strong>
                          <span>
                            Connect to the database with your application's own connection pool.
                          </span>
                        </button>
                        <button
                          type="button"
                          className="db-layout-card"
                          aria-pressed={Boolean(spec.pooling)}
                          disabled={busy}
                          onClick={() => {
                            if (!spec.pooling)
                              update({
                                pooling: {
                                  mode: 'session',
                                  instances: 2,
                                  max_client_connections: 200,
                                  default_pool_size: 10,
                                  read_only: false,
                                },
                              })
                          }}
                        >
                          <Network size={20} aria-hidden="true" />
                          <strong>Managed PgBouncer</strong>
                          <span>
                            Share a bounded pool of server connections across application clients.
                          </span>
                        </button>
                      </div>
                    </fieldset>
                    {spec.pooling && (
                      <div className="grid gap-4">
                        <div className="grid gap-4 sm:grid-cols-2">
                          <label>
                            Pooling mode
                            <SelectField
                              label="Pooling mode"
                              value={spec.pooling.mode}
                              disabled={busy}
                              options={[
                                { value: 'session', label: 'Session' },
                                { value: 'transaction', label: 'Transaction' },
                              ]}
                              onValueChange={(mode) =>
                                update({
                                  pooling: {
                                    ...spec.pooling!,
                                    mode: mode as 'session' | 'transaction',
                                  },
                                })
                              }
                            />
                            <span className="field-help">
                              {spec.pooling.mode === 'session'
                                ? 'Keeps one server connection for each client session. Supports session state.'
                                : 'Releases the server after each transaction. Session settings, temporary tables across transactions and LISTEN need a direct or session connection.'}
                            </span>
                          </label>
                          <label htmlFor="db-pool-instances">
                            Poolers per endpoint
                            <Input
                              id="db-pool-instances"
                              type="number"
                              min={1}
                              max={3}
                              required
                              disabled={busy}
                              value={spec.pooling.instances}
                              onChange={(event) =>
                                update({
                                  pooling: {
                                    ...spec.pooling!,
                                    instances: Number(event.target.value),
                                  },
                                })
                              }
                            />
                            <span className="field-help">
                              250m CPU and 256Mi memory per instance. One instance has no pooler
                              redundancy.
                            </span>
                          </label>
                          <label htmlFor="db-pool-clients">
                            Client connections per pooler
                            <Input
                              id="db-pool-clients"
                              type="number"
                              min={20}
                              max={2000}
                              required
                              disabled={busy}
                              value={spec.pooling.max_client_connections}
                              onChange={(event) =>
                                update({
                                  pooling: {
                                    ...spec.pooling!,
                                    max_client_connections: Number(event.target.value),
                                  },
                                })
                              }
                            />
                          </label>
                          <label htmlFor="db-pool-servers">
                            Server connections per pooler
                            <Input
                              id="db-pool-servers"
                              type="number"
                              min={1}
                              max={20}
                              required
                              disabled={busy}
                              value={spec.pooling.default_pool_size}
                              onChange={(event) =>
                                update({
                                  pooling: {
                                    ...spec.pooling!,
                                    default_pool_size: Number(event.target.value),
                                  },
                                })
                              }
                            />
                          </label>
                        </div>
                        <label className="db-create-confirm">
                          <input
                            type="checkbox"
                            disabled={busy || !spec.replicas}
                            checked={spec.pooling.read_only}
                            onChange={(event) =>
                              update({
                                pooling: { ...spec.pooling!, read_only: event.target.checked },
                              })
                            }
                          />
                          <span>
                            Add a separate pooled read endpoint
                            {!spec.replicas && ' · requires a replica'}
                          </span>
                        </label>
                        <Note>
                          {poolerInstances(spec)} pooler instances in total. Pooling policy is fixed
                          at creation. Clients must retry after failover; replica reads may lag.
                          Direct endpoints remain available.
                        </Note>
                      </div>
                    )}
                  </FormSection>
                )}
                {spec.engine === 'vitess' && (
                  <FormSection
                    title="Vitess routing and recovery"
                    description="Choose the native destination used for tablet recovery. Sharded databases also require explicit table routing."
                  >
                    <label>
                      Native backup destination
                      <SelectField
                        label="Native backup destination"
                        value={spec.vitess?.backup_destination_id || ''}
                        disabled={busy || backupDestinations.isPending}
                        required
                        options={[
                          { value: '', label: backupDestinations.isPending ? 'Loading destinations…' : 'Choose a destination' },
                          ...vitessDestinations.map((destination) => ({ value: destination.id, label: `${destination.name} · revision ${destination.revision}` })),
                        ]}
                        onValueChange={(backup_destination_id) => {
                          const destination = vitessDestinations.find((item) => item.id === backup_destination_id)
                          update({ vitess: { tables: spec.vitess?.tables || [], backup_destination_id, backup_destination_revision: destination?.revision || 0 } })
                        }}
                      />
                      <span className="field-help">Only destinations assigned to {project} / {environment} are shown. The selected revision is fixed with the database, and the server verifies operator approval before creation.</span>
                    </label>
                    {backupDestinations.error && <FormError focus={false}>Backup destinations could not be loaded. Your other entries are preserved.</FormError>}
                    {!backupDestinations.isPending && !backupDestinations.error && !vitessDestinations.length && (
                      <Note>No native backup destination is available in this project and environment. Create one before continuing; the server will also verify operator approval.</Note>
                    )}
                    {spec.shards > 1 && (
                      <div className="grid gap-3">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <strong className="text-sm">Table routing</strong>
                          <Button type="button" size="sm" disabled={busy || (spec.vitess?.tables?.length || 0) >= 128} onClick={() => update({ vitess: { ...spec.vitess!, tables: [...(spec.vitess?.tables || []), { name: '', sharding_column: '' }] } })}>Add table</Button>
                        </div>
                        <p className="field-help">Add 1–128 tables. Each table uses one integer hash-routing column; names must be distinct SQL identifiers.</p>
                        {(spec.vitess?.tables || []).map((table, index) => (
                          <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]" key={index}>
                            <label>Table name<Input required value={table.name} disabled={busy} maxLength={64} pattern="[A-Za-z_][A-Za-z0-9_]{0,63}" onChange={(event) => update({ vitess: { ...spec.vitess!, tables: spec.vitess!.tables!.map((item, itemIndex) => itemIndex === index ? { ...item, name: event.target.value } : item) } })} /></label>
                            <label>Sharding column<Input required value={table.sharding_column} disabled={busy} maxLength={64} pattern="[A-Za-z_][A-Za-z0-9_]{0,63}" onChange={(event) => update({ vitess: { ...spec.vitess!, tables: spec.vitess!.tables!.map((item, itemIndex) => itemIndex === index ? { ...item, sharding_column: event.target.value } : item) } })} /></label>
                            <Button type="button" size="sm" disabled={busy} aria-label={`Remove ${table.name || `table ${index + 1}`}`} onClick={() => update({ vitess: { ...spec.vitess!, tables: spec.vitess!.tables!.filter((_, itemIndex) => itemIndex !== index) } })}><Trash2 size={14} aria-hidden="true" />Remove</Button>
                          </div>
                        ))}
                      </div>
                    )}
                    <Note>vtgate accepts MySQL TLS on port 3306. {spec.replicas > 0 ? 'Use app@primary for writes and app@replica for replica reads. Replica reads may lag.' : 'Use app@primary for reads and writes.'}</Note>
                  </FormSection>
                )}
                <Note>
                  TLS enforcement is checked against the running database after deployment. {spec.engine === 'vitess' ? 'The native recovery destination is required at creation; exported backup schedules remain separate.' : 'Backup schedules must be configured after creation; replicas do not replace backups.'}
                </Note>
              </div>
            )}

            {step === 4 && (
              <div className="grid gap-4">
                {['clickhouse', 'oracle'].includes(spec.engine) && (
                  <Note>
                    Development preview. Review the current acceptance status before using this
                    database for production workloads.
                  </Note>
                )}
                <FormSection title="Deployment configuration">
                  <dl className="db-create-facts">
                    <div>
                      <dt>Database</dt>
                      <dd>
                        {spec.name} · {engineName(spec.engine)} {spec.version}
                      </dd>
                    </div>
                    <div>
                      <dt>Destination</dt>
                      <dd>
                        {project} / {environment}
                      </dd>
                    </div>
                    <div>
                      <dt>Topology</dt>
                      <dd>{databaseLayoutSummary(spec)}</dd>
                    </div>
                    <div>
                      <dt>Per member</dt>
                      <dd>
                        {spec.cpu} CPU · {spec.memory} memory · {spec.storage_gib} GiB storage
                      </dd>
                    </div>
                    <div>
                      <dt>Total allocation</dt>
                      <dd>
                        {total?.cpu.toLocaleString()} CPU cores ·{' '}
                        {total && (total.memoryMiB / 1024).toLocaleString()} GiB memory ·{' '}
                        {databaseStorageGiB(spec)} GiB storage
                      </dd>
                    </div>
                    <div>
                      <dt>Placement</dt>
                      <dd>
                        {placement} ·{' '}
                        {spec.placement?.node_names?.join(', ') || 'All authorized nodes'}
                      </dd>
                    </div>
                    <div>
                      <dt>Security</dt>
                      <dd>Required TLS · private endpoints · generated credentials</dd>
                    </div>
                    <div>
                      <dt>Connection routing</dt>
                      <dd>
                        {spec.engine === 'vitess'
                          ? `vtgate · ${spec.mode === 'cluster' ? 'app@primary writes and app@replica reads' : 'app@primary reads and writes'} · reviewed table routing`
                          : spec.engine === 'mysql'
                          ? `MySQL Router · ${routerInstances(spec)} instances · ${spec.replicas ? 'write and replica routes' : 'write route'}`
                          : spec.pooling
                            ? `PgBouncer · ${spec.pooling.mode} · ${poolerInstances(spec)} instances · ${spec.pooling.read_only ? 'write and read endpoints' : 'write endpoint'} · ${spec.pooling.max_client_connections} clients and ${spec.pooling.default_pool_size} server connections per pooler`
                            : spec.engine === 'mongodb'
                              ? 'Replica-set discovery · driver selects the primary and read preference'
                              : spec.engine === 'clickhouse' && spec.mode === 'cluster'
                                ? 'Any data member · Distributed tables combine shards'
                                : spec.engine === 'oracle'
                                  ? 'TCPS · APP schema · FREEPDB1 service'
                                  : 'Direct connections'}
                      </dd>
                    </div>
                    <div>
                      <dt>Backups</dt>
                      <dd>{spec.engine === 'vitess' ? `${vitessDestinations.find((item) => item.id === spec.vitess?.backup_destination_id)?.name || 'Native destination required'} · revision ${spec.vitess?.backup_destination_revision || '—'} · exported schedules set up after creation` : 'No schedule configured · set up after creation'}</dd>
                    </div>
                    {spec.engine === 'vitess' && <div><dt>Table routing</dt><dd>{spec.shards > 1 ? `${spec.vitess?.tables?.length || 0} reviewed table entries` : 'Single shard · no table routing entries'}</dd></div>}
                  </dl>
                </FormSection>
                <Note>
                  Creating this database allocates persistent storage and compute. Capacity and
                  permissions are checked when the request is submitted.
                </Note>
                <label className="db-create-confirm">
                  <input
                    type="checkbox"
                    checked={confirmed}
                    disabled={busy}
                    onChange={(event) => setConfirmed(event.target.checked)}
                  />
                  <span>I have reviewed the topology, resources and connection policy.</span>
                </label>
              </div>
            )}

            {(error || (attempted && issue)) && (
              <FormError className="db-create-error text-destructive">{error || issue}</FormError>
            )}
          </div>

          <div className="db-create-footer">
            <Button asChild disabled={busy}>
              <Link
                to="/databases"
                search={{ project, environment }}
                aria-disabled={busy}
                onClick={(event) => {
                  if (busy) event.preventDefault()
                }}
              >
                Cancel
              </Link>
            </Button>
            <span className="db-create-footer-progress" aria-live="polite">
              {step + 1} / 5 · {databaseCreateSteps[step]}
            </span>
            <div className="flex flex-wrap gap-2">
              {step > 0 && (
                <Button type="button" disabled={busy} onClick={() => move(step - 1)}>
                  <ArrowLeft size={15} aria-hidden="true" />
                  Back
                </Button>
              )}
              <Button type="submit" variant="primary" disabled={busy || (step === 4 && !confirmed)}>
                {busy
                  ? 'Creating…'
                  : step === 4
                    ? 'Create database'
                    : step === 3
                      ? 'Review deployment'
                      : 'Continue'}
                {step === 4 ? (
                  <CheckCheck size={15} aria-hidden="true" />
                ) : (
                  <ArrowRight size={15} aria-hidden="true" />
                )}
              </Button>
            </div>
          </div>

          <aside className="db-create-summary" aria-label="Deployment summary">
            <div className="db-create-summary-title">
              <h2>Deployment summary</h2>
              <HeadingHelp title="deployment summary">
                This is your requested configuration. Running members and health appear after the
                database is created. CPU and memory totals include required sidecars, routers,
                poolers and Keeper members. Storage includes backup-staging and log volumes.
                Additional operating and recovery headroom is reserved when capacity is checked.
              </HeadingHelp>
            </div>
            <div className="db-create-summary-identity">
              <ServiceIcon name={spec.engine} size={32} />
              <div>
                <strong>{spec.name || 'Untitled database'}</strong>
                <span>
                  {engineName(spec.engine)} {spec.version}
                </span>
              </div>
            </div>
            <div
              className="db-create-topology"
              aria-label={`Requested topology: ${members} ${members === 1 ? 'member' : 'members'}`}
            >
              <span className="db-create-topology-label">Requested topology</span>
              <div className="db-create-member">
                <Server size={20} aria-hidden="true" />
                <span>
                  {spec.engine === 'vitess'
                    ? `${spec.shards} ${spec.shards === 1 ? 'shard' : 'shards'} · ${spec.replicas + 1} ${spec.replicas === 0 ? 'tablet' : 'tablets'} each`
                    : spec.engine === 'clickhouse' && spec.mode === 'cluster'
                    ? `${spec.shards} shards · ${spec.replicas + 1} copies each`
                    : spec.engine === 'redis' && spec.mode === 'cluster'
                      ? `${spec.shards} primary shards`
                      : 'Primary'}
                </span>
              </div>
              {spec.mode === 'cluster' && spec.engine !== 'clickhouse' && (
                <>
                  <div className="db-create-connector" />
                  <div className="db-create-replicas">
                    {Array.from({ length: Math.min(6, spec.replicas) }, (_, i) => (
                      <div className="db-create-member" key={i}>
                        <Layers3 size={17} aria-hidden="true" />
                        <span>
                          {spec.engine === 'redis'
                            ? `Replica ${i + 1} per shard`
                            : `Replica ${i + 1}`}
                        </span>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>
            {keeperInstances(spec) > 0 && (
              <p className="field-help">
                Three Keeper members coordinate replication. They do not store application tables.
              </p>
            )}
            {spec.engine === 'vitess' && (
              <p className="field-help">Applications connect through vtgate. Three etcd members hold routing metadata; table data remains on the tablets.</p>
            )}
            {spec.engine === 'mongodb' && (
              <p className="field-help">
                Each member includes a 100m CPU / 256Mi agent and a separate 1 GiB log volume.{' '}
                {spec.mode === 'cluster'
                  ? `${members} voting members, including ${spec.replicas} replicas. Writes and primary election require ${Math.floor(members / 2) + 1} connected members.`
                  : 'Standalone uses a one-member replica set without failover.'}
              </p>
            )}
            <dl className="db-create-summary-facts">
              <div>
                <dt>
                  <Network size={14} aria-hidden="true" /> Members
                </dt>
                <dd>{members}</dd>
              </div>
              {spec.engine === 'mysql' && (
                <div>
                  <dt>
                    <Network size={14} aria-hidden="true" /> Routers
                  </dt>
                  <dd>{routerInstances(spec)}</dd>
                </div>
              )}
              {keeperInstances(spec) > 0 && (
                <div>
                  <dt>
                    <Network size={14} aria-hidden="true" /> Keeper members
                  </dt>
                  <dd>{keeperInstances(spec)}</dd>
                </div>
              )}
              {spec.pooling && (
                <div>
                  <dt>
                    <Network size={14} aria-hidden="true" /> Poolers
                  </dt>
                  <dd>{poolerInstances(spec)}</dd>
                </div>
              )}
              <div>
                <dt>
                  <Cpu size={14} aria-hidden="true" /> Total CPU
                </dt>
                <dd>
                  {total
                    ? `${total.cpu.toLocaleString()} ${total.cpu === 1 ? 'core' : 'cores'}`
                    : '—'}
                </dd>
              </div>
              <div>
                <dt>
                  <MemoryStick size={14} aria-hidden="true" /> Total memory
                </dt>
                <dd>{total ? `${(total.memoryMiB / 1024).toLocaleString()} GiB` : '—'}</dd>
              </div>
              <div>
                <dt>
                  <HardDrive size={14} aria-hidden="true" /> Total storage
                </dt>
                <dd>{databaseStorageGiB(spec)} GiB</dd>
              </div>
            </dl>
            <div className="db-create-summary-security">
              <LockKeyhole size={14} aria-hidden="true" />
              <span>Private networking</span>
              <ShieldCheck size={14} aria-hidden="true" />
              <span>TLS required</span>
            </div>
          </aside>
        </div>
      </form>
    </FormPage>
  )
}
