import { useState } from 'react'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'

export function TemplateSecretField({
  templateId,
  field,
  query,
  value,
  onChange,
  replacing,
  busy,
  onBusy,
  onSaved,
  onCancel,
}: {
  templateId: string
  field: components['schemas']['TemplateSecretField']
  query: { project: string; environment: string; application: string }
  value: string
  onChange: (value: string) => void
  replacing: boolean
  busy: boolean
  onBusy: (value: boolean) => void
  onSaved: () => void
  onCancel: () => void
}) {
  const [error, setError] = useState('')
  const pem = ['certificate', 'private-key'].includes(field.format)
  const derived = ['postgres-url', 'redis-url'].includes(field.format)
  async function save(generate = false) {
    if (busy) return
    onBusy(true)
    setError('')
    try {
      await unwrap(
        client.PUT('/templates/{id}/secrets/{name}', {
          params: { path: { id: templateId, name: field.name }, query },
          body: { ...(generate ? { generate: true } : { value }), replace: replacing },
        }),
      )
      onSaved()
    } catch (err) {
      setError(message(err))
    } finally {
      onBusy(false)
    }
  }
  return (
    <form
      className="auth-form"
      onSubmit={(event) => {
        event.preventDefault()
        void save()
      }}
    >
      <label>
        {field.name}
        {pem ? (
          <Textarea
            value={value}
            onChange={(event) => onChange(event.target.value)}
            rows={5}
            maxLength={65536}
            autoComplete="off"
            spellCheck={false}
            required
            disabled={busy}
          />
        ) : (
          <Input
            type="password"
            value={value}
            onChange={(event) => onChange(event.target.value)}
            maxLength={4096}
            autoComplete="new-password"
            spellCheck={false}
            required
            disabled={busy}
          />
        )}
        <span className="field-help">{field.description}</span>
      </label>
      {pem && (
        <label>
          Load a PEM file
          <Input
            type="file"
            accept=".pem,.crt,.key,text/plain"
            disabled={busy}
            onChange={async (event) => {
              const file = event.target.files?.[0]
              if (!file) return
              if (file.size > 65536) {
                setError('Use a PEM file smaller than 64 KiB.')
                return
              }
              try {
                onChange(await file.text())
                setError('')
              } catch {
                setError('The file could not be read. Paste its contents instead.')
              }
            }}
          />
        </label>
      )}
      {field.generate && !derived && (
        <div className="inline-actions">
          <Button
            type="button"
            disabled={busy}
            onClick={() => {
              try {
                const bytes = crypto.getRandomValues(
                  new Uint8Array(field.format === 'hex32' ? 16 : 32),
                )
                onChange(
                  field.format === 'hex32'
                    ? Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
                    : btoa(String.fromCharCode(...bytes)),
                )
                setError('')
              } catch {
                setError(
                  'Secure generation is unavailable in this browser. Supply your own generated value.',
                )
              }
            }}
          >
            Generate value
          </Button>
          <Button
            type="button"
            disabled={busy || !value}
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(value)
              } catch {
                setError('Clipboard access was denied. Keep your own copy before saving.')
              }
            }}
          >
            Copy value
          </Button>
          <span className="field-help">Generated values are saved only when you choose Save.</span>
        </div>
      )}
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <div className="inline-actions">
        <Button type="submit" variant="primary" disabled={busy || !value}>
          {busy ? 'Saving…' : replacing ? 'Replace secret' : 'Save secret'}
        </Button>
        {derived && (
          <Button type="button" disabled={busy} onClick={() => void save(true)}>
            {replacing ? 'Rebuild and replace URL' : 'Build and save URL'}
          </Button>
        )}
        <Button type="button" disabled={busy} onClick={onCancel}>
          Close editor
        </Button>
      </div>
    </form>
  )
}
