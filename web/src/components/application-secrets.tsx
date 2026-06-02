import { useState } from 'react'
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
          <Button variant="primary" onClick={() => setEdit('')}>
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
                <div className="toolbar-actions">
                  <Button size="sm" onClick={() => setEdit(secret.name)}>
                    Replace value
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setError('')
                      setRemove(secret.name)
                    }}
                  >
                    Delete
                  </Button>
                </div>
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
            if (!open) setEdit(null)
          }}
          title={edit ? `Replace ${edit}` : 'Save an application secret'}
          description="The value is stored only in this application’s secret scope."
        >
          <div className="dialog-body">
            <SecretForm
              query={query}
              initialName={edit}
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
            disabled={busy}
            onClick={async () => {
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
}: {
  query: { project: string; environment: string; application: string }
  initialName?: string
  onSaved: () => void
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
        }
      }}
    >
      <label>
        Secret name
        <input
          value={name}
          readOnly={Boolean(initialName)}
          onChange={(e) => setName(e.target.value)}
          pattern="[A-Za-z0-9_-]+"
          maxLength={80}
          required
        />
      </label>
      <label>
        Value
        <textarea
          value={value}
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
