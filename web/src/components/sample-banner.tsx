import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { ErrorState, Status } from './shared'
import { Icon } from './icons'
export default function SampleBanner({ applicationId }: { applicationId?: string }) {
  const scope = useScope()
  const cache = useQueryClient()
  const [remove, setRemove] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const sample = useQuery({
    queryKey: ['showcase'],
    queryFn: ({ signal }) => unwrap(client.GET('/showcase', { signal })),
    enabled:
      scope.project === 'demo' &&
      scope.environment === 'development' &&
      scope.can('deployments:read'),
    gcTime: 0,
    refetchInterval: (query) =>
      ['queued', 'removing'].includes(query.state.data?.state || '') ||
      ['queued', 'running'].includes(query.state.data?.deployment_status || '')
        ? 10000
        : false,
  })
  const item = sample.data
  if (
    !item?.sample ||
    ['removed', 'skipped'].includes(item.state) ||
    item.project !== scope.project ||
    item.environment !== scope.environment ||
    (applicationId && item.application_id !== applicationId)
  )
    return null
  return (
    <>
      <section className="sample-banner">
        <div className="sample-banner-symbol">
          <Icon name="box" size={24} />
        </div>
        <div>
          <div className="title-row">
            <strong>Sample application · {item.name}</strong>
            <span className="label-chip">Fresh-install example</span>
            {item.deployment_status && <Status value={item.deployment_status} small />}
          </div>
          <p>{item.message}</p>
        </div>
        <div className="toolbar-actions">
          {item.application_id && !applicationId && (
            <Link
              className="button button-sm"
              to="/applications/$applicationId"
              params={{ applicationId: item.application_id }}
            >
              Explore sample
            </Link>
          )}
          {item.removable && (
            <Button size="sm" variant="ghost" onClick={() => setRemove(true)}>
              Remove sample
            </Button>
          )}
        </div>
      </section>
      <Dialog
        open={remove}
        onOpenChange={(open) => {
          if (!busy) setRemove(open)
        }}
        title="Remove the sample application?"
        description={`Remove the tracked ${item.name} application and its owned Kubernetes resources.`}
      >
        <div className="dialog-body">
          <dl className="service-definition-list">
            <div>
              <dt>Scope</dt>
              <dd>
                {item.project} / {item.environment}
              </dd>
            </div>
            <div>
              <dt>Application</dt>
              <dd>{item.application_id || 'Not accepted yet'}</dd>
            </div>
            <div>
              <dt>Current revision</dt>
              <dd>{item.application_revision}</dd>
            </div>
          </dl>
          <p className="field-help">
            The server checks the sample marker and current application revision before removing it.
            A removed sample is not recreated.
          </p>
          {error && <ErrorState error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRemove(false)}>
            Keep sample
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.POST('/showcase/remove', {
                    body: {
                      expected_revision: item.revision,
                      expected_application_id: item.application_id,
                      expected_application_revision: item.application_revision,
                    },
                  }),
                )
                setRemove(false)
                void sample.refetch()
                void cache.invalidateQueries({ queryKey: ['applications'] })
                void cache.invalidateQueries({ queryKey: ['application', item.application_id] })
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Requesting removal…' : 'Remove tracked sample'}
          </Button>
        </div>
      </Dialog>
    </>
  )
}
