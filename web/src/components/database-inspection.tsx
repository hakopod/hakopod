import { useState } from 'react'
import type { ManagedDatabase } from '../lib/databases'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useQueryClient } from '@tanstack/react-query'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Note } from './shared'

export function DatabaseInspection({ database: d }: { database: ManagedDatabase }) {
  const [checked, setChecked] = useState(false)
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const cache = useQueryClient()
  if (!d.recovery?.restored_at) return null
  if (d.recovery.inspected_at)
    return (
      <p className="py-2">Recovery inspection recorded {timestamp(d.recovery.inspected_at)}.</p>
    )
  return (
    <form
      className="grid gap-3 py-3"
      onSubmit={async (e) => {
        e.preventDefault()
        setBusy(true)
        setError('')
        try {
          const result = await unwrap(
            client.POST('/databases/{id}/inspect', {
              params: { path: { id: d.id } },
              body: {
                job_id: d.recovery!.job_id,
                expected_revision: d.revision,
                confirm_name: name,
                inspected: checked,
              },
            }),
          )
          cache.setQueryData(['managed-database', d.id], result)
        } catch (err) {
          setError(message(err))
        } finally {
          setBusy(false)
        }
      }}
    >
      <Note>
        Use the private endpoint and database credentials to inspect your recovered records, schema
        and expiry with your database client. Recording this acknowledgement does not switch any
        application connection.
      </Note>
      <label className="flex min-h-11 items-center gap-2">
        <Input
          type="checkbox"
          required
          checked={checked}
          onChange={(e) => setChecked(e.target.checked)}
        />
        I inspected the recovered data and understand its captured recovery point.
      </label>
      <label>
        Type {d.spec.name} to record inspection
        <Input value={name} onChange={(e) => setName(e.target.value)} required />
      </label>
      {error && (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      )}
      <div>
        <Button type="submit" disabled={busy || !checked || name !== d.spec.name}>
          {busy ? 'Recording…' : 'Record inspection'}
        </Button>
      </div>
    </form>
  )
}
