import { RequestError } from './shared'
import { useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application, Plan } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { canAccess, useScope } from '../lib/scope'
import { APIError, message } from '../lib/api'
import { withoutService, serviceVolumeRemoval } from '../lib/remove-service'
import { Dialog } from './ui/dialog'
import { Button } from './ui/button'
import { Icon } from './icons'
import { DiffTable } from './deploy-dialog'

export function DeleteServiceDialog({
  application,
  service,
  onClose,
}: {
  application: Application
  service: string
  onClose: () => void
}) {
  const scope = useScope()
  // Freeze the accepted revision while the user reviews this destructive change.
  const [snapshot] = useState(application)
  const [deleteVolumes, setDeleteVolumes] = useState(false)
  const removal = serviceVolumeRemoval(snapshot.spec, service)
  const canDeleteVolumes =
    canAccess(scope.identity, snapshot.project, 'deployments:write') &&
    Boolean(
      scope.identity.admin ||
      scope.identity.can_manage_applications ||
      scope.identity.project_roles?.some(
        (role) => role.project === snapshot.project && role.role === 'admin',
      ),
    )
  const [plan, setPlan] = useState<Plan | null>(null)
  const submitting = useRef(false)
  const [conflict, setConflict] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const key = useRef(crypto.randomUUID())
  const cache = useQueryClient()
  const navigate = useNavigate()
  async function submit() {
    if (submitting.current || conflict || (deleteVolumes && !canDeleteVolumes)) return
    submitting.current = true
    setBusy(true)
    setError('')
    try {
      const scope = { project: snapshot.project, environment: snapshot.environment }
      if (!plan) {
        const result = await unwrap(
          client.POST('/plan', {
            body: {
              ...scope,
              spec: deleteVolumes ? removal.spec : withoutService(snapshot.spec, service),
              ...(deleteVolumes ? { delete_service_volumes: [service] } : {}),
            },
          }),
        )
        if (result.application_id !== snapshot.id || result.expected_revision !== snapshot.revision)
          throw new Error(
            'The application changed. Close this dialog and review the latest configuration before deleting.',
          )
        setPlan(result)
      } else {
        const result = await unwrap(
          client.POST('/deployments', {
            body: {
              ...scope,
              spec: plan.spec,
              expected_revision: plan.expected_revision,
              ...(deleteVolumes ? { delete_service_volumes: [service] } : {}),
            },
            params: { header: { 'Idempotency-Key': key.current } },
          }),
        )
        void cache.invalidateQueries({ queryKey: ['applications'] })
        void cache.invalidateQueries({ queryKey: ['application', snapshot.id] })
        onClose()
        void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: result.id } })
      }
    } catch (err) {
      if (err instanceof APIError && err.status === 409) {
        setConflict(true)
        setError(
          'The application changed. Close this dialog and review the latest configuration before deleting.',
        )
      } else setError(message(err))
    } finally {
      setBusy(false)
      submitting.current = false
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      title={`Delete ${service}?`}
      description={`Remove ${service} from ${snapshot.spec.name} and stop its traffic. ${removal.claims.length > 0 && (canDeleteVolumes || deleteVolumes) ? 'Volumes are kept unless you choose to delete them below. Backups are kept.' : 'Volumes and backups are kept.'} This creates a new application deployment.`}
    >
      <div className="grid gap-4 p-4">
        {!plan && (
          <p className="text-sm text-muted-foreground">
            Review the removal before confirming. References from other services must be resolved
            before deletion can proceed.
          </p>
        )}
        {removal.claims.length > 0 && (canDeleteVolumes || deleteVolumes) && (
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={deleteVolumes}
              disabled={busy || Boolean(plan) || !canDeleteVolumes}
              onChange={(event) => setDeleteVolumes(event.target.checked)}
            />
            <span>
              Permanently delete unused volumes too
              <span className="block break-all text-xs text-muted-foreground">
                {removal.claims.join(', ')}. This erases their data after service removal succeeds.
                Shared volumes and backups are kept.
              </span>
            </span>
          </label>
        )}
        {plan && <DiffTable changes={plan.changes} />}
        {plan && deleteVolumes && (
          <p className="text-sm">
            Confirming deletes this service and permanently erases the listed volumes. Storage quota
            stays reserved until reclamation completes.
          </p>
        )}
        {plan?.warnings?.map((warning) => (
          <p key={warning} className="text-sm">
            {warning}
          </p>
        ))}
        {deleteVolumes && !canDeleteVolumes && (
          <p className="text-sm" role="status">
            Permission to delete volumes is no longer available. Close this dialog and review the
            service again.
          </p>
        )}
        {error && <RequestError error={error} />}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button
          variant="danger"
          disabled={busy || conflict || (deleteVolumes && !canDeleteVolumes)}
          onClick={() => void submit()}
        >
          <Icon name="trash" size={14} />
          {busy
            ? 'Working…'
            : plan
              ? deleteVolumes
                ? 'Delete service and volumes'
                : 'Delete service'
              : 'Review deletion'}
        </Button>
      </div>
    </Dialog>
  )
}
