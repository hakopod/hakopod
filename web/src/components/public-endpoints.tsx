import { useId, useRef, useState } from 'react'
import type { PublicEndpoint } from '../lib/public-endpoints'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { Icon } from './icons'
import { Copy } from './shared'

function EndpointRow({ endpoint, copy = false }: { endpoint: PublicEndpoint; copy?: boolean }) {
  return (
    <div className="flex min-w-0 items-center gap-2">
      <div className="min-w-0 flex-1">
        <a
          className="inline-flex min-w-0 max-w-full items-center gap-1"
          href={endpoint.url}
          target="_blank"
          rel="noreferrer"
          aria-label={`${endpoint.service}: ${endpoint.url} (opens in a new tab)`}
          title={endpoint.url}
        >
          <span className="truncate">
            {endpoint.url.replace(/^https?:\/\//, '').replace(/\/$/, '')}
          </span>
          <Icon name="external" size={12} className="shrink-0" />
        </a>
        <div className="text-xs text-muted-foreground">
          <span className="break-all">{endpoint.service}</span>
          {' · '}
          {endpoint.label}
          {endpoint.custom && <> · {endpoint.status}</>}
        </div>
      </div>
      {copy && <Copy value={endpoint.url} />}
    </div>
  )
}

export function PublicEndpoints({
  endpoints,
  compact = false,
}: {
  endpoints: PublicEndpoint[]
  compact?: boolean
}) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const trigger = useRef<HTMLButtonElement>(null)
  const searchId = useId()
  const limit = compact ? 2 : 3
  const filtered = endpoints.filter((endpoint) =>
    `${endpoint.url} ${endpoint.service} ${endpoint.label}`
      .toLowerCase()
      .includes(search.trim().toLowerCase()),
  )
  if (!endpoints.length)
    return compact ? null : <span>No public endpoints configured or observed</span>
  return (
    <div className="min-w-0 max-w-full">
      <div
        className={
          compact ? 'flex min-w-0 flex-wrap items-start gap-x-6 gap-y-2' : 'grid min-w-0 gap-3'
        }
      >
        {endpoints.slice(0, limit).map((endpoint) => (
          <div
            className={compact ? 'w-full min-w-0 sm:w-64' : 'min-w-0'}
            key={endpoint.service + endpoint.url}
          >
            <EndpointRow endpoint={endpoint} copy={!compact} />
          </div>
        ))}
        {endpoints.length > limit && (
          <Button
            ref={trigger}
            size="sm"
            variant="ghost"
            onClick={() => {
              setSearch('')
              setOpen(true)
            }}
          >
            All endpoints ({endpoints.length})
          </Button>
        )}
      </div>
      <Dialog
        open={open}
        onOpenChange={setOpen}
        title="Public endpoints"
        description="Custom domains and observed public addresses, grouped by service. Configured routing does not confirm DNS, TLS or application health."
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          trigger.current?.focus()
        }}
      >
        <div className="dialog-body grid min-w-0 gap-3">
          <label htmlFor={searchId}>Find a domain or service</label>
          <Input
            id={searchId}
            type="search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="Search endpoints"
          />
          <p className="text-xs text-muted-foreground" role="status">
            {filtered.length} of {endpoints.length} endpoints
          </p>
          <div className="max-h-[50vh] min-w-0 overflow-y-auto">
            {filtered.length ? (
              <ul className="m-0 grid min-w-0 list-none gap-4 p-0">
                {filtered.map((endpoint) => (
                  <li className="min-w-0" key={endpoint.service + endpoint.url}>
                    <EndpointRow endpoint={endpoint} copy />
                  </li>
                ))}
              </ul>
            ) : (
              <p>No endpoints match your search.</p>
            )}
          </div>
        </div>
      </Dialog>
    </div>
  )
}
