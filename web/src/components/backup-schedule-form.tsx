import { Input } from './ui/input'
import { useState } from 'react'
import { useNavigate, Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { type BackupSource, type BackupSchedule, useBackupSchedules } from '../lib/backups'
import { FormPage, FormHint, FormSection } from './form-page'
import { BackupSourceFields } from './backup-source-fields'
import { Button } from './ui/button'
import { Empty, ErrorState, Loading } from './shared'
export default function BackupSchedulePage({ id }: { id?: string }) {
  const schedules = useBackupSchedules()
  if (id && schedules.isPending) return <Loading />
  if (id && schedules.error) return <ErrorState error={schedules.error} />
  const schedule = schedules.data?.items.find((item) => item.id === id)
  if (id && !schedule)
    return (
      <Empty
        title="Schedule not found"
        description="This backup schedule is no longer available."
      />
    )
  return <BackupScheduleForm key={id || 'new'} schedule={schedule} />
}
function BackupScheduleForm({ schedule }: { schedule?: BackupSchedule }) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [name, setName] = useState(schedule?.name || '')
  const [source, setSource] = useState<BackupSource | null>(schedule?.source || null)
  const [destination, setDestination] = useState(schedule?.destination_id || '')
  const [hours, setHours] = useState(schedule?.interval_hours || 24)
  const [retention, setRetention] = useState(schedule?.retention_count || 7)
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <FormPage
      title={schedule ? 'Edit backup schedule' : 'Create a backup schedule'}
      description="Run recurring database backups with an explicit interval and retention count."
      breadcrumbs={[
        { label: 'Backups', to: '/backups' },
        { label: schedule ? schedule.name : 'New schedule' },
      ]}
      icon="clock"
      help={
        <>
          <FormHint title="Choose a useful cadence">
            Match the interval to how much recent data you can afford to recreate.
          </FormHint>
          <FormHint title="Retention is a count">
            Old backups from this schedule become eligible for cleanup after the configured count is
            exceeded.
          </FormHint>
        </>
      }
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy || !source) return
          setBusy(true)
          setError('')
          try {
            const body = {
              name,
              destination_id: destination,
              source,
              interval_hours: hours,
              retention_count: retention,
              enabled,
              expected_revision: schedule?.revision || 0,
            }
            if (schedule)
              await unwrap(
                client.PUT('/backup-schedules/{id}', {
                  params: { path: { id: schedule.id } },
                  body,
                }),
              )
            else await unwrap(client.POST('/backup-schedules', { body }))
            void cache.invalidateQueries({ queryKey: ['backup-schedules'] })
            void navigate({ to: '/backups', search: { tab: 'schedules' } })
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="form-body">
          <FormSection title="Database and destination" icon="database">
            <label>
              Schedule name
              <Input
                value={name}
                onChange={(event) => setName(event.target.value)}
                maxLength={80}
                required
                placeholder="Daily production backup"
              />
            </label>
            <BackupSourceFields
              source={source}
              onSource={setSource}
              destination={destination}
              onDestination={setDestination}
            />
          </FormSection>
          <FormSection title="Timing and retention" icon="clock">
            <div className="form-grid-two">
              <label>
                Interval in hours
                <Input
                  type="number"
                  min={1}
                  max={8760}
                  required
                  value={hours}
                  onChange={(event) => setHours(Number(event.target.value))}
                />
              </label>
              <label>
                Backups to retain
                <Input
                  type="number"
                  min={1}
                  max={100}
                  required
                  value={retention}
                  onChange={(event) => setRetention(Number(event.target.value))}
                />
              </label>
            </div>
            <label className="checkbox-row">
              <Input
                type="checkbox"
                checked={enabled}
                onChange={(event) => setEnabled(event.target.checked)}
              />
              Enable this schedule
            </label>
          </FormSection>
          {error && <ErrorState error={error} />}
        </div>
        <div className="form-footer">
          <Link className="button" to="/backups" search={{ tab: 'schedules' }}>
            Cancel
          </Link>
          <Button type="submit" variant="primary" disabled={busy || !source || !destination}>
            {busy ? 'Saving…' : 'Save schedule'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
