import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
import type { Project } from './types'

export function useProjects() {
  return useQuery({
    queryKey: ['projects'],
    queryFn: ({ signal }) => unwrap(client.GET('/projects', { signal })),
    staleTime: 60000,
  })
}

export type ProjectRouteScope = {
  project: Project | undefined
  environment: string
  status: 'loading' | 'missing-project' | 'missing-environment' | 'ready'
}

// A project URL must never fall back to the previously selected workspace.
export function resolveProjectRouteScope(
  projects: Project[] | undefined,
  projectName: string,
  requestedEnvironment?: unknown,
): ProjectRouteScope {
  if (!projects) return { project: undefined, environment: '', status: 'loading' }
  const project = projects.find((item) => item.name === projectName)
  if (!project) return { project: undefined, environment: '', status: 'missing-project' }
  const environment =
    requestedEnvironment === undefined
      ? project.environments[0]?.name
      : project.environments.find((item) => item.name === requestedEnvironment)?.name
  if (!environment) return { project, environment: '', status: 'missing-environment' }
  return { project, environment, status: 'ready' }
}
