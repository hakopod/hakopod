import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { FormError, FormPage, FormSection } from './form-page'
import { Empty, ErrorState, Loading, Note, Status } from './shared'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { message, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useBackupDestinations } from '../lib/backups'
import { canAccess, useResourceScope, useScope } from '../lib/scope'
import { CompatibilityReport, type CompatibilityReportValue } from './compatibility-report'
import {
  managedPlatformName,
  useManagedPlatform,
  useManagedPlatformRecoveryOperations,
  useManagedPlatforms,
  type ManagedPlatformRecoveryOperation,
  type ManagedPlatformRecoveryRequest,
  type ManagedPlatformRecoveryReview,
} from '../lib/managed-platforms'

export function PlatformRecoveryForm({ id, kind }: { id: string; kind: 'backup' | 'restore' }) {
  const source = useManagedPlatform(id)
  useResourceScope(source.data)
  const { identity } = useScope()
  const canManage = Boolean(
    source.data &&
    !identity.application &&
    canAccess(identity, source.data.project, 'deployments:write'),
  )
  const destinations = useBackupDestinations(canManage && kind === 'backup')
  const platforms = useManagedPlatforms(
    source.data?.project || '',
    source.data?.environment || '',
    canManage && kind === 'restore',
  )
  const operations = useManagedPlatformRecoveryOperations(id, canManage && kind === 'restore')
  const [destinationID, setDestinationID] = useState('')
  const [targetID, setTargetID] = useState('')
  const [artifactID, setArtifactID] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [review, setReview] = useState<ManagedPlatformRecoveryReview | null>(null)
  const [reviewRequest, setReviewRequest] = useState<ManagedPlatformRecoveryRequest | null>(null)
  const [confirmedBackup, setConfirmedBackup] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [, refreshExpiry] = useState(0)
  const mutation = useRef(false)
  const idempotency = useRef('')
  const navigate = useNavigate()
  const cache = useQueryClient()
  useEffect(() => {
    const deadline = Date.parse(review?.expires_at || '')
    if (!Number.isFinite(deadline)) return
    const timer = window.setTimeout(
      () => {
        refreshExpiry((value) => value + 1)
        setConfirmation('')
        setConfirmedBackup(false)
      },
      Math.min(Math.max(deadline - Date.now(), 0), 2_147_483_647),
    )
    return () => window.clearTimeout(timer)
  }, [review?.expires_at])
  const title = kind === 'backup' ? 'Back up platform' : 'Restore platform'
  const frame = (content: React.ReactNode) => (
    <FormPage
      title={title}
      description="Review the source, archive and destination before starting the operation."
      breadcrumbs={[]}
    >
      {content}
    </FormPage>
  )
  if (source.isPending) return frame(<Loading />)
  if (source.error || !source.data)
    return frame(<ErrorState error={source.error || new Error('Platform unavailable')} />)
  if (!canManage)
    return frame(<Note>Platform recovery requires deployment permission in this project.</Note>)
  const item = source.data
  const scope = { project: item.project, environment: item.environment }
  const back = (
    <Button asChild>
      <Link
        to="/platforms/$platformId"
        params={{ platformId: id }}
        search={{ ...scope, tab: 'recovery' }}
      >
        Back to platform
      </Link>
    </Button>
  )
  if (item.status !== 'ready')
    return frame(
      <Empty
        title="Platform must be ready"
        description="Wait for the current operation to finish before starting a backup or restore."
        action={back}
      />,
    )
  if (
    (kind === 'backup' && destinations.isPending) ||
    (kind === 'restore' && (platforms.isPending || operations.isPending))
  )
    return frame(<Loading />)
  const loadError = kind === 'backup' ? destinations.error : platforms.error || operations.error
  if (loadError) return frame(<ErrorState error={loadError} />)
  const eligibleDestinations =
    destinations.data?.items.filter(
      (value) =>
        value.project === item.project &&
        value.environment === item.environment &&
        Boolean(value.encryption_recipient),
    ) || []
  const targets =
    platforms.data?.items.filter(
      (value) =>
        value.id !== id &&
        value.project === item.project &&
        value.environment === item.environment &&
        value.spec.kind === item.spec.kind &&
        value.status === 'ready',
    ) || []
  const archives =
    operations.data?.items.filter(
      (value) =>
        value.kind === 'backup' &&
        value.status === 'succeeded' &&
        value.result_artifact_id &&
        value.expected_source_revision === item.revision,
    ) || []
  const target = targets.find((value) => value.id === targetID)
  const destination = eligibleDestinations.find((value) => value.id === destinationID)
  const expired = Boolean(
    review &&
    (!Number.isFinite(Date.parse(review.expires_at)) ||
      Date.parse(review.expires_at) <= Date.now()),
  )
  const changed = Boolean(
    review &&
    (review.intent.expected_source_revision !== item.revision ||
      (kind === 'restore' && review.intent.expected_target_revision !== target?.revision) ||
      (kind === 'backup' && review.intent.destination_revision !== destination?.revision)),
  )
  const compatibilityBlocked = Boolean((review as unknown as { compatibility?: CompatibilityReportValue } | null)?.compatibility?.blocked)
  const reset = () => {
    setReview(null)
    setReviewRequest(null)
    setError('')
    setConfirmation('')
    setConfirmedBackup(false)
    idempotency.current = ''
  }
  async function submit() {
    if (mutation.current) return
    mutation.current = true
    setBusy(true)
    setError('')
    try {
      if (!review || expired || changed) {
        const request: ManagedPlatformRecoveryRequest = {
          kind,
          ...scope,
          source_platform_id: id,
          expected_source_revision: item.revision,
        }
        if (kind === 'backup') {
          if (!destination) throw new Error('Choose a configured encrypted backup destination.')
          request.destination_id = destination.id
          request.destination_revision = destination.revision
        } else {
          if (!target || !archives.some((value) => value.result_artifact_id === artifactID))
            throw new Error('Choose a completed archive and a separate recovery target.')
          request.target_platform_id = target.id
          request.expected_target_revision = target.revision
          request.artifact_id = artifactID
          request.confirm_target_name = target.spec.name
        }
        const next = await unwrap(
          client.POST('/managed-platform-recovery/reviews', { body: request }),
        )
        setReview(next)
        setReviewRequest(request)
        setConfirmation('')
        setConfirmedBackup(false)
        idempotency.current = ''
        return
      }
      if (
        !reviewRequest ||
        (kind === 'restore' && confirmation !== target?.spec.name) ||
        (kind === 'backup' && !confirmedBackup)
      )
        throw new Error('Confirm this review before proceeding.')
      if (!idempotency.current) idempotency.current = crypto.randomUUID()
      await unwrap(
        client.POST('/managed-platform-recovery/operations', {
          body: { ...reviewRequest, review },
          params: { header: { 'Idempotency-Key': idempotency.current } },
        }),
      )
      void cache.invalidateQueries({ queryKey: ['managed-platform-recovery-operations', id] })
      await navigate({
        to: '/platforms/$platformId',
        params: { platformId: id },
        search: { ...scope, tab: 'recovery' },
      })
    } catch (value) {
      setError(message(value))
    } finally {
      mutation.current = false
      setBusy(false)
    }
  }
  return frame(
    <form
      onSubmit={(event) => {
        event.preventDefault()
        void submit()
      }}
    >
      <FormSection title="Source">
        <p className="text-sm break-all">
          {item.spec.name} · {managedPlatformName(item.spec.kind)} · revision {item.revision}
        </p>
        <p className="mt-2 text-xs text-muted-foreground break-all">
          {item.project} / {item.environment}
        </p>
      </FormSection>
      {!review && (
        <FormSection title={kind === 'backup' ? 'Backup destination' : 'Archive and target'}>
          {kind === 'backup' ? (
            <>
              <label>
                Encrypted destination
                <SelectField
                  label="Encrypted destination"
                  required
                  value={destinationID}
                  disabled={busy}
                  onValueChange={(value) => {
                    setDestinationID(value)
                    reset()
                  }}
                  options={[
                    { value: '', label: 'Choose a destination' },
                    ...eligibleDestinations.map((value) => ({
                      value: value.id,
                      label: value.name,
                    })),
                  ]}
                />
              </label>
              {!eligibleDestinations.length && (
                <Note>
                  No encrypted backup destination is configured for this project and environment.
                </Note>
              )}
              <Note>The platform briefly pauses writes while it captures a consistent backup.</Note>
            </>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              <label>
                Archive
                <SelectField
                  label="Archive"
                  required
                  value={artifactID}
                  disabled={busy}
                  onValueChange={(value) => {
                    setArtifactID(value)
                    reset()
                  }}
                  options={[
                    { value: '', label: 'Choose a completed backup' },
                    ...archives.map((value) => ({
                      value: value.result_artifact_id!,
                      label: `${value.result_artifact_id} · revision ${value.expected_source_revision}`,
                    })),
                  ]}
                />
              </label>
              <label>
                Separate recovery target
                <SelectField
                  label="Separate recovery target"
                  required
                  value={targetID}
                  disabled={busy}
                  onValueChange={(value) => {
                    setTargetID(value)
                    reset()
                  }}
                  options={[
                    { value: '', label: 'Choose an empty platform' },
                    ...targets.map((value) => ({
                      value: value.id,
                      label: `${value.spec.name} · revision ${value.revision}`,
                    })),
                  ]}
                />
              </label>
              <div className="sm:col-span-2">
                <Note>
                  The server checks that the target is empty and compatible. Recovery leaves the
                  source in place and does not change application connections.
                </Note>
                {!archives.length && (
                  <Note>No completed backup is available for this source revision.</Note>
                )}
                {!targets.length && (
                  <Button asChild>
                    <Link to="/platforms/new" search={scope}>
                      Create recovery target
                    </Link>
                  </Button>
                )}
              </div>
            </div>
          )}
        </FormSection>
      )}
      {review && (
        <FormSection title="Review">
          <div className="grid gap-3 text-sm">
            <p className="break-all">
              {kind === 'backup'
                ? `Destination: ${destination?.name || destinationID}`
                : `Target: ${target?.spec.name || targetID}`}
            </p>
            {kind === 'restore' && <p className="break-all">Archive: {artifactID}</p>}
            <p>Review expires {timestamp(review.expires_at)}.</p>
            <CompatibilityReport value={(review as unknown as { compatibility?: CompatibilityReportValue }).compatibility} />
            {expired && <Note>This review has expired. Refresh it before continuing.</Note>}
            {changed && (
              <Note>The source, target or destination changed. Refresh this review.</Note>
            )}
            {kind === 'backup' ? (
              <label className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={confirmedBackup}
                  disabled={busy || expired || changed}
                  onChange={(event) => setConfirmedBackup(event.target.checked)}
                />
                <span>Pause writes briefly and create this backup.</span>
              </label>
            ) : (
              <label>
                Type {target?.spec.name} to confirm the recovery target
                <Input
                  required
                  autoComplete="off"
                  value={confirmation}
                  disabled={busy || expired || changed}
                  onChange={(event) => setConfirmation(event.target.value)}
                />
              </label>
            )}
            <div>
              <Button type="button" disabled={busy} onClick={reset}>
                Edit or refresh review
              </Button>
            </div>
          </div>
        </FormSection>
      )}
      {error && <FormError>{error}</FormError>}
      <div className="form-footer">
        {back}
        <Button
          type="submit"
          variant="primary"
          disabled={
            busy ||
            expired ||
            changed ||
            compatibilityBlocked ||
            Boolean(
              review && (kind === 'backup' ? !confirmedBackup : confirmation !== target?.spec.name),
            )
          }
        >
          {busy
            ? 'Working…'
            : review
              ? kind === 'backup'
                ? 'Start backup'
                : 'Start recovery'
              : 'Review'}
        </Button>
      </div>
    </form>,
  )
}

