import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { Copy, Loading, Note, PageHeader, Status } from '../components/shared'
import { Button } from '../components/ui/button'
import {
  ResourceEmpty,
  ResourceError,
  ResourceRow,
  ResourceRows,
  StaleNotice,
} from '../components/resource-card'

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
  const filtered = items.filter(
    (build) =>
      `${build.name} ${build.service} ${build.repository}`
        .toLowerCase()
        .includes(search.toLowerCase()) &&
      (provider === 'all' || build.provider === provider),
  )
  return (
    <div className="ops-page">
      <PageHeader
        eyebrow="WORKSPACE / SOURCE BUILDS"
        title="Source builds"
        description="Build container images with GitHub Actions or GitLab CI, using Dockerfiles or Cloud Native Buildpacks."
        action={
          scope.can('deployments:write') && (
            <Button asChild variant="primary">
              <Link to="/builds/new">
                <Icon name="plus" size={15} />
                New source build
              </Link>
            </Button>
          )
        }
      />
      <section className="ops-resource-list resource-section" aria-label="Source builds">
        <div className="resource-toolbar">
          <div className="ops-search-input">
            <Icon name="search" size={15} />
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              aria-label="Filter source builds"
              placeholder="Find build or repository…"
            />
          </div>
          <SelectField
            label={'Git provider'}
            className="compact-select"
            value={provider}
            onValueChange={(value) => setProvider(value)}
            options={[
              {
                value: 'all',
                label: 'All providers',
              },
              {
                value: 'github',
                label: 'GitHub',
              },
              {
                value: 'gitlab',
                label: 'GitLab',
              },
            ]}
          />
          <span className="form-spacer" />
          {!builds.isPending && !builds.error && (
            <span className="muted-text">{items.length} build configurations</span>
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
            title="No source builds"
            description="A source build turns a repository into a verified image you can deploy."
            action={
              scope.can('deployments:write') && (
                <Button asChild>
                  <Link to="/builds/new">Create source build</Link>
                </Button>
              )
            }
          />
        ) : !filtered.length ? (
          <ResourceEmpty
            icon="search"
            title="No matching source builds"
            description="Change the name or provider filter."
          />
        ) : (
          <>
            {builds.error && (
              <StaleNotice updatedAt={builds.dataUpdatedAt} retry={() => void builds.refetch()} />
            )}
            <ResourceRows label="Source build configurations">
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
                      <span className="inline-flex min-w-0 items-center gap-1">
                        <Icon name={build.provider === 'gitlab' ? 'gitlab' : 'github'} size={14} />
                        <code>{build.repository}</code>
                      </span>
                      <code>{build.branch}</code>
                      <span>
                        {build.mode === 'framework'
                          ? `${build.framework?.framework} · ${build.framework?.runtime}`
                          : build.mode === 'buildpacks'
                            ? `Buildpacks · ${build.preset}`
                            : 'Dockerfile'}{' '}
                        · {build.architecture}
                      </span>
                    </>
                  }
                  status={
                    <Status
                      value={
                        build.installed_revision === build.revision
                          ? 'installed'
                          : 'installation required'
                      }
                      small
                    />
                  }
                  meta={
                    <>
                      {build.auto_build ? 'Build on push' : 'Manual build'}
                      <br />
                      {build.auto_deploy ? 'Automatic deployment' : 'Manual deployment review'}
                    </>
                  }
                  actions={<Copy value={build.id} label={`Copy build ID ${build.id}`} />}
                />
              ))}
            </ResourceRows>
          </>
        )}
      </section>
      <Note>
        Builds use the selected Git provider’s runner capacity and container registry. Hakopod has
        no always-running builder and does not load application build tools into the management
        process.
      </Note>
    </div>
  )
}
