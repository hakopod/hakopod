import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { Copy, Loading, PageHeader } from '../components/shared'
import { Button } from '../components/ui/button'
import {
  ResourceEmpty,
  ResourceError,
  ResourceRow,
  ResourceRows,
  StaleNotice,
} from '../components/resource-card'
import { ProviderMark, WorkflowStatus, buildMethodLabel } from '../components/build-summary'

// Filters earn their place only once the list is long enough to scan.
const filterThreshold = 6

export const Route = createFileRoute('/builds')({ component: Builds })
function Builds() {
  const location = useLocation()
  if (location.pathname !== '/builds') return <Outlet />
  return <BuildList />
}
function BuildList() {
  const scope = useScope()
  const [search, setSearch] = useState('')
  const [provider, setProvider] = useState('all')
  const builds = useQuery({
    queryKey: ['builds', scope.project, scope.environment],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/builds', {
          signal,
          params: { query: { project: scope.project, environment: scope.environment } },
        }),
      ),
    gcTime: 0,
  })
  const items = builds.data?.items || []
  const providers = new Set(items.map((build) => build.provider))
  const filtered = items.filter(
    (build) =>
      `${build.name} ${build.service} ${build.repository}`
        .toLowerCase()
        .includes(search.trim().toLowerCase()) &&
      (provider === 'all' || build.provider === provider),
  )
  const canWrite = scope.can('deployments:write')
  return (
    <div className="ops-page builds-page">
      <PageHeader
        eyebrow="WORKSPACE / SOURCE BUILDS"
        title="Source builds"
        description="Each source build turns a repository branch into a verified image. Builds run in your GitHub Actions or GitLab CI account and use its runners and registry; Hakopod has no always-running builder."
        action={
          // An empty list offers its one action in the empty state instead.
          canWrite &&
          !(builds.data && !items.length) && (
            <Button asChild variant="primary">
              <Link to="/builds/new">
                <Icon name="plus" size={15} />
                New source build
              </Link>
            </Button>
          )
        }
      />
      <section className="resource-section builds-section" aria-labelledby="builds-heading">
        <div className="section-toolbar builds-toolbar">
          <div className="builds-heading">
            <h2 id="builds-heading">Build configurations</h2>
            {items.length > 0 && <span className="builds-count">{items.length}</span>}
          </div>
          {items.length >= filterThreshold && (
            <div className="builds-filters">
              <div className="ops-search-input">
                <Icon name="search" size={15} />
                <Input
                  type="search"
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                  aria-label="Filter source builds"
                  placeholder="Find build or repository…"
                />
              </div>
              {providers.size > 1 && (
                <SelectField
                  label="Git provider"
                  className="compact-select"
                  value={provider}
                  onValueChange={setProvider}
                  options={[
                    { value: 'all', label: 'All providers' },
                    { value: 'github', label: 'GitHub' },
                    { value: 'gitlab', label: 'GitLab' },
                  ]}
                />
              )}
            </div>
          )}
        </div>
        {builds.isPending ? (
          <Loading />
        ) : builds.error && !builds.data ? (
          <ResourceError
            error={builds.error}
            retry={() => void builds.refetch()}
            subject="source builds"
          />
        ) : !items.length ? (
          <ResourceEmpty
            icon="code"
            title={`No source builds in ${scope.project} / ${scope.environment}`}
            description="A source build turns a repository branch into a verified image you can deploy."
            action={
              canWrite && (
                <Button asChild variant="primary">
                  <Link to="/builds/new">Build from a repository</Link>
                </Button>
              )
            }
          />
        ) : !filtered.length ? (
          <ResourceEmpty
            icon="search"
            title="No builds match these filters"
            action={
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setSearch('')
                  setProvider('all')
                }}
              >
                Clear filters
              </Button>
            }
          />
        ) : (
          <>
            {builds.error && (
              <StaleNotice updatedAt={builds.dataUpdatedAt} retry={() => void builds.refetch()} />
            )}
            <ResourceRows label="Source build configurations" className="builds-rows">
              {filtered.map((build) => (
                <ResourceRow
                  key={build.id}
                  identity={
                    <Link
                      className="resource-row-link"
                      to="/builds/$buildId"
                      params={{ buildId: build.id }}
                    >
                      {build.name} / {build.service}
                    </Link>
                  }
                  detail={
                    <>
                      <span className="builds-repository">
                        <ProviderMark provider={build.provider} />
                        <code>{build.repository}</code>
                      </span>
                      <span className="builds-branch">
                        <Icon name="branch" size={13} />
                        <code>{build.branch}</code>
                      </span>
                      <span>
                        {buildMethodLabel(build)} · Linux {build.architecture.toUpperCase()}
                      </span>
                    </>
                  }
                  status={
                    <span className="builds-state">
                      <WorkflowStatus build={build} />
                      <span>Configuration r{build.revision}</span>
                    </span>
                  }
                  meta={
                    <span className="builds-automation">
                      <span>{build.auto_build ? 'Builds on push' : 'Manual builds'}</span>
                      <span>
                        {build.auto_deploy ? 'Deploys automatically' : 'Deploys after review'}
                      </span>
                    </span>
                  }
                  actions={
                    <Copy
                      value={build.id}
                      label={`Copy build ID for ${build.name} / ${build.service}`}
                      iconOnly
                    />
                  }
                />
              ))}
            </ResourceRows>
          </>
        )}
      </section>
    </div>
  )
}
