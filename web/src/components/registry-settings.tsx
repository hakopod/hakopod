import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Empty, ErrorState, Loading, Note, Status } from './shared'

type Registry = components['schemas']['RegistryInfo']
export default function RegistrySettings() {
  const scope = useScope()
  const query = { project: scope.project, environment: scope.environment }
  const [edit, setEdit] = useState<Registry | 'new' | null>(null)
  const [remove, setRemove] = useState<Registry | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const registries = useQuery({
    queryKey: ['registries', query.project, query.environment],
    queryFn: ({ signal }) => unwrap(client.GET('/registries', { signal, params: { query } })),
    enabled: Boolean(query.project && query.environment),
    gcTime: 0,
  })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Private registries</h2>
          <p>
            Pull credentials for {query.project} / {query.environment}.
          </p>
        </div>
        {scope.can('deployments:write') && (
          <Button variant="primary" onClick={() => setEdit('new')}>
            Add registry
          </Button>
        )}
      </div>
      {registries.isPending ? (
        <Loading />
      ) : registries.error ? (
        <ErrorState error={registries.error} />
      ) : !registries.data?.items.length ? (
        <Empty
          icon="lock"
          title="No registry credentials"
          description="Public images work without credentials. Add a credential to pull private images."
        />
      ) : (
        <div className="panel settings-session-list">
          {registries.data.items.map((registry) => (
            <div className="settings-list-row" key={registry.name}>
              <div>
                <strong>{registry.name}</strong>
                <small>
                  {registry.registry} · Updated {timestamp(registry.updated_at)} · r
                  {registry.revision}
                </small>
                {registry.message && <p className="field-help">{registry.message}</p>}
                <Status value={registry.synchronized ? 'synchronized' : 'not verified'} small />
              </div>
              {scope.can('deployments:write') && (
                <div className="toolbar-actions">
                  <Button size="sm" disabled={busy} onClick={() => setEdit(registry)}>
                    Rotate credential
                  </Button>
                  {!registry.synchronized && (
                    <Button
                      size="sm"
                      disabled={busy}
                      onClick={async () => {
                        setBusy(true)
                        setError('')
                        try {
                          await unwrap(
                            client.POST('/registries/{name}/sync', {
                              params: { path: { name: registry.name }, query },
                            }),
                          )
                          void registries.refetch()
                        } catch (err) {
                          setError(message(err))
                        } finally {
                          setBusy(false)
                        }
                      }}
                    >
                      Synchronize now
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setError('')
                      setRemove(registry)
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
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <Note>
        Credentials are write-only. Select the saved name when staging a service, or set{' '}
        <code>registry_credential</code> in TOML. Running containers continue after credential
        rotation; future image pulls use the saved credential.
      </Note>
      {edit && (
        <RegistryForm
          registry={edit === 'new' ? undefined : edit}
          project={query.project}
          environment={query.environment}
          onClose={() => setEdit(null)}
          onSaved={() => {
            setEdit(null)
            void registries.refetch()
          }}
        />
      )}
      <Dialog
        open={Boolean(remove)}
        onOpenChange={(open) => {
          if (!busy && !open) setRemove(null)
        }}
        title={`Delete ${remove?.name || 'registry credential'}?`}
        description="Services using this credential may fail future private image pulls."
      >
        <div className="dialog-body">
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRemove(null)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              if (!remove) return
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.DELETE('/registries/{name}', {
                    params: { path: { name: remove.name } },
                    body: { ...query, expected_revision: remove.revision },
                  }),
                )
                setRemove(null)
                void registries.refetch()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Delete credential
          </Button>
        </div>
      </Dialog>
    </>
  )
}

function RegistryForm({
  registry,
  project,
  environment,
  onClose,
  onSaved,
}: {
  registry?: Registry
  project: string
  environment: string
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(registry?.name || '')
  const [host, setHost] = useState(registry?.registry || '')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [realm, setRealm] = useState(registry?.token_realm || '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={registry ? `Rotate ${registry.name}` : 'Add a private registry'}
      description="Hakopod stores the credential securely and synchronizes image pull access."
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy) return
          setBusy(true)
          setError('')
          try {
            const body = {
              name,
              project,
              environment,
              registry: host,
              username,
              password,
              token_realm: realm,
              ...(registry ? { expected_revision: registry.revision } : {}),
            }
            if (registry)
              await unwrap(client.PUT('/registries/{name}', { params: { path: { name } }, body }))
            else await unwrap(client.POST('/registries', { body }))
            setPassword('')
            onSaved()
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body auth-form">
          <label>
            Credential name
            <input
              value={name}
              readOnly={Boolean(registry)}
              onChange={(e) => setName(e.target.value)}
              pattern="[a-z][a-z0-9-]*"
              maxLength={63}
              required
            />
          </label>
          <label>
            Registry host
            <input
              value={host}
              readOnly={Boolean(registry)}
              onChange={(e) => setHost(e.target.value)}
              placeholder="ghcr.io"
              maxLength={253}
              required
            />
          </label>
          <label>
            Username
            <input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="off"
              maxLength={256}
              required
            />
          </label>
          <label>
            Password or access token
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="off"
              maxLength={4096}
              required
            />
          </label>
          <label>
            Token realm (optional)
            <input
              type="url"
              value={realm}
              onChange={(e) => setRealm(e.target.value)}
              placeholder="Only when your registry needs a separate token host"
              maxLength={512}
            />
          </label>
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save credential'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
