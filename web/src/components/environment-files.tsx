import { RequestError } from './shared'
import { useRef, useState } from 'react'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'

export type EnvironmentFile = { id: string; path: string; content: string }
const maximum = 128 * 1024

export function environmentFilePayload(files: EnvironmentFile[]) {
  const result: Record<string, string> = Object.create(null)
  let total = 0
  for (const file of files) {
    if (!file.path || Object.hasOwn(result, file.path))
      throw new Error('Give each environment file a unique path matching env_file.')
    total += new TextEncoder().encode(file.content).length
    result[file.path] = file.content
  }
  if (files.length > 8 || total > maximum)
    throw new Error('Attach at most 8 environment files totalling 128 KiB.')
  return result
}

export function EnvironmentFiles({
  files,
  onChange,
  disabled,
  onBusyChange,
}: {
  files: EnvironmentFile[]
  onChange: (files: EnvironmentFile[]) => void
  disabled?: boolean
  onBusyChange: (busy: boolean) => void
}) {
  const fileInput = useRef<HTMLInputElement>(null)
  const [error, setError] = useState('')
  const [path, setPath] = useState('.env')
  const [content, setContent] = useState('')
  const [pasting, setPasting] = useState(false)
  function attach(added: EnvironmentFile[]) {
    const next = [...files, ...added]
    environmentFilePayload(next)
    onChange(next)
    setError('')
  }
  return (
    <section className="env-files" aria-labelledby="env-files-title">
      <div className="env-files-heading">
        <div>
          <h3 id="env-files-title">Environment files</h3>
          <p className="field-help">
            Match the paths in <code>env_file</code>. Later files override earlier ones; explicit
            variables override files. Sensitive values become secret references at review.
          </p>
        </div>
        <div className="env-files-actions">
          <input
            ref={fileInput}
            className="hidden!"
            type="file"
            multiple
            disabled={disabled}
            aria-label="Upload environment files"
            onChange={(event) => {
              const selected = Array.from(event.target.files || [])
              event.target.value = ''
              if (!selected.length) return
              if (
                selected.length + files.length > 8 ||
                selected.reduce((sum, file) => sum + file.size, 0) > maximum
              ) {
                setError('Attach at most 8 environment files totalling 128 KiB.')
                return
              }
              onBusyChange(true)
              void Promise.all(
                selected.map(async (file) => ({
                  id: crypto.randomUUID(),
                  path: file.name,
                  content: await file.text(),
                })),
              )
                .then(attach)
                .catch((cause) =>
                  setError(
                    cause instanceof Error ? cause.message : 'Could not read the environment file.',
                  ),
                )
                .finally(() => onBusyChange(false))
            }}
          />
          <Button
            type="button"
            size="sm"
            variant="ghost"
            aria-expanded={pasting}
            disabled={disabled || files.length >= 8}
            onClick={() => setPasting((open) => !open)}
          >
            Paste
          </Button>
          <Button
            type="button"
            size="sm"
            disabled={disabled || files.length >= 8}
            onClick={() => fileInput.current?.click()}
          >
            Upload
          </Button>
        </div>
      </div>
      {files.length > 0 && (
        <ul className="env-files-list" aria-label="Attached environment files">
          {files.map((file) => (
            <li key={file.id}>
              <Input
                aria-label="File path"
                value={file.path}
                disabled={disabled}
                maxLength={200}
                onChange={(event) =>
                  onChange(
                    files.map((item) =>
                      item.id === file.id ? { ...item, path: event.target.value } : item,
                    ),
                  )
                }
              />
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={disabled}
                onClick={() => onChange(files.filter((item) => item.id !== file.id))}
                aria-label={`Remove ${file.path}`}
              >
                Remove
              </Button>
            </li>
          ))}
        </ul>
      )}
      {pasting && (
        <div className="env-files-paste">
          <label className="field-stack">
            File path
            <Input
              value={path}
              disabled={disabled}
              maxLength={200}
              onChange={(event) => setPath(event.target.value)}
            />
          </label>
          <label className="field-stack">
            Contents
            <Textarea
              className="code-editor"
              value={content}
              disabled={disabled}
              rows={5}
              maxLength={maximum}
              spellCheck={false}
              autoComplete="off"
              placeholder="KEY=value"
              onChange={(event) => setContent(event.target.value)}
            />
          </label>
          <div className="env-files-paste-actions">
            <Button type="button" size="sm" variant="ghost" onClick={() => setPasting(false)}>
              Cancel
            </Button>
            <Button
              type="button"
              size="sm"
              variant="primary"
              disabled={disabled || !path || !content}
              onClick={() => {
                try {
                  attach([{ id: crypto.randomUUID(), path, content }])
                  setContent('')
                  setPasting(false)
                } catch (cause) {
                  setError(cause instanceof Error ? cause.message : 'Could not attach the file.')
                }
              }}
            >
              Attach file
            </Button>
          </div>
        </div>
      )}
      {error && <RequestError error={error} />}
    </section>
  )
}
