import { useScope } from '../lib/scope'
import { Input } from './ui/input'
import { useState } from 'react'
import { useNavigate, Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { type BackupDestination, useBackupDestinations } from '../lib/backups'
import { FormPage, FormHint, FormSection } from './form-page'
import { Button } from './ui/button'
import { Copy, Empty, ErrorState, Loading, Note } from './shared'
export default function BackupDestinationPage({ id }: { id?: string }) {
  const destinations = useBackupDestinations()
  if (id && destinations.isPending) return <Loading />
  if (id && destinations.error) return <ErrorState error={destinations.error} />
  const destination = destinations.data?.items.find((item) => item.id === id)
  if (id && !destination)
    return (
      <Empty
        title="Destination not found"
        description="This object storage destination is no longer available."
      />
    )
  return <BackupDestinationForm key={id || 'new'} destination={destination} />
}
function BackupDestinationForm({ destination }: { destination?: BackupDestination }) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [name, setName] = useState(destination?.name || '')
  const [endpoint, setEndpoint] = useState(destination?.endpoint || '')
  const [region, setRegion] = useState(destination?.region || 'us-east-1')
  const [bucket, setBucket] = useState(destination?.bucket || '')
  const [prefix, setPrefix] = useState(destination?.prefix || 'hakopod')
  const [pathStyle, setPathStyle] = useState(destination?.path_style ?? true)
  const [allowHTTP, setAllowHTTP] = useState(destination?.allow_http || false)
  const [access, setAccess] = useState('')
  const [secret, setSecret] = useState('')
  const [session, setSession] = useState('')
  const [identity, setIdentity] = useState('')
  const [recovery, setRecovery] = useState('')
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const back = () => void navigate({ to: '/backups', search: { tab: 'destinations' } })
  return (
    <FormPage
      title={
        recovery
          ? 'Save your recovery key'
          : destination
            ? 'Edit object storage'
            : 'Add object storage'
      }
      description={
        recovery
          ? 'This key is shown once. Store it independently from the Hakopod installation.'
          : 'Connect an S3-compatible bucket for encrypted backup artifacts.'
      }
      breadcrumbs={[
        { label: 'Backups', to: '/backups' },
        { label: destination ? destination.name : 'Object storage' },
      ]}
      icon="archive"
      help={
        <>
          <FormHint title="Use a dedicated bucket">
            A separate bucket or prefix makes access and retention easier to review.
          </FormHint>
          <FormHint title="Storage identity is fixed">
            After creation, only the display name and credentials can change. Create another
            destination to move to a different bucket or encryption key.
          </FormHint>
        </>
      }
    >
      {recovery ? (
        <>
          <FormSection title="Recovery key" icon="key">
            <Note>
              Save this key in a secure location outside this installation. It is needed to decrypt
              the backup artifacts if the installation is lost.
            </Note>
            <div className="secret-once">
              <code>{recovery}</code>
              <Copy value={recovery} label="Copy recovery key" />
            </div>
            <label className="checkbox-row min-h-11">
              <Input
                type="checkbox"
                checked={saved}
                onChange={(event) => setSaved(event.target.checked)}
              />
              I saved the recovery key separately.
            </label>
          </FormSection>
          <div className="form-footer">
            <Button
              variant="primary"
              disabled={!saved}
              onClick={() => {
                setRecovery('')
                back()
              }}
            >
              Finish
            </Button>
          </div>
        </>
      ) : (
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (busy) return
            setBusy(true)
            setError('')
            try {
              const body = {
                name,
                endpoint,
                region,
                bucket,
                prefix,
                path_style: pathStyle,
                allow_http: allowHTTP,
                expected_revision: destination?.revision || 0,
                ...(access ? { access_key_id: access } : {}),
                ...(secret ? { secret_access_key: secret } : {}),
                ...(session ? { session_token: session } : {}),
                ...(identity ? { encryption_identity: identity } : {}),
              }
              if (destination) {
                await unwrap(
                  client.PUT('/backup-destinations/{id}', {
                    params: { path: { id: destination.id } },
                    body,
                  }),
                )
                back()
              } else {
                const result = await unwrap(client.POST('/backup-destinations', { body }))
                if (result.recovery_key) setRecovery(result.recovery_key)
                else back()
              }
              setAccess('')
              setSecret('')
              setSession('')
              setIdentity('')
              void cache.invalidateQueries({ queryKey: ['backup-destinations'] })
            } catch (err) {
              setError(message(err))
            } finally {
              setBusy(false)
            }
          }}
        >
          <div className="form-body">
            <FormSection
              title="Destination"
              description="Choose the endpoint and bucket supplied by your storage provider."
              icon="archive"
            >
              <label>
                Display name
                <Input
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  maxLength={80}
                  required
                  placeholder="Production backups"
                />
              </label>
              <label>
                S3 endpoint
                <Input
                  type="url"
                  value={endpoint}
                  disabled={Boolean(destination)}
                  onChange={(event) => setEndpoint(event.target.value)}
                  maxLength={512}
                  placeholder="https://s3.example.com"
                  required
                />
              </label>
              <div className="form-grid-two">
                <label>
                  Region
                  <Input
                    value={region}
                    disabled={Boolean(destination)}
                    onChange={(event) => setRegion(event.target.value)}
                    maxLength={128}
                    required
                  />
                </label>
                <label>
                  Bucket
                  <Input
                    value={bucket}
                    disabled={Boolean(destination)}
                    onChange={(event) => setBucket(event.target.value)}
                    maxLength={128}
                    required
                  />
                </label>
              </div>
              <label>
                Object prefix
                <Input
                  value={prefix}
                  disabled={Boolean(destination)}
                  onChange={(event) => setPrefix(event.target.value)}
                  maxLength={512}
                />
              </label>
              <label className="checkbox-row min-h-11">
                <Input
                  type="checkbox"
                  checked={pathStyle}
                  disabled={Boolean(destination)}
                  onChange={(event) => setPathStyle(event.target.checked)}
                />
                Use path-style bucket addressing
              </label>
              {scope.identity.admin && (
                <label className="checkbox-row min-h-11">
                  <Input
                    type="checkbox"
                    checked={allowHTTP}
                    disabled={Boolean(destination)}
                    onChange={(event) => setAllowHTTP(event.target.checked)}
                  />
                  Allow an HTTP endpoint
                </label>
              )}
              {!scope.identity.admin && (
                <FormHint title="Workspace storage">
                  Use a public HTTPS S3-compatible endpoint on port 443. Private addresses and
                  redirects are blocked.
                </FormHint>
              )}
              {allowHTTP && (
                <Note>
                  HTTP exposes storage credentials and traffic to the network. Use it only for an
                  intentional trusted local endpoint.
                </Note>
              )}
            </FormSection>
            <FormSection
              title="Storage credentials"
              description={
                destination
                  ? 'Leave credential fields empty to preserve the saved values.'
                  : 'Use a credential scoped to this bucket and prefix.'
              }
              icon="lock"
            >
              <label>
                Access key ID
                <Input
                  type="password"
                  value={access}
                  onChange={(event) => setAccess(event.target.value)}
                  maxLength={256}
                  autoComplete="off"
                  required={!destination}
                />
              </label>
              <label>
                Secret access key
                <Input
                  type="password"
                  value={secret}
                  onChange={(event) => setSecret(event.target.value)}
                  maxLength={4096}
                  autoComplete="off"
                  required={!destination}
                />
              </label>
              <label>
                Session token (optional)
                <Input
                  type="password"
                  value={session}
                  onChange={(event) => setSession(event.target.value)}
                  maxLength={4096}
                  autoComplete="off"
                />
              </label>
            </FormSection>
            {!destination && (
              <FormSection
                title="Backup encryption"
                description="Generate a fresh age key or use an existing identity."
                icon="key"
              >
                <label>
                  Existing age identity (optional)
                  <Input
                    type="password"
                    value={identity}
                    onChange={(event) => setIdentity(event.target.value)}
                    maxLength={256}
                    autoComplete="off"
                    placeholder="Leave empty to generate a recovery key"
                  />
                </label>
                <Note>A newly generated recovery key is shown once after saving.</Note>
              </FormSection>
            )}
            {error && <ErrorState error={error} />}
          </div>
          <div className="form-footer">
            <Link className="button" to="/backups" search={{ tab: 'destinations' }}>
              Cancel
            </Link>
            <Button variant="primary" type="submit" disabled={busy}>
              {busy ? 'Saving…' : 'Save destination'}
            </Button>
          </div>
        </form>
      )}
    </FormPage>
  )
}
