import type { components } from './api.generated'

export type FrameworkPlan = components['schemas']['FrameworkPlan']
export const frameworkRecipes = [
  {
    id: 'nextjs',
    label: 'Next.js',
    icon: 'nextdotjs',
    category: 'Fullstack',
    hint: 'Node server or static export',
  },
  {
    id: 'astro',
    label: 'Astro',
    icon: 'astro',
    category: 'Frontend',
    hint: 'Static by default; Node adapter supported',
  },
  {
    id: 'sveltekit',
    label: 'SvelteKit',
    icon: 'svelte',
    category: 'Fullstack',
    hint: 'Requires adapter-node or adapter-static',
  },
  {
    id: 'tanstack-start',
    label: 'TanStack Start',
    icon: 'tanstack',
    category: 'Fullstack',
    hint: 'Confirm your Node adapter output path',
  },
  { id: 'vite', label: 'Vite', icon: 'vite', category: 'Frontend', hint: 'Static frontend output' },
  {
    id: 'node',
    label: 'Node.js',
    icon: 'nodedotjs',
    category: 'Backend',
    hint: 'Uses your build and start scripts',
  },
  {
    id: 'static',
    label: 'Plain HTML',
    icon: 'html5',
    category: 'Static',
    hint: 'Serves index.html without a build step',
  },
] as const
export type FrameworkID = (typeof frameworkRecipes)[number]['id']
export const frameworkLabel = (id: string) =>
  frameworkRecipes.find((item) => item.id === id)?.label || id

// Manual recipes are starting points, not repository observations. Detection
// remains authoritative for adapters, lockfiles, scripts and static exports.
export function frameworkRecipe(id: FrameworkID, manager = 'npm'): FrameworkPlan {
  if (id === 'static')
    return {
      framework: id,
      runtime: 'static',
      package_manager: 'none',
      install_command: 'true',
      build_command: 'true',
      output_directory: '.',
      port: 8080,
    }
  const pm: FrameworkPlan['package_manager'] =
    manager === 'pnpm' || manager === 'yarn' || manager === 'bun' ? manager : 'npm'
  const plan: FrameworkPlan = {
    framework: id,
    runtime: 'node',
    package_manager: pm,
    install_command: `${pm} install`,
    build_command: `${pm} run build`,
    start_command: `${pm} run start`,
    port: 3000,
  }
  if (id === 'astro' || id === 'vite')
    return { ...plan, runtime: 'static', output_directory: 'dist', start_command: '', port: 8080 }
  if (id === 'nextjs') plan.start_command = './node_modules/.bin/next start --hostname 0.0.0.0'
  if (id === 'sveltekit') plan.start_command = 'node build/index.js'
  if (id === 'tanstack-start') plan.start_command = 'node .output/server/index.mjs'
  return plan
}

export function sameFrameworkPlan(left: FrameworkPlan, right: FrameworkPlan) {
  return (
    [
      'framework',
      'runtime',
      'package_manager',
      'install_command',
      'build_command',
      'start_command',
      'output_directory',
      'port',
    ] as const
  ).every((field) => (left[field] ?? '') === (right[field] ?? ''))
}
