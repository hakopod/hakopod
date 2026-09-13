import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { useState } from 'react'
import { MoreHorizontal } from 'lucide-react'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Empty, ErrorState, Loading, Note } from './shared'

export default function ApplicationSecrets({
  project,
  environment,
  application,
}: {
  project: string
  environment: string
  application: string
}) {
  const scope = useScope()
  const query = { project, environment, application }
  const cache = useQueryClient()
  const [edit, setEdit] = useState<string | null>(null)
  const [remove, setRemove] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const secrets = useQuery({
    queryKey: ['secrets', project, environment, application],
    queryFn: ({ signal }) => unwrap(client.GET('/secrets', { signal, params: { query } })),
    gcTime: 0,
  })
  const refresh = () =>
    void cache.invalidateQueries({ queryKey: ['secrets', project, environment, application] })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Application secrets</h2>
          <p>
            Scoped to {project} / {environment} / {application}.
          </p>
        </div>
        {scope.can('deployments:write') && (
          <Button variant="primary" disabled={busy} onClick={() => setEdit('')}>
            Add secret
          </Button>
        )}
      </div>
      {secrets.isPending ? (
        <Loading rows={2} />
      ) : secrets.error ? (
        <ErrorState error={secrets.error} />
      ) : !secrets.data?.items.length ? (
        <Empty
          icon="lock"
          title="No saved secrets"
          description="Store a value here, then reference its name from your service configuration."
        />
      ) : (
        <div className="panel settings-session-list">
          {secrets.data.items.map((secret) => (
            <div className="settings-list-row" key={secret.name}>
              <div>
                <strong className="mono">{secret.name}</strong>
                <small>Updated {timestamp(secret.updated_at)}</small>
              </div>
              {scope.can('deployments:write') && (
                <Menu
                  trigger={
                    <Button
                      variant="ghost"
                      size="icon"
                      disabled={busy}
                      aria-label={`Actions for secret ${secret.name}`}
                    >
                      <MoreHorizontal size={16} strokeWidth={1.75} />
                    </Button>
                  }
                >
                  <MenuItem disabled={busy} onSelect={() => setEdit(secret.name)}>
                    Replace value
                  </MenuItem>
                  <MenuItem
                    destructive
                    disabled={busy}
                    onSelect={() => {
                      setError('')
                      setConfirmation('')
                      setRemove(secret.name)
                    }}
                  >
                    Delete
                  </MenuItem>
                </Menu>
              )}
            </div>
          ))}
        </div>
      )}
      <Note>
        Values are write-only and never returned by this dashboard. Bind a saved name with{' '}
        <code>services.web.secrets.API_TOKEN = &#123; ref = "NAME" &#125;</code> in TOML. Restart
        affected services after replacing a value.
      </Note>
      {edit !== null && (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!busy && !open) setEdit(null)
          }}
          title={edit ? `Replace ${edit}` : 'Save an application secret'}
          description="The value is stored only in this application’s secret scope."
        >
          <div className="dialog-body">
            <SecretForm
              query={query}
              initialName={edit}
              onBusyChange={setBusy}
              onSaved={() => {
                setEdit(null)
                refresh()
              }}
            />
          </div>
        </Dialog>
      )}
      <Dialog
        open={Boolean(remove)}
        onOpenChange={(open) => {
          if (!busy && !open) setRemove('')
        }}
        title={`Delete ${remove}?`}
        description="Workloads referencing this name may fail on their next start. The deleted value cannot be recovered here."
      >
        <div className="dialog-body">
          <label>
            Type <code>{remove}</code> to delete this secret
            <Input
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              aria-label="Confirm secret name"
            />
          </label>
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRemove('')}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy || !remove || confirmation !== remove}
            onClick={async () => {
              if (busy || !remove || confirmation !== remove) return
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.DELETE('/secrets/{name}', { params: { path: { name: remove }, query } }),
                )
                setRemove('')
                refresh()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Delete secret
          </Button>
        </div>
      </Dialog>
    </>
  )
}

export function SecretForm({
  query,
  initialName = '',
  onSaved,
  onBusyChange,
}: {
  query: { project: string; environment: string; application: string }
  initialName?: string
  onSaved: () => void
  onBusyChange?: (busy: boolean) => void
}) {
  const [name, setName] = useState(initialName)
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <form
      className="auth-form"
      onSubmit={async (e) => {
        e.preventDefault()
        if (busy) return
        setBusy(true)
        onBusyChange?.(true)
        setError('')
        try {
          await unwrap(
            client.PUT('/secrets/{name}', { params: { path: { name }, query }, body: { value } }),
          )
          setValue('')
          onSaved()
        } catch (err) {
          setError(message(err))
        } finally {
          setBusy(false)
          onBusyChange?.(false)
        }
      }}
    >
      <label>
        Secret name
        <Input
          value={name}
          readOnly={Boolean(initialName)}
          disabled={busy}
          onChange={(e) => setName(e.target.value)}
          pattern="[a-z](([a-z0-9]|-){0,38}[a-z0-9])?"
          maxLength={40}
          required
        />
      </label>
      <label>
        Value
        <Textarea
          value={value}
          disabled={busy}
          onChange={(e) => setValue(e.target.value)}
          rows={3}
          maxLength={65536}
          autoComplete="off"
          spellCheck={false}
          required
        />
      </label>
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <Button type="submit" variant="primary" disabled={busy || !name || !value}>
        {busy ? 'Saving…' : 'Save secret'}
      </Button>
    </form>
  )
}
