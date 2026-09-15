import type { components } from '../lib/api.generated'
import { Input } from './ui/input'
import { SelectField } from './ui/select'

export type FrameworkPlan = components['schemas']['FrameworkPlan']
const frameworkNames: Record<string, string> = {
  astro: 'Astro',
  nextjs: 'Next.js',
  sveltekit: 'SvelteKit',
  'tanstack-start': 'TanStack Start',
  vite: 'Vite',
  node: 'Node.js',
  static: 'Plain HTML',
}
export const frameworkLabel = (name: string) => frameworkNames[name] || name
export const defaultFrameworkPlan: FrameworkPlan = {
  framework: 'node',
  runtime: 'node',
  package_manager: 'npm',
  install_command: 'npm ci',
  build_command: 'npm run build',
  start_command: 'npm run start',
  port: 3000,
}
export function FrameworkBuildFields({
  value,
  onChange,
}: {
  value: FrameworkPlan
  onChange: (value: FrameworkPlan) => void
}) {
  const set = (key: keyof FrameworkPlan, next: string | number) =>
    onChange({ ...value, [key]: next })
  return (
    <div className="grid gap-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <label>
          Framework
          <SelectField
            label="Framework"
            value={value.framework}
            onValueChange={(v) => set('framework', v)}
            options={[
              'astro',
              'nextjs',
              'sveltekit',
              'tanstack-start',
              'vite',
              'node',
              'static',
            ].map((v) => ({ value: v, label: frameworkLabel(v) }))}
          />
        </label>
        <label>
          Runtime
          <SelectField
            label="Runtime"
            value={value.runtime}
            onValueChange={(runtime) =>
              onChange({
                ...value,
                runtime: runtime as FrameworkPlan['runtime'],
                port: runtime === 'static' ? 8080 : 3000,
                output_directory: runtime === 'static' ? value.output_directory || 'dist' : '',
                start_command: runtime === 'static' ? '' : value.start_command || 'npm run start',
              })
            }
            options={[
              { value: 'static', label: 'Static files' },
              { value: 'node', label: 'Node server' },
            ]}
          />
        </label>
      </div>
      <label>
        Package manager
        <SelectField
          label="Package manager"
          value={value.package_manager}
          onValueChange={(v) => set('package_manager', v)}
          options={['npm', 'pnpm', 'yarn', 'bun', 'none'].map((v) => ({ value: v, label: v }))}
        />
      </label>
      <label>
        Install command
        <Input
          value={value.install_command}
          maxLength={1024}
          required
          onChange={(e) => set('install_command', e.target.value)}
        />
      </label>
      <label>
        Build command
        <Input
          value={value.build_command}
          maxLength={1024}
          required
          onChange={(e) => set('build_command', e.target.value)}
        />
      </label>
      {value.runtime === 'static' ? (
        <label>
          Output directory
          <Input
            value={value.output_directory || ''}
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
            maxLength={1024}
            required
            onChange={(e) => set('start_command', e.target.value)}
          />
        </label>
      )}
      <p className="muted-text">
        Changing framework or package manager labels keeps your commands. Review these settings
        against your framework configuration. Builds run in your Git provider account. Static files
        use port 8080; server output needs a compatible Node adapter.
      </p>
    </div>
  )
}
