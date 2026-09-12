import { useId } from 'react'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { useTheme } from '../lib/appearance'
import { Icon } from './icons'

export function AppearanceSettings() {
  const [theme, setTheme] = useTheme()
  const titleId = useId()
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
    </section>
  )
}
