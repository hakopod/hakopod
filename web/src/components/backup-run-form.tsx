import { useRef, useState } from 'react'
import { useNavigate, Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { type BackupSource } from '../lib/backups'
import { FormPage, FormHint, FormSection } from './form-page'
import { BackupSourceFields } from './backup-source-fields'
import { Button } from './ui/button'
import { ErrorState } from './shared'
export default function BackupRunForm() {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const key = useRef('')
  const [source, setSource] = useState<BackupSource | null>(null)
  const [destination, setDestination] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <FormPage
      title="Run a database backup"
      description="Create an encrypted database dump in a saved object storage destination."
      breadcrumbs={[{ label: 'Backups', to: '/backups' }, { label: 'Run backup' }]}
      icon="archive"
      help={
        <>
          <FormHint title="Pick a recovery destination">
            Keep the bucket and recovery key accessible outside this installation. A local copy
            alone does not protect against host loss.
          </FormHint>
          <FormHint title="Check the result">
            The job page reports the actual completion, size, and any failure. A queued job is not a
            completed backup.
          </FormHint>
        </>
      }
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
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
        }}
      >
        <FormSection
          title="Source and destination"
          description="Only discovered database services and the management database are selectable."
          icon="database"
        >
          <BackupSourceFields
            source={source}
            onSource={(value) => {
              setSource(value)
              key.current = ''
            }}
            destination={destination}
            onDestination={(value) => {
              setDestination(value)
              key.current = ''
            }}
          />
          {error && <ErrorState error={error} />}
        </FormSection>
        <div className="form-footer">
          <Link className="button" to="/backups">
            Cancel
          </Link>
          <Button type="submit" variant="primary" disabled={busy || !source || !destination}>
            {busy ? 'Starting…' : 'Run encrypted backup'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
