import { RequestError } from './shared'
import { useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application, Plan } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import { withoutService } from '../lib/remove-service'
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
  // Freeze the accepted revision while the user reviews this destructive change.
  const [snapshot] = useState(application)
  const [plan, setPlan] = useState<Plan | null>(null)
  const submitting = useRef(false)
  const [conflict, setConflict] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const key = useRef(crypto.randomUUID())
  const cache = useQueryClient()
  const navigate = useNavigate()
  async function submit() {
    if (submitting.current || conflict) return
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
              spec: withoutService(snapshot.spec, service),
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
            body: { ...scope, spec: plan.spec, expected_revision: plan.expected_revision },
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
      description={`Remove ${service} from ${snapshot.spec.name} and stop its traffic. Persistent volumes and backups are retained. This creates a new application deployment.`}
    >
      <div className="grid gap-4 p-4">
        {!plan && (
          <p className="text-sm text-muted-foreground">
            Review the removal before confirming. References from other services must be resolved
            before deletion can proceed.
          </p>
        )}
        {plan && <DiffTable changes={plan.changes} />}
        {plan?.warnings?.map((warning) => (
          <p key={warning} className="text-sm">
            {warning}
          </p>
        ))}
        {error && <RequestError error={error} />}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button variant="danger" disabled={busy || conflict} onClick={() => void submit()}>
          <Icon name="trash" size={14} />
          {busy ? 'Working…' : plan ? 'Delete service' : 'Review deletion'}
        </Button>
      </div>
    </Dialog>
  )
}
