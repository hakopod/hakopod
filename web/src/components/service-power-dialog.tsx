import { useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
export function ServicePowerDialog({
  application,
  service,
  onClose,
}: {
  application: Application
  service: string
  onClose: () => void
}) {
  const [snapshot] = useState(application)
  const scheduled = Boolean(snapshot.spec.services[service]?.job?.schedule)
  const resume = Boolean(snapshot.spec.services[service]?.suspended)
  const [conflict, setConflict] = useState(false)
  const [busy, setBusy] = useState(false),
    [error, setError] = useState('')
  const key = useRef(crypto.randomUUID()),
    running = useRef(false)
  const navigate = useNavigate(),
    cache = useQueryClient()
  async function submit() {
    if (running.current || conflict) return
    running.current = true
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.POST(
          resume
            ? '/applications/{id}/services/{service}/resume'
            : '/applications/{id}/services/{service}/stop',
          {
            body: { expected_revision: snapshot.revision },
            params: {
              path: { id: snapshot.id, service },
              header: { 'Idempotency-Key': key.current },
            },
          },
        ),
      )
      void cache.invalidateQueries({ queryKey: ['application', snapshot.id] })
      onClose()
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: result.id } })
    } catch (err) {
      if (err instanceof APIError && err.status === 409) {
        setConflict(true)
        setError(
          message(err) + ' Close this dialog and review the latest deployment before retrying.',
        )
      } else setError(message(err))
    } finally {
      running.current = false
      setBusy(false)
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      title={`${resume ? 'Resume' : scheduled ? 'Pause' : 'Stop'} ${service}${scheduled ? ' schedule' : ''}?`}
      description={
        scheduled
          ? resume
            ? 'Enable future scheduled runs. Missed runs older than one minute are skipped.'
            : 'Pause future scheduled runs. Any active job is allowed to finish; configuration and volumes are retained.'
          : resume
            ? 'Restore the saved replica count and autoscaling configuration through a new deployment.'
            : 'Scale this service to zero and suspend autoscaling. Configuration, domains, volumes and the saved replica count are retained. Dependent services may become unavailable.'
      }
    >
      {error && (
        <p role="alert" className="inline-error p-4">
          {error}
        </p>
      )}
      <div className="dialog-footer">
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button
          variant={resume ? 'primary' : 'danger'}
          disabled={busy || conflict}
          onClick={() => void submit()}
        >
          {busy
            ? 'Submitting…'
            : scheduled
              ? resume
                ? 'Resume schedule'
                : 'Pause schedule'
              : resume
                ? 'Resume service'
                : 'Stop service'}
        </Button>
      </div>
    </Dialog>
  )
}
