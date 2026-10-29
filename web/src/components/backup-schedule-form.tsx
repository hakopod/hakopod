import { Input } from './ui/input'
import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { type BackupSource, type BackupSchedule, useBackupSchedules } from '../lib/backups'
import { FormPage } from './form-page'
import { BackupSourceFields } from './backup-source-fields'
import { everyHours, useBackupNames } from './backup-names'
import { ChoiceCards, FlowSteps, PendingButton } from './create-flow'
import { Icon } from './icons'
import { ResourceEmpty } from './resource-card'
import { ErrorState, Loading, RequestError } from './shared'
import { Consequence, Consequences, ReviewFacts } from './task-review'
import { Button } from './ui/button'

export default function BackupSchedulePage({ id }: { id?: string }) {
  const schedules = useBackupSchedules()
  const navigate = useNavigate()
  if (id && schedules.isPending) return <Loading />
  if (id && schedules.error)
    return <ErrorState error={schedules.error} retry={() => void schedules.refetch()} />
  const schedule = schedules.data?.items.find((item) => item.id === id)
  if (id && !schedule)
    return (
      <ResourceEmpty
        icon="clock"
        title="Schedule not found"
        description="It may have been removed. The schedule list shows the current schedules."
        action={
          <Button onClick={() => void navigate({ to: '/backups', search: { tab: 'schedules' } })}>
            Open schedules
          </Button>
        }
      />
    )
  return <BackupScheduleForm key={id || 'new'} schedule={schedule} />
}

const cadences = [
  { value: '6', title: 'Every 6 hours', description: 'Up to 6 hours of changes to recreate' },
  { value: '24', title: 'Daily', description: 'Up to a day of changes to recreate' },
  { value: '168', title: 'Weekly', description: 'Up to a week of changes to recreate' },
  { value: 'custom', title: 'Custom', description: 'Any interval from 1 to 8,760 hours' },
] as const

type Step = 'what' | 'when' | 'review'

