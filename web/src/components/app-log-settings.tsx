import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import { retentionNote, validateLogSettings } from '../lib/app-logs'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { ErrorState, Loading, Note, RequestError } from './shared'
import { SettingsSection } from './settings-section'
import type { components } from '../lib/api.generated'

type Settings = components['schemas']['AppLogSettings']

export default function AppLogSettings() {
  const query = useQuery({
    queryKey: ['app-log-settings'],
    queryFn: ({ signal }) => unwrap(client.GET('/settings/app-logs', { signal })),
    retry: false,
    refetchOnWindowFocus: false,
  })
  if (query.isPending) return <Loading />
  // Admin only: hide the section when the API says no.
  if (query.error instanceof APIError && query.error.status === 403) return null
  if (query.error || !query.data)
    return <ErrorState error={query.error} retry={() => void query.refetch()} />
  return <Form initial={query.data} />
}

export function Form({ initial }: { initial: Settings }) {
  const [enabled, setEnabled] = useState(initial.enabled)
  const [days, setDays] = useState(String(initial.retention_days))
  const [mib, setMib] = useState(String(initial.max_mib_per_app))
  const [revision, setRevision] = useState(initial.revision)
  const [saved, setSaved] = useState<Settings>(initial)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const [ok, setOk] = useState(false)
  const problems = validateLogSettings(days, mib)
  const invalid = Boolean(problems.retention || problems.size)
  const save = async () => {
    if (busy || invalid) return
    setBusy(true)
    setError('')
    setConflict(false)
    setOk(false)
    try {
      const next = await unwrap(
        client.PUT('/settings/app-logs', {
          body: {
            enabled,
            retention_days: Number(days),
            max_mib_per_app: Number(mib),
            expected_revision: revision,
          },
        }),
      )
      setSaved(next)
      setRevision(next.revision)
      setOk(true)
    } catch (e) {
      if (e instanceof APIError && e.status === 409) {
        // Keep what the operator typed; only pick up the new revision.
        try {
          const latest = await unwrap(client.GET('/settings/app-logs'))
          setRevision(latest.revision)
          setSaved(latest)
        } catch {}
        setConflict(true)
      } else setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <SettingsSection
      title="Application logs"
      help="Hakopod collects application logs into stored history. Shorter retention or a smaller limit prunes older lines at the next periodic cleanup."
      state={
        saved.enabled
          ? retentionNote(saved.retention_days, saved.max_mib_per_app)
          : 'Collection off · stored history is still pruned to the saved limits'
      }
    >
      <form
        className="settings-log-form"
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <label className="checkbox-row">
          <Input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Collect application logs
        </label>
        <div className="form-grid-two">
          <div className="grid gap-1 text-sm">
            <label htmlFor="app-log-days">Retention (days)</label>
            <Input
              id="app-log-days"
              inputMode="numeric"
              value={days}
              aria-invalid={Boolean(problems.retention)}
              aria-describedby={problems.retention ? 'app-log-days-error' : undefined}
              onChange={(e) => setDays(e.target.value)}
            />
            {problems.retention && (
              <p id="app-log-days-error" role="alert" className="m-0 text-destructive">
                {problems.retention}
              </p>
            )}
          </div>
          <div className="grid gap-1 text-sm">
            <label htmlFor="app-log-mib">Max MiB per application</label>
            <Input
              id="app-log-mib"
              inputMode="numeric"
              value={mib}
              aria-invalid={Boolean(problems.size)}
              aria-describedby={problems.size ? 'app-log-mib-error' : undefined}
              onChange={(e) => setMib(e.target.value)}
            />
            {problems.size && (
              <p id="app-log-mib-error" role="alert" className="m-0 text-destructive">
                {problems.size}
              </p>
            )}
          </div>
        </div>
        {conflict && (
          <Note>
            These settings were changed by someone else. Your values are kept; review them and save
            again. Currently saved: {saved.retention_days} days, {saved.max_mib_per_app} MiB.
          </Note>
        )}
        {error && <RequestError error={error} />}
        <div className="settings-inline-controls">
          <Button type="submit" variant="primary" disabled={busy || invalid}>
            {busy ? 'Saving…' : 'Save log retention'}
          </Button>
          {ok && (
            <p role="status" className="m-0 text-sm">
              Saved.
            </p>
          )}
        </div>
      </form>
    </SettingsSection>
  )
}
