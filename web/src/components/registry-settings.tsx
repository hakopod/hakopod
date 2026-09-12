import { Input } from './ui/input'
import { useState } from 'react'
import { MoreHorizontal } from 'lucide-react'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { useNavigate } from '@tanstack/react-router'
import { FormPage, FormSection, FormHint } from './form-page'
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
  const navigate = useNavigate()
  const [remove, setRemove] = useState<Registry | null>(null)
  const [confirmation, setConfirmation] = useState('')
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
          <Button
            variant="primary"
            onClick={() => void navigate({ to: '/infrastructure/registries/new' })}
          >
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
                <Menu
                  trigger={
                    <Button
                      variant="ghost"
                      size="icon"
                      disabled={busy}
                      aria-label={`Actions for registry ${registry.name}`}
                    >
                      <MoreHorizontal size={16} strokeWidth={1.75} />
                    </Button>
                  }
                >
                  <MenuItem
                    disabled={busy}
                    onSelect={() =>
                      void navigate({
                        to: '/infrastructure/registries/$name',
                        params: { name: registry.name },
                      })
                    }
                  >
                    Rotate credential
                  </MenuItem>
                  {!registry.synchronized && (
                    <MenuItem
                      disabled={busy}
                      onSelect={async () => {
                        if (busy) return
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
                    </MenuItem>
                  )}
                  <MenuItem
                    destructive
                    disabled={busy}
                    onSelect={() => {
                      setError('')
                      setConfirmation('')
                      setRemove(registry)
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
      {error && !remove && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <Note>
        Credentials are write-only. Select the saved name when staging a service, or set{' '}
        <code>registry_credential</code> in TOML. Running containers continue after credential
        rotation; future image pulls use the saved credential.
      </Note>
      <Dialog
        open={Boolean(remove)}
        onOpenChange={(open) => {
          if (!busy && !open) setRemove(null)
        }}
        title={`Delete ${remove?.name || 'registry credential'}?`}
        description="Services using this credential may fail future private image pulls."
      >
        <div className="dialog-body">
          <label>
            Type <code>{remove?.name}</code> to delete this credential
            <Input
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              aria-label="Confirm registry credential name"
            />
          </label>
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
            disabled={busy || !remove || confirmation !== remove.name}
            onClick={async () => {
              if (!remove || busy || confirmation !== remove.name) return
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

export function RegistryForm({
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
    <FormPage
      breadcrumbs={[
        { label: 'Infrastructure', to: '/infrastructure' },
        { label: 'Registries', to: '/infrastructure' },
        { label: registry ? 'Rotate credential' : 'Add registry' },
      ]}
      icon="lock"
      help={
        <FormHint title="Use a dedicated token">
          Grant only the package read permissions needed for your images. Credential values are
          write-only.
        </FormHint>
      }
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
        <div className="form-body auth-form">
          <FormSection
            title="Registry identity"
            description="Saved applications refer to the credential name."
            icon="box"
          >
            <label>
              Credential name
              <Input
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
              <Input
                value={host}
                readOnly={Boolean(registry)}
                onChange={(e) => setHost(e.target.value)}
                placeholder="ghcr.io"
                maxLength={253}
                required
              />
            </label>
          </FormSection>
          <FormSection
            title="Authentication"
            description="Secrets are encrypted and never returned by the API."
            icon="lock"
          >
            <label>
              Username
              <Input
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="off"
                maxLength={256}
                required
              />
            </label>
            <label>
              Password or access token
              <Input
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
              <Input
                type="url"
                value={realm}
                onChange={(e) => setRealm(e.target.value)}
                placeholder="Only when your registry needs a separate token host"
                maxLength={512}
              />
            </label>
          </FormSection>
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="form-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save credential'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