export function PlatformRecoveryHistory({ id, canManage }: { id: string; canManage: boolean }) {
  const query = useManagedPlatformRecoveryOperations(id)
  if (query.isPending) return <Loading />
  if (query.error && !query.data) return <ErrorState error={query.error} />
  return (
    <>
      {query.error && (
        <Note>Recovery history could not be refreshed. Showing the last received operations.</Note>
      )}
      {!query.data?.items.length ? (
        <Empty
          title="No recovery operations"
          description="Backups and restores appear here after the server accepts them."
        />
      ) : (
        <ul className="divide-y divide-border">
          {query.data.items.map((operation) => (
            <RecoveryOperation
              key={operation.id}
              operation={operation}
              canManage={canManage}
              refresh={() => void query.refetch()}
            />
          ))}
        </ul>
      )}
    </>
  )
}

function RecoveryOperation({
  operation,
  canManage,
  refresh,
}: {
  operation: ManagedPlatformRecoveryOperation
  canManage: boolean
  refresh: () => void
}) {
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const locked = useRef(false)
  const active = !['succeeded', 'failed', 'cancelled'].includes(operation.status)
  async function cancel() {
    if (locked.current) return
    locked.current = true
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.POST('/managed-platform-recovery-operations/{id}/cancel', {
          params: { path: { id: operation.id } },
        }),
      )
      setConfirming(false)
      refresh()
    } catch (value) {
      setError(message(value))
    } finally {
      locked.current = false
      setBusy(false)
    }
  }
  return (
    <li className="min-w-0 py-4">
      <div className="flex flex-wrap items-center gap-2">
        <Status value={operation.status} />
        <strong className="text-sm font-medium">
          {operation.kind === 'backup' ? 'Backup' : 'Restore'}
        </strong>
        <span className="text-xs text-muted-foreground">
          Source revision {operation.expected_source_revision}
        </span>
        {canManage && active && !operation.cancel_requested && (
          <Button
            type="button"
            size="sm"
            className="ml-auto"
            onClick={() => setConfirming((value) => !value)}
            disabled={busy}
          >
            Cancel operation
          </Button>
        )}
      </div>
      <p className="mt-2 text-sm break-all">{operation.phase || 'Waiting to start'}</p>
      {operation.message && <p className="mt-1 text-sm break-all">{operation.message}</p>}
      {operation.cancel_requested && active && (
        <Note>Cancellation requested. Cleanup must finish before the operation stops.</Note>
      )}
      <details className="mt-2 text-xs text-muted-foreground">
        <summary className="cursor-pointer py-2">Operation details</summary>
        <dl className="grid gap-2 py-2">
          <div>
            <dt>ID</dt>
            <dd className="break-all font-mono">{operation.id}</dd>
          </div>
          {operation.target_platform_id && (
            <div>
              <dt>Target</dt>
              <dd>
                <Link
                  to="/platforms/$platformId"
                  params={{ platformId: operation.target_platform_id }}
                  search={{ project: operation.project, environment: operation.environment }}
                  className="break-all underline"
                >
                  {operation.target_platform_id}
                </Link>
              </dd>
            </div>
          )}
          {(operation.result_artifact_id || operation.artifact_id) && (
            <div>
              <dt>Archive</dt>
              <dd className="break-all font-mono">
                {operation.result_artifact_id || operation.artifact_id}
              </dd>
            </div>
          )}
        </dl>
      </details>
      {confirming && active && !operation.cancel_requested && (
        <div className="mt-3 rounded border border-border p-3">
          <p className="mb-3 text-sm">
            Stop this {operation.kind} after its current work reaches a safe cleanup point?
          </p>
          <div className="flex flex-wrap gap-2">
            <Button type="button" disabled={busy} onClick={() => setConfirming(false)}>
              Keep running
            </Button>
            <Button type="button" disabled={busy} onClick={() => void cancel()}>
              {busy ? 'Requesting…' : 'Request cancellation'}
            </Button>
          </div>
        </div>
      )}
      {error && <FormError>{error}</FormError>}
    </li>
  )
}
