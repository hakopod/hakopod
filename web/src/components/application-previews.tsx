import { Trash2 } from 'lucide-react'
import { useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { Empty, ErrorState, Loading, Status } from './shared'

type Preview = components['schemas']['Preview']
export function ApplicationPreviews({ application }: { application: Application }) {
  const submitting = useRef(false)
  const scope = useScope(),
    cache = useQueryClient()
  const canManage =
    scope.identity.can_manage_previews ||
    scope.identity.admin ||
    scope.identity.project_roles?.some(
      (role) => role.project === application.project && role.role === 'admin',
    )
  const [deleting, setDeleting] = useState<Preview | null>(null)
  const [confirmation, setConfirmation] = useState(''),
    [error, setError] = useState(''),
    [busy, setBusy] = useState(false)
  const query = useInfiniteQuery({
    queryKey: ['previews', application.id],
    initialPageParam: '',
    queryFn: ({ signal, pageParam }) =>
      unwrap(
        client.GET('/applications/{id}/previews', {
          signal,
          params: { path: { id: application.id }, query: { cursor: pageParam } },
        }),
      ),
    getNextPageParam: (last) => last.next_cursor || undefined,
    maxPages: 3,
    refetchInterval: (query) =>
      query.state.data?.pages.some((page) => page.items.some((item) => item.state !== 'deleted'))
        ? 15000
        : false,
    gcTime: 0,
  })
  if (query.isPending) return <Loading />
  if (query.error) return <ErrorState error={query.error} />
  const previews = query.data.pages.flatMap((page) => page.items)
  return (
    <div className="grid gap-4">
      <div className="section-toolbar">
        <h2>Preview environments</h2>
        {canManage && (
          <Button variant="primary" asChild>
            <Link
              to="/applications/$applicationId/previews/new"
              params={{ applicationId: application.id }}
            >
              Create preview
            </Link>
          </Button>
        )}
      </div>
      <p className="text-sm muted-text">
        Isolated workloads with their own secrets and volumes. Cleanup starts at expiry and retries
        until complete. Up to three active previews per project.
      </p>
      {!previews.length ? (
        <Empty
          title="No previews yet"
          description="Test an image before changing your running application."
        />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5">
          {previews.map((preview) => (
            <article className="ops-catalog-card" key={preview.id}>
              <div className="ops-card-header">
                <h3 className="min-w-0 break-all">{preview.name}</h3>
                <Status value={preview.state} />
              </div>
              <dl className="grid min-w-0 gap-3 text-sm">
                <div>
                  <dt className="muted-text">Branch / reference</dt>
                  <dd className="break-all">{preview.branch || 'Not specified'}</dd>
                </div>
                <div>
                  <dt className="muted-text">Expires</dt>
                  <dd>{timestamp(preview.expires_at)}</dd>
                </div>
              </dl>
              {preview.cleanup_error && (
                <p role="status" className="inline-error">
                  Cleanup will retry: {preview.cleanup_error}
                </p>
              )}
              <div className="ops-card-footer">
                {preview.application_id && (
                  <Button size="sm" asChild>
                    <Link
                      to="/applications/$applicationId"
                      params={{ applicationId: preview.application_id }}
                    >
                      Open preview
                    </Link>
                  </Button>
                )}
                {canManage && preview.state === 'active' && (
                  <Button
                    size="sm"
                    variant="danger"
                    onClick={() => {
                      setDeleting(preview)
                      setConfirmation('')
                      setError('')
                    }}
                  >
                    <Trash2 size={14} aria-hidden="true" />
                    Delete preview
                  </Button>
                )}
              </div>
            </article>
          ))}
        </div>
      )}
      {query.hasNextPage && (
        <Button disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
          Load more
        </Button>
      )}
      {deleting && (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!open && !busy) setDeleting(null)
          }}
          title={`Delete ${deleting.name}?`}
          description="This deletes the preview's containers, volumes and native secrets. Your original application is retained."
        >
          <div className="grid gap-3 p-4">
            <label>
              Type {deleting.name}
              <Input value={confirmation} onChange={(e) => setConfirmation(e.target.value)} />
            </label>
            {error && (
              <p role="alert" className="inline-error">
                {error}
              </p>
            )}
          </div>
          <div className="dialog-footer">
            <Button disabled={busy} onClick={() => setDeleting(null)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              disabled={busy || confirmation !== deleting.name}
              onClick={async () => {
                if (submitting.current) return
                submitting.current = true
                setBusy(true)
                setError('')
                try {
                  await unwrap(
                    client.DELETE('/previews/{preview}', {
                      params: { path: { preview: deleting.id } },
                      body: { confirmation },
                    }),
                  )
                  setDeleting(null)
                  void cache.invalidateQueries({ queryKey: ['previews', application.id] })
                } catch (err) {
                  setError(message(err))
                } finally {
                  submitting.current = false
                  setBusy(false)
                }
              }}
            >
              <Trash2 size={14} aria-hidden="true" />
              {busy ? 'Deleting…' : 'Delete preview'}
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  )
}
