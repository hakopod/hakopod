import { useEffect, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import type { components } from '../lib/api.generated'
import { APIError, message, relative } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { draftKey } from '../lib/drafts'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { RequestError } from './shared'
import { DiffTable } from './deploy-dialog'
import { ReviewFacts, ReviewNotice, plural, reviewDialog } from './operation-review'

type Draft = components['schemas']['Draft']

/**
 * One review for everything staged on an application: what changes and who staged it, the
 * plan check, then a deploy that names what goes live. Nothing is sent until the final action.
 */
export function DraftReview({
  app,
  draft,
  onClose,
}: {
  app: Application
  draft: Draft
  onClose: () => void
}) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [busy, setBusy] = useState<'' | 'deploy' | 'discard'>('')
  const [confirmDiscard, setConfirmDiscard] = useState(false)
  const [error, setError] = useState('')
  // One idempotency key per review, so a lost response never creates a second release.
  const key = useRef(crypto.randomUUID())
  // Focus starts on Cancel, and returns there once discard is armed, so Enter never
  // reaches the destructive button by accident.
  const cancel = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (confirmDiscard) cancel.current?.focus()
  }, [confirmDiscard])
  const conflicted = draft.conflicts.length > 0
  const plan = useQuery({
    queryKey: ['draft-plan', app.id, draft.revision],
    queryFn: () =>
      unwrap(
        client.POST('/applications/{id}/draft/plan', {
          params: { path: { id: app.id } },
          body: { expected_draft_revision: draft.revision },
        }),
      ),
    enabled: !conflicted,
    retry: false,
    gcTime: 0,
  })
  const authors = [...new Set(draft.entries.map((entry) => entry.author || 'Someone'))]
  const count = draft.changes.length
  const missing = plan.data?.missing_secrets || []
  const refresh = () => queryClient.invalidateQueries({ queryKey: draftKey(app.id) })

  async function deploy() {
    if (!plan.data) return
    setBusy('deploy')
    setError('')
    try {
      const accepted = await unwrap(
        client.POST('/applications/{id}/draft/deploy', {
          params: { path: { id: app.id }, header: { 'Idempotency-Key': key.current } },
          body: {
            expected_draft_revision: draft.revision,
            expected_revision: plan.data.expected_revision,
          },
        }),
      )
      void refresh()
      void queryClient.invalidateQueries({ queryKey: ['application', app.id] })
      onClose()
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: accepted.id } })
    } catch (err) {
      if (err instanceof APIError && [400, 403, 404, 409, 422].includes(err.status)) {
        key.current = crypto.randomUUID()
        void refresh()
      }
      setError(message(err))
      setBusy('')
    }
  }

  async function discard() {
    setBusy('discard')
    setError('')
    try {
      await unwrap(
        client.DELETE('/applications/{id}/draft', {
          params: { path: { id: app.id } },
          body: { expected_draft_revision: draft.revision },
        }),
      )
      void refresh()
      onClose()
    } catch (err) {
      setError(message(err))
      void refresh()
      setBusy('')
    }
  }

  return (
    <Dialog
      open
      className={reviewDialog}
      onOpenChange={(open) => !open && !busy && onClose()}
      onOpenAutoFocus={(event) => {
        event.preventDefault()
        cancel.current?.focus()
      }}
      title={`Review ${plural(count, 'staged change')}`}
      description="Staged changes go live together when you deploy them. Nothing changes until then."
    >
      <div className="dialog-body">
        <ReviewFacts
          facts={[
            ['Scope', `${app.project} / ${app.environment}`],
            ['Revision', `${draft.current_revision} → ${draft.current_revision + 1}`],
            ['Staged by', `${authors.join(', ')} · updated ${relative(draft.updated_at)}`],
            draft.stale &&
              !conflicted && [
                'Merged',
                `Carried forward onto revision ${draft.current_revision}, which was deployed after these changes were staged.`,
              ],
          ]}
        />
        {conflicted && (
          <ReviewNotice tone="danger" title="These changes conflict with a newer deployment">
            Revision {draft.current_revision} also changed {draft.conflicts.join(', ')}. Discard the
            staged changes and make them again on the current configuration.
          </ReviewNotice>
        )}
        <DiffTable changes={draft.changes} />
        {!conflicted &&
          (plan.isPending ? (
            <p className="field-help" role="status">
              Checking the plan…
            </p>
          ) : plan.error ? (
            <RequestError error={plan.error} />
          ) : (
            <>
              {(plan.data?.warnings || []).map((warning) => (
                <ReviewNotice key={warning} tone="warn" title="Check before deploying">
                  {warning}
                </ReviewNotice>
              ))}
              {missing.length > 0 && (
                <ReviewNotice tone="danger" title="Secrets are missing">
                  Set {missing.join(', ')} before deploying. The changes stay staged.
                </ReviewNotice>
              )}
            </>
          ))}
        {error && <RequestError error={error} />}
      </div>
      <div className="dialog-footer">
        {confirmDiscard ? (
          <Button
            variant="danger"
            aria-busy={busy === 'discard' || undefined}
            disabled={Boolean(busy)}
            onClick={() => void discard()}
          >
            {busy === 'discard' ? 'Discarding…' : `Discard ${plural(count, 'change')}`}
          </Button>
        ) : (
          <Button variant="ghost" disabled={Boolean(busy)} onClick={() => setConfirmDiscard(true)}>
            Discard all
          </Button>
        )}
        <Button ref={cancel} disabled={Boolean(busy)} onClick={onClose}>
          Cancel
        </Button>
        <Button
          variant="primary"
          aria-busy={busy === 'deploy' || undefined}
          disabled={Boolean(busy) || conflicted || !plan.data || missing.length > 0}
          onClick={() => void deploy()}
        >
          {busy === 'deploy'
            ? 'Deploying…'
            : `Deploy ${plural(count, 'change')} to ${app.environment}`}
        </Button>
      </div>
    </Dialog>
  )
}
