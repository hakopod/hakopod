import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { SecretForm } from './application-secrets'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { Copy, Empty, ErrorState, Note } from './shared'
import { Icon } from './icons'

export function ServiceSecrets({
  application,
  serviceName,
}: {
  application: Application
  serviceName: string
}) {
  const scope = useScope()
  const cache = useQueryClient()
  const query = {
    project: application.project,
    environment: application.environment,
    application: application.name,
  }
  const queryKey = ['secrets', query.project, query.environment, query.application]
  const secrets = useQuery({
    queryKey,
    queryFn: ({ signal }) => unwrap(client.GET('/secrets', { signal, params: { query } })),
    retry: false,
    gcTime: 0,
  })
  const [create, setCreate] = useState(false)
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [checking, setChecking] = useState(false)
  const [saving, setSaving] = useState(false)
  const [edit, setEdit] = useState<{ name: string; unbound: boolean } | null>(null)
  const [saved, setSaved] = useState<{ name: string; unbound: boolean } | null>(null)
  const bindings = Object.entries(application.spec.services[serviceName]?.secrets || {}).sort(
    ([left], [right]) => left.localeCompare(right),
  )
  const references = [...new Set(bindings.map(([, secret]) => secret.ref))]
  const users = (reference: string) =>
    Object.entries(application.spec.services)
      .filter(([, service]) =>
        Object.values(service.secrets || {}).some((secret) => secret.ref === reference),
      )
      .map(([name]) => name)
      .sort()
  const canWrite = scope.can('deployments:write')
  const metadataReady = Boolean(secrets.data) && !secrets.error && !secrets.isFetching
  const bindingLink = (label: string) => (
    <Button asChild>
      <Link
        to="/applications/$applicationId/configure"
        params={{ applicationId: application.id }}
        search={{ mode: 'toml', service: serviceName }}
      >
        <Icon name="code" size={14} />
        {label}
      </Link>
    </Button>
  )
  return (
    <div className="service-secrets-tab">
      <div className="section-toolbar">
        <div>
          <h2>Secret references</h2>
          <p>
            {bindings.length} variables bound to {references.length} application secrets.
          </p>
        </div>
        {canWrite && (
          <div className="toolbar-actions">
            {bindingLink('Edit bindings')}
            <Button
              variant="primary"
              disabled={!metadataReady}
              onClick={() => {
                setName('')
                setError('')
                setCreate(true)
              }}
            >
              <Icon name="plus" size={14} />
              Add secret
            </Button>
          </div>
        )}
      </div>
      <p className="field-help">
        Values are write-only. Bindings belong to this service; stored values belong to the
        application and may be shared. Restart affected services after replacing a value.
      </p>
      {secrets.error && <ErrorState error={secrets.error} retry={() => void secrets.refetch()} />}
      {saved && (
        <div role="status">
          <Note>
            <strong>{saved.name}</strong> saved.{' '}
            {users(saved.name).length > 0 &&
              `Restart ${users(saved.name).join(', ')} to load the new value. `}
            {saved.unbound &&
              `It is not attached to ${serviceName} yet. Add its reference with Edit bindings, then review and deploy the service change.`}
          </Note>
        </div>
      )}
      {bindings.length ? (
        <div className="panel settings-session-list">
          {references.map((reference) => {
            const metadata = secrets.data?.items.find((secret) => secret.name === reference)
            const shared = users(reference).filter((name) => name !== serviceName)
            return (
              <div className="settings-list-row" key={reference}>
                <div>
                  <strong className="mono">
                    {bindings
                      .filter(([, secret]) => secret.ref === reference)
                      .map(([name]) => name)
                      .join(', ')}
                  </strong>
                  <span className="copyable-address">
                    <Icon name="lock" size={13} />
                    <code className="break-text">{reference}</code>
                    <Copy value={reference} />
                  </span>
                  <small>
                    {secrets.isPending
                      ? 'Checking saved value…'
                      : secrets.error
                        ? 'Saved status unavailable'
                        : metadata
                          ? `Saved · updated ${timestamp(metadata.updated_at)}`
                          : 'No saved value'}
                  </small>
                  {shared.length > 0 && <small>Also used by {shared.join(', ')}.</small>}
                </div>
                {canWrite && (
                  <Button
                    size="sm"
                    disabled={!metadataReady}
                    onClick={() => setEdit({ name: reference, unbound: false })}
                  >
                    {metadata ? 'Replace value' : 'Set value'}
                  </Button>
                )}
              </div>
            )
          })}
        </div>
      ) : (
        <Empty
          icon="lock"
          title="No secrets bound to this service"
          description="Save a value, then bind its reference to an environment variable in this service’s reviewed configuration."
        />
      )}
      <Dialog
        open={create}
        onOpenChange={(open) => {
          if (!checking) setCreate(open)
        }}
        title="Add a service secret"
        description="Choose a new application secret name. Saving its value and binding it to this service are separate steps."
      >
        <div className="dialog-body">
          <form
            className="auth-form"
            onSubmit={async (event) => {
              event.preventDefault()
              if (checking) return
              setChecking(true)
              setError('')
              try {
                const current = await secrets.refetch()
                if (current.error) throw current.error
                if (!current.data) throw new Error('Secret metadata is unavailable. Try again.')
                if (current.data.items.some((secret) => secret.name === name))
                  throw new Error(
                    'This application already has a secret with that name. Choose another name, or use Edit bindings to attach the existing reference.',
                  )
                setCreate(false)
                setEdit({ name, unbound: !references.includes(name) })
              } catch (cause) {
                setError(message(cause))
              } finally {
                setChecking(false)
              }
            }}
          >
            <label>
              Secret name
              <Input
                value={name}
                onChange={(event) => setName(event.target.value)}
                pattern="[a-z](([a-z0-9]|-){0,38}[a-z0-9])?"
                maxLength={40}
                autoComplete="off"
                spellCheck={false}
                required
                disabled={checking}
                placeholder={`${serviceName.slice(0, 30)}-secret`}
              />
              <span className="field-help">
                Lowercase letters, numbers and hyphens; up to 40 characters.
              </span>
            </label>
            {error && (
              <div className="inline-error" role="alert">
                {error}
              </div>
            )}
            <Button type="submit" variant="primary" disabled={checking || !name}>
              {checking ? 'Checking…' : 'Continue to value'}
            </Button>
          </form>
        </div>
      </Dialog>
      {edit && (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!saving && !open) setEdit(null)
          }}
          title={`${secrets.data?.items.some((secret) => secret.name === edit.name) ? 'Replace' : 'Set'} ${edit.name}`}
          description={[
            users(edit.name).length
              ? `Used by ${users(edit.name).join(', ')}. Saving this name creates or replaces its application value for every listed service on its next start.`
              : `This value is saved in ${application.name}'s secret scope. Saving an existing name replaces its stored value.`,
            edit.unbound
              ? `It is not attached to ${serviceName} until you review and deploy a binding.`
              : '',
          ]
            .filter(Boolean)
            .join(' ')}
        >
          <div className="dialog-body">
            <SecretForm
              query={query}
              initialName={edit.name}
              onBusyChange={setSaving}
              onSaved={() => {
                setSaved(edit)
                setEdit(null)
                void cache.invalidateQueries({ queryKey })
              }}
            />
          </div>
        </Dialog>
      )}
    </div>
  )
}
