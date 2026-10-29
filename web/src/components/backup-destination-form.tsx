import { useScope } from '../lib/scope'
import { Input } from './ui/input'
import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { type BackupDestination, useBackupDestinations } from '../lib/backups'
import { FormPage } from './form-page'
import { ChoiceCards, FlowSteps, PendingButton } from './create-flow'
import { Icon } from './icons'
import { ResourceEmpty } from './resource-card'
import { Consequence, Consequences, ReviewFacts } from './task-review'
import { Button } from './ui/button'
import { Copy, ErrorState, Loading, Note, RequestError } from './shared'
export default function BackupDestinationPage({ id }: { id?: string }) {
  const destinations = useBackupDestinations()
  if (id && destinations.isPending) return <Loading />
  if (id && destinations.error)
    return <ErrorState error={destinations.error} retry={() => void destinations.refetch()} />
  const destination = destinations.data?.items.find((item) => item.id === id)
  if (id && !destination)
    return (
      <ResourceEmpty
        icon="archive"
        title="Object storage not found"
        description="It may have been removed. The Object storage tab lists the current destinations."
      />
    )
  return <BackupDestinationForm key={id || 'new'} destination={destination} />
}
type Step = 'bucket' | 'credentials' | 'encryption' | 'review'

function BackupDestinationForm({ destination }: { destination?: BackupDestination }) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const steps: Step[] = destination
    ? ['credentials', 'review']
    : ['bucket', 'credentials', 'encryption', 'review']
  const [step, setStep] = useState<Step>(steps[0])
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
  const [issue, setIssue] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const back = () => void navigate({ to: '/backups', search: { tab: 'destinations' } })
  const location = [bucket, prefix].filter(Boolean).join('/')
  const credentialsChanged = Boolean(access || secret || session)
  // Field checks at each step boundary; the server validates the endpoint and credentials.
  function next() {
    const problem =
      step === 'bucket'
        ? !name.trim()
          ? 'Enter a display name.'
          : !/^https?:\/\/[^\s/]+/.test(endpoint)
            ? 'Enter the endpoint URL, for example https://s3.example.com.'
            : endpoint.startsWith('http://') && !allowHTTP
              ? 'Use an HTTPS endpoint, or allow HTTP for a trusted local endpoint.'
              : !region.trim() || !bucket.trim()
                ? 'Enter the region and bucket.'
                : ''
        : step === 'credentials'
          ? !name.trim()
            ? 'Enter a display name.'
            : !destination && (!access || !secret)
              ? 'Enter the access key ID and secret access key.'
              : destination && Boolean(access) !== Boolean(secret)
                ? 'Enter both the access key ID and the secret access key to replace them.'
                : ''
          : ''
    setIssue(problem)
    if (!problem) setStep(steps[steps.indexOf(step) + 1])
  }
  async function save() {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const body = {
        name: name.trim(),
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
      let key = ''
      if (destination)
        await unwrap(
          client.PUT('/backup-destinations/{id}', {
            params: { path: { id: destination.id } },
            body,
          }),
        )
      else key = (await unwrap(client.POST('/backup-destinations', { body }))).recovery_key || ''
      setAccess('')
      setSecret('')
      setSession('')
      setIdentity('')
      void cache.invalidateQueries({ queryKey: ['backup-destinations'] })
      if (key) setRecovery(key)
      else back()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  if (recovery)
    return (
      <FormPage
        title="Save your recovery key"
        description="This key decrypts every backup in this storage. It is shown once."
        breadcrumbs={[{ label: 'Backups', to: '/backups' }, { label: name }]}
        icon="key"
      >
        <div className="form-body create-flow backup-flow">
          <p className="network-flow-lead">
            {name} is saved. Store this key somewhere outside this installation, such as a password
            manager. Without it, backups in {location} cannot be restored after the installation is
            lost.
          </p>
          <div className="secret-once">
            <code>{recovery}</code>
            <Copy value={recovery} label="Copy recovery key" />
          </div>
          <label className="checkbox-row min-h-11">
            <input
              type="checkbox"
              checked={saved}
              onChange={(event) => setSaved(event.target.checked)}
            />
            I saved the recovery key outside this installation
          </label>
        </div>
        <div className="form-footer deploy-footer">
          <span className="dialog-footer-note">
            <Icon name="lock" size={13} />
            The key is not shown again
          </span>
          <div className="deploy-footer-actions">
            <Button
              variant="primary"
              disabled={!saved}
              onClick={() => {
                setRecovery('')
                back()
              }}
            >
              Done, open object storage
            </Button>
          </div>
        </div>
      </FormPage>
    )
  const titles: Record<Step, string> = {
    bucket: 'Bucket',
    credentials: destination ? 'Name and credentials' : 'Credentials',
    encryption: 'Encryption',
    review: 'Review',
  }
  return (
    <FormPage
      title={
        step === 'review'
          ? `Review ${name}`
          : destination
            ? `Edit ${destination.name}`
            : 'Add object storage'
      }
      description="An S3-compatible bucket that holds encrypted backups. Credentials stay on the server and are never shown again."
      breadcrumbs={[
        { label: 'Backups', to: '/backups' },
        { label: destination ? destination.name : 'Object storage' },
      ]}
      icon="archive"
    >
      <div className="network-flow-context">
        <FlowSteps
          steps={steps.map((item) => titles[item])}
          current={steps.indexOf(step)}
          disabled={busy}
          onSelect={(index) => {
            setIssue('')
            setStep(steps[index])
          }}
        />
      </div>
      <div className="form-body create-flow backup-flow">
        {destination && step !== 'review' && (
          <ReviewFacts
            label="Fixed storage"
            items={[
              ['Endpoint', <span className="mono">{destination.endpoint}</span>],
              ['Bucket and prefix', <span className="mono">{location}</span>],
              ['Region', destination.region],
            ]}
          />
        )}
        {step === 'bucket' ? (
          <section className="create-section" aria-labelledby="destination-bucket-heading">
            <div className="create-section-heading">
              <h2 id="destination-bucket-heading">Bucket</h2>
              <span>Endpoint, bucket and prefix cannot change after this is saved</span>
            </div>
            <div className="create-fields backup-destination-fields">
              <label className="backup-field-wide">
                Display name
                <Input
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  maxLength={80}
                  autoFocus
                />
              </label>
              <label className="backup-field-wide">
                S3 endpoint
                <Input
                  type="url"
                  value={endpoint}
                  onChange={(event) => setEndpoint(event.target.value)}
                  maxLength={512}
                  placeholder="https://s3.example.com"
                  aria-describedby="destination-endpoint-help"
                />
                <span id="destination-endpoint-help" className="field-help">
                  {scope.identity.admin
                    ? 'The provider’s S3 API URL.'
                    : 'A public HTTPS endpoint on port 443. Private addresses and redirects are blocked.'}
                </span>
              </label>
              <label>
                Region
                <Input
                  value={region}
                  onChange={(event) => setRegion(event.target.value)}
                  maxLength={128}
                />
              </label>
              <label>
                Bucket
                <Input
                  value={bucket}
                  onChange={(event) => setBucket(event.target.value)}
                  maxLength={128}
                />
              </label>
              <label className="backup-field-wide">
                Object prefix <span className="muted-text">(optional)</span>
                <Input
                  value={prefix}
                  onChange={(event) => setPrefix(event.target.value)}
                  maxLength={512}
                  aria-describedby="destination-prefix-help"
                />
                <span id="destination-prefix-help" className="field-help">
                  Backups are written under {location || 'the bucket root'}. A dedicated prefix
                  keeps access and lifecycle rules easy to review.
                </span>
              </label>
              <label className="checkbox-row min-h-11 backup-field-wide">
                <input
                  type="checkbox"
                  checked={pathStyle}
                  onChange={(event) => setPathStyle(event.target.checked)}
                />
                Use path-style addressing (most self-hosted S3 servers need this)
              </label>
              {scope.identity.admin && (
                <label className="checkbox-row min-h-11 backup-field-wide">
                  <input
                    type="checkbox"
                    checked={allowHTTP}
                    onChange={(event) => setAllowHTTP(event.target.checked)}
                  />
                  Allow an HTTP endpoint
                </label>
              )}
            </div>
            {allowHTTP && (
              <Note>
                HTTP sends storage credentials and backup traffic unencrypted over the network. Use
                it only for a trusted local endpoint.
              </Note>
            )}
          </section>
        ) : step === 'credentials' ? (
          <section className="create-section" aria-labelledby="destination-credentials-heading">
            <div className="create-section-heading">
              <h2 id="destination-credentials-heading">
                {destination ? 'Name and credentials' : 'Credentials'}
              </h2>
              <span>
                {destination
                  ? 'Leave credentials empty to keep the saved ones'
                  : 'Use a key scoped to this bucket and prefix'}
              </span>
            </div>
            <div className="create-fields backup-destination-fields">
              {destination && (
                <label className="backup-field-wide">
                  Display name
                  <Input
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    maxLength={80}
                  />
                </label>
              )}
              <label>
                Access key ID
                <Input
                  type="password"
                  value={access}
                  onChange={(event) => setAccess(event.target.value)}
                  maxLength={256}
                  autoComplete="off"
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
                />
              </label>
              <label className="backup-field-wide">
                Session token <span className="muted-text">(optional)</span>
                <Input
                  type="password"
                  value={session}
                  onChange={(event) => setSession(event.target.value)}
                  maxLength={4096}
                  autoComplete="off"
                  aria-describedby="destination-session-help"
                />
                <span id="destination-session-help" className="field-help">
                  Only for temporary credentials. Backups fail once a session token expires.
                </span>
              </label>
            </div>
          </section>
        ) : step === 'encryption' ? (
          <section className="create-section" aria-labelledby="destination-encryption-heading">
            <div className="create-section-heading">
              <h2 id="destination-encryption-heading">Encryption</h2>
              <span>Backups are encrypted with age before upload</span>
            </div>
            <ChoiceCardsEncryption
              identity={identity}
              onIdentity={setIdentity}
            />
          </section>
        ) : (
          <>
            <ReviewFacts
              label="Object storage"
              items={[
                ['Name', <strong>{name}</strong>],
                ['Endpoint', <span className="mono">{endpoint}</span>],
                ['Bucket and prefix', <span className="mono">{location}</span>],
                [
                  'Region and addressing',
                  `${region} · ${pathStyle ? 'path-style' : 'virtual-hosted'}${allowHTTP ? ' · HTTP allowed' : ''}`,
                ],
                [
                  'Credentials',
                  destination
                    ? credentialsChanged
                      ? 'Replaced with the values you entered'
                      : 'Unchanged'
                    : `Access key${session ? ' and session token' : ''} provided`,
                ],
                ...(destination
                  ? []
                  : [
                      [
                        'Encryption',
                        identity ? 'Your existing age identity' : 'A new recovery key, shown once',
                      ] as const,
                    ]),
              ]}
            />
            <Consequences>
              {destination ? (
                <Consequence>
                  Saves the name{credentialsChanged ? ' and credentials' : ''}. Schedules and
                  backups already using this storage keep using it.
                </Consequence>
              ) : (
                <>
                  <Consequence>
                    Saves {name} for backups and schedules. Use Test connection in the list to
                    check the bucket and credentials.
                  </Consequence>
                  <Consequence tone="warning">
                    Endpoint, bucket, prefix and encryption key cannot change later. To move
                    backups elsewhere, add another destination.
                  </Consequence>
                  {!identity && (
                    <Consequence>
                      The next page shows a recovery key once. You need it to restore backups if
                      this installation is lost.
                    </Consequence>
                  )}
                </>
              )}
              <Consequence>Credentials are write-only and never shown again.</Consequence>
            </Consequences>
          </>
        )}
        {issue && (
          <p className="field-error" role="alert">
            {issue}
          </p>
        )}
        {error && <RequestError error={error} />}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {step === 'review' ? 'Saves only the reviewed storage' : 'Nothing is saved until the review'}
        </span>
        <div className="deploy-footer-actions">
          <Button
            disabled={busy}
            onClick={() => {
              setIssue('')
              const index = steps.indexOf(step)
              if (index === 0) back()
              else setStep(steps[index - 1])
            }}
          >
            {steps.indexOf(step) === 0 ? 'Cancel' : 'Back'}
          </Button>
          <PendingButton
            variant="primary"
            pending={busy}
            pendingLabel="Saving…"
            onClick={() => (step === 'review' ? void save() : next())}
          >
            <span className="network-primary-label">
              {step === 'review'
                ? destination
                  ? `Save ${name}`
                  : `Add object storage ${name}`
                : `Continue to ${titles[steps[steps.indexOf(step) + 1]].toLowerCase()}`}
            </span>
            <Icon name="arrow" size={15} />
          </PendingButton>
        </div>
      </div>
    </FormPage>
  )
}

// Generate a key (the default) or bring an existing age identity.
function ChoiceCardsEncryption({
  identity,
  onIdentity,
}: {
  identity: string
  onIdentity: (value: string) => void
}) {
  const [mode, setMode] = useState<'generate' | 'existing'>(identity ? 'existing' : 'generate')
  return (
    <>
      <ChoiceCards
        label="Encryption key"
        compact
        value={mode}
        onChange={(value) => {
          setMode(value)
          if (value === 'generate') onIdentity('')
        }}
        choices={[
          {
            value: 'generate',
            title: 'Generate a recovery key',
            description: 'Shown once after saving. Store it outside this installation.',
          },
          {
            value: 'existing',
            title: 'Use an existing age identity',
            description: 'For backups you already decrypt with a key you hold.',
          },
        ]}
      />
      {mode === 'existing' && (
        <label>
          Age identity
          <Input
            type="password"
            value={identity}
            onChange={(event) => onIdentity(event.target.value)}
            maxLength={256}
            autoComplete="off"
            placeholder="AGE-SECRET-KEY-1…"
          />
        </label>
      )}
    </>
  )
}
