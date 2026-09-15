import { EnvironmentFields } from './runtime-settings-fields'
import { parseEnvironment, type EnvironmentRow } from '../lib/service-environment'
import { useState } from 'react'
import type { Application } from '../lib/types'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { Note } from './shared'

export type ComposeDraft = components['schemas']['ComposeImport']

export function ComposeImport({
  application,
  project,
  environment,
  name: initialName,
  onUse,
}: {
  application?: Application
  project: string
  environment: string
  name: string
  onUse: (draft: ComposeDraft) => void
}) {
  const [name, setName] = useState(application?.name || initialName)
  const [yaml, setYAML] = useState('')
  const [variables, setVariables] = useState<EnvironmentRow[]>([])
  const [draft, setDraft] = useState<ComposeDraft | null>(null)
  const [acknowledged, setAcknowledged] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  function reset() {
    setDraft(null)
    setAcknowledged(false)
    setError('')
  }
  async function convert() {
    setBusy(true)
    setError('')
    setDraft(null)
    setAcknowledged(false)
    try {
      const values = parseEnvironment(
        variables.filter((row) => row.name || row.value),
        [],
      )
      setDraft(
        await unwrap(
          client.POST('/compose/convert', {
            body: {
              project,
              environment,
              name,
              yaml,
              variables: values,
              ...(application
                ? { application_id: application.id, expected_revision: application.revision }
                : {}),
            },
          }),
        ),
      )
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="grid min-w-0 gap-4">
      {application && (
        <Note>
          Compose adds new services to this application. Existing service names cannot be replaced.
        </Note>
      )}
      <label className="field-stack">
        Application name
        <Input
          value={name}
          disabled={Boolean(application) || busy}
          maxLength={40}
          placeholder="my-app (or use name in Compose)"
          onChange={(event) => {
            setName(event.target.value)
            reset()
          }}
        />
      </label>
      <label className="field-stack">
        Compose file
        <Input
          type="file"
          accept=".yaml,.yml,text/yaml,application/yaml"
          disabled={busy}
          onChange={(event) => {
            const file = event.target.files?.[0]
            if (!file) return
            reset()
            if (file.size > 262144) {
              setError('Compose files must be at most 256 KiB.')
              return
            }
            setBusy(true)
            void file
              .text()
              .then(setYAML)
              .catch(() => setError('Could not read this file. Paste the YAML below.'))
              .finally(() => setBusy(false))
          }}
        />
      </label>
      <label className="field-stack">
        Docker Compose YAML
        <Textarea
          className="code-editor"
          value={yaml}
          disabled={busy}
          maxLength={262144}
          spellCheck={false}
          autoComplete="off"
          rows={12}
          placeholder={
            'services:\n  web:\n    image: nginxinc/nginx-unprivileged:alpine\n    ports:\n      - "8080:8080"'
          }
          onChange={(event) => {
            setYAML(event.target.value)
            reset()
          }}
        />
      </label>
      <details>
        <summary className="cursor-pointer">Interpolation variables</summary>
        <div className="mt-2">
          <EnvironmentFields
            rows={variables}
            onChange={(rows) => {
              setVariables(rows)
              reset()
            }}
            label="Compose interpolation"
            disabled={busy}
            allowSecrets={false}
          />
        </div>
        <p className="field-help">
          Paste or import .env values for Compose placeholders. Host variables and shell commands
          are never evaluated. Use application secret references for passwords and tokens.
        </p>
      </details>
      <p className="field-help">
        Import services with built images. Dockerfile builds, host mounts, Compose health checks and
        other unsupported options require explicit changes. Published ports become private until you
        configure HTTP or a provisioned TCP listener.
      </p>
      {error && (
        <p className="inline-error" role="alert">
          {error}
        </p>
      )}
      <div>
        <Button disabled={busy || !yaml.trim()} onClick={() => void convert()} variant="primary">
          {busy ? 'Converting…' : 'Generate config.toml'}
        </Button>
      </div>
      {draft && (
        <section className="grid min-w-0 gap-3" aria-label="Generated configuration">
          <label className="field-stack">
            Generated config.toml
            <Textarea
              className="code-editor"
              value={draft.toml}
              readOnly
              rows={12}
              spellCheck={false}
            />
          </label>
          {draft.warnings.map((warning, index) => (
            <Note key={index}>{warning}</Note>
          ))}
          <label className="checkbox-label">
            <Input
              type="checkbox"
              checked={acknowledged}
              onChange={(event) => setAcknowledged(event.target.checked)}
            />
            I have reviewed these conversion differences.
          </label>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="primary" disabled={!acknowledged} onClick={() => onUse(draft)}>
              Edit generated TOML
            </Button>
            <Button
              onClick={() => {
                const url = URL.createObjectURL(
                  new Blob([draft.toml], { type: 'application/toml' }),
                )
                const link = document.createElement('a')
                link.href = url
                link.download = 'config.toml'
                link.click()
                URL.revokeObjectURL(url)
              }}
            >
              Download config.toml
            </Button>
          </div>
          <p className="field-help">
            Nothing is deployed yet. Edit the TOML, then review the deployment plan.
          </p>
        </section>
      )}
    </div>
  )
}
