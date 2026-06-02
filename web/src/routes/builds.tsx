import { lazy, Suspense, useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Button } from '../components/ui/button'
import { Icon } from '../components/icons'
import { Empty, ErrorState, Loading, Note, PageHeader } from '../components/shared'

const BuildForm = lazy(() => import('../components/build-form'))
export const Route = createFileRoute('/builds')({ component: Builds })
function Builds() {
  const location = useLocation()
  if (location.pathname !== '/builds') return <Outlet />
  return <BuildList />
}
function BuildList() {
  const scope = useScope()
  const [create, setCreate] = useState(false)
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
  return (
    <>
      <PageHeader
        eyebrow="WORKSPACE / SOURCE BUILDS"
        title="From repository to running service."
        description="Build container images on demand with GitHub Actions, Dockerfiles, or Cloud Native Buildpacks."
        action={
          scope.can('deployments:write') && (
            <Button variant="primary" onClick={() => setCreate(true)}>
              <Icon name="plus" size={15} />
              New source build
            </Button>
          )
        }
      />
      {builds.isPending ? (
        <Loading />
      ) : builds.error ? (
        <ErrorState error={builds.error} />
      ) : !builds.data?.items.length ? (
        <Empty
          icon="code"
          title="Start with your source code"
          description="Connect a repository, review its generated workflow, build, then deploy the verified image."
          action={
            scope.can('deployments:write') && (
              <Button onClick={() => setCreate(true)}>Create source build</Button>
            )
          }
        />
      ) : (
        <div className="catalog-grid">
          {builds.data.items.map((build) => (
            <Link
              to="/builds/$buildId"
              params={{ buildId: build.id }}
              className="panel catalog-card"
              key={build.id}
            >
              <div className="title-row">
                <Icon name="branch" size={22} />
                <h2>
                  {build.name} / {build.service}
                </h2>
              </div>
              <p>
                {build.repository} · {build.branch}
              </p>
              <div className="toolbar-actions">
                <span className="label-chip">
                  {build.mode === 'buildpacks' ? `Buildpacks · ${build.preset}` : 'Dockerfile'}
                </span>
                <span className="label-chip">
                  {build.installed_revision === build.revision
                    ? 'Workflow installed'
                    : 'Workflow installation required'}
                </span>
              </div>
            </Link>
          ))}
        </div>
      )}
      <Note>
        Builds use GitHub Actions runner capacity and publish to GHCR. Hakopod has no always-running
        builder and does not load application build tools into the management process.
      </Note>
      {create && (
        <Suspense fallback={<Loading />}>
          <BuildForm onClose={() => setCreate(false)} />
        </Suspense>
      )}
    </>
  )
}
