import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { client, unwrap } from '../lib/client'
import { managedPlatformName, platformSearch, useManagedPlatform } from '../lib/managed-platforms'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Loading, ErrorState } from '../components/shared'
import { ReviewFacts, ReviewNotice } from '../components/operation-review'
import { Input } from '../components/ui/input'
import { Button } from '../components/ui/button'
import { Icon } from '../components/icons'
import { useResourceScope, useScope, canAccess } from '../lib/scope'

export const Route = createFileRoute('/platforms/$platformId/delete')({
  validateSearch: platformSearch,
  component: DeletePlatform,
})
function DeletePlatform() {
  const id = Route.useParams().platformId
  Route.useSearch()
  const query = useManagedPlatform(id)
  const navigate = useNavigate()
  const { identity } = useScope()
  useResourceScope(query.data)
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  if (query.isPending) return <Loading />
  if (query.error && !query.data) return <ErrorState error={query.error} />
  const item = query.data!
  const scope = { project: item.project, environment: item.environment }
  const canManage = !identity.application && canAccess(identity, item.project, 'deployments:write')
  async function remove() {
    setBusy(true)
    setError('')
    try {
      const intent = {
        id: item.id,
        project: item.project,
        environment: item.environment,
        expected_revision: item.revision,
        kind: 'delete' as const,
        confirm_name: confirmation,
        spec: item.spec,
      }
      const reviewed = await unwrap(client.POST('/managed-platforms/reviews', { body: intent }))
      if (reviewed.blocked || !reviewed.review)
        throw new Error('Deletion review was blocked. Refresh the resource and try again.')
      await unwrap(
        client.POST('/managed-platforms/operations', {
          params: { header: { 'Idempotency-Key': crypto.randomUUID() } },
          body: { ...intent, review: reviewed.review },
        }),
      )
      await navigate({ to: '/platforms', search: scope })
    } catch (value) {
      setError(value instanceof Error ? value.message : 'Deletion failed.')
    } finally {
      setBusy(false)
    }
  }
  if (!canManage)
    return <ErrorState error={new Error('You do not have permission to delete this platform.')} />
  return (
    <FormPage
      title={`Delete ${item.spec.name}`}
      description="Review the exact platform before scheduling deletion."
      breadcrumbs={[]}
    >
      <FormSection
        title="Deletion review"
        description="Hakopod deletes only resources carrying this platform's durable ownership claims."
      >
        {query.error && (
          <ReviewNotice tone="warn" title="Refresh failed">
            Confirmed deletion is disabled until current data is available.
          </ReviewNotice>
        )}
        <ReviewFacts
          facts={[
            ['Scope', `${item.project} / ${item.environment}`],
            [
              'Platform',
              <>
                <code>{item.spec.name}</code> · {managedPlatformName(item.spec.kind)} · revision{' '}
                {item.revision}
              </>,
            ],
            ['Removed', 'Its namespace and the persistent volumes it owns.'],
            ['Kept', 'Resources without this platform’s ownership claim.'],
            ['Data', 'Owned volumes are erased and cannot be recovered without a backup.'],
          ]}
        />
        <ReviewNotice title="Failed cleanup stays visible">
          If removal fails partway, the operation remains listed so you can retry it.
        </ReviewNotice>
        <label className="grid min-w-0 gap-1.5 py-4">
          <span className="[overflow-wrap:anywhere]">
            Type <strong>{item.spec.name}</strong> to confirm
          </span>
          <Input
            className="w-full min-w-0"
            value={confirmation}
            onChange={(event) => setConfirmation(event.target.value)}
            autoComplete="off"
          />
        </label>
        {error && <FormError>{error}</FormError>}
        <div className="form-footer">
          <Button asChild>
            <Link to="/platforms/$platformId" params={{ platformId: id }} search={scope}>
              Cancel
            </Link>
          </Button>
          <Button
            variant="destructive"
            aria-busy={busy || undefined}
            disabled={busy || Boolean(query.error) || confirmation !== item.spec.name}
            onClick={() => void remove()}
          >
            <Icon name="trash" size={14} />
            {busy ? 'Scheduling…' : `Delete ${item.spec.name}`}
          </Button>
        </div>
      </FormSection>
    </FormPage>
  )
}
