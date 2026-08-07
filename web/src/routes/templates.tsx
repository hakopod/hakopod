import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { ServiceIcon } from '../components/service-icon'
import { Empty, ErrorState, Loading, PageHeader } from '../components/shared'

export const Route = createFileRoute('/templates')({
  validateSearch: (search: Record<string, unknown>): { q?: string; category?: string } => ({
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
  agent: 'Agents Deploy',
  ai: 'AI & models',
}
function Templates() {
  const scope = useScope()
  const { q = '', category = '' } = Route.useSearch()
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
  return (
    <>
      <PageHeader
        eyebrow="WORKSPACE / CATALOG"
        title={category === 'agent' ? 'Agents Deploy' : 'Find your next building block.'}
        description={
          category === 'agent'
            ? 'Self-hosted agent tools and interfaces, with explicit resources and deployment review.'
            : 'Databases, useful applications, and AI services for infrastructure you own.'
        }
      />
      <div className="catalog-toolbar">
        <label className="catalog-search">
          <Icon name="search" size={18} />
          <span className="sr-only">Search templates</span>
          <input
            value={q}
            maxLength={100}
            placeholder="Search applications, databases, and agents"
            onChange={(event) =>
              void navigate({
                search: { q: event.target.value || undefined, category: category || undefined },
                replace: true,
              })
            }
          />
        </label>
        <span className="muted-text">
          {matches.length} {matches.length === 1 ? 'template' : 'templates'}
        </span>
      </div>
      <nav className="catalog-categories" aria-label="Template categories">
        <button
          className={!category ? 'selected' : ''}
          onClick={() => void navigate({ search: { q: q || undefined } })}
        >
          All <span>{items.length}</span>
        </button>
        {categories.map((value) => (
          <button
            key={value}
            className={category === value ? 'selected' : ''}
            onClick={() => void navigate({ search: { q: q || undefined, category: value } })}
          >
            {labels[value] || value}
            <span>{items.filter((item) => item.category === value).length}</span>
          </button>
        ))}
      </nav>
      {templates.isPending ? (
        <Loading />
      ) : templates.error ? (
        <ErrorState error={templates.error} retry={() => void templates.refetch()} />
      ) : !matches.length ? (
        <Empty
          icon="search"
          title="No matching templates"
          description="Try a shorter search or choose another category."
        />
      ) : (
        <div className="catalog-grid template-catalog">
          {matches.map((template) => (
            <article className="panel catalog-card" key={template.id}>
              <div className="title-row">
                <div className="template-icon">
                  <ServiceIcon name={template.id} size={32} />
                </div>
                <span className="label-chip">{labels[template.category] || template.category}</span>
              </div>
              <h2>{template.name}</h2>
              <p>{template.description}</p>
              <div className="template-card-meta">
                <span>{template.license}</span>
                <span>{template.architectures.join(' / ') || 'Deployment guide'}</span>
                {template.required_secrets.length > 0 && (
                  <span>
                    <Icon name="lock" size={12} /> {template.required_secrets.length} required{' '}
                    {template.required_secrets.length === 1 ? 'secret' : 'secrets'}
                  </span>
                )}
              </div>
              <p className="template-resource-summary">{template.resource_summary}</p>
              {template.requirements.length > 0 && (
                <p className="template-requirement">
                  <Icon name="info" size={13} />
                  {template.requirements[0]}
                </p>
              )}
              <div className="toolbar-actions">
                {scope.can('deployments:write') || !template.deployable ? (
                  <Link
                    className="button button-primary"
                    to="/templates/$templateId"
                    params={{ templateId: template.id }}
                  >
                    {template.deployable ? 'Configure' : 'Deployment guide'}{' '}
                    <Icon name="arrow" size={14} />
                  </Link>
                ) : (
                  <span className="field-help">Deployment access required</span>
                )}
                {template.upstream.startsWith('https://') && (
                  <a
                    href={template.upstream}
                    target="_blank"
                    rel="noreferrer"
                    className="button button-ghost"
                    aria-label={`${template.name} upstream project`}
                  >
                    <Icon name="external" size={15} />
                  </a>
                )}
              </div>
            </article>
          ))}
        </div>
      )}
    </>
  )
}