function BackupScheduleForm({ schedule }: { schedule?: BackupSchedule }) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const names = useBackupNames()
  const [step, setStep] = useState<Step>(schedule ? 'when' : 'what')
  const [name, setName] = useState(schedule?.name || '')
  const [source, setSource] = useState<BackupSource | null>(schedule?.source || null)
  const [destination, setDestination] = useState(schedule?.destination_id || '')
  const [hours, setHours] = useState(schedule?.interval_hours || 24)
  const [custom, setCustom] = useState(
    Boolean(schedule && ![6, 24, 168].includes(schedule.interval_hours)),
  )
  const [retention, setRetention] = useState(schedule?.retention_count || 7)
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true)
  const [issue, setIssue] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const sourceName = source ? names.source(source) : ''
  const steps: Step[] = ['what', 'when', 'review']
  const back = () => void navigate({ to: '/backups', search: { tab: 'schedules' } })
  function next() {
    setError('')
    if (step === 'what') {
      if (!source || !destination) return setIssue('Choose a database and object storage.')
      if (!name) setName(`${sourceName} ${everyHours(hours)}`.slice(0, 80))
      setIssue('')
      return setStep('when')
    }
    const problem = !name.trim()
      ? 'Enter a schedule name.'
      : !Number.isInteger(hours) || hours < 1 || hours > 8760
        ? 'Use an interval between 1 and 8,760 hours.'
        : !Number.isInteger(retention) || retention < 1 || retention > 100
          ? 'Keep between 1 and 100 backups.'
          : ''
    setIssue(problem)
    if (!problem) setStep('review')
  }
  async function save() {
    if (busy || !source) return
    setBusy(true)
    setError('')
    try {
      const body = {
        name: name.trim(),
        destination_id: destination,
        source,
        interval_hours: hours,
        retention_count: retention,
        enabled,
        expected_revision: schedule?.revision || 0,
      }
      if (schedule)
        await unwrap(
          client.PUT('/backup-schedules/{id}', { params: { path: { id: schedule.id } }, body }),
        )
      else await unwrap(client.POST('/backup-schedules', { body }))
      void cache.invalidateQueries({ queryKey: ['backup-schedules'] })
      back()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  // The server sets the next run to one interval after each save.
  const firstRun = new Date(Date.now() + hours * 3600000).toISOString()
  return (
    <FormPage
      title={
        step === 'review'
          ? `Review ${name}`
          : schedule
            ? `Edit ${schedule.name}`
            : 'New backup schedule'
      }
      description="A schedule backs up one database on a fixed interval and keeps a set number of its backups."
      breadcrumbs={[
        { label: 'Backups', to: '/backups' },
        { label: schedule ? schedule.name : 'New schedule' },
      ]}
      icon="clock"
    >
      <div className="network-flow-context">
        <FlowSteps
          steps={['Database and storage', 'Interval and retention', 'Review']}
          current={steps.indexOf(step)}
          disabled={busy}
          onSelect={(index) => {
            setIssue('')
            setStep(steps[index])
          }}
        />
      </div>
      <div className="form-body create-flow backup-flow">
        {step === 'what' ? (
          <BackupSourceFields
            source={source}
            onSource={(value) => {
              setSource(value)
              setIssue('')
            }}
            destination={destination}
            onDestination={(value) => {
              setDestination(value)
              setIssue('')
            }}
          />
        ) : step === 'when' ? (
          <>
            <section className="create-section" aria-labelledby="schedule-interval-heading">
              <div className="create-section-heading">
                <h2 id="schedule-interval-heading">How often</h2>
                <span>Match it to how much recent data you can afford to recreate</span>
              </div>
              <ChoiceCards
                label="Interval"
                compact
                value={custom ? 'custom' : String(hours)}
                onChange={(value) => {
                  setCustom(value === 'custom')
                  if (value !== 'custom') setHours(Number(value))
                }}
                choices={cadences}
              />
              {custom && (
                <label className="backup-number-field">
                  Interval in hours
                  <Input
                    type="number"
                    min={1}
                    max={8760}
                    value={hours}
                    onChange={(event) => setHours(Number(event.target.value))}
                  />
                </label>
              )}
            </section>
            <section className="create-section" aria-labelledby="schedule-retention-heading">
              <div className="create-section-heading">
                <h2 id="schedule-retention-heading">Retention</h2>
              </div>
              <div className="create-fields backup-schedule-fields">
                <label className="backup-number-field">
                  Backups to keep
                  <Input
                    type="number"
                    min={1}
                    max={100}
                    value={retention}
                    onChange={(event) => setRetention(Number(event.target.value))}
                    aria-describedby="schedule-retention-help"
                  />
                  <span id="schedule-retention-help" className="field-help">
                    Keeps the newest {retention || 'N'} backups this schedule made, about{' '}
                    {retention && hours ? `${Math.round((retention * hours) / 24)} days` : 'N days'}{' '}
                    of history. Older ones from this schedule are deleted from the bucket.
                  </span>
                </label>
                <label>
                  Schedule name
                  <Input
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    maxLength={80}
                  />
                </label>
                <label className="checkbox-row min-h-11">
                  <input
                    type="checkbox"
                    checked={enabled}
                    onChange={(event) => setEnabled(event.target.checked)}
                  />
                  Run this schedule
                  {!enabled && <span className="muted-text"> · saved as paused</span>}
                </label>
              </div>
            </section>
          </>
        ) : (
          source && (
            <>
              <ReviewFacts
                label="Schedule"
                items={[
                  ['Database', <strong>{sourceName}</strong>],
                  [
                    'Stored in',
                    <span>
                      {names.destination(destination)}
                      <span className="block muted-text mono">{names.bucket(destination)}</span>
                    </span>,
                  ],
                  ['Interval', everyHours(hours).replace(/^./, (c) => c.toUpperCase())],
                  ['Keeps', `Newest ${retention}`],
                ]}
              />
              <Consequences>
                {enabled ? (
                  <Consequence>
                    {schedule ? 'Saving resets the next run to' : 'The first backup runs'} about{' '}
                    {hours} {hours === 1 ? 'hour' : 'hours'} after you save, around{' '}
                    {timestamp(firstRun)}
                    {schedule ? ` (currently ${timestamp(schedule.next_run_at)})` : ''}.
                  </Consequence>
                ) : (
                  <Consequence>Saved as paused. No backup runs until you enable it.</Consequence>
                )}
                <Consequence tone="warning">
                  Once this schedule has more than {retention} backups, the oldest ones it made are
                  deleted from the bucket. Backups you run by hand are not affected.
                </Consequence>
                <Consequence>
                  Each run appears in Job history with its size, duration and outcome.
                </Consequence>
              </Consequences>
            </>
          )
        )}
        {issue && <p className="field-error" role="alert">{issue}</p>}
        {error && <RequestError error={error} />}
      </div>
      <div className="form-footer deploy-footer">
        <span className="dialog-footer-note">
          <Icon name="lock" size={13} />
          {step === 'review' ? 'Saves only the reviewed schedule' : 'Nothing is saved until the review'}
        </span>
        <div className="deploy-footer-actions">
          <Button
            disabled={busy}
            onClick={() => {
              const index = steps.indexOf(step)
              if (index === 0 || (schedule && step === 'when')) back()
              else setStep(steps[index - 1])
            }}
          >
            {step === 'what' || (schedule && step === 'when') ? 'Cancel' : 'Back'}
          </Button>
          <PendingButton
            variant="primary"
            pending={busy}
            pendingLabel="Saving…"
            onClick={() => (step === 'review' ? void save() : next())}
          >
            <span className="network-primary-label">
              {step === 'review'
                ? schedule
                  ? `Save ${name}`
                  : `Create schedule ${name}`
                : step === 'what'
                  ? 'Choose interval'
                  : 'Review schedule'}
            </span>
            <Icon name="arrow" size={15} />
          </PendingButton>
        </div>
      </div>
    </FormPage>
  )
}
