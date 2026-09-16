import { RequestError } from './shared'
import { useRef, useState, type ComponentProps } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Pencil } from 'lucide-react'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { useScope } from '../lib/scope'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import type { Application, Project } from '../lib/types'

type RenamePresentation = {
  trigger?: 'icon' | 'none'
  initiallyOpen?: boolean
  onClose?: () => void
  onCloseAutoFocus?: ComponentProps<typeof Dialog>['onCloseAutoFocus']
}

export function RenameName({
  kind,
  name,
  id,
  revision,
  save,
  trigger: triggerKind = 'icon',
  initiallyOpen = false,
  onClose,
  onCloseAutoFocus,
}: RenamePresentation & {
  kind: string
  name: string
  id: string
  revision: number
  save: (name: string, revision: number) => Promise<unknown>
}) {
  const trigger = useRef<HTMLButtonElement>(null)
  const [open, setOpen] = useState(initiallyOpen)
  const [value, setValue] = useState(name)
  const [expected, setExpected] = useState(revision)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const close = () => {
    setOpen(false)
    onClose?.()
  }
  return (
    <>
      {triggerKind !== 'none' && (
        <Button
          ref={trigger}
          variant="ghost"
          size="icon"
          aria-label={`Rename ${kind} ${name}`}
          onClick={() => {
            setValue(name)
            setExpected(revision)
            setError('')
            setOpen(true)
          }}
        >
          <Pencil size={14} />
        </Button>
      )}
      <Dialog
        onCloseAutoFocus={
          onCloseAutoFocus ||
          ((event) => {
            event.preventDefault()
            trigger.current?.focus()
          })
        }
        open={open}
        onOpenChange={(next) => {
          if (!busy && !next) close()
        }}
        title={`Rename ${kind}`}
        description={`ID: ${id}`}
      >
        <form
          onSubmit={(event) => {
            event.preventDefault()
            if (busy || !value.trim()) return
            setBusy(true)
            setError('')
            void save(value.trim(), expected)
              .then(close)
              .catch((cause) =>
                setError(
                  message(cause) +
                    ' Close this dialog and reopen it to review the latest name before retrying.',
                ),
              )
              .finally(() => setBusy(false))
          }}
        >
          <div className="dialog-body field-stack">
            <label className="field">
              <span>Name</span>
              <Input
                autoFocus
                value={value}
                maxLength={80}
                required
                disabled={busy}
                onChange={(event) => setValue(event.target.value)}
              />
            </label>
            <p className="field-help">
              This changes the displayed name. Resource IDs, configuration, domains and stored data
              stay the same.
            </p>
            {error && <RequestError error={error} />}
          </div>
          <div className="dialog-footer">
            <Button disabled={busy} onClick={close}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={busy || !value.trim()}>
              {busy ? 'Saving…' : 'Save name'}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  )
}
export function RenameResource({
  application,
  project,
  service,
  ...presentation
}: RenamePresentation & {
  application?: Application
  project?: Project
  service?: string
}) {
  const scope = useScope(),
    cache = useQueryClient()
  const projectID = application?.project || project?.name || ''
  const allowed =
    scope.identity.admin ||
    scope.identity.can_manage_applications ||
    scope.identity.project_roles?.some(
      (role) => role.project === projectID && role.role === 'admin',
    )
  if (!allowed || (!application && !project)) return null
  const kind = service ? 'service' : application ? 'application' : 'project'
  const name = application
    ? service
      ? application.service_display_names?.[service] || service
      : application.display_name || application.name
    : project!.display_name || project!.name
  return (
    <RenameName
      {...presentation}
      kind={kind}
      name={name}
      id={service || application?.name || project!.name}
      revision={application?.metadata_revision || project?.metadata_revision || 1}
      save={async (name, revision) => {
        const body = { display_name: name, expected_metadata_revision: revision }
        try {
          if (application) {
            if (service)
              await unwrap(
                client.PUT('/applications/{id}/services/{service}/name', {
                  params: { path: { id: application.id, service } },
                  body,
                }),
              )
            else
              await unwrap(
                client.PUT('/applications/{id}/name', {
                  params: { path: { id: application.id } },
                  body,
                }),
              )
            await cache.invalidateQueries({ queryKey: ['application', application.id] })
            await cache.invalidateQueries({ queryKey: ['applications'] })
          } else {
            await unwrap(
              client.PUT('/projects/{id}/name', { params: { path: { id: project!.name } }, body }),
            )
            await cache.invalidateQueries({ queryKey: ['projects'] })
          }
        } finally {
          await cache.invalidateQueries({
            queryKey: application ? ['application', application.id] : ['projects'],
          })
        }
      }}
    />
  )
}
