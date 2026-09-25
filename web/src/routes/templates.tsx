import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Badge } from '@hakopod/hatch-ui/components/badge'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { ServiceIcon } from '../components/service-icon'
import { ComputeNotice } from '../components/compute-notice'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Dialog } from '../components/ui/dialog'
import { Copy, Empty, ErrorState, Loading, Note, PageHeader } from '../components/shared'

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
  const scope = useScope()
  const features = useEditionFeatures()
  const { q = '', category = '', application } = Route.useSearch()
  const navigate = Route.useNavigate()
  const [selected, setSelected] = useState('')
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
  const detail = items.find((item) => item.id === selected)
  return (
    <div className="catalog-page">
      <PageHeader
        eyebrow="WORKSPACE / CATALOG"
        title={category === 'agent' ? 'Agents' : 'Catalog'}
        description="Choose a database, application, or AI service. Review its requirements before deploying."
      />
      {application && (
        <div className="flex flex-wrap items-center gap-2 py-3">
          <span className="text-sm">Adding to an existing application</span>
          <Button asChild size="sm">
            <Link to="/applications/$applicationId" params={{ applicationId: application }}>
              Back to application
            </Link>
          </Button>
        </div>
      )}
      <div className="catalog-toolbar">
        <nav className="catalog-categories" aria-label="Template categories">
          {[['', 'All'], ...categories.map((value) => [value, labels[value] || value])].map(
            ([value, label]) => (
              <Button
                key={value}
                variant="chip"
                aria-pressed={category === value}
                onClick={() =>
                  void navigate({
                    search: { application, q: q || undefined, category: value || undefined },
                  })
                }
              >
                {label}{' '}
                <span className="catalog-category-count">
                  {value ? items.filter((item) => item.category === value).length : items.length}
                </span>
              </Button>
            ),
          )}
        </nav>
        <label className="catalog-search">
          <Icon name="search" size={18} />
          <span className="sr-only">Search templates</span>
          <Input
            value={q}
            maxLength={100}
            placeholder="Search templates…"
            onChange={(event) =>
              void navigate({
                search: {
                  application,
                  q: event.target.value || undefined,
                  category: category || undefined,
                },
                replace: true,
              })
            }
          />
        </label>
      </div>
      <div className="catalog-result-count" role="status">
        {matches.length} {matches.length === 1 ? 'template' : 'templates'}
      </div>
      <ComputeNotice />
      {templates.isPending ? (
        <Loading rows={4} />
      ) : templates.error ? (
        <ErrorState error={templates.error} retry={() => void templates.refetch()} />
      ) : !matches.length ? (
        <Empty
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
        <div className="catalog-grid template-catalog">
          {matches.map((template) => (
            <button
              key={template.id}
              type="button"
              className="catalog-card interactive"
              onClick={() => setSelected(template.id)}
            >
              <Brackets />
              <div className="catalog-card-header">
                <ServiceIcon
                  background={template.logo_background}
                  src={template.logo || undefined}
                  name={template.id}
                  size={28}
                />
                <Badge>{labels[template.category] || template.category}</Badge>
                {template.id === 'managed-actions' && <Badge>Pro</Badge>}
              </div>
              <h2>{template.name}</h2>
              <span className="catalog-slug">{template.id}</span>
              <p className="catalog-description">{template.description}</p>
              <span className="catalog-card-meta">
                {features.hostedCompute && template.workload_requirements?.length
                  ? 'Requires your server'
                  : template.deployable
                    ? template.architectures.join(' / ')
                    : 'Deployment guide'}
                <Icon name="arrow" size={16} />
              </span>
            </button>
          ))}
          {[0, 1, 2, 3].map((index) => (
            <div key={`filler-${index}`} className="catalog-filler" aria-hidden="true" />
          ))}
        </div>
      )}
      <Dialog
        sheet
        open={Boolean(detail)}
        onOpenChange={(open) => {
          if (!open) setSelected('')
        }}
        title={detail?.name || 'Template'}
        description={detail?.description || 'Requirements and deployment options.'}
      >
        {detail && (
          <>
            <div className="dialog-body catalog-detail">
              <div className="catalog-detail-identity">
                <ServiceIcon
                  background={detail.logo_background}
                  src={detail.logo || undefined}
                  name={detail.id}
                  size={36}
                />
                <span className="copy-id">
                  <code>{detail.id}</code>
                  <Copy value={detail.id} />
                </span>
                <Badge>{labels[detail.category] || detail.category}</Badge>
              </div>
              <dl className="service-definition-list">
                <div>
                  <dt>License</dt>
                  <dd>{detail.license}</dd>
                </div>
                <div>
                  <dt>Architectures</dt>
                  <dd>{detail.architectures.join(' / ') || 'See deployment guide'}</dd>
                </div>
                <div>
                  <dt>Resources</dt>
                  <dd>{detail.resource_summary}</dd>
                </div>
              </dl>
              {detail.required_secrets.length > 0 && (
                <section>
                  <h3>
                    {detail.deployable ? 'Required secrets' : 'Candidate credential references'}
                  </h3>
                  <p>
                    {detail.deployable
                      ? 'Set these values during deployment review.'
                      : 'Some providers are alternatives. Finalize required bindings after resolving the migration prerequisites.'}
                  </p>
                  <div className="catalog-secret-list">
                    {detail.required_secrets.map((name) => (
                      <code key={name}>{name}</code>
                    ))}
                  </div>
                </section>
              )}
              {detail.requirements.length > 0 && (
                <section>
                  <h3>Before you deploy</h3>
                  <ul>
                    {detail.requirements.map((requirement) => (
                      <li key={requirement}>{requirement}</li>
                    ))}
                  </ul>
                </section>
              )}
              {detail.verification && <Note>{detail.verification}</Note>}
              {detail.configuration && (
                <section>
                  <h3>Configuration</h3>
                  <p>{detail.configuration}</p>
                </section>
              )}
              {detail.upstream.startsWith('https://') && (
                <a href={detail.upstream} target="_blank" rel="noreferrer" className="text-link">
                  View upstream project <Icon name="external" size={14} />
                </a>
              )}
            </div>
            <div className="dialog-footer">
              <Button variant="ghost" onClick={() => setSelected('')}>
                Close
              </Button>
              {scope.can('deployments:write') || !detail.deployable ? (
                <Button asChild variant="primary">
                  <Link
                    to="/templates/$templateId"
                    params={{ templateId: detail.id }}
                    search={{ application }}
                  >
                    {features.hostedCompute && detail.workload_requirements?.length
                      ? 'View requirements'
                      : detail.deployable
                        ? 'Use template'
                        : 'Open deployment guide'}
                    <Icon name="arrow" size={14} />
                  </Link>
                </Button>
              ) : (
                <p>Deployment access required</p>
              )}
            </div>
          </>
        )}
      </Dialog>
    </div>
  )
}
