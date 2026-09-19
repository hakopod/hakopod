import { ComputeNotice } from '../components/compute-notice'
import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useResourceScope, useScope } from '../lib/scope'
import { httpFunction } from '../lib/http-functions'
import type { Service } from '../lib/types'
import { DeploymentForm } from '../components/deploy-dialog'
import { FormPage } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { Empty, ErrorState, HeadingHelp, Loading } from '../components/shared'

export const Route = createFileRoute('/applications/$applicationId/services/new')({
  component: AddService,
})

function AddService() {
  const { applicationId } = Route.useParams()
  const navigate = useNavigate()
  const scope = useScope()
  const app = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    gcTime: 0,
  })
  useResourceScope(app.data)
  const placement = useQuery({
    queryKey: ['placement-nodes', app.data?.project, app.data?.environment, app.data?.name],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/placement/nodes', {
          signal,
          params: {
            query: {
              project: app.data!.project,
              environment: app.data!.environment,
              application: app.data!.name,
            },
          },
        }),
      ),
    enabled: Boolean(app.data && scope.can('deployments:write')),
    staleTime: 30_000,
  })
  const [functionName, setFunctionName] = useState('')
  const [name, setName] = useState('')
  const [image, setImage] = useState('')
  const [reuse, setReuse] = useState('')
  const [draft, setDraft] = useState<{ name: string; service: Service }>()
  if (app.isPending) return <Loading />
  if (app.error || !app.data) return <ErrorState error={app.error} />
  if (!scope.can('deployments:write'))
    return (
      <Empty
        title="Deployment access required"
        description="Your role cannot add services to this application."
      />
    )
  const application = app.data
  if (draft)
    return (
      <DeploymentForm
        application={application}
        addedService={draft}
        serviceName={draft.name}
        onClose={() => setDraft(undefined)}
      />
    )
  const collision = !!application.spec.services[name]
  const functionCollision = !!application.spec.services[functionName]
  const functionAvailable = !placement.error && placement.data?.serverless_available === true
  return (
    <FormPage
      description={`Add a service to ${application.display_name || application.name}.`}
      title="Add service"
      breadcrumbs={[
        {
          label: application.display_name || application.name,
          to: `/applications/${applicationId}`,
        },
        { label: 'Add service' },
      ]}
    >
      <div className="grid gap-6 py-4">
        <ComputeNotice />
        <p className="text-sm">
          Application: <strong>{application.display_name || application.name}</strong>
        </p>
        <section className="grid gap-3">
          <h2>Start from a catalog template or Compose</h2>
          <div className="flex flex-wrap gap-2">
            <Button asChild>
              <Link to="/templates" search={{ application: applicationId }}>
                Browse catalog
              </Link>
            </Button>
            <Button asChild>
              <Link
                to="/applications/$applicationId/configure"
                params={{ applicationId }}
                search={{ mode: 'compose' }}
              >
                Import Docker Compose
              </Link>
            </Button>
          </div>
          <p className="text-sm text-muted-foreground">
            Templates include their required services and storage. You can review every addition
            before deploying.
          </p>
        </section>
        <form
          aria-label="Start an HTTP function"
          className="grid gap-3"
          onSubmit={(event) => {
            event.preventDefault()
            if (!functionAvailable || functionCollision) return
            const submitter = (event.nativeEvent as SubmitEvent).submitter
            const language = submitter?.getAttribute('value') === 'python' ? 'python' : 'javascript'
            setDraft({ name: functionName, service: httpFunction(language) })
          }}
        >
          <div className="flex items-center gap-2">
            <h2>Start an HTTP function</h2>
            <HeadingHelp title="HTTP functions">
              Start a public JavaScript or Python HTTP service that sleeps when idle. Edit its
              source and review the configuration before deploying.
            </HeadingHelp>
          </div>
          <label className="grid gap-1">
            Function service name
            <Input
              value={functionName}
              onChange={(event) => setFunctionName(event.target.value)}
              pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?"
              maxLength={40}
              required
              placeholder="hello"
              aria-describedby="function-service-name-help"
              error={
                functionCollision
                  ? 'This application already has a service with this name.'
                  : undefined
              }
            />
          </label>
          <p id="function-service-name-help" className="text-sm text-muted-foreground">
            Use a unique name with lowercase letters, digits and hyphens.
          </p>
          {placement.isPending ? (
            <p className="text-sm text-muted-foreground" role="status">
              Checking serverless availability…
            </p>
          ) : placement.error ? (
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span>Serverless availability could not be checked.</span>
              <Button
                size="sm"
                disabled={placement.isFetching}
                onClick={() => void placement.refetch()}
              >
                Retry
              </Button>
            </div>
          ) : !functionAvailable ? (
            <p className="text-sm text-muted-foreground">
              The installation owner must enable the activation gateway before using HTTP functions.
            </p>
          ) : null}
          <div className="flex flex-wrap gap-2">
            <Button
              type="submit"
              name="language"
              value="javascript"
              disabled={!functionAvailable || functionCollision}
            >
              Start JavaScript function
            </Button>
            <Button
              type="submit"
              name="language"
              value="python"
              disabled={!functionAvailable || functionCollision}
            >
              Start Python function
            </Button>
          </div>
        </form>
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            if (collision) return
            const original = reuse ? application.spec.services[reuse] : undefined
            setDraft({
              name,
              service: {
                image,
                port: original?.port || 8080,
                public: false,
                size: 'small',
                replicas: 1,
                registry_credential: original?.registry_credential,
              },
            })
          }}
        >
          <h2>Use a container image</h2>
          <label className="grid gap-1">
            Service name
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?"
              maxLength={40}
              required
              placeholder="worker"
              aria-describedby="service-name-help"
              error={
                collision ? 'This application already has a service with this name.' : undefined
              }
            />
          </label>
          <p id="service-name-help" className="text-sm text-muted-foreground">
            Use a unique name with lowercase letters, digits and hyphens.
          </p>
          <div className="grid gap-1">
            <span>Reuse an existing service image</span>
            <SelectField
              label="Reuse an existing service image"
              value={reuse}
              onValueChange={(value) => {
                setReuse(value)
                if (value) setImage(application.spec.services[value].image)
              }}
              options={[
                { value: '', label: 'Enter an image' },
                ...Object.entries(application.spec.services).map(([key, service]) => ({
                  value: key,
                  label: `${application.service_display_names?.[key] || key} · ${service.image}`,
                })),
              ]}
            />
          </div>
          <label className="grid gap-1">
            Container image
            <Input
              required
              value={image}
              onChange={(e) => {
                setImage(e.target.value)
                setReuse('')
              }}
              placeholder="ghcr.io/owner/app:latest"
              aria-describedby="service-image-help"
            />
          </label>
          <p id="service-image-help" className="text-sm text-muted-foreground">
            Reuse your API image for a worker, then choose its run command and variables on the next
            step. Storage and commands are configured separately.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              onClick={() =>
                void navigate({ to: '/applications/$applicationId', params: { applicationId } })
              }
            >
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={collision}>
              Configure service
            </Button>
          </div>
        </form>
      </div>
    </FormPage>
  )
}
