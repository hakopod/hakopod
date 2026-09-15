import { useState } from 'react'
import { Field } from './ui/field'
import type { Project } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { projectIDHelp, projectIDPattern } from '../lib/project-input'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'

export const environmentSuggestions = ['development', 'staging', 'production'] as const

export default function ProjectEnvironment({
  project,
  onClose,
  onCreated,
  restoreFocus,
}: {
  project: Project
  onClose: () => void
  onCreated: (environment: string) => void
  restoreFocus: () => void
}) {
  const [environment, setEnvironment] = useState<string>(
    environmentSuggestions.find(
      (name) => !project.environments.some((item) => item.name === name),
    ) || 'preview',
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const exists = project.environments.some((item) => item.name === environment)
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      title="Create an environment"
      onCloseAutoFocus={(event) => {
        event.preventDefault()
        restoreFocus()
      }}
      description={`Keep applications and access separate within ${project.display_name || project.name}.`}
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy) return
          if (!projectIDPattern.test(environment)) {
            setError(projectIDHelp)
            return
          }
          setBusy(true)
          setError('')
          try {
            if (!exists)
              await unwrap(
                client.POST('/projects/{project}/environments', {
                  params: { path: { project: project.name } },
                  body: { name: environment },
                }),
              )
            onCreated(environment)
            onClose()
          } catch (cause) {
            setError(message(cause))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body field-stack">
          <Field
            label="Environment ID"
            value={environment}
            onChange={(event) => setEnvironment(event.target.value)}
            required
            disabled={busy}
            maxLength={40}
            help={projectIDHelp}
            autoComplete="off"
            spellCheck={false}
          />
          <div className="hako-environment-picks" aria-label="Suggested environments">
            {environmentSuggestions.map((name) => (
              <Button
                key={name}
                type="button"
                variant="ghost"
                size="sm"
                disabled={busy}
                onClick={() => {
                  setEnvironment(name)
                  setError('')
                }}
              >
                {name}
              </Button>
            ))}
          </div>
          {exists && (
            <p className="field-help">This environment already exists. Continue to switch to it.</p>
          )}
          {error && (
            <p role="alert" className="inline-error">
              {error}
            </p>
          )}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : exists ? 'Switch environment' : 'Create environment'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
