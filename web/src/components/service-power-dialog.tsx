import { RequestError } from './shared'
import { useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { focusedDialog, ReviewFacts, ReviewNotice } from './operation-review'
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
  const label = snapshot.service_display_names?.[service] || service
  const verb = resume ? 'Resume' : scheduled ? 'Pause' : 'Stop'
  const action = `${verb} ${label}${scheduled ? ' schedule' : ''}`
  return (
    <Dialog
      open
      className={focusedDialog}
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      title={`${action}?`}
      description={
        scheduled
          ? resume
            ? 'Future scheduled runs start again.'
            : 'Future scheduled runs stop until you resume them.'
          : resume
            ? 'A new deployment brings the service back.'
            : 'A new deployment scales the service to zero.'
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
            [
              'Runtime',
              scheduled
                ? resume
                  ? 'Future runs are enabled. Missed runs older than one minute are skipped.'
                  : 'Future runs are paused. An active job is allowed to finish.'
                : resume
                  ? 'The saved replica count and autoscaling configuration are restored.'
                  : 'Scaled to zero; autoscaling is suspended.',
            ],
            [
              'Kept',
              scheduled
                ? 'Configuration and volumes.'
                : 'Configuration, domains, volumes and the saved replica count.',
            ],
          ]}
        />
        {!scheduled && !resume && (
          <ReviewNotice tone="warn" title="Dependent services may become unavailable">
            Anything that calls this service gets no response until you resume it.
          </ReviewNotice>
        )}
        {error && <RequestError error={error} />}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button
          variant={resume ? 'primary' : 'danger'}
          aria-busy={busy || undefined}
          disabled={busy || conflict}
          onClick={() => void submit()}
        >
          {busy ? 'Submitting…' : action}
        </Button>
      </div>
    </Dialog>
  )
}
