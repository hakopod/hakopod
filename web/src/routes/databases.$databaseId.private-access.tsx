import { useEffect, useRef, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useDatabase } from '../lib/databases'
import { useResourceScope } from '../lib/scope'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import type { components } from '../lib/api.generated'
import { FormPage } from '../components/form-page'
import { Copy, ErrorState, Loading, Note, RequestError } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { endpointName } from '../lib/database-view'

type Guide = components['schemas']['DatabasePrivateAccessGuide']
type InputValue = components['schemas']['DatabasePrivateAccessInput']

export const Route = createFileRoute('/databases/$databaseId/private-access')({ component: Page })
function Page() {
  const id = Route.useParams().databaseId
  return <PrivateAccess key={id} id={id} />
}
function PrivateAccess({ id }: { id: string }) {
  const database = useDatabase(id)
  useResourceScope(database.data)
  const [location, setLocation] = useState<InputValue['location']>('local')
  const [endpoint, setEndpoint] = useState('')
  const [host, setHost] = useState('')
  const [context, setContext] = useState('')
  const [port, setPort] = useState('15432')
  const [guide, setGuide] = useState<Guide>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const active = useRef<AbortController | null>(null)
  const output = useRef<HTMLDivElement>(null)
  useEffect(() => () => active.current?.abort(), [])
  useEffect(() => {
    setGuide(undefined)
  }, [location, endpoint, host, context, port])
  useEffect(() => {
    if (guide) output.current?.focus()
  }, [guide])
  if (database.isPending) return <Loading />
  if (!database.data) return <ErrorState error={database.error} />
  const d = database.data
  const selected = endpoint || d.observation.endpoints[0]?.purpose || ''
  const generate = async () => {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    const timeout = setTimeout(() => controller.abort(), 12000)
    setBusy(true)
    setError('')
    setGuide(undefined)
    try {
      const value = await unwrap(
        client.POST('/databases/{id}/private-access', {
          signal: controller.signal,
          params: { path: { id } },
          body: {
            location,
            endpoint: selected,
            ...(location !== 'kubernetes'
              ? { ssh_host: host, kube_context: context, local_port: Number(port) }
              : {}),
          },
        }),
      )
      if (!controller.signal.aborted) setGuide(value)
    } catch (cause) {
      if (active.current === controller) setError(message(cause))
    } finally {
      clearTimeout(timeout)
      if (active.current === controller) {
        active.current = null
        setBusy(false)
      }
    }
  }
  return (
    <FormPage
      title="Private connection"
      breadcrumbs={[]}
      description="Choose where the client runs to get the correct network and certificate steps. Generating a guide does not open a connection."
    >
      <form
        className="grid gap-4"
        onSubmit={(event) => {
          event.preventDefault()
          void generate()
        }}
      >
        <fieldset className="grid min-w-0 gap-4" disabled={busy}>
          <SelectField
            label="Where will the database client run?"
            value={location}
            onValueChange={(value) => setLocation(value as InputValue['location'])}
            options={[
              { value: 'local', label: 'On my computer' },
              { value: 'ssh', label: 'On the SSH host' },
              { value: 'kubernetes', label: 'Inside an application container' },
            ]}
          />
          <SelectField
            label="Database endpoint"
            value={selected}
            onValueChange={setEndpoint}
            options={d.observation.endpoints.map((item) => ({
              value: item.purpose,
              label: `${endpointName(item.purpose)} · ${item.port}`,
            }))}
          />
          {location !== 'kubernetes' && (
            <>
              {location === 'local' && (
                <label className="grid gap-1 text-sm">
                  SSH host
                  <Input
                    value={host}
                    onChange={(event) => setHost(event.target.value)}
                    placeholder="operator@your-server"
                    maxLength={253}
                  />
                  <span className="text-xs text-muted-foreground">
                    A host you can access with SSH that has an authorized kubectl context.
                  </span>
                </label>
              )}
              <label className="grid gap-1 text-sm">
                Kubernetes context on that host
                <Input
                  value={context}
                  onChange={(event) => setContext(event.target.value)}
                  placeholder="Your installation context"
                  maxLength={253}
                />
                <span className="text-xs text-muted-foreground">
                  Use the exact context name from kubectl config get-contexts on the host.
                </span>
              </label>
              <label className="grid gap-1 text-sm">
                Local port
                <Input
                  type="number"
                  min={1024}
                  max={65535}
                  required
                  value={port}
                  onChange={(event) => setPort(event.target.value)}
                />
                <span className="text-xs text-muted-foreground">
                  Choose an unused port on the SSH host and your computer.
                </span>
              </label>
            </>
          )}
        </fieldset>
        {error && <RequestError error={error} />}
        <div className="flex flex-wrap gap-2">
          <Button type="submit" variant="primary" disabled={busy || !selected}>
            {busy ? 'Preparing guide…' : 'Show connection steps'}
          </Button>
        </div>
      </form>
      {guide && (
        <div
          className="mt-6 grid min-w-0 gap-4 rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2"
          ref={output}
          tabIndex={-1}
          aria-label="Private connection steps"
        >
          <p className="text-xs text-muted-foreground">
            Endpoint observed {timestamp(guide.observed_at)} · database revision {guide.revision}
          </p>
          {guide.blockers.map((text) => (
            <Note key={text}>{text}</Note>
          ))}
          {guide.warnings.map((text) => (
            <Note key={text}>{text}</Note>
          ))}
          <ol className="grid gap-5">
            {guide.steps.map((step, index) => (
              <li key={step.title} className="grid min-w-0 gap-2">
                <h2 className="text-sm">
                  {index + 1}. {step.title}
                </h2>
                <p className="text-sm text-muted-foreground">{step.instruction}</p>
                {step.command && (
                  <div className="flex min-w-0 items-start gap-2">
                    <pre
                      className="min-w-0 flex-1 overflow-x-auto rounded bg-muted p-3 text-xs"
                      tabIndex={0}
                    >
                      <code>{step.command}</code>
                    </pre>
                    <Copy
                      value={step.command}
                      label={`Copy step ${index + 1}`}
                      className="min-h-10"
                    />
                  </div>
                )}
              </li>
            ))}
          </ol>
          <Button asChild>
            <Link
              to="/databases/$databaseId"
              params={{ databaseId: id }}
              search={{ project: d.project, environment: d.environment, tab: 'connections' }}
            >
              Connections &amp; security
            </Link>
          </Button>
        </div>
      )}
    </FormPage>
  )
}
