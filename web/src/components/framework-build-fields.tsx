import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { fieldError } from '../lib/form-errors'

import { useRef, useState } from 'react'
import {
  frameworkRecipes,
  frameworkRecipe,
  sameFrameworkPlan,
  type FrameworkID,
  type FrameworkPlan,
} from '../lib/framework-recipes'
import { Button } from './ui/button'
import { Icon } from './icons'
export { frameworkLabel, type FrameworkPlan } from '../lib/framework-recipes'
export const defaultFrameworkPlan = frameworkRecipe('node')

export function FrameworkBuildFields({
  value,
  onChange,
  error = '',
  detected,
}: {
  value: FrameworkPlan
  onChange: (value: FrameworkPlan) => void
  error?: string
  detected?: FrameworkPlan
}) {
  const [category, setCategory] = useState('All')
  const drafts = useRef(new Map<string, FrameworkPlan>())
  const customized = detected && !sameFrameworkPlan(value, detected)
  const set = (key: keyof FrameworkPlan, next: string | number) =>
    onChange({ ...value, [key]: next })
  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div role="group" aria-label="Show frameworks" className="framework-filter">
          {['All', 'Frontend', 'Fullstack', 'Backend', 'Static'].map((item) => (
            <button
              key={item}
              type="button"
              aria-pressed={category === item}
              onClick={() => setCategory(item)}
            >
              {item}
            </button>
          ))}
        </div>
        {detected && (
          <Button
            type="button"
            size="sm"
            disabled={!customized}
            onClick={() => {
              drafts.current.clear()
              setCategory('All')
              onChange({ ...detected })
            }}
          >
            Use detected settings
          </Button>
        )}
      </div>
      <fieldset className="framework-tiles">
        <legend className="sr-only">Framework</legend>
        {frameworkRecipes
          .filter(
            (item) =>
              category === 'All' || item.category === category || item.id === value.framework,
          )
          .map((item) => (
            <label key={item.id} className="framework-tile">
              <input
                type="radio"
                name="framework-recipe"
                value={item.id}
                checked={value.framework === item.id}
                className="sr-only"
                onChange={() => {
                  drafts.current.set(value.framework, { ...value })
                  const next =
                    drafts.current.get(item.id) ||
                    (detected?.framework === item.id
                      ? detected
                      : frameworkRecipe(item.id as FrameworkID, value.package_manager))
                  onChange({ ...next })
                }}
              />
              <span
                aria-hidden="true"
                className="framework-tile-mark"
                style={{ maskImage: `url(/icons/${item.icon}.svg)` }}
              />
              <span className="framework-tile-check" aria-hidden="true">
                <Icon name="check" size={14} />
              </span>
              <strong>{item.label}</strong>
              <span>
                {item.category}
                {detected?.framework === item.id ? ' · Detected' : ''}
              </span>
            </label>
          ))}
      </fieldset>
      {fieldError(error, 'framework.framework') && (
        <p className="field-error" role="alert">
          {fieldError(error, 'framework.framework')}
        </p>
      )}
      <p className="field-help">
        {frameworkRecipes.find((item) => item.id === value.framework)?.hint}. Switching frameworks
        keeps each recipe's edits while this form is open.
      </p>
      <p className="min-w-0 break-all text-sm text-[var(--muted)]">
        {value.runtime === 'static' ? (
          <>
            Serve <code>{value.output_directory}</code> on port 8080
          </>
        ) : (
          <>
            Start <code>{value.start_command}</code> on port {value.port}
          </>
        )}
      </p>
      <details
        className="form-disclosure"
        open={Boolean(error.includes('framework.')) || undefined}
      >
        <summary>
          Build settings{' '}
          <span className="muted-text">
            {detected && !customized ? 'Detected' : 'Review recipe'} · {value.package_manager} ·{' '}
            {value.runtime === 'static' ? 'Static files' : 'Node server'}
          </span>
        </summary>
        <div className="create-fields pt-4">
          <label>
            Runtime
            <SelectField
              label="Runtime"
              value={value.runtime}
              error={fieldError(error, 'framework.runtime')}
              onValueChange={(runtime) =>
                onChange({
                  ...value,
                  runtime: runtime as FrameworkPlan['runtime'],
                  port: runtime === 'static' ? 8080 : 3000,
                  output_directory: runtime === 'static' ? value.output_directory || 'dist' : '',
                  start_command:
                    runtime === 'static'
                      ? ''
                      : value.start_command ||
                        `${value.package_manager === 'none' ? 'npm' : value.package_manager} run start`,
                })
              }
              options={[
                { value: 'static', label: 'Static files' },
                { value: 'node', label: 'Node server' },
              ]}
            />
          </label>
          <label>
            Package manager
            <SelectField
              label="Package manager"
              value={value.package_manager}
              error={fieldError(error, 'framework.package_manager')}
              onValueChange={(v) => set('package_manager', v)}
              options={['npm', 'pnpm', 'yarn', 'bun', 'none'].map((v) => ({
                value: v,
                label: v,
                disabled: v === 'none' && value.framework !== 'static',
              }))}
            />
          </label>
          <label>
            Install command
            <Input
              value={value.install_command}
              error={fieldError(error, 'framework.install_command')}
              maxLength={1024}
              required
              onChange={(e) => set('install_command', e.target.value)}
            />
          </label>
          <label>
            Build command
            <Input
              value={value.build_command}
              error={fieldError(error, 'framework.build_command')}
              maxLength={1024}
              required
              onChange={(e) => set('build_command', e.target.value)}
            />
          </label>
          {value.runtime === 'node' && (
            <label>
              Server port
              <Input
                type="number"
                min={1}
                max={65535}
                required
                value={value.port}
                error={fieldError(error, 'framework.port', 'port')}
                onChange={(event) => set('port', Number(event.target.value))}
              />
            </label>
          )}
          {value.runtime === 'static' ? (
            <label>
              Output directory
              <Input
                value={value.output_directory || ''}
                error={fieldError(error, 'framework.output_directory')}
                maxLength={200}
                required
                onChange={(e) => set('output_directory', e.target.value)}
              />
            </label>
          ) : (
            <label>
              Start command
              <Input
                value={value.start_command || ''}
                error={fieldError(error, 'framework.start_command')}
                maxLength={1024}
                required
                onChange={(e) => set('start_command', e.target.value)}
              />
            </label>
          )}
        </div>
        <p className="muted-text">
          Changing the package manager keeps your commands; update them to match. Builds run in your
          Git provider account. Static files use port 8080; server output needs a compatible Node
          adapter.
        </p>
      </details>
    </div>
  )
}
