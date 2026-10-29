import { RequestError } from './shared'
import { useState, type ComponentProps } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { Icon } from './icons'
import { ReviewFacts, ReviewNotice, reviewDialog } from './operation-review'

export function DeleteResource({
  project,
  application,
  trigger = 'button',
  initiallyOpen = false,
  onClose,
  onCloseAutoFocus,
}: {
  project: string
  application?: Application
  trigger?: 'button' | 'icon' | 'none'
  initiallyOpen?: boolean
  onClose?: () => void
  onCloseAutoFocus?: ComponentProps<typeof Dialog>['onCloseAutoFocus']
}) {
  const scope = useScope()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [open, setOpen] = useState(initiallyOpen)
  const [reviewed, setReviewed] = useState<Application | undefined>(() =>
    initiallyOpen && application ? structuredClone(application) : undefined,
  )
  const [confirmation, setConfirmation] = useState('')
  const [deleteData, setDeleteData] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const kind = application ? 'application' : 'project'
  const name = (open ? reviewed?.name : application?.name) || project
  const allowed = application
    ? scope.identity.admin ||
      scope.identity.can_manage_applications ||
      scope.identity.project_roles?.some(
        (role) => role.project === project && role.role === 'admin',
      )
    : scope.identity.admin
  if (!allowed) return null
  const current = open ? reviewed : application
  const empty = !current || Object.keys(current.spec.services).length === 0
  const close = () => {
    setOpen(false)
    onClose?.()
  }
  async function remove() {
    if (busy || !empty || confirmation !== name) return
    setBusy(true)
    setError('')
    try {
      if (reviewed) {
        await unwrap(
          client.DELETE('/applications/{id}', {
            params: { path: { id: reviewed.id } },
            body: {
              expected_revision: reviewed.revision,
              confirm_name: confirmation,
              delete_data: deleteData,
            },
          }),
        )
      } else {
        await unwrap(
          client.DELETE('/projects/{id}', {
            params: { path: { id: project } },
            body: { confirm_name: confirmation },
          }),
        )
      }
      await queryClient.invalidateQueries({ queryKey: [reviewed ? 'applications' : 'projects'] })
      await queryClient.invalidateQueries({ queryKey: ['retained-storage'] })
      if (reviewed) queryClient.removeQueries({ queryKey: ['application', reviewed.id] })
      close()
      if (reviewed)
        void navigate({
          to: '/projects/$project',
          params: { project: reviewed.project },
          search: { environment: reviewed.environment },
        })
      else {
        if (scope.project === project) scope.syncScope('', '')
        void navigate({ to: '/' })
      }
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      {trigger !== 'none' && (
        <Button
          variant={trigger === 'icon' ? 'ghost' : 'danger'}
          size={trigger === 'icon' ? 'icon' : 'sm'}
          aria-label={trigger === 'icon' ? `Delete ${kind} ${name}` : undefined}
          disabled={!empty}
          title={!empty ? 'Remove all services before deleting this application.' : undefined}
          onClick={() => {
            setReviewed(application ? structuredClone(application) : undefined)
            setConfirmation('')
            setDeleteData(false)
            setError('')
            setOpen(true)
          }}
        >
          <Icon name="trash" size={14} />
          {trigger !== 'icon' && <>Delete {kind}</>}
        </Button>
      )}
      <Dialog
        open={open}
        className={reviewDialog}
        onCloseAutoFocus={onCloseAutoFocus}
        onOpenChange={(next) => {
          if (!busy) {
            if (next) setOpen(true)
            else close()
          }
        }}
        title={`Delete ${name}?`}
        description={
          empty
            ? 'This cannot be undone. Review what is removed and what is kept.'
            : 'This application still has services.'
        }
      >
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void remove()
          }}
        >
          <div className="dialog-body">
            {!empty && reviewed && (
              <ReviewNotice tone="warn" title="Remove all services first">
                Remove every service through a reviewed deployment before deleting this application.
                <div className="mt-3">
                  <Button asChild size="sm">
                    <Link
                      to="/applications/$applicationId"
                      params={{ applicationId: reviewed.id }}
                      search={{ tab: 'services' }}
                      onClick={close}
                    >
                      Open services
                    </Link>
                  </Button>
                </div>
              </ReviewNotice>
            )}
            <ReviewFacts
              facts={
                application
                  ? [
                      [
                        'Scope',
                        `${reviewed?.project || project} / ${reviewed?.environment || application.environment}`,
                      ],
                      [
                        'Application',
                        <>
                          <code>{name}</code> · revision{' '}
                          {reviewed?.revision ?? application.revision}
                        </>,
                      ],
                      [
                        'Removed',
                        deleteData
                          ? 'Deployment and build history, Git bindings, persistent volumes and native secrets.'
                          : 'Deployment and build history and Git bindings.',
                      ],
                      [
                        'Kept',
                        deleteData
                          ? 'Saved backups.'
                          : 'Persistent volumes, native secrets and saved backups.',
                      ],
                      [
                        'Name',
                        'Stays reserved so old credentials and callbacks cannot reach a new resource.',
                      ],
                    ]
                  : [
                      ['Project', <code key="project">{project}</code>],
                      ['Removed', 'The empty project and its environments.'],
                      ['Kept', 'Personal projects stay linked to their accounts.'],
                      [
                        'Name',
                        'Stays reserved so old credentials and callbacks cannot reach a new resource.',
                      ],
                    ]
              }
            />
            <ReviewNotice title="Before deleting">
              {application
                ? 'Active work must finish and enabled backup schedules must be stopped.'
                : 'Remove its resources, reclaim retained storage and remove scope grants in every environment. Pending operations must finish.'}
            </ReviewNotice>
            {application && (
              <label className="review-option">
                <input
                  type="checkbox"
                  checked={deleteData}
                  onChange={(event) => setDeleteData(event.target.checked)}
                  disabled={busy}
                />
                <span>
                  Permanently delete persistent volumes and native secrets too
                  <small>Database files cannot be recovered without a backup.</small>
                </span>
              </label>
            )}
            <label className="grid gap-1.5">
              <span>
                Type <strong className="break-all">{name}</strong> to confirm
              </span>
              <Input
                autoComplete="off"
                value={confirmation}
                onChange={(event) => setConfirmation(event.target.value)}
                disabled={busy || !empty}
              />
            </label>
            {error && <RequestError error={error} />}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={close}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="danger"
              aria-busy={busy || undefined}
              disabled={busy || !empty || confirmation !== name}
            >
              <Icon name="trash" size={14} />
              {busy ? 'Deleting…' : `Delete ${kind} ${name}`}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  )
}
