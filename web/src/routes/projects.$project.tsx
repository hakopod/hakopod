import { createFileRoute, Link } from '@tanstack/react-router'
import { ApplicationList } from '../components/application-list'
import { Empty, ErrorState, Loading, PageHeader } from '../components/shared'
import { Button } from '../components/ui/button'
import { resolveProjectRouteScope, useProjects } from '../lib/projects'

export const Route = createFileRoute('/projects/$project')({
  validateSearch: (search: Record<string, unknown>): { environment?: string } => ({
    environment:
      search.environment === undefined
        ? undefined
        : typeof search.environment === 'string'
          ? search.environment
          : '',
  }),
  component: ProjectApplications,
})

function ProjectApplications() {
  const { project: projectName } = Route.useParams()
  const { environment } = Route.useSearch()
  const projects = useProjects()
  const selected = resolveProjectRouteScope(projects.data?.items, projectName, environment)
  if (projects.isPending || (selected.status === 'missing-project' && projects.isFetching))
    return <Loading />
  if (projects.error)
    return <ErrorState error={projects.error} retry={() => void projects.refetch()} />
  if (selected.status === 'missing-project')
    return (
      <div className="ops-page project-unavailable-page">
        <PageHeader title="Project unavailable" />
        <Empty
          title="This project is not available"
          description="It may have been removed, or your account may no longer have access."
          action={
            <Button asChild>
              <Link to="/">View projects</Link>
            </Button>
          }
        />
      </div>
    )
  if (selected.status === 'missing-environment')
    return (
      <div className="ops-page project-unavailable-page">
        <PageHeader title="Environment unavailable" />
        <Empty
          title={
            selected.project?.environments.length
              ? 'Choose an available environment'
              : 'No environments available'
          }
          description={
            selected.project?.environments.length
              ? 'This environment is not available in the selected project.'
              : 'Create an environment for this project, or ask a project administrator for access.'
          }
          action={
            selected.project?.environments.length ? (
              <div className="toolbar-actions">
                {selected.project.environments.map((item) => (
                  <Button key={item.name} asChild>
                    <Link
                      to="/projects/$project"
                      params={{ project: projectName }}
                      search={{ environment: item.name }}
                    >
                      {item.name}
                    </Link>
                  </Button>
                ))}
              </div>
            ) : (
              <Button asChild>
                <Link to="/">View projects</Link>
              </Button>
            )
          }
        />
      </div>
    )
  if (selected.status !== 'ready' || !selected.project) return <Loading />
  return (
    <ApplicationList
      key={`${selected.project.name}/${selected.environment}`}
      project={selected.project}
      environment={selected.environment}
    />
  )
}
