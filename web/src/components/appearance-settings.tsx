import { useEffect, useId, useState } from 'react'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { useAccent, useTheme } from '../lib/appearance'
import {
  accentForeground,
  accentPresets,
  DEFAULT_ACCENT,
  validAccent,
} from '../lib/appearance-color'
import { Icon } from './icons'
import { Input } from './ui/input'
import { Button } from './ui/button'

export function AppearanceSettings() {
  const [theme, setTheme] = useTheme()
  const [accent, setAccent] = useAccent()
  const [draft, setDraft] = useState(accent)
  const [status, setStatus] = useState('')
  const [error, setError] = useState('')
  const titleId = useId()
  const colorId = useId()
  useEffect(() => setDraft(accent), [accent])
  const chooseAccent = (color: string) => {
    if (!validAccent(color)) {
      setError('Use a six-digit hex color, for example #D8FF45.')
      return
    }
    setError('')
    setDraft(color.toUpperCase())
    setStatus(
      setAccent(color)
        ? 'Accent saved for this browser.'
        : 'Accent applied for this visit. Browser storage is unavailable.',
    )
  }
  return (
    <section className="hako-appearance" aria-labelledby={titleId}>
      <div>
        <h2 id={titleId}>Appearance</h2>
        <p>Choose the theme for this browser. Changes are saved automatically.</p>
      </div>
      <div className="hako-theme-options" role="group" aria-label="Color theme">
        {(['dark', 'light'] as const).map((value) => (
          <button
            type="button"
            key={value}
            className="hako-theme-option interactive"
            aria-pressed={theme === value}
            onClick={() => setTheme(value)}
          >
            <span className={`hako-theme-preview hako-theme-preview-${value}`} aria-hidden="true">
              <span />
              <span />
              <span />
            </span>
            <span className="hako-theme-label">
              <Icon name={value === 'dark' ? 'moon' : 'sun'} size={16} />
              {value === 'dark' ? 'Dark' : 'Light'}
              {theme === value && <Icon name="check" size={16} />}
            </span>
            <Brackets bold={theme === value} />
          </button>
        ))}
      </div>
      <p className="hako-theme-caption" role="status">
        {theme === 'dark' ? 'Dark' : 'Light'} theme selected.
      </p>
      <section className="hako-accent-settings" aria-labelledby={`${titleId}-accent`}>
        <div>
          <h3 id={`${titleId}-accent`}>Accent color</h3>
          <p>Personalize buttons and highlights. Text contrast adjusts automatically.</p>
        </div>
        <div className="hako-accent-options" role="group" aria-label="Accent presets">
          {accentPresets.map(({ name, color }) => (
            <button
              type="button"
              key={name}
              className="hako-accent-option interactive"
              aria-pressed={accent === color}
              onClick={() => chooseAccent(color)}
            >
              <span
                style={{ background: color, color: accentForeground(color) }}
                aria-hidden="true"
              >
                {accent === color && <Icon name="check" size={14} />}
              </span>
              {name}
              <Brackets />
            </button>
          ))}
        </div>
        <div className="hako-accent-custom">
          <label htmlFor={colorId}>Custom color</label>
          <div>
            <input
              type="color"
              aria-label="Choose custom accent color"
              value={accent}
              onChange={(event) => chooseAccent(event.target.value)}
            />
            <Input
              id={colorId}
              value={draft}
              maxLength={7}
              spellCheck={false}
              autoComplete="off"
              aria-invalid={Boolean(error)}
              aria-describedby={error ? `${colorId}-error` : undefined}
              onChange={(event) => setDraft(event.target.value)}
              onBlur={() => {
                if (draft.toUpperCase() !== accent) chooseAccent(draft)
              }}
              onKeyDown={(event) => {
                if (event.key === 'Enter') {
                  event.preventDefault()
                  chooseAccent(draft)
                }
              }}
            />
            <Button size="sm" variant="ghost" onClick={() => chooseAccent(DEFAULT_ACCENT)}>
              Reset
            </Button>
          </div>
          {error && (
            <p id={`${colorId}-error`} className="field-help error" role="alert">
              {error}
            </p>
          )}
        </div>
        <p className="hako-theme-caption" role="status">
          {status || 'Saved with your theme on this browser.'}
        </p>
      </section>
    </section>
  )
}
