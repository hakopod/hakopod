import { useRef, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { databaseSearch } from '../lib/databases'
import { useBackupDestinations, byteSize } from '../lib/backups'
import { canAccess, useScope } from '../lib/scope'
import { client, unwrap } from '../lib/client'
import { editionFetch } from '../lib/client-edition'
import { message, timestamp } from '../lib/api'
import type { components } from '../lib/api.generated'
import { FormPage, FormSection } from '../components/form-page'
import { Empty, ErrorState, Loading, Note } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'

export const Route = createFileRoute('/databases/import')({
  validateSearch: (
    search: Record<string, unknown>,
  ): { project: string; environment: string; import_id?: string } => ({
    ...databaseSearch(search),
    ...(typeof search.import_id === 'string' && /^[a-f0-9]{32}$/.test(search.import_id)
      ? { import_id: search.import_id }
      : {}),
  }),
  component: Page,
})
const browserLimit = 64 * 1024 * 1024
function Page() {
  const scope = Route.useSearch()
  return scope.import_id ? (
    <ResumeImport
      key={`${scope.project}/${scope.environment}/${scope.import_id}`}
      scope={scope}
      id={scope.import_id}
    />
  ) : (
    <ImportPage key={`${scope.project}/${scope.environment}`} scope={scope} />
  )
}
type ImportReview = components['schemas']['DatabaseImport']
function ResumeImport({
  scope,
  id,
}: {
  scope: { project: string; environment: string }
  id: string
}) {
  const destinations=useBackupDestinations()
  const query = useQuery({
    queryKey: ['database-import', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/backup-imports/{id}', { signal, params: { path: { id } } })),
    enabled: !!scope.project && !!scope.environment,
    gcTime: 0,
  })
  const artifactId = query.data?.artifact_id
  const stored = useQuery({
    queryKey: ['backup-artifact', artifactId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/backup-artifacts/{id}', { signal, params: { path: { id: artifactId! } } }),
      ),
    enabled: query.data?.status === 'completed' && !!artifactId,
    gcTime: 0,
  })
  if (!scope.project || !scope.environment) return <ImportPage scope={scope} />
  if (query.isPending || destinations.isPending || (query.data?.status === 'completed' && stored.isPending))
    return (
      <FormPage
        title="Import Docker backup"
        description="Continue an existing reviewed import."
        breadcrumbs={[]}
      >
        <Loading />
      </FormPage>
    )
  if (query.error || destinations.error || !query.data || (query.data.status === 'completed' && stored.error))
    return (
      <FormPage
        title="Import Docker backup"
        description="Continue an existing reviewed import."
        breadcrumbs={[]}
      >
        <ErrorState error={query.error || destinations.error || stored.error} />
      </FormPage>
    )
  return <ImportPage key={id} scope={scope} initial={query.data} initialArtifact={stored.data} />
}
function ImportPage({
  scope,
  initial,
  initialArtifact,
}: {
  scope: { project: string; environment: string }
  initial?: ImportReview
  initialArtifact?: components['schemas']['BackupArtifact']
}) {
  const { identity } = useScope()
  const destinations = useBackupDestinations()
  const cache = useQueryClient()
  const [destination, setDestination] = useState(initial?.spec.destination_id || '')
  const [name, setName] = useState(initial?.spec.source_name || '')
  const [engine, setEngine] = useState<'postgresql' | 'redis'>(
    initial?.spec.engine === 'redis' ? 'redis' : 'postgresql',
  )
  const [version, setVersion] = useState(initial?.spec.source_version || '17')
  const [captured, setCaptured] = useState(() => {
    if (!initial) return ''
    const d = new Date(initial.spec.captured_at)
    return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 19)
  })
  const [file, setFile] = useState<File | null>(null)
  const [review, setReview] = useState<components['schemas']['DatabaseImport'] | null>(
    initial || null,
  )
  const [artifact, setArtifact] = useState<components['schemas']['BackupArtifact'] | null>(
    initialArtifact || null,
  )
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const key = useRef('')
  const reset = () => {
    setReview(null)
    setConfirmed(false)
    key.current = ''
  }
  const eligible =
    destinations.data?.items.filter(
      (d) => !d.project || (d.project === scope.project && d.environment === scope.environment),
    ) || []
  const expired = !!review && Date.parse(review.expires_at) <= Date.now()
  if (!scope.project || !scope.environment)
    return (
      <FormPage
        title="Import Docker backup"
        description="Import a verified archive into a separate managed database."
        breadcrumbs={[]}
      >
        <Empty
          title="Choose a project and environment"
          description="Open Import from your project's Databases page."
        />
      </FormPage>
    )
  if (identity.application || !canAccess(identity, scope.project, 'deployments:write'))
    return (
      <FormPage
        title="Import Docker backup"
        description="Import a verified archive into a separate managed database."
        breadcrumbs={[]}
      >
        <Note>Import requires project deployment permission.</Note>
      </FormPage>
    )
  return (
    <FormPage
      title="Import Docker backup"
      description="Upload an existing PostgreSQL custom dump or standalone Redis RDB archive. Your Docker application remains unchanged."
      breadcrumbs={[]}
    >
      {artifact ? (
        <FormSection title="Archive verified">
          <p role="status">
            {name} · {byteSize(artifact.bytes)} stored and verified.
          </p>
          <p>Recovery point: {timestamp(artifact.captured_at)}</p>
          <p>
            Create a separate database, choose this archive under Recover, then inspect the
            recovered data before replacing an application's saved connection.
          </p>
          <Note>
            The capture time is your attestation. Changes on Docker after this point require a fresh
            capture before final cutover.
          </Note>
          <div className="flex flex-wrap gap-2">
            <Button asChild variant="primary">
              <Link to="/databases/new" search={scope}>
                Create recovery target
              </Link>
            </Button>
            <Button asChild>
              <Link to="/databases" search={scope}>
                Databases
              </Link>
            </Button>
          </div>
        </FormSection>
      ) : (
        <form
          onSubmit={async (e) => {
            e.preventDefault()
            if (busy || !file) return
            setError('')
            try {
              if (file.size < 16 || file.size > browserLimit)
                throw new Error(
                  'Choose an archive up to 64 MiB. Use the CLI for archives up to 2 GiB.',
                )
              if (!review) {
                setBusy('Checking archive checksum…')
                const digest = await crypto.subtle.digest('SHA-256', await file.arrayBuffer())
                const sha256 = Array.from(new Uint8Array(digest), (b) =>
                  b.toString(16).padStart(2, '0'),
                ).join('')
                if (!key.current) key.current = crypto.randomUUID()
                setReview(
                  await unwrap(
                    client.POST('/backup-imports', {
                      params: { header: { 'Idempotency-Key': key.current } },
                      body: {
                        destination_id: destination,
                        source_name: name,
                        engine,
                        source_version: version,
                        captured_at: new Date(captured).toISOString(),
                        bytes: file.size,
                        sha256,
                      },
                    }),
                  ),
                )
              } else {
                if (!confirmed || expired)
                  throw new Error(
                    'Review the recovery point and confirm the import before uploading.',
                  )
                if (!eligible.some((d) => d.id === review.spec.destination_id))
                  throw new Error('The reviewed destination is not available in this scope.')
                setBusy('Checking reviewed archive…')
                const digest = await crypto.subtle.digest('SHA-256', await file.arrayBuffer())
                const checksum = Array.from(new Uint8Array(digest), (b) =>
                  b.toString(16).padStart(2, '0'),
                ).join('')
                if (file.size !== review.spec.bytes || checksum !== review.spec.sha256)
                  throw new Error(
                    'Choose the exact archive from the approved review; size or checksum differs.',
                  )
                setBusy('Uploading and verifying archive…')
                const response = await editionFetch(`/api/backup-imports/${review.id}/archive`, {
                  method: 'PUT',
                  headers: { 'Content-Type': 'application/octet-stream' },
                  body: file,
                  signal: AbortSignal.timeout(15 * 60 * 1000),
                })
                const body = await response.json()
                if (!response.ok)
                  throw new Error(
                    body.error?.message ||
                      'Archive upload failed. Your inputs are preserved; retry the same review.',
                  )
                setArtifact(body)
                void cache.invalidateQueries({ queryKey: ['backup-artifacts'] })
              }
            } catch (err) {
              setError(message(err))
            } finally {
              setBusy('')
            }
          }}
        >
          <FormSection title="Archive">
            {initial && (
              <Note>
                Continuing a saved review. Choose the same archive file; its size and checksum must
                match. Editing the metadata requires a new review.
              </Note>
            )}
            <Note>
              Export PostgreSQL 17 or 18 with pg_dump -Fc, or upload a standalone Redis 8 RDB file.
              The browser accepts up to 64 MiB; the CLI accepts up to 2 GiB.
            </Note>
            {destinations.isPending ? (
              <Loading />
            ) : destinations.error ? (
              <ErrorState error={destinations.error} />
            ) : !eligible.length ? (
              <Empty
                title="Configure a backup destination"
                description="Imports need an eligible S3 destination with encryption."
                action={
                  <Button asChild>
                    <Link to="/backups/destinations/new">Add destination</Link>
                  </Button>
                }
              />
            ) : null}
            <fieldset disabled={!!busy} className="grid min-w-0 grid-cols-1 gap-4 sm:grid-cols-2">
              <label className="min-w-0">
                S3 destination
                <SelectField
                  label="S3 destination"
                  value={destination}
                  required
                  onValueChange={(v) => {
                    setDestination(v)
                    reset()
                  }}
                  options={[
                    { value: '', label: 'Choose destination' },
                    ...eligible.map((d) => ({ value: d.id, label: d.name })),
                  ]}
                />
              </label>
              <label className="min-w-0">
                Source name
                <Input
                  required
                  value={name}
                  pattern="[a-z][a-z0-9\-]{0,62}"
                  maxLength={63}
                  onChange={(e) => {
                    setName(e.target.value)
                    reset()
                  }}
                />
              </label>
              <label className="min-w-0">
                Engine
                <SelectField
                  label="Engine"
                  value={engine}
                  onValueChange={(v) => {
                    setEngine(v as 'postgresql' | 'redis')
                    setVersion(v === 'redis' ? '8' : '17')
                    reset()
                  }}
                  options={[
                    { value: 'postgresql', label: 'PostgreSQL' },
                    { value: 'redis', label: 'Redis' },
                  ]}
                />
              </label>
              <label className="min-w-0">
                Source version
                <SelectField
                  label="Source version"
                  value={version}
                  onValueChange={(v) => {
                    setVersion(v)
                    reset()
                  }}
                  options={(engine === 'redis' ? ['8'] : ['17', '18']).map((v) => ({
                    value: v,
                    label: v,
                  }))}
                />
              </label>
              <label className="min-w-0">
                Captured recovery point
                <Input
                  type="datetime-local"
                  step="1"
                  required
                  value={captured}
                  onChange={(e) => {
                    setCaptured(e.target.value)
                    reset()
                  }}
                />
                <span className="text-sm text-muted-foreground">
                  Use the time the archive was captured, in your local time zone.
                </span>
              </label>
              <label className="min-w-0">
                Archive file
                <Input
                  type="file"
                  className="min-w-0 w-full"
                  required
                  onChange={(e) => {
                    setFile(e.target.files?.[0] || null)
                    if (!initial || !review) reset()
                    else setConfirmed(false)
                  }}
                />
              </label>
            </fieldset>
          </FormSection>
          {review && (
            <FormSection title="Review import">
              <p>
                {name} · {engine === 'postgresql' ? 'PostgreSQL' : 'Redis'} {version} ·{' '}
                {byteSize(review.spec.bytes)}
              </p>
              <p>
                Recovery point: {timestamp(review.spec.captured_at)} · Review expires{' '}
                {timestamp(review.expires_at)}
              </p>
              <p className="break-all">SHA-256: {review.spec.sha256}</p>
              <Note>
                This uploads an encrypted archive. Your source Docker application stays available.
                Changes after the recovery point need a new capture before final cutover.
              </Note>
              {engine === 'redis' && (
                <Note>
                  Redis archives preserve values and expiry. Cluster recovery distributes the
                  standalone archive across the target's shards.
                </Note>
              )}
              <label className="flex min-h-11 items-center gap-2">
                <input
                  type="checkbox"
                  checked={confirmed}
                  disabled={!!busy}
                  onChange={(e) => setConfirmed(e.target.checked)}
                />
                I confirm the source version and captured recovery point.
              </label>
              {expired && (
                <Note>This review expired. Choose Edit review, then review the import again.</Note>
              )}
            </FormSection>
          )}
          {error && (
            <p role="alert" className="form-error">
              {error}
            </p>
          )}
          {busy && <p role="status">{busy}</p>}
          <div className="flex flex-wrap gap-2 py-4">
            <Button
              type="submit"
              variant="primary"
              disabled={!!busy || !file || !destination || (!!review && (!confirmed || expired))}
            >
              {review ? 'Upload and verify' : 'Review import'}
            </Button>
            {review && (
              <Button type="button" disabled={!!busy} onClick={reset}>
                Edit review
              </Button>
            )}
            <Button asChild>
              <Link to="/databases" search={scope}>
                Back to databases
              </Link>
            </Button>
          </div>
        </form>
      )}
    </FormPage>
  )
}
