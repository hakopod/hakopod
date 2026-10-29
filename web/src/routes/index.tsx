import { RenameResource } from '../components/rename-resource'
import { DeleteResource } from '../components/delete-resource'
import { lazy, Suspense, useState, type CSSProperties } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useProjects } from '../lib/projects'
import { useScope } from '../lib/scope'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Tooltip } from '../components/ui/surfaces'
import { Icon } from '../components/icons'
import { Copy, Loading, PageHeader } from '../components/shared'
import { relative } from '../lib/api'
import type { components } from '../lib/api.generated'
import {
  GuidedEmpty,
  ResourceCard,
  ResourceEmpty,
  ResourceError,
  StaleNotice,
} from '../components/resource-card'

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
  const all = sumTotals(items.map(projectTotals))
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
        <div>
          <dt>Applications</dt>
          <dd>{projects.data && all.known ? all.applications : '—'}</dd>
        </div>
        <div>
          <dt>Failing</dt>
          <dd className={all.failed ? 'project-summary-failing' : undefined}>
            {projects.data && all.known ? all.failed : '—'}
          </dd>
        </div>
      </dl>
      <section className="projects-catalog" aria-label="Projects">
        {(items.length > 0 || !projects.data) && (
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
        )}
        {projects.isPending ? (
          <Loading />
        ) : projects.error && !projects.data ? (
          <ResourceError
            error={projects.error}
            retry={() => void projects.refetch()}
            subject="projects"
          />
        ) : !items.length && scope.identity.admin ? (
          <GuidedEmpty
            icon="grid"
            satellites={[
              { icon: 'box', label: 'Apps' },
              { icon: 'database', label: 'Data' },
              { icon: 'branch', label: 'Stages' },
              { icon: 'network', label: 'Networks' },
            ]}
            title="Create your first project"
            description="A project holds your applications, databases and networks, with an environment for each stage such as development and production."
            primary={
              <Button variant="primary" onClick={() => setProjectOpen(true)}>
                <Icon name="plus" size={14} />
                Create project
              </Button>
            }
            steps={[
              { title: 'Create a project', detail: 'Name it and choose its first environment.' },
              {
                title: 'Deploy an application',
                detail: 'From a repository, a container image or a Compose file.',
              },
              {
                title: 'Bring in your team',
                detail: 'Share the project and give each person the role they need.',
              },
            ]}
          />
        ) : !items.length ? (
          <ResourceEmpty
            icon="grid"
            title="No projects yet"
            description="Projects appear here when an administrator gives you access."
          />
        ) : !filtered.length ? (
          <ResourceEmpty
            icon="search"
            title="No matching projects"
            description="Try another project name or description."
          />
        ) : (
          <>
            {projects.error && (
              <StaleNotice
                updatedAt={projects.dataUpdatedAt}
                retry={() => void projects.refetch()}
              />
            )}
            <div className="resource-grid" aria-label="Project cards">
              {visible.map((project) => {
                const name = project.display_name || project.name
                const totals = projectTotals(project)
                return (
                  <ResourceCard
                    key={project.id}
                    cue={<Monogram name={name} />}
                    name={
                      <Link
                        className="ops-card-link"
                        to="/projects/$project"
                        params={{ project: project.name }}
                        search={{ environment: project.environments[0]?.name }}
                        aria-label={`Open ${name} project`}
                      >
                        {name}
                      </Link>
                    }
                    menu={
                      <>
                        <Button asChild variant="ghost" size="icon">
                          <Link
                            to="/projects/$project"
                            params={{ project: project.name }}
                            search={{ tab: 'manage' }}
                            aria-label={`Settings for project ${name}`}
                          >
                            <Icon name="settings" size={14} />
                          </Link>
                        </Button>
                        <RenameResource project={project} />
                        {scope.identity.admin && !project.personal && (
                          <DeleteResource project={project.name} trigger="icon" />
                        )}
                      </>
                    }
                    meta={
                      <>
                        {project.personal === undefined
                          ? 'Project'
                          : project.personal
                            ? 'Personal project'
                            : 'Shared project'}
                        {project.description && <> · {project.description}</>}
                      </>
                    }
                    endpoint={
                      project.environments.length ? (
                        <ul
                          className="project-card-environments"
                          aria-label={`${name} environments`}
                        >
                          {project.environments.slice(0, 4).map((environment) => (
                            <li key={environment.name}>
                              <Link
                                to="/projects/$project"
                                params={{ project: project.name }}
                                search={{ environment: environment.name }}
                                title={environmentHealth(environment.summary)}
                              >
                                <span
                                  className="project-env-dot"
                                  data-tone={environmentTone(environment.summary)}
                                  aria-hidden="true"
                                />
                                {environment.name}
                                <span className="sr-only">
                                  : {environmentHealth(environment.summary)}
                                </span>
                              </Link>
                            </li>
                          ))}
                          {project.environments.length > 4 && (
                            <li>
                              <span aria-hidden="true">+{project.environments.length - 4}</span>
                              <span className="sr-only">
                                {project.environments.length - 4} more environments:{' '}
                                {project.environments
                                  .slice(4)
                                  .map((item) => item.name)
                                  .join(', ')}
                              </span>
                            </li>
                          )}
                        </ul>
                      ) : (
                        <span className="resource-card-absent">No environments</span>
                      )
                    }
                    state={<ProjectStats project={project} />}
                    metric={
                      <div className="project-card-footer">
                        <span>
                          {totals.last ? (
                            <time dateTime={totals.last} title={totals.last}>
                              Deployed {relative(totals.last).toLowerCase()}
                            </time>
                          ) : totals.known ? (
                            'Never deployed'
                          ) : null}
                        </span>
                        <div className="copy-id">
                          <code title={project.name}>{project.name}</code>
                          <Copy
                            value={project.name}
                            label={`Copy project ID ${project.name}`}
                            iconOnly
                          />
                        </div>
                      </div>
                    }
                  />
                )
              })}
            </div>
          </>
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

type Project = components['schemas']['Project']
type EnvironmentSummary = Project['environments'][number]['summary']
type Totals = {
  known: boolean
  applications: number
  services: number
  failed: number
  deploying: number
  last?: string
}

function sumTotals(list: Totals[]): Totals {
  return list.reduce<Totals>(
    (total, item) => ({
      known: total.known || item.known,
      applications: total.applications + item.applications,
      services: total.services + item.services,
      failed: total.failed + item.failed,
      deploying: total.deploying + item.deploying,
      last: !item.last || (total.last && total.last > item.last) ? total.last : item.last,
    }),
    { known: false, applications: 0, services: 0, failed: 0, deploying: 0 },
  )
}

function projectTotals(project: Project): Totals {
  return sumTotals(
    project.environments.map(({ summary }) => ({
      known: Boolean(summary),
      applications: summary?.applications ?? 0,
      services: summary?.services ?? 0,
      failed: summary?.failed ?? 0,
      deploying: summary?.deploying ?? 0,
      last: summary?.last_deployed_at,
    })),
  )
}

function environmentTone(summary: EnvironmentSummary) {
  if (!summary || !summary.applications) return 'empty'
  if (summary.failed) return 'failed'
  if (summary.deploying) return 'deploying'
  return 'healthy'
}

function environmentHealth(summary: EnvironmentSummary) {
  if (!summary) return 'Health not available'
  if (!summary.applications) return 'No applications'
  if (summary.failed) return `${summary.failed} failing of ${summary.applications}`
  if (summary.deploying) return `${summary.deploying} deploying`
  return `${summary.applications} healthy`
}

const plural = (count: number, word: string) => `${count} ${word}${count === 1 ? '' : 's'}`

/** Counts across the project's environments, from stored release state. */
function ProjectStats({ project }: { project: Project }) {
  const totals = projectTotals(project)
  if (!totals.known) return null
  return (
    <dl className="project-card-stats">
      <div>
        <dt>Apps</dt>
        <dd>{totals.applications}</dd>
      </div>
      <div>
        <dt>Services</dt>
        <dd>{totals.services}</dd>
      </div>
      <div data-tone={totals.failed ? 'failed' : totals.deploying ? 'deploying' : undefined}>
        <dt>{totals.failed ? 'Failing' : totals.deploying ? 'Deploying' : 'Failing'}</dt>
        <dd title={totals.failed ? plural(totals.failed, 'application') + ' failed' : undefined}>
          {totals.failed || totals.deploying || 0}
        </dd>
      </div>
    </dl>
  )
}

/** Two-letter tile tinted from the project name, so projects are told apart at a glance. */
function Monogram({ name }: { name: string }) {
  const words = name
    .replace(/[^\p{L}\p{N}]+/gu, ' ')
    .trim()
    .split(' ')
  const letters = (
    words.length > 1 ? words[0][0] + words[1][0] : (words[0] || '?').slice(0, 2)
  ).toUpperCase()
  let hue = 0
  for (const char of name) hue = (hue * 31 + char.charCodeAt(0)) % 360
  return (
    <span className="project-monogram" style={{ '--hue': hue } as CSSProperties}>
      {letters}
    </span>
  )
}
