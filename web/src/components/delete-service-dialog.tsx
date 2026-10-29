import { RequestError } from './shared'
import { useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application, Plan } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { canAccess, useScope } from '../lib/scope'
import { APIError, message } from '../lib/api'
import { withoutService, serviceVolumeRemoval } from '../lib/remove-service'
import { actionsRemovalMessage } from '../lib/actions-provider'
import { Dialog } from './ui/dialog'
import { Button } from './ui/button'
import { Icon } from './icons'
import { DiffTable } from './deploy-dialog'
import { plural, ReviewFacts, ReviewNotice, reviewDialog } from './operation-review'

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
  const label = snapshot.service_display_names?.[service] || service
  const domains = Object.entries(snapshot.spec.domains || {})
    .filter(([, target]) => target === service)
    .map(([domain]) => domain)
  const volumeChoice = removal.claims.length > 0 && (canDeleteVolumes || deleteVolumes)
  const action = deleteServiceAction(label, plan ? (deleteVolumes ? removal.claims.length : 0) : -1)
  return (
    <Dialog
      open
      className={reviewDialog}
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      title={`Delete ${label}?`}
      description={
        plan
          ? 'Check the planned configuration change, then confirm.'
          : 'Review what is removed and what is kept. Nothing changes until you confirm.'
      }
    >
      <div className="dialog-body">
        <ReviewFacts
          facts={[
            ['Scope', `${snapshot.project} / ${snapshot.environment}`],
            [
              'Application',
              <>
                {snapshot.display_name || snapshot.name} · revision {snapshot.revision}
              </>,
            ],
            ['Service', <code key="service">{service}</code>],
            domains.length > 0 && ['Domains removed', domains.join(', ')],
            [
              'Code',
              'The service stops and its traffic ends. Other services keep running and are not rebuilt.',
            ],
            ['Configuration', `A new deployment removes ${service} from the application.`],
            [
              'Data',
              deleteVolumes
                ? `Permanently erases ${removal.claims.join(', ')}. Shared volumes and backups are kept.`
                : removal.claims.length > 0
                  ? `Volumes are kept: ${removal.claims.join(', ')}. Backups are kept.`
                  : 'No volumes are attached. Backups are kept.',
            ],
          ]}
        />
        {snapshot.spec.services[service]?.actions && (
          <ReviewNotice tone="warn" title="Managed workflows change">
            {actionsRemovalMessage(snapshot.spec.services[service].actions!)}
          </ReviewNotice>
        )}
        {volumeChoice && (
          <label className="review-option">
            <input
              type="checkbox"
              checked={deleteVolumes}
              disabled={busy || Boolean(plan) || !canDeleteVolumes}
              onChange={(event) => setDeleteVolumes(event.target.checked)}
            />
            <span>
              Permanently delete unused volumes too
              <small>
                {removal.claims.join(', ')}. This erases their data after service removal succeeds.
                Shared volumes and backups are kept.
              </small>
            </span>
          </label>
        )}
        {!plan && (
          <p className="field-help">
            The review plans the change first. References from other services must be resolved
            before deletion can proceed.
          </p>
        )}
        {plan && <DiffTable changes={plan.changes} />}
        {plan && deleteVolumes && (
          <ReviewNotice tone="danger" title="Volume data cannot be recovered">
            Confirming deletes this service and permanently erases the listed volumes. Storage quota
            stays reserved until reclamation completes.
          </ReviewNotice>
        )}
        {plan?.warnings?.map((warning) => (
          <ReviewNotice key={warning} tone="warn" title="Plan warning">
            {warning}
          </ReviewNotice>
        ))}
        {deleteVolumes && !canDeleteVolumes && (
          <p className="field-help" role="status">
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
          variant={plan ? 'danger' : 'primary'}
          aria-busy={busy || undefined}
          disabled={busy || conflict || (deleteVolumes && !canDeleteVolumes)}
          onClick={() => void submit()}
        >
          {plan && <Icon name="trash" size={14} />}
          {busy ? (plan ? 'Deleting…' : 'Planning…') : action}
        </Button>
      </div>
    </Dialog>
  )
}

/** volumes: -1 before the plan exists, otherwise the number of volumes erased. */
export function deleteServiceAction(service: string, volumes: number) {
  if (volumes < 0) return 'Review deletion'
  return volumes ? `Delete ${service} and ${plural(volumes, 'volume')}` : `Delete ${service}`
}
