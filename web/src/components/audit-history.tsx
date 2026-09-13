import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useLicense } from '../lib/license'
import { message, timestamp } from '../lib/api'
import { SelectField } from './ui/select'
import { Button } from './ui/button'
import { ErrorState, Loading, Note } from './shared'

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
      const response = await fetch('/api/audit/export?' + query, {
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
  return (
    <section aria-label="User audit history" className="space-y-3">
      <div className="flex flex-wrap items-end gap-3">
        <SelectField
          label={enabled ? 'User history' : 'User history · Pro'}
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
        <Button
          variant="outline"
          disabled={!enabled || !identity || exporting}
          onClick={() => void exportPage()}
        >
          {exporting ? 'Exporting…' : exportBefore ? 'Export next 1,000 events' : 'Export CSV'}
        </Button>
      </div>
      {!enabled && (
        <Note>
          User history and CSV export require Pro. Recent security events remain available below.
        </Note>
      )}
      {users.error && <ErrorState error={users.error} />}
      {error && <ErrorState error={new Error(error)} />}
      {enabled &&
        identity &&
        (history.isPending ? (
          <Loading />
        ) : history.error ? (
          <ErrorState error={history.error} />
        ) : (
          <>
            <div className="table-container">
              <table>
                <thead>
                  <tr>
                    <th>Action</th>
                    <th>Resource</th>
                    <th>Time</th>
                  </tr>
                </thead>
                <tbody>
                  {history.data?.items.map((event) => (
                    <tr key={event.id}>
                      <td>{event.action}</td>
                      <td>
                        <code>{event.resource}</code>
                      </td>
                      <td>{timestamp(event.time)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {!history.data?.items.length && <Note>No recorded activity for this user.</Note>}
            <div className="flex gap-2">
              {before && (
                <Button variant="outline" onClick={() => setBefore(undefined)}>
                  Latest events
                </Button>
              )}
              {Boolean(history.data?.next_cursor) && (
                <Button variant="outline" onClick={() => setBefore(history.data!.next_cursor)}>
                  Older events
                </Button>
              )}
            </div>
          </>
        ))}
    </section>
  )
}
