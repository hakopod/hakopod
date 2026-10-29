import { useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import {
  engineLabel,
  engineManagedSource,
  sourceFromTarget,
  sourceKey,
  type BackupSource,
} from '../lib/backups'
import { FormPage } from './form-page'
import { BackupSourceFields } from './backup-source-fields'
import { useBackupNames } from './backup-names'
import { FlowSteps, PendingButton } from './create-flow'
import { Icon } from './icons'
import { RequestError } from './shared'
import { Consequence, Consequences, ReviewFacts } from './task-review'
import { Button } from './ui/button'

export default function BackupRunForm({
  initialSource,
  initialDestination,
}: {
  // A sourceKey and destination ID, used by "Run again" links.
  initialSource?: string
  initialDestination?: string
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const names = useBackupNames()
  const key = useRef('')
  const [picked, setPicked] = useState<BackupSource | null>(null)
  const [pickedDestination, setPickedDestination] = useState('')
  const [touched, setTouched] = useState(false)
  const [reviewing, setReviewing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const preset = touched
    ? undefined
    : names.targets.data?.items.find(
        (item) => item.available && sourceKey(item) === initialSource,
      )
  const source = picked || (preset ? sourceFromTarget(preset) : null)
  const destination =
    pickedDestination ||
    (!touched && names.destinations.data?.items.some((item) => item.id === initialDestination)
      ? initialDestination || ''
      : '')
  const target = source
    ? names.targets.data?.items.find((item) => sourceKey(item) === sourceKey(source))
    : undefined
  const storage = names.destinations.data?.items.find((item) => item.id === destination)
  const sourceName = source ? names.source(source) : ''
  async function start() {
    if (busy || !source || !destination) return
    setBusy(true)
    setError('')
    if (!key.current) key.current = crypto.randomUUID()
    try {
      const job = await unwrap(
        client.POST('/backups', {
          body: { destination_id: destination, source },
          params: { header: { 'Idempotency-Key': key.current } },
        }),
      )
      void cache.invalidateQueries({ queryKey: ['backups'] })
      void navigate({ to: '/backups/$jobId', params: { jobId: job.id } })
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <FormPage
      title={reviewing ? `Back up ${sourceName}` : 'Run a backup'}
      description="A one-off encrypted database backup, written to object storage. Schedules run the same backup on an interval."
      breadcrumbs={[{ label: 'Backups', to: '/backups' }, { label: 'Run backup' }]}
      icon="archive"
    >
      <div className="network-flow-context">
        <FlowSteps
          steps={['Database and storage', 'Review']}
          current={reviewing ? 1 : 0}
          disabled={busy}
          onSelect={() => setReviewing(false)}
        />
      </div>
      <div className="form-body create-flow backup-flow">
        {reviewing && source && storage ? (
          <>
            <ReviewFacts
              label="Backup"
              items={[
                ['Database', <strong>{sourceName}</strong>],
                ['Engine', engineLabel(source.engine)],
                [
                  'Stored in',
                  <span>
                    {storage.name}
                    <span className="block muted-text mono">
                      {[storage.bucket, storage.prefix].filter(Boolean).join('/')}
                    </span>
                  </span>,
                ],
                [
                  'Encrypted for',
                  <span className="mono" title={storage.encryption_recipient}>
                    {storage.encryption_recipient.slice(0, 16)}…
                  </span>,
                ],
              ]}
            />
            <Consequences>
              <Consequence>
                Starts now. The job page follows it and reports the size, duration and outcome, or
                the error if it fails.
              </Consequence>
              {engineManagedSource(source) ? (
                <Consequence>
                  {engineLabel(source.engine)} writes this backup to the bucket itself. Hakopod waits
                  for it for up to two hours.
                </Consequence>
              ) : (
                <Consequence>
                  The dump is encrypted before it leaves this installation. Restoring it needs the
                  destination’s recovery key.
                </Consequence>
              )}
              {target?.message && <Consequence tone="warning">{target.message}</Consequence>}
            </Consequences>
          </>
        ) : (
          <BackupSourceFields
            source={source}
            onSource={(value) => {
              setTouched(true)
              if (!pickedDestination) setPickedDestination(destination)
              setPicked(value)
              key.current = ''
            }}
            destination={destination}
            onDestination={(value) => {
              setTouched(true)
              if (!picked) setPicked(source)
              setPickedDestination(value)
              key.current = ''
            }}
          />
        )}
        {error && <RequestError error={error} />}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {reviewing ? 'Starts one backup job' : 'Nothing runs until you confirm'}
        </span>
        <div className="deploy-footer-actions">
          <Button
            disabled={busy}
            onClick={() => (reviewing ? setReviewing(false) : void navigate({ to: '/backups' }))}
          >
            {reviewing ? 'Back' : 'Cancel'}
          </Button>
          <PendingButton
            variant="primary"
            pending={busy}
            pendingLabel="Starting…"
            disabled={!source || !storage}
            onClick={() => (reviewing ? void start() : setReviewing(true))}
          >
            <span className="network-primary-label">
              {reviewing ? `Back up ${sourceName} now` : 'Review backup'}
            </span>
            <Icon name="arrow" size={15} />
          </PendingButton>
        </div>
      </div>
    </FormPage>
  )
}
