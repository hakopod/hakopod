import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { Copy, Empty, ErrorState, Loading, Note, PageHeader, Status } from '../components/shared'

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
            <Link className="button button-primary" to="/builds/new">
              <Icon name="plus" size={15} />
              New source build
            </Link>
          )
        }
      />
      <section className="ops-resource-list" aria-label="Source builds">
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
        ) : builds.error ? (
          <ErrorState error={builds.error} retry={() => void builds.refetch()} />
        ) : !builds.data?.items.length ? (
          <Empty
            icon="code"
            title="Start with your source code"
            description="Connect a repository, review its generated workflow, build, then deploy the verified image."
            action={
              scope.can('deployments:write') && (
                <Link className="button" to="/builds/new">
                  Create source build
                </Link>
              )
            }
          />
        ) : !filtered.length ? (
          <Empty
            icon="search"
            title="No matching source builds"
            description="Change the name or provider filter."
          />
        ) : (
          <div className="table-container ops-table">
            <table>
              <thead>
                <tr>
                  <th>Service / workflow</th>
                  <th>Repository</th>
                  <th>Build method</th>
                  <th>Automation</th>
                  <th>
                    <span className="sr-only">Open build</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((build) => (
                  <tr key={build.id}>
                    <td>
                      <div className="ops-object">
                        <Status
                          value={
                            build.installed_revision === build.revision
                              ? 'installed'
                              : 'installation required'
                          }
                          small
                        />
                        <Link
                          className="ops-object-name"
                          to="/builds/$buildId"
                          params={{ buildId: build.id }}
                        >
                          {build.name} / {build.service}
                        </Link>
                      </div>
                      <div className="ops-object-id">
                        <code>{build.id}</code>
                        <Copy value={build.id} />
                      </div>
                    </td>
                    <td>
                      <div className="ops-object">
                        <Icon name={build.provider === 'gitlab' ? 'gitlab' : 'github'} size={16} />
                        <code>{build.repository}</code>
                      </div>
                      <small className="ops-table-sub mono">{build.branch}</small>
                    </td>
                    <td>
                      {build.mode === 'framework'
                        ? `${build.framework?.framework} · ${build.framework?.runtime}`
                        : build.mode === 'buildpacks'
                          ? `Buildpacks · ${build.preset}`
                          : 'Dockerfile'}
                      <small className="ops-table-sub mono">{build.architecture}</small>
                    </td>
                    <td>
                      {build.auto_build ? 'Build on push' : 'Manual build'}
                      <small className="ops-table-sub">
                        {build.auto_deploy ? 'Automatic deployment' : 'Manual deployment review'}
                      </small>
                    </td>
                    <td>
                      <Link
                        className="button button-ghost"
                        to="/builds/$buildId"
                        params={{ buildId: build.id }}
                        aria-label={`Open ${build.name} build`}
                      >
                        <Icon name="chevron" size={16} />
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
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
