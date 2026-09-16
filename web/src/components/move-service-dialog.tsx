import { useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { Dialog } from './ui/dialog'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { DiffTable } from './deploy-dialog'
import { ErrorState, Loading, Note, RequestError } from './shared'

export function MoveServiceDialog({
  application,
  service,
  onClose,
  restoreFocus,
}: {
  application: Application
  service: string
  onClose: () => void
  restoreFocus?: () => void
}) {
  const [source] = useState(application)
  const [destination, setDestination] = useState('')
  const [name, setName] = useState(service)
  const [plan, setPlan] = useState<components['schemas']['ServiceMovePlan']>()
  const [reviewed, setReviewed] = useState<components['schemas']['ServiceMoveInput']>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [deployment, setDeployment] = useState('')
  const lock = useRef(false)
  const key = useRef(crypto.randomUUID())
  const cache = useQueryClient()
  const apps = useInfiniteQuery({
    queryKey: ['service-move-destinations', source.project, source.environment],
    initialPageParam: '',
    maxPages: 20,
    queryFn: ({ signal, pageParam }) =>
      unwrap(
        client.GET('/applications', {
          signal,
          params: {
            query: { project: source.project, environment: source.environment, cursor: pageParam },
          },
        }),
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
    gcTime: 0,
  })
  const appItems = apps.data?.pages.flatMap((page) => page.items) || []
  async function submit() {
    if (lock.current) return
    lock.current = true
    setBusy(true)
    setError('')
    try {
      if (!plan) {
        const target = await unwrap(
          client.GET('/applications/{id}', { params: { path: { id: destination } } }),
        )
        const input = {
          destination_id: target.id,
          destination_service: name,
          source_revision: source.revision,
          destination_revision: target.revision,
        }
        const result = await unwrap(
          client.POST('/applications/{id}/services/{service}/move-plan', {
            params: { path: { id: source.id, service } },
            body: input,
          }),
        )
        setPlan(result)
        setReviewed(input)
      } else if (reviewed) {
        const result = await unwrap(
          client.POST('/applications/{id}/services/{service}/move', {
            params: {
              path: { id: source.id, service },
              header: { 'Idempotency-Key': key.current },
            },
            body: reviewed,
          }),
        )
        setDeployment(result.id)
        void cache.invalidateQueries({ queryKey: ['service-moves', source.id] })
        void cache.invalidateQueries({ queryKey: ['application', destination] })
        void cache.invalidateQueries({ queryKey: ['applications'] })
      }
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
      lock.current = false
    }
  }
  return (
    <Dialog
      onCloseAutoFocus={(event) => {
        if (restoreFocus) {
          event.preventDefault()
          restoreFocus()
        }
      }}
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      title={`Move ${source.service_display_names?.[service] || service}`}
      description="Deploy a copy in another application, then confirm removal of the original after checking it works."
    >
      <form
        className="flex min-h-0 flex-1 flex-col overflow-hidden"
        onSubmit={(event) => {
          event.preventDefault()
          void submit()
        }}
      >
        <div className="grid min-h-0 gap-4 overflow-y-auto p-4">
          {destination && (
            <p className="text-sm break-words">
              <strong>
                {source.display_name || source.name} / {service}
              </strong>{' '}
              →{' '}
              <strong>
                {appItems.find((app) => app.id === destination)?.display_name ||
                  appItems.find((app) => app.id === destination)?.name ||
                  destination}{' '}
                / {name}
              </strong>
            </p>
          )}
          {deployment ? (
            <Note>
              The destination deployment is queued. The original is still running. Return to this
              application's Services tab to finish the move after checking the destination.
              <div className="mt-3">
                <Button asChild>
                  <Link to="/deployments/$deploymentId" params={{ deploymentId: deployment }}>
                    View deployment
                  </Link>
                </Button>
              </div>
            </Note>
          ) : plan ? (
            <>
              {plan.warnings.map((warning) => (
                <p key={warning} className="text-sm">
                  {warning}
                </p>
              ))}
              <p className="text-sm">{plan.secret_count} secret bindings will be copied.</p>
              <h3>Destination changes</h3>
              <DiffTable changes={plan.destination_changes} />
              <details>
                <summary>Original application after finishing</summary>
                <DiffTable changes={plan.source_changes} />
              </details>
            </>
          ) : (
            <>
              {apps.isPending ? (
                <Loading />
              ) : apps.error ? (
                <ErrorState error={apps.error} />
              ) : (
                <div className="grid gap-1">
                  <span>Destination application</span>
                  <SelectField
                    label="Destination application"
                    value={destination}
                    onValueChange={setDestination}
                    required
                    options={[
                      { value: '', label: 'Choose an application' },
                      ...appItems
                        .filter((app) => app.id !== source.id)
                        .map((app) => ({
                          value: app.id,
                          label: app.display_name || app.name,
                          disabled: !['healthy', 'empty'].includes(app.status),
                        })),
                    ]}
                  />
                </div>
              )}
              {apps.data && !apps.hasNextPage && !appItems.some((app) => app.id !== source.id) && (
                <Note>
                  Create another application in this project and environment to move the service
                  into it.
                  <div className="mt-3">
                    <Button asChild>
                      <Link to="/applications/new">Create application</Link>
                    </Button>
                  </div>
                </Note>
              )}
              {apps.hasNextPage && (
                <Button
                  type="button"
                  disabled={apps.isFetchingNextPage}
                  onClick={() => void apps.fetchNextPage()}
                >
                  {apps.isFetchingNextPage ? 'Loading…' : 'Load more applications'}
                </Button>
              )}
              <p className="text-sm text-muted-foreground">
                Choose an application in {source.project} / {source.environment} with a successful
                deployment. Pause automatic deployments before moving.
              </p>
              <label className="grid gap-1">
                Destination service name
                <Input
                  aria-describedby="move-service-name-help"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?"
                  maxLength={40}
                  required
                />
              </label>
              <p id="move-service-name-help" className="text-sm text-muted-foreground">
                Use a unique name with lowercase letters, digits and hyphens.
              </p>
              <p className="text-sm text-muted-foreground">
                Volumes, custom domains, jobs and service dependencies require an explicit
                migration. The review checks these before changing anything.
              </p>
            </>
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer shrink-0">
          <Button type="button" disabled={busy} onClick={onClose}>
            {deployment ? 'Close' : 'Cancel'}
          </Button>
          {!deployment && plan && (
            <Button
              type="button"
              disabled={busy}
              onClick={() => {
                setPlan(undefined)
                setReviewed(undefined)
                key.current = crypto.randomUUID()
              }}
            >
              Back
            </Button>
          )}
          {!deployment && (
            <Button type="submit" variant="primary" disabled={busy || !destination || !name}>
              {busy ? 'Working…' : plan ? 'Deploy in destination' : 'Review move'}
            </Button>
          )}
        </div>
      </form>
    </Dialog>
  )
}

export function ServiceMoves({ application }: { application: Application }) {
  const cache = useQueryClient()
  const moves = useQuery({
    queryKey: ['service-moves', application.id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/service-moves', {
          signal,
          params: { path: { id: application.id } },
        }),
      ),
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
    gcTime: 0,
  })
  const [selected, setSelected] = useState<components['schemas']['ServiceMove']>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const key = useRef('')
  const lock = useRef(false)
  const items = (moves.data?.items || []).filter(
    (move) => !move.removal_id || move.status !== 'succeeded',
  )
  return (
    <>
      {moves.error && <RequestError error={moves.error} />}
      {items.length > 0 && (
        <section className="grid gap-3 py-4" aria-label="Service moves">
          <h2>Service moves</h2>
          {items.map((move) => (
            <div
              key={move.id}
              className="flex flex-wrap items-center justify-between gap-3 rounded border border-border p-3"
            >
              <div className="min-w-0">
                <strong>
                  {move.service} → {move.destination_service}
                </strong>
                <p className="text-sm text-muted-foreground">
                  {move.removal_id
                    ? `Original removal: ${move.status}`
                    : move.status === 'succeeded'
                      ? 'Destination deployed. Check it, then finish the move.'
                      : `Destination deployment: ${move.status}. Original service retained.`}
                </p>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button asChild size="sm">
                  <Link
                    to="/deployments/$deploymentId"
                    params={{ deploymentId: move.removal_id || move.id }}
                  >
                    View deployment
                  </Link>
                </Button>
                <Button asChild size="sm">
                  <Link
                    to="/applications/$applicationId"
                    params={{ applicationId: move.destination_id }}
                    search={{ service: move.destination_service }}
                  >
                    Open destination
                  </Link>
                </Button>
                {!move.removal_id && (
                  <Button
                    size="sm"
                    disabled={
                      move.status !== 'succeeded' || application.revision !== move.source_revision
                    }
                    onClick={() => {
                      setSelected(move)
                      setError('')
                      key.current = crypto.randomUUID()
                    }}
                  >
                    Finish move
                  </Button>
                )}
              </div>
              {!move.removal_id && application.revision !== move.source_revision && (
                <p className="w-full text-sm">
                  The original application changed. Review both services before removing the
                  original through its service menu.
                </p>
              )}
            </div>
          ))}
        </section>
      )}
      {selected && (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!open && !busy) setSelected(undefined)
          }}
          title={`Finish moving ${selected.service}?`}
          description="Confirm that you checked the destination service and updated its clients. This stops and removes the original service in a new deployment. The destination stays running."
        >
          <div className="grid gap-3 p-4">
            <p className="text-sm">
              If anything changed in either application, the server will keep the original and ask
              you to review again.
            </p>
            {error && <RequestError error={error} />}
          </div>
          <div className="dialog-footer">
            <Button disabled={busy} onClick={() => setSelected(undefined)}>
              Keep original for now
            </Button>
            <Button
              variant="danger"
              disabled={busy}
              onClick={async () => {
                if (lock.current) return
                lock.current = true
                setBusy(true)
                setError('')
                try {
                  await unwrap(
                    client.POST('/applications/{id}/service-moves/{move}/finish', {
                      params: {
                        path: { id: application.id, move: selected.id },
                        header: { 'Idempotency-Key': key.current },
                      },
                    }),
                  )
                  setSelected(undefined)
                  void cache.invalidateQueries({ queryKey: ['service-moves', application.id] })
                  void cache.invalidateQueries({ queryKey: ['application', application.id] })
                  void cache.invalidateQueries({ queryKey: ['applications'] })
                } catch (cause) {
                  setError(message(cause))
                } finally {
                  setBusy(false)
                  lock.current = false
                }
              }}
            >
              {busy ? 'Removing…' : 'Remove original service'}
            </Button>
          </div>
        </Dialog>
      )}
    </>
  )
}
