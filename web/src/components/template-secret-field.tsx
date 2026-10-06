import { RequestError } from './shared'
import { useState } from 'react'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'

export async function generateTemplateSecretValue(format: string) {
  if (format === 'base64-rsa-private-key') {
    let timer: ReturnType<typeof setTimeout> | undefined
    try {
      return await Promise.race([
        (async () => {
          const keys = await crypto.subtle.generateKey(
            {
              name: 'RSA-OAEP',
              modulusLength: 2048,
              publicExponent: new Uint8Array([1, 0, 1]),
              hash: 'SHA-256',
            },
            true,
            ['encrypt', 'decrypt'],
          )
          const encoded = btoa(
            String.fromCharCode(
              ...new Uint8Array(await crypto.subtle.exportKey('pkcs8', keys.privateKey)),
            ),
          )
          const pem = `-----BEGIN PRIVATE KEY-----\n${encoded.match(/.{1,64}/g)!.join('\n')}\n-----END PRIVATE KEY-----\n`
          return btoa(pem)
        })(),
        new Promise<never>((_, reject) => {
          timer = setTimeout(() => reject(new Error('Secure generation timed out.')), 10000)
        }),
      ])
    } finally {
      clearTimeout(timer)
    }
  }
  const bytes = crypto.getRandomValues(
    new Uint8Array(format === 'hex32' ? 16 : format === 'token64' ? 48 : 32),
  )
  return format === 'hex32'
    ? Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
    : btoa(String.fromCharCode(...bytes))
}

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
  custom = false,
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
  custom?: boolean
}) {
  const [error, setError] = useState('')
  const [generating, setGenerating] = useState(false)
  const pem = ['certificate', 'private-key'].includes(field.format)
  const derived = ['postgres-url', 'redis-url'].includes(field.format)
  async function save(generate = false) {
    if (busy) return
    onBusy(true)
    setError('')
    try {
      if (custom) {
        await unwrap(client.POST('/secrets/{name}', {
          params: { path: { name: field.name }, query },
          body: { value },
        }))
      } else await unwrap(
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
        {pem || custom ? (
          <Textarea
            aria-label={field.name}
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
            aria-label={field.name}
            type="password"
            value={value}
            onChange={(event) => onChange(event.target.value)}
            maxLength={field.format === 'base64-rsa-private-key' ? 8192 : 4096}
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
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            disabled={busy}
            onClick={async () => {
              if (busy || generating) return
              setGenerating(true)
              onBusy(true)
              try {
                onChange(await generateTemplateSecretValue(field.format))
                setError('')
              } catch {
                setError(
                  'Secure generation could not finish. Try again or supply your own generated value.',
                )
              } finally {
                setGenerating(false)
                onBusy(false)
              }
            }}
          >
            {generating ? 'Generating…' : 'Generate value'}
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
          <span className="field-help basis-full">Generated values are saved only when you choose Save.</span>
        </div>
      )}
      {error && <RequestError error={error} />}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" variant="primary" disabled={busy || !value}>
          {busy && !generating ? 'Saving…' : replacing ? 'Replace secret' : 'Save secret'}
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
