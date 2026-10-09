import { useEffect, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { editionFetch } from '../lib/client-edition'
import { message } from '../lib/api'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Icon } from '../components/icons'
import { Note, Status } from '../components/shared'

type Item = { id: string; category: string; path: string; bytes: number }
type Review = {
  id: string
  expires_at: string
  inventory: {
    filesystem: { capacity_bytes: number; available_bytes: number }
    items: Item[]
    protected: string[]
    planned_bytes: number
  }
}
type ReceiptEntry = { id: string; bytes?: number; reason?: string }
type Receipt = {
  status: 'not_started' | 'running' | 'interrupted' | 'succeeded'
  planned_bytes?: number
  removed_bytes?: number
  reclaimed_bytes?: number
  available_before_bytes?: number
  available_after_bytes?: number
  removed?: ReceiptEntry[]
  skipped?: ReceiptEntry[]
  uncertain?: ReceiptEntry[]
}
type Operation = {
  id: string
  status: 'running' | 'succeeded' | 'failed'
  message?: string
  receipt?: Receipt
}

const size = (bytes: number) =>
  `${(bytes / 1024 ** 2).toLocaleString(undefined, { maximumFractionDigits: 1 })} MiB`

export const Route = createFileRoute('/infrastructure/cleanup')({ component: Cleanup })

