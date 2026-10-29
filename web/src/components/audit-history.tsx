import { editionFetch } from '../lib/client-edition'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useLicense } from '../lib/license'
import { message, relative, timestamp } from '../lib/api'
import { SelectField } from './ui/select'
import { Button } from './ui/button'
import { ErrorState, Loading } from './shared'
import { SettingsEmpty, SettingsRow, SettingsRows, SettingsSection } from './settings-section'

export function AuditHistory() {
  const license = useLicense()
  const enabled = Boolean(
    license.data?.catalog.some((item) => item.id === 'audit_history' && item.enabled),
  )
  const [identity, setIdentity] = useState('')
  const [before, setBefore] = useState<number | undefined>()
  const [exportBefore, setExportBefore] = useState<number | undefined>()
  const [exporting, setExporting] = useState(false)
  const [error, setError] = useState('')
  const users = useQuery({
    queryKey: ['users'],
    queryFn: ({ signal }) => unwrap(client.GET('/users', { signal })),
    enabled,
  })
  const history = useQuery({
    queryKey: ['audit-history', identity, before],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/audit/history', {
          signal,
          params: { query: { identity_id: identity, before } },
        }),
      ),
    enabled: enabled && Boolean(identity),
  })
  const exportPage = async () => {
    setExporting(true)
    setError('')
    try {
      const query = new URLSearchParams({ identity_id: identity })
      if (exportBefore) query.set('before', String(exportBefore))
      const response = await editionFetch('/api/audit/export?' + query, {
        credentials: 'same-origin',
        cache: 'no-store',
      })
      if (!response.ok) throw new Error((await response.json()).error?.message || 'Export failed.')
      const url = URL.createObjectURL(await response.blob())
      const link = document.createElement('a')
      link.href = url
      link.download = 'hakopod-user-audit.csv'
      link.click()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      const next = Number(response.headers.get('X-Hakopod-Next-Cursor'))
      setExportBefore(next > 0 ? next : undefined)
    } catch (cause) {
      setError(message(cause))
    } finally {
      setExporting(false)
    }
  }
  const selected = users.data?.items.find((user) => user.id === identity)
  return (
    <SettingsSection
      title="User history"
      help="Every recorded action by one user, with CSV export in pages of 1,000 events."
      state={
        !enabled
          ? 'Requires Pro. Recent events above stay available.'
          : selected
            ? `Showing ${selected.name || selected.email}`
            : 'Choose a user to see their history'
      }
    >
      <div className="settings-inline-controls">
        <SelectField
          label="User"
          value={identity}
          disabled={!enabled || users.isPending}
          onValueChange={(value) => {
            setIdentity(value)
            setBefore(undefined)
            setExportBefore(undefined)
            setError('')
          }}
          options={[
            { value: '', label: 'Choose a user' },
            ...(users.data?.items.map((user) => ({
              value: user.id,
              label: user.name + ' · ' + user.email,
            })) || []),
          ]}
        />
        <Button disabled={!enabled || !identity || exporting} onClick={() => void exportPage()}>
          {exporting ? 'Exporting…' : exportBefore ? 'Export next 1,000 events' : 'Export CSV'}
        </Button>
      </div>
      {users.error && <ErrorState error={users.error} />}
      {error && <ErrorState error={new Error(error)} />}
      {enabled &&
        identity &&
        (history.isPending ? (
          <Loading />
        ) : history.error ? (
          <ErrorState error={history.error} />
        ) : !history.data?.items.length ? (
          <SettingsEmpty title="No recorded activity">
            This user has no audit events in the retained history.
          </SettingsEmpty>
        ) : (
          <>
            <SettingsRows label={`History for ${selected?.name || 'user'}`}>
              {history.data.items.map((event) => (
                <SettingsRow
                  key={event.id}
                  className="settings-row-audit"
                  name={event.action}
                  detail={<code>{event.resource}</code>}
                  status={
                    <time dateTime={event.time} title={timestamp(event.time)}>
                      {relative(event.time)}
                    </time>
                  }
                />
              ))}
            </SettingsRows>
            {(before || Boolean(history.data.next_cursor)) && (
              <div className="settings-inline-controls">
                {before && <Button onClick={() => setBefore(undefined)}>Latest events</Button>}
                {Boolean(history.data.next_cursor) && (
                  <Button onClick={() => setBefore(history.data!.next_cursor)}>Older events</Button>
                )}
              </div>
            )}
          </>
        ))}
    </SettingsSection>
  )
}
