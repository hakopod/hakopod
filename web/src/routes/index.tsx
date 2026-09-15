import { RenameResource } from '../components/rename-resource'
import { DeleteResource } from '../components/delete-resource'
import { lazy, Suspense, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { useProjects } from '../lib/projects'
import { useScope } from '../lib/scope'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Badge, Tooltip } from '../components/ui/surfaces'
import { Icon } from '../components/icons'
import { Copy, Empty, ErrorState, Loading, PageHeader } from '../components/shared'

const ProjectWizard = lazy(() => import('../components/project-wizard'))

export const Route = createFileRoute('/')({ component: Projects })

function Projects() {
  const scope = useScope()
  const projects = useProjects()
  const navigate = useNavigate()
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [projectOpen, setProjectOpen] = useState(false)
  const items = projects.data?.items || []
  const needle = search.trim().toLowerCase()
  const filtered = items.filter((project) =>
    `${project.name} ${project.display_name || ''} ${project.description || ''}`
      .toLowerCase()
      .includes(needle),
  )
  const pages = Math.max(1, Math.ceil(filtered.length / 24))
  const currentPage = Math.min(page, pages)
  const visible = filtered.slice((currentPage - 1) * 24, currentPage * 24)
  return (
    <div className="ops-page projects-page">
      <PageHeader
        title="Projects"
        description="Choose a project to open its applications and environments."
        action={
          scope.identity.admin && (
            <Button variant="primary" onClick={() => setProjectOpen(true)}>
              <Icon name="plus" size={15} />
              New project
            </Button>
          )
        }
      />
      <dl className="application-summary" aria-label="Project summary">
        <div>
          <dt>Projects</dt>
          <dd>{projects.data ? items.length : '—'}</dd>
        </div>
        <div>
          <dt>Environments</dt>
          <dd>
            {projects.data
              ? items.reduce((total, project) => total + project.environments.length, 0)
              : '—'}
          </dd>
        </div>
      </dl>
      <section className="projects-catalog" aria-label="Projects">
        <div className="resource-toolbar">
          <div className="ops-search-input">
            <Icon name="search" size={15} />
            <Input
              aria-label="Filter projects"
              placeholder="Find project…"
              value={search}
              onChange={(event) => {
                setSearch(event.target.value)
                setPage(1)
              }}
            />
          </div>
          <span className="form-spacer" />
          <Tooltip content="Refresh projects" side="bottom">
            <Button
              variant="ghost"
              size="icon"
              aria-label="Refresh projects"
              onClick={() => void projects.refetch()}
            >
              <Icon name="refresh" size={15} className={projects.isFetching ? 'spin' : ''} />
            </Button>
          </Tooltip>
        </div>
        {projects.isPending ? (
          <Loading />
        ) : projects.error ? (
          <ErrorState error={projects.error} retry={() => void projects.refetch()} />
        ) : !items.length ? (
          <Empty
            title="No projects yet"
            description={
              scope.identity.admin
                ? 'Create a project to organize your applications and environments.'
                : 'Projects appear here when your administrator gives you access.'
            }
            action={
              scope.identity.admin && (
                <Button onClick={() => setProjectOpen(true)}>Create project</Button>
              )
            }
          />
        ) : !filtered.length ? (
          <Empty
            title="No matching projects"
            description="Try another project name or description."
          />
        ) : (
          <div className="ops-catalog-grid project-catalog-grid" aria-label="Project cards">
            {visible.map((project) => {
              const name = project.display_name || project.name
              return (
                <article className="ops-catalog-card project-card interactive" key={project.id}>
                  <Brackets />
                  <div className="ops-card-heading">
                    {project.personal !== undefined && (
                      <Badge>{project.personal ? 'Personal' : 'Shared'}</Badge>
                    )}
                    <div className="ops-card-actions relative z-10 ml-auto flex items-center gap-2">
                      <RenameResource project={project} />
                      {scope.identity.admin && !project.personal && (
                        <DeleteResource project={project.name} trigger="icon" />
                      )}
                    </div>
                  </div>
                  <h2>
                    <Link
                      className="ops-card-link"
                      to="/projects/$project"
                      params={{ project: project.name }}
                      search={{ environment: project.environments[0]?.name }}
                      aria-label={`Open ${name} project`}
                    >
                      {name}
                    </Link>
                  </h2>
                  <div className="ops-object-id">
                    <code title={project.name}>{project.name}</code>
                    <Copy value={project.name} />
                  </div>
                  {project.description && (
                    <p className="project-card-description">{project.description}</p>
                  )}
                  {project.environments.length > 0 && (
                    <ul className="project-card-environments" aria-label={`${name} environments`}>
                      {project.environments.slice(0, 3).map((environment) => (
                        <li key={environment.name}>{environment.name}</li>
                      ))}
                      {project.environments.length > 3 && (
                        <li>
                          <span aria-hidden="true">+{project.environments.length - 3}</span>
                          <span className="sr-only">
                            {project.environments.length - 3} more environments:{' '}
                            {project.environments
                              .slice(3)
                              .map((item) => item.name)
                              .join(', ')}
                          </span>
                        </li>
                      )}
                    </ul>
                  )}
                  <div className="ops-card-footer">
                    <span>
                      {project.environments.length}{' '}
                      {project.environments.length === 1 ? 'environment' : 'environments'}
                    </span>
                    <Icon name="arrow" size={15} />
                  </div>
                </article>
              )
            })}
          </div>
        )}
        {pages > 1 && !projects.error && (
          <div className="list-pagination">
            <Button size="sm" disabled={currentPage === 1} onClick={() => setPage(currentPage - 1)}>
              Previous
            </Button>
            <span className="muted-text" role="status">
              Page {currentPage} of {pages}
            </span>
            <Button
              size="sm"
              disabled={currentPage === pages}
              onClick={() => setPage(currentPage + 1)}
            >
              Next
            </Button>
          </div>
        )}
      </section>
      {scope.identity.admin && projectOpen && (
        <Suspense fallback={null}>
          <ProjectWizard
            open={projectOpen}
            onOpenChange={setProjectOpen}
            onCreated={(project, environment) => {
              void projects.refetch().then(() =>
                navigate({
                  to: '/projects/$project',
                  params: { project },
                  search: { environment },
                }),
              )
            }}
          />
        </Suspense>
      )}
    </div>
  )
}