function Cleanup() {
  const [review, setReview] = useState<Review | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [operation, setOperation] = useState<Operation | null>(null)
  const [retryKey, setRetryKey] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function request(
    path: string,
    options: { method?: 'GET' | 'POST'; body?: unknown; key?: string } = {},
  ) {
    const method = options.method || 'POST'
    const response = await editionFetch(`/api/installation/cleanup/${path}`, {
      method,
      headers: {
        ...(method === 'POST' ? { 'Content-Type': 'application/json' } : {}),
        ...(options.key ? { 'Idempotency-Key': options.key } : {}),
      },
      body: method === 'POST' ? JSON.stringify(options.body || {}) : undefined,
    })
    const value = await response.json()
    if (!response.ok) throw new Error(value?.error?.message || 'Cleanup request failed.')
    return value
  }

  useEffect(() => {
    if (!operation || operation.status !== 'running') return
    let stopped = false
    let timer: ReturnType<typeof setTimeout>
    const poll = async () => {
      try {
        const value = (await request(`operations/${operation.id}`, { method: 'GET' })) as Operation
        if (!stopped) setOperation(value)
      } catch (value) {
        if (!stopped) setError(message(value))
      }
      if (!stopped) timer = setTimeout(poll, 2000)
    }
    timer = setTimeout(poll, 1000)
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  }, [operation?.id, operation?.status])

  async function preview() {
    setBusy(true)
    setError('')
    try {
      setReview((await request('review')) as Review)
      setOperation(null)
      setConfirmation('')
      setRetryKey(crypto.randomUUID())
    } catch (value) {
      setError(message(value))
    } finally {
      setBusy(false)
    }
  }

  async function execute() {
    if (!review || !retryKey) return
    setBusy(true)
    setError('')
    try {
      setOperation(
        (await request('execute', {
          body: { review_id: review.id, confirmation },
          key: retryKey,
        })) as Operation,
      )
    } catch (value) {
      setError(message(value))
    } finally {
      setBusy(false)
    }
  }

  const receipt = operation?.receipt
  const canRetry =
    operation?.status === 'running' &&
    (receipt?.status === 'interrupted' || receipt?.status === 'not_started')

  return (
    <FormPage
      title="Safe server cleanup"
      description="Review exact installer-owned temporary files before removing them."
      breadcrumbs={[]}
    >
      <FormSection title="Disk inventory">
        <Note>
          Cleanup never includes database volumes, application volumes, backups, K3s state, secrets,
          installed releases or rollback material. RAM pressure is separate from disk usage.
        </Note>
        {!review && (
          <Button variant="primary" disabled={busy} onClick={() => void preview()}>
            {busy ? 'Checking…' : 'Preview removable files'}
          </Button>
        )}
        {review && (
          <>
            <dl className="service-definition-list">
              <div>
                <dt>Disk capacity</dt>
                <dd>
                  {size(review.inventory.filesystem.available_bytes)} available of{' '}
                  {size(review.inventory.filesystem.capacity_bytes)}
                </dd>
              </div>
              <div>
                <dt>Memory pressure</dt>
                <dd>Not measured by cleanup. Inspect live node memory separately.</dd>
              </div>
              <div>
                <dt>Planned removal</dt>
                <dd>
                  {size(review.inventory.planned_bytes)} across {review.inventory.items.length}{' '}
                  files
                </dd>
              </div>
            </dl>
            <div className="table-scroll" tabIndex={0} aria-label="Reviewed removable files">
              <table>
                <thead>
                  <tr>
                    <th>Category</th>
                    <th>Exact file</th>
                    <th>Allocated size</th>
                  </tr>
                </thead>
                <tbody>
                  {review.inventory.items.map((item) => (
                    <tr key={item.id}>
                      <td>{item.category}</td>
                      <td>
                        <code className="break-all">{item.path}</code>
                      </td>
                      <td>{size(item.bytes)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {!review.inventory.items.length ? (
              <Note>No reviewed temporary files are removable.</Note>
            ) : (
              <label className="grid gap-1">
                Type remove reviewed files to confirm
                <Input
                  value={confirmation}
                  onChange={(event) => setConfirmation(event.target.value)}
                  autoComplete="off"
                />
              </label>
            )}
          </>
        )}
        {operation && (
          <section className="grid gap-3" aria-label="Cleanup operation receipt">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="font-medium">Cleanup receipt</h3>
              <Status value={operation.status} />
            </div>
            <dl className="service-definition-list">
              <div>
                <dt>Operation</dt>
                <dd>
                  <code className="break-all">{operation.id}</code>
                </dd>
              </div>
              {receipt?.planned_bytes !== undefined && (
                <div>
                  <dt>Reviewed</dt>
                  <dd>{size(receipt.planned_bytes)}</dd>
                </div>
              )}
              {receipt?.removed_bytes !== undefined && (
                <div>
                  <dt>Removed files</dt>
                  <dd>
                    {size(receipt.removed_bytes)} across {receipt.removed?.length || 0} files
                  </dd>
                </div>
              )}
              {receipt?.reclaimed_bytes !== undefined && (
                <div>
                  <dt>Observed increase in free space</dt>
                  <dd>
                    {size(receipt.reclaimed_bytes)}
                    <span className="block text-xs text-muted">
                      Other server processes can change free space while cleanup runs.
                    </span>
                  </dd>
                </div>
              )}
              {receipt?.available_before_bytes !== undefined &&
                receipt.available_after_bytes !== undefined && (
                  <div>
                    <dt>Available space</dt>
                    <dd>
                      {size(receipt.available_before_bytes)} before ·{' '}
                      {size(receipt.available_after_bytes)} after
                    </dd>
                  </div>
                )}
              <div>
                <dt>Skipped / uncertain</dt>
                <dd>
                  {receipt?.skipped?.length || 0} skipped · {receipt?.uncertain?.length || 0}{' '}
                  uncertain
                </dd>
              </div>
            </dl>
            {operation.status === 'running' && receipt?.status === 'running' && (
              <Note>
                Cleanup is running. This page checks the durable receipt until it finishes.
              </Note>
            )}
            {canRetry && (
              <Note>
                Cleanup stopped before it finished. Retry resumes this operation with the same
                reviewed files and retry key.
              </Note>
            )}
            {operation.status === 'succeeded' && (
              <Note>The receipt above reports the measured filesystem change.</Note>
            )}
          </section>
        )}
        {error && <FormError>{error}</FormError>}
      </FormSection>
      <div className="form-footer !static">
        <Button asChild>
          <Link to="/infrastructure" search={{ tab: 'nodes' }}>
            Back
          </Link>
        </Button>
        {review && operation?.status !== 'succeeded' && (
          <Button
            variant="destructive"
            disabled={
              busy ||
              confirmation !== 'remove reviewed files' ||
              !review.inventory.items.length ||
              (operation?.status === 'running' && !canRetry)
            }
            onClick={() => void execute()}
          >
            <Icon name="trash" size={14} />
            {busy ? 'Removing…' : canRetry ? 'Retry cleanup' : 'Remove reviewed files'}
          </Button>
        )}
      </div>
    </FormPage>
  )
}
