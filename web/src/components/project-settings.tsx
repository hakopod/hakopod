import { lazy, Suspense, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Project } from '../lib/types'
import { canAccess, canCreateEnvironment, useScope } from '../lib/scope'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { dashboardEdition } from '../lib/dashboard-edition'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { FormError } from './form-page'
import { Icon } from './icons'
import { Note, PageHeader } from './shared'
import { RenameResource } from './rename-resource'
import { DeleteResource } from './delete-resource'

const ProjectEnvironment = lazy(() => import('./project-environment'))

export function ProjectSettings({ project }: { project: Project }) {
  const { identity } = useScope()
  const cache = useQueryClient()
  const navigate = useNavigate()
  const createTrigger = useRef<HTMLButtonElement>(null)
  const [creating, setCreating] = useState(false)
  const manage =
    !dashboardEdition.cloud &&
    !identity.mfa_required &&
    canCreateEnvironment(identity, project.name)
  const environment = project.environments[0]?.name
  return (
    <div className="ops-page">
      <PageHeader
        title="Project settings"
        description="Manage this project's name, environments and deletion. IDs remain fixed after creation."
        action={
          environment ? (
            <Button asChild>
              <Link
                to="/projects/$project"
                params={{ project: project.name }}
                search={{ environment }}
              >
                Applications
              </Link>
            </Button>
          ) : (
            <Button asChild>
              <Link to="/">View projects</Link>
            </Button>
          )
        }
      />
      <section className="grid gap-4 py-4" aria-labelledby="project-settings-details">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 id="project-settings-details">Project</h2>
          <div className="flex items-center gap-2">
            <RenameResource project={project} />
            {!dashboardEdition.cloud && identity.admin && !project.personal && (
              <DeleteResource project={project.name} trigger="icon" />
            )}
          </div>
        </div>
        <dl className="grid gap-3 sm:grid-cols-2">
          <div>
            <dt className="muted-text">Name</dt>
            <dd className="break-words">{project.display_name || project.name}</dd>
          </div>
          <div>
            <dt className="muted-text">Project ID</dt>
            <dd>
              <code className="break-all">{project.name}</code>
            </dd>
          </div>
        </dl>
        {project.personal && (
          <Note>
            The personal project and its development environment stay linked to your account. You
            can remove additional empty environments.
          </Note>
        )}
      </section>
      <section className="grid gap-3 py-4" aria-labelledby="project-settings-environments">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 id="project-settings-environments">Environments</h2>
          {manage && (
            <Button ref={createTrigger} onClick={() => setCreating(true)}>
              <Icon name="plus" size={14} />
              New environment
            </Button>
          )}
        </div>
        {project.environments.length ? (
          <ul className="grid gap-2" aria-label="Project environments">
            {project.environments.map((item) => (
              <li
                key={item.name}
                className="flex min-w-0 flex-wrap items-center justify-between gap-3 py-2"
              >
                <code className="min-w-0 break-all">{item.name}</code>
                <div className="flex items-center gap-2">
                  <Button asChild size="sm" className="min-h-9">
                    <Link
                      to="/projects/$project"
                      params={{ project: project.name }}
                      search={{ environment: item.name }}
                    >
                      Open
                    </Link>
                  </Button>
                  {manage &&
                    canAccess(identity, project.name, 'deployments:write') &&
                    !(project.personal && item.name === 'development') && (
                      <DeleteEnvironment project={project.name} environment={item.name} />
                    )}
                </div>
              </li>
            ))}
          </ul>
        ) : (
          <p className="field-help">
            No environments remain. Create one to deploy applications in this project.
          </p>
        )}
      </section>
      {creating && (
        <Suspense fallback={null}>
          <ProjectEnvironment
            project={project}
            onClose={() => setCreating(false)}
            restoreFocus={() => createTrigger.current?.focus()}
            onCreated={(name) => {
              void cache
                .invalidateQueries({ queryKey: ['projects'] })
                .then(() =>
                  navigate({
                    to: '/projects/$project',
                    params: { project: project.name },
                    search: { environment: name, tab: 'manage' },
                  }),
                )
            }}
          />
        </Suspense>
      )}
    </div>
  )
}

function DeleteEnvironment({ project, environment }: { project: string; environment: string }) {
  const cache = useQueryClient()
  const navigate = useNavigate()
  const scope = useScope()
  const trigger = useRef<HTMLButtonElement>(null)
  const [open, setOpen] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function remove() {
    if (busy || confirmation !== environment) return
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.DELETE('/projects/{project}/environments/{environment}', {
          params: { path: { project, environment } },
          body: { confirm_name: confirmation },
        }),
      )
      await cache.invalidateQueries({ queryKey: ['projects'] })
      cache.removeQueries({ queryKey: ['applications', project, environment] })
      if (scope.project === project && scope.environment === environment)
        scope.syncScope(project, '')
      setOpen(false)
      await navigate({
        to: '/projects/$project',
        params: { project },
        search: { tab: 'manage' },
        replace: true,
      })
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <Button
        ref={trigger}
        variant="ghost"
        size="icon"
        aria-label={`Delete environment ${environment}`}
        onClick={() => {
          setConfirmation('')
          setError('')
          setOpen(true)
        }}
      >
        <Icon name="trash" size={14} />
      </Button>
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!busy) setOpen(next)
        }}
        title={`Delete ${environment}?`}
        description={`${project} / ${environment}`}
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          trigger.current?.focus()
        }}
      >
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void remove()
          }}
        >
          <div className="dialog-body field-stack">
            <p>
              Delete resources, reclaim retained storage and remove scope grants first. Pending
              operations must finish before you delete this environment.
            </p>
            <p>
              This removes the empty environment and revokes its scoped API keys. The environment ID
              stays reserved. This cannot be undone.
            </p>
            <label className="field">
              <span>
                Type the environment ID <strong>{environment}</strong> to confirm
              </span>
              <Input
                autoComplete="off"
                value={confirmation}
                onChange={(event) => setConfirmation(event.target.value)}
                disabled={busy}
              />
            </label>
            {error && <FormError>{error}</FormError>}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="danger" disabled={busy || confirmation !== environment}>
              <Icon name="trash" size={14} />
              {busy ? 'Deleting…' : 'Delete environment'}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  )
}
