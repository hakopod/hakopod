import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Badge } from '@hakopod/hatch-ui/components/badge'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { client, unwrap } from '../lib/client'
import { Icon } from '../components/icons'
import { ServiceIcon } from '../components/service-icon'
import { ComputeNotice } from '../components/compute-notice'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Loading, PageHeader } from '../components/shared'
import { ResourceEmpty, ResourceError } from '../components/resource-card'

export const Route = createFileRoute('/templates')({
  validateSearch: (
    search: Record<string, unknown>,
  ): { q?: string; category?: string; application?: string; runner?: string } => ({
    runner: typeof search.runner === 'string' ? search.runner : undefined,
    application: typeof search.application === 'string' ? search.application : undefined,
    q: typeof search.q === 'string' ? search.q.slice(0, 100) : undefined,
    category: typeof search.category === 'string' ? search.category.slice(0, 40) : undefined,
  }),
  component: TemplatesRoute,
})
function TemplatesRoute() {
  return useLocation().pathname === '/templates' ? <Templates /> : <Outlet />
}
const labels: Record<string, string> = {
  application: 'Applications',
  database: 'Databases',
  agent: 'Agents',
  ai: 'AI & models',
  automation: 'Automation',
}
function Templates() {
  const features = useEditionFeatures()
  const { q = '', category = '', application } = Route.useSearch()
  const navigate = Route.useNavigate()
  const templates = useQuery({
    queryKey: ['templates'],
    queryFn: ({ signal }) => unwrap(client.GET('/templates', { signal })),
    staleTime: 300000,
  })
  const items = templates.data?.items || []
  const categories = [...new Set(items.map((item) => item.category))]
  const matches = items.filter(
    (item) =>
      (!category || item.category === category) &&
      `${item.name} ${item.description} ${item.category}`.toLowerCase().includes(q.toLowerCase()),
  )
  const filter = (next: { q?: string; category?: string }, replace = false) =>
    void navigate({
      search: {
        application,
        q: (next.q ?? q) || undefined,
        category: (next.category ?? category) || undefined,
      },
      replace,
    })
  return (
    <div className="catalog-page templates-page">
      <PageHeader
        eyebrow="WORKSPACE / CATALOG"
        title={category === 'agent' ? 'Agents' : 'Catalog'}
        description="Choose a database, application, or AI service. Each template shows what it creates and what it needs before you deploy."
      />
      {application && (
        <div className="templates-target">
          <span>Adding services to an existing application.</span>
          <Button asChild size="sm" variant="ghost">
            <Link to="/applications/$applicationId" params={{ applicationId: application }}>
              Back to application
            </Link>
          </Button>
        </div>
      )}
      <div className="templates-toolbar">
        <label className="templates-search">
          <Icon name="search" size={16} />
          <span className="sr-only">Search templates</span>
          <Input
            type="search"
            value={q}
            maxLength={100}
            placeholder="Search templates"
            onChange={(event) => filter({ q: event.target.value }, true)}
          />
        </label>
        <div className="catalog-categories" role="group" aria-label="Category">
          {[['', 'All'], ...categories.map((value) => [value, labels[value] || value])].map(
            ([value, label]) => (
              <Button
                key={value}
                variant="chip"
                aria-pressed={category === value}
                onClick={() => filter({ category: value })}
              >
                {label}{' '}
                <span className="catalog-category-count">
                  {value ? items.filter((item) => item.category === value).length : items.length}
                </span>
              </Button>
            ),
          )}
        </div>
        <span className={q ? 'templates-count' : 'sr-only'} role="status">
          {templates.data &&
            (q ? `${matches.length} of ${items.length}` : `${matches.length} templates`)}
        </span>
      </div>
      <ComputeNotice />
      {templates.isPending ? (
        <Loading rows={4} />
      ) : templates.error && !templates.data ? (
        <ResourceError
          error={templates.error}
          retry={() => void templates.refetch()}
          subject="the template catalog"
        />
      ) : !matches.length ? (
        <ResourceEmpty
          icon="search"
          title="No matching templates"
          description="Try a shorter search or choose another category."
          action={
            <Button onClick={() => void navigate({ search: { application } })}>
              Clear filters
            </Button>
          }
        />
      ) : (
        <ul className="templates-grid" aria-label="Templates">
          {matches.map((template) => (
            <li key={template.id} className="ops-catalog-card template-card interactive">
              <Brackets />
              <div className="template-card-top">
                <ServiceIcon
                  background={template.logo_background}
                  src={template.logo || undefined}
                  name={template.id}
                  size={24}
                />
                <h2>
                  <Link
                    className="template-card-link"
                    to="/templates/$templateId"
                    params={{ templateId: template.id }}
                    search={{ application }}
                  >
                    {template.name}
                  </Link>
                </h2>
                {template.id === 'managed-actions' && <Badge>Pro</Badge>}
              </div>
              <p className="template-card-description" title={template.description}>
                {template.description}
              </p>
              <p className="template-card-meta">
                {features.hostedCompute && template.workload_requirements?.length
                  ? 'Requires your server'
                  : template.deployable
                    ? template.architectures.join(' · ')
                    : 'Deployment guide'}
              </p>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
