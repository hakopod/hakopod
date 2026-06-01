import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { validAccent } from '../lib/appearance'
import { message } from '../lib/api'
import { Button } from './ui/button'
import { ErrorState, Loading, Note } from './shared'

export function AppearanceSettings() {
  const cache = useQueryClient()
  const appearance = useQuery({
    queryKey: ['appearance'],
    queryFn: ({ signal }) => unwrap(client.GET('/settings/appearance', { signal })),
    staleTime: 300000,
  })
  const [color, setColor] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  useEffect(() => {
    if (appearance.data) setColor(appearance.data.accent_color)
  }, [appearance.data])
  if (appearance.isPending) return <Loading rows={2} />
  if (appearance.error || !appearance.data)
    return <ErrorState error={appearance.error} retry={() => void appearance.refetch()} />
  return (
    <section className="panel service-summary-panel settings-form-panel">
      <div className="panel-heading">
        <h2>Installation appearance</h2>
      </div>
      <p className="muted-text">
        Choose an accent shared by everyone in this installation. Each person keeps their own light
        or dark theme.
      </p>
      <form
        className="field-stack"
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy || !validAccent(color)) return
          setBusy(true)
          setError('')
          setSaved(false)
          try {
            const result = await unwrap(
              client.PATCH('/settings/appearance', { body: { accent_color: color } }),
            )
            cache.setQueryData(['appearance'], result)
            setSaved(true)
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <label htmlFor="accent-value">Accent color</label>
        <div className="accent-editor">
          <input
            type="color"
            aria-label="Choose accent color"
            value={validAccent(color) ? color : appearance.data.accent_color}
            onChange={(event) => {
              setColor(event.target.value)
              setSaved(false)
            }}
          />
          <input
            id="accent-value"
            value={color}
            pattern="#[0-9A-Fa-f]{6}"
            maxLength={7}
            onChange={(event) => {
              setColor(event.target.value)
              setSaved(false)
            }}
            placeholder="#A494F5"
            required
          />
          <span
            className="accent-swatch"
            style={{ backgroundColor: validAccent(color) ? color : undefined }}
            aria-label="Selected accent preview"
          />
        </div>
        <p className="field-help">
          Text and controls adapt this color for readable contrast in both themes.
        </p>
        {error && (
          <div className="inline-error" role="alert">
            {error}
          </div>
        )}
        {saved && (
          <p role="status" className="success-text">
            Installation accent saved.
          </p>
        )}
        <div>
          <Button
            type="submit"
            variant="primary"
            disabled={
              busy ||
              !validAccent(color) ||
              color.toLowerCase() === appearance.data.accent_color.toLowerCase()
            }
          >
            {busy ? 'Saving…' : 'Save accent color'}
          </Button>
        </div>
      </form>
      <Note>
        This setting is saved by the management API and can be changed only by an administrator.
      </Note>
    </section>
  )
}
