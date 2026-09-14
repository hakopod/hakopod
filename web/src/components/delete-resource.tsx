import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { Icon } from './icons'

export function DeleteResource({
  project,
  application,
}: {
  project: string
  application?: Application
}) {
  const scope = useScope()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const [reviewed, setReviewed] = useState<Application | undefined>()
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const kind = application ? 'application' : 'project'
  const name = (open ? reviewed?.name : application?.name) || project
  const allowed = application
    ? scope.identity.admin ||
      scope.identity.project_roles?.some(
        (role) => role.project === project && role.role === 'admin',
      )
    : scope.identity.admin
  if (!allowed) return null
  const empty = !application || Object.keys(application.spec.services).length === 0
  async function remove() {
    if (busy || confirmation !== name) return
    setBusy(true)
    setError('')
    try {
      if (reviewed) {
        await unwrap(
          client.DELETE('/applications/{id}', {
            params: { path: { id: reviewed.id } },
            body: { expected_revision: reviewed.revision, confirm_name: confirmation },
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
      if (reviewed) queryClient.removeQueries({ queryKey: ['application', reviewed.id] })
      setOpen(false)
      if (reviewed)
        void navigate({
          to: '/projects/$project',
          params: { project: reviewed.project },
          search: { environment: reviewed.environment },
        })
      else void navigate({ to: '/' })
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <Button
        variant="danger"
        size="sm"
        disabled={!empty}
        title={!empty ? 'Remove all services before deleting this application.' : undefined}
        onClick={() => {
          setReviewed(application ? structuredClone(application) : undefined)
          setConfirmation('')
          setError('')
          setOpen(true)
        }}
      >
        <Icon name="trash" size={14} />
        Delete {kind}
      </Button>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!busy) setOpen(next)
        }}
        title={`Delete ${name}?`}
        description={
          application
            ? `${reviewed?.project || project} / ${reviewed?.environment || application.environment} · Revision ${reviewed?.revision ?? application.revision}`
            : `Project ID: ${project}`
        }
      >
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void remove()
          }}
        >
          <div className="dialog-body field-stack">
            <p>
              {application
                ? 'This removes the empty application, its deployment and build history, and Git bindings. Persistent volumes and backups are retained for the operator. Active work or enabled backup schedules must be stopped first.'
                : 'Only an empty project can be deleted. Remove applications, builds, networks and secret-provider scope grants across every environment first. Personal workspaces cannot be deleted.'}
            </p>
            <p className="field-help">
              This cannot be undone. Its {application ? 'name' : 'ID'} stays reserved to prevent old
              credentials or callbacks reaching another resource.
            </p>
            <label>
              Type <strong>{name}</strong> to confirm
              <Input
                autoComplete="off"
                value={confirmation}
                onChange={(event) => setConfirmation(event.target.value)}
                disabled={busy}
              />
            </label>
            {error && (
              <div className="inline-error" role="alert">
                {error}
              </div>
            )}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="danger" disabled={busy || confirmation !== name}>
              {busy ? 'Deleting…' : `Delete ${kind}`}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  )
}
