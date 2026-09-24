import { useEffect, useRef, useState } from 'react'
import type { Plan } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { Button } from './ui/button'
import { Textarea } from './ui/textarea'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Note, RequestError } from './shared'

// Shared by raw TOML, Compose, source imports and built-image review. Values
// stay in this form only, never in TOML, query caches or deployment history.
export function DeploymentSecrets({
  plan,
  project,
  environment,
  busy,
  onBusy,
  onChange,
}: {
  plan: Plan
  project: string
  environment: string
  busy: boolean
  onBusy: (busy: boolean) => void
  onChange: (missing: string[]) => void
}) {
  const valueInput = useRef<HTMLInputElement | HTMLTextAreaElement>(null)
  const statusHeading = useRef<HTMLHeadingElement>(null)
  const focusAfterRequest = useRef(false)
  const [value, setValue] = useState('')
  const [format, setFormat] = useState('base64url')
  const [multiline, setMultiline] = useState(false)
  const [error, setError] = useState('')
  const missing = plan.missing_secrets || []
  const name = missing[0]
  useEffect(() => {
    setValue('')
    setError('')
  }, [name, project, environment, plan.spec.name])
  useEffect(() => {
    if (!busy && focusAfterRequest.current) {
      focusAfterRequest.current = false
      ;(name ? valueInput.current : statusHeading.current)?.focus({ preventScroll: true })
    }
  }, [busy, name])
  if (!plan.required_secrets?.length) return null
  async function check() {
    const result = await unwrap(
      client.POST('/secrets/requirements', {
        body: { project, environment, spec: plan.spec },
      }),
    )
    onChange(result.missing_secrets)
  }
  async function save(generate: boolean) {
    if (busy || !name) return
    setError('')
    focusAfterRequest.current = true
    onBusy(true)
    try {
      await unwrap(
        client.POST('/secrets/{name}', {
          params: { path: { name }, query: { project, environment, application: plan.spec.name } },
          body: generate ? { generate: true, format: format as 'base64url' | 'hex' } : { value },
        }),
      )
      setValue('')
      await check()
    } catch (err) {
      setError(message(err))
    } finally {
      onBusy(false)
    }
  }
  return (
    <section className="grid min-w-0 gap-3 py-4" aria-label="Application secret setup">
      <h3 ref={statusHeading} tabIndex={-1}>
        Application secrets
      </h3>
      {name ? (
        <>
          <p>
            {missing.length} missing {missing.length === 1 ? 'secret' : 'secrets'}. Save them before
            deploying.
          </p>
          <p className="break-all text-sm">{missing.join(', ')}</p>
          <label className="grid min-w-0 gap-2">
            <span>
              Value for <code className="break-all">{name}</code>
            </span>
            {multiline ? (
              <Textarea
                ref={(node) => {
                  valueInput.current = node
                }}
                value={value}
                onChange={(event) => setValue(event.target.value)}
                disabled={busy}
                autoComplete="off"
                spellCheck={false}
                maxLength={65536}
                rows={4}
              />
            ) : (
              <Input
                type="password"
                ref={(node) => {
                  valueInput.current = node
                }}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') {
                    event.preventDefault()
                    if (!busy && value) void save(false)
                  }
                }}
                value={value}
                onChange={(event) => setValue(event.target.value)}
                disabled={busy}
                autoComplete="new-password"
                spellCheck={false}
                maxLength={65536}
              />
            )}
          </label>
          <label className="checkbox-label min-h-11">
            <Input
              type="checkbox"
              checked={multiline}
              disabled={busy}
              onChange={(event) => setMultiline(event.target.checked)}
            />
            Multiline value (visible while editing)
          </label>
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="primary"
              disabled={busy || !value}
              onClick={() => void save(false)}
            >
              Save value
            </Button>
            <Button
              type="button"
              disabled={busy}
              onClick={() => {
                focusAfterRequest.current = true
                onBusy(true)
                setError('')
                void check()
                  .catch((err) => setError(message(err)))
                  .finally(() => onBusy(false))
              }}
            >
              Recheck saved secrets
            </Button>
          </div>
          <details>
            <summary className="min-h-11 cursor-pointer py-3">
              Generate a new password or random secret
            </summary>
            <div className="grid gap-3 pt-3">
              <Note>
                Use generation for a new password or random signing secret. Supply API tokens,
                certificates and existing database passwords from their provider. Check the
                application's required format.
              </Note>
              <SelectField
                label="Generated format"
                value={format}
                onValueChange={setFormat}
                disabled={busy}
                options={[
                  { value: 'base64url', label: 'URL-safe Base64 · 32 random bytes' },
                  { value: 'hex', label: 'Hexadecimal · 64 characters' },
                ]}
              />
              <Button
                type="button"
                variant="primary"
                disabled={busy}
                onClick={() => void save(true)}
              >
                Generate and save
              </Button>
            </div>
          </details>
        </>
      ) : (
        <p role="status">All referenced application secrets are saved.</p>
      )}
      {error && <RequestError error={error} />}
    </section>
  )
}
