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
  function attach(added: EnvironmentFile[]) {
    const next = [...files, ...added]
    environmentFilePayload(next)
    onChange(next)
    setError('')
  }
  return (
    <section className="grid min-w-0 gap-2" aria-label="Environment file attachments">
      <div className="field-stack">
        <span>Environment files</span>
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
        <div>
          <Button
            type="button"
            disabled={disabled || files.length >= 8}
            onClick={() => fileInput.current?.click()}
          >
            Upload environment files
          </Button>
        </div>
      </div>
      <p className="field-help">
        Match the paths in env_file. Later files override earlier files; explicit variables override
        files. Sensitive values are saved as secret references when you review.
      </p>
      {files.map((file) => (
        <div key={file.id} className="flex min-w-0 items-end gap-2">
          <label className="field-stack min-w-0 flex-1">
            File path
            <Input
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
          </label>
          <Button
            type="button"
            size="sm"
            disabled={disabled}
            onClick={() => onChange(files.filter((item) => item.id !== file.id))}
            aria-label={`Remove ${file.path}`}
          >
            Remove
          </Button>
        </div>
      ))}
      <details>
        <summary className="cursor-pointer">Paste an environment file</summary>
        <div className="mt-2 grid gap-2">
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
            .env contents
            <Textarea
              value={content}
              disabled={disabled}
              rows={5}
              maxLength={maximum}
              spellCheck={false}
              autoComplete="off"
              onChange={(event) => setContent(event.target.value)}
            />
          </label>
          <div>
            <Button
              type="button"
              size="sm"
              disabled={disabled || !path || !content}
              onClick={() => {
                try {
                  attach([{ id: crypto.randomUUID(), path, content }])
                  setContent('')
                } catch (cause) {
                  setError(cause instanceof Error ? cause.message : 'Could not attach the file.')
                }
              }}
            >
              Attach file
            </Button>
          </div>
        </div>
      </details>
      {error && <RequestError error={error} />}
    </section>
  )
}
