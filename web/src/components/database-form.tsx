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
import { FormPage, FormSection } from './form-page'
import { Note } from './shared'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'

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
}
export function DatabaseForm({
  project,
  environment,
  database,
}: {
  project: string
  environment: string
  database?: ManagedDatabase
}) {
  const [spec, setSpec] = useState<DatabaseSpec>(database?.spec || initial)
  const [review, setReview] = useState<{ id: string; plan?: DatabaseResizeReview } | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const key = useRef('')
  const navigate = useNavigate()
  const cache = useQueryClient()
  const { identity } = useScope()
  const canManage = !identity.application && canAccess(identity, project, 'deployments:write')
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
  return (
    <FormPage
      title={database ? 'Resize database' : 'New database'}
      description="Database resources are independent of application replicas. Review the allocation before creating or resizing."
      breadcrumbs={[]}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy) return
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
                ]}
                onValueChange={(v) =>
                  update({
                    engine: v as DatabaseSpec['engine'],
                    version: v === 'redis' ? '8' : '17',
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
                options={(spec.engine === 'redis' ? ['8'] : ['17', '18']).map((value) => ({
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
                    replicas: v === 'cluster' ? 1 : 0,
                    shards: v === 'cluster' && spec.engine === 'redis' ? 3 : 1,
                  })
                }
              />
            </label>
            {spec.mode === 'cluster' && (
              <label>
                {spec.engine === 'redis' ? 'Replicas per shard' : 'Replicas'}
                <Input
                  type="number"
                  min={1}
                  max={spec.engine === 'redis' ? 2 : 4}
                  required
                  value={spec.replicas}
                  disabled={busy}
                  onChange={(e) => update({ replicas: Number(e.target.value) })}
                />
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
            <label>
              CPU per member
              <Input
                required
                value={spec.cpu}
                disabled={busy}
                onChange={(e) => update({ cpu: e.target.value })}
              />
              <span className="field-help">100m to 16 cores; 1000m is one core.</span>
            </label>
            <label>
              Memory per member
              <Input
                required
                value={spec.memory}
                disabled={busy}
                onChange={(e) => update({ memory: e.target.value })}
              />
              <span className="field-help">128Mi to 64Gi.</span>
            </label>
            <label>
              Storage per member (GiB)
              <Input
                type="number"
                required
                min={database?.spec.storage_gib || 1}
                max={1024}
                value={spec.storage_gib}
                disabled={busy}
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
            <p>{databaseSummary(spec)}</p>
            <p>
              {spec.shards * (1 + spec.replicas)} members, each with {spec.cpu} CPU, {spec.memory}{' '}
              memory and {spec.storage_gib} GiB storage.
            </p>
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
                I have reviewed the resources and this change.
              </label>
            )}
          </FormSection>
        )}
        {error && (
          <p role="alert" className="text-destructive py-3">
            {error}
          </p>
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
              busy ||
              expired ||
              Boolean(review && (!confirmed || review.plan?.blocked_reasons.length))
            }
          >
            {busy ? 'Working…' : !review ? 'Review' : database ? 'Apply resize' : 'Create database'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
