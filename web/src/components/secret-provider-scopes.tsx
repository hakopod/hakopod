import { useState } from 'react'
import type { Project } from '../lib/types'
import type { ProviderScope } from '../lib/secret-providers'
import { Input } from './ui/input'
import { Button } from './ui/button'

export function SecretProviderScopes({
  projects,
  scopes,
  onChange,
  disabled = false,
}: {
  projects: Project[]
  scopes: ProviderScope[]
  onChange: (scopes: ProviderScope[]) => void
  disabled?: boolean
}) {
  const [search, setSearch] = useState('')
  const missing = scopes.filter(
    (scope) => !projects.some((project) => project.name === scope.project),
  )
  const all = [
    ...projects,
    ...missing.map((scope) => ({
      id: scope.project,
      name: scope.project,
      environments: (scope.environments || []).map((name) => ({ name })),
      display_name: '',
    })),
  ]
  const shown = all.filter((project) =>
    `${project.name} ${project.display_name || ''}`.toLowerCase().includes(search.toLowerCase()),
  )
  return (
    <div className="grid gap-3">
      <p className="field-help">
        Only selected projects can use this provider. Leave a project's environments unselected to
        allow all of its environments.
      </p>
      {projects.length === 0 && (
        <p className="field-help">
          Create a project before configuring a provider. At least one project must be granted
          access.
        </p>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="muted-text text-sm">{scopes.length} of 32 projects selected</span>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={disabled || scopes.length === 0}
          onClick={() => onChange([])}
        >
          Clear all
        </Button>
      </div>
      {projects.length > 8 && (
        <label>
          Find a project
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="Project name"
          />
        </label>
      )}
      <div className="grid gap-2">
        {shown.map((project) => {
          const selected = scopes.find((scope) => scope.project === project.name)
          const environments = [
            ...project.environments,
            ...(selected?.environments || [])
              .filter(
                (name) => !project.environments.some((environment) => environment.name === name),
              )
              .map((name) => ({ name, unavailable: true })),
          ]
          return (
            <div
              key={project.name}
              className="grid gap-2 border-b border-[var(--hairline)] py-2 last:border-b-0"
            >
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={Boolean(selected)}
                  disabled={disabled || (!selected && scopes.length >= 32)}
                  onChange={(event) =>
                    onChange(
                      event.target.checked
                        ? [...scopes, { project: project.name, environments: [] }]
                        : scopes.filter((scope) => scope.project !== project.name),
                    )
                  }
                />
                <span>
                  {project.display_name || project.name}
                  {project.display_name && project.display_name !== project.name && (
                    <small className="muted-text ml-2">{project.name}</small>
                  )}
                  {missing.some((scope) => scope.project === project.name) && (
                    <small className="muted-text ml-2">No longer available</small>
                  )}
                </span>
              </label>
              {selected && (
                <fieldset className="ml-6 grid gap-2 border-0 p-0">
                  <legend className="muted-text mb-2 text-xs">
                    {selected.environments?.length
                      ? 'Selected environments'
                      : 'All environments, including new ones'}
                  </legend>
                  <div className="flex flex-wrap gap-x-5 gap-y-2">
                    {environments.map((environment) => (
                      <label key={environment.name} className="checkbox-row">
                        <Input
                          type="checkbox"
                          aria-label={`${project.name} / ${environment.name}`}
                          checked={selected.environments?.includes(environment.name) || false}
                          disabled={disabled}
                          onChange={(event) =>
                            onChange(
                              scopes.map((scope) =>
                                scope.project === project.name
                                  ? {
                                      ...scope,
                                      environments: event.target.checked
                                        ? [...(scope.environments || []), environment.name]
                                        : (scope.environments || []).filter(
                                            (name) => name !== environment.name,
                                          ),
                                    }
                                  : scope,
                              ),
                            )
                          }
                        />
                        {environment.name}
                        {'unavailable' in environment && (
                          <small className="muted-text">No longer available</small>
                        )}
                      </label>
                    ))}
                    {environments.length === 0 && (
                      <span className="muted-text text-sm">
                        This project has no environments yet.
                      </span>
                    )}
                  </div>
                </fieldset>
              )}
            </div>
          )
        })}
        {shown.length === 0 && <p className="muted-text text-sm">No matching projects.</p>}
      </div>
    </div>
  )
}
