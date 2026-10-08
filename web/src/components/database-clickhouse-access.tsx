import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { apiDatabaseSpec, type ManagedDatabase, type DatabaseResizeReview } from '../lib/databases'
import { FormError, FormSection } from './form-page'
import { Note } from './shared'
import { Button } from './ui/button'

export function DatabaseClickHouseAccess({ database }: { database: ManagedDatabase }) {
  const [review, setReview] = useState<{ id: string; plan: DatabaseResizeReview } | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [clock, setClock] = useState(Date.now)
  const [error, setError] = useState('')
  const key = useRef('')
  const cache = useQueryClient()
  const navigate = useNavigate()
  const enabled = database.spec.clickhouse?.access_profile === 'tenant_admin'
  const expired = Boolean(review && Date.parse(review.plan.expires_at) <= clock)
  useEffect(() => {
    if (!review) return
    const timer = window.setTimeout(
      () => setClock(Date.now()),
      Math.max(0, Date.parse(review.plan.expires_at) - Date.now() + 1),
    )
    return () => window.clearTimeout(timer)
  }, [review])
  const proposed = { ...database.spec, clickhouse: { access_profile: 'tenant_admin' as const } }
  return (
    <FormSection title="Application access">
      <p>
        {enabled
          ? 'Tenant administration is enabled.'
          : 'Application queries are enabled. The application login cannot create tenant users.'}
      </p>
      {enabled ? (
        <Note>
          Keep this login in a trusted provisioning service. Disabling tenant administration
          requires migration and revocation of existing tenant credentials. Backups exclude tenant
          users, quotas and row policies.
        </Note>
      ) : database.spec.mode !== 'standalone' ? (
        <Note>Tenant administration requires a dedicated standalone ClickHouse instance.</Note>
      ) : (
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (busy) return
            setBusy(true)
            setError('')
            try {
              if (!review || Date.parse(review.plan.expires_at) <= Date.now()) {
                const next = await unwrap(
                  client.POST('/databases/{id}/resize-plan', {
                    params: { path: { id: database.id } },
                    body: { spec: apiDatabaseSpec(proposed) },
                  }),
                )
                setClock(Date.now())
                setReview(next)
                setConfirmed(false)
                key.current = ''
                return
              }
              if (!confirmed || review.plan.blocked_reasons.length) return
              if (!key.current) key.current = crypto.randomUUID()
              await unwrap(
                client.POST('/databases/{id}/resize', {
                  params: { path: { id: database.id }, header: { 'Idempotency-Key': key.current } },
                  body: {
                    spec: apiDatabaseSpec(proposed),
                    review_id: review.id,
                    expected_revision: database.revision,
                  },
                }),
              )
              await cache.invalidateQueries({ queryKey: ['managed-database', database.id] })
              await cache.invalidateQueries({ queryKey: ['managed-databases'] })
              await navigate({
                to: '/databases/$databaseId',
                params: { databaseId: database.id },
                search: { project: database.project, environment: database.environment },
              })
            } catch (failure) {
              setError(message(failure))
            } finally {
              setBusy(false)
            }
          }}
          className="grid gap-3"
        >
          <Note>
            Tenant administration lets the application login create and delete users, quotas and row
            policies, and delegate SELECT on app.*. Backups do not retain tenant access entities.
            Recreate and verify tenant access after restore.
          </Note>
          {review && (
            <>
              {review.plan.warnings.map((warning) => (
                <Note key={warning}>{warning}</Note>
              ))}
              {review.plan.blocked_reasons.map((reason) => (
                <p key={reason} role="alert">
                  {reason}
                </p>
              ))}
              {expired ? (
                <p role="status">This review expired. Request a new review.</p>
              ) : (
                <label className="flex items-start gap-2">
                  <input
                    type="checkbox"
                    checked={confirmed}
                    disabled={busy}
                    onChange={(event) => setConfirmed(event.target.checked)}
                  />
                  <span>
                    I approve tenant administration on this dedicated instance and will restore
                    tenant access separately from data.
                  </span>
                </label>
              )}
            </>
          )}
          {error && <FormError>{error}</FormError>}
          <Button
            type="submit"
            variant="primary"
            disabled={
              busy ||
              Boolean(review && !expired && (!confirmed || review.plan.blocked_reasons.length))
            }
          >
            {busy
              ? 'Submitting…'
              : !review || expired
                ? 'Review tenant administration'
                : 'Enable tenant administration'}
          </Button>
        </form>
      )}
    </FormSection>
  )
}
