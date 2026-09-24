import assert from 'node:assert/strict'
import test from 'node:test'
import { frameworkRecipe, sameFrameworkPlan } from './framework-recipes'

test('manual recipes distinguish static assets from Node server output', () => {
  const html = frameworkRecipe('static', 'pnpm')
  assert.equal(html.package_manager, 'none')
  assert.equal(html.output_directory, '.')
  assert.equal(html.port, 8080)
  assert.equal(html.start_command, undefined)
  const astro = frameworkRecipe('astro', 'pnpm')
  assert.equal(astro.build_command, 'pnpm run build')
  assert.equal(astro.runtime, 'static')
  assert.equal(astro.output_directory, 'dist')
  const svelte = frameworkRecipe('sveltekit', 'bun')
  assert.equal(svelte.runtime, 'node')
  assert.equal(svelte.start_command, 'node build/index.js')
  assert.equal(svelte.output_directory, undefined)
  assert.equal(svelte.port, 3000)
})

test('moving from plain HTML to a framework restores a usable package manager', () => {
  const next = frameworkRecipe('nextjs', frameworkRecipe('static').package_manager)
  assert.equal(next.package_manager, 'npm')
  assert.equal(next.install_command, 'npm install')
  assert.equal(next.start_command, './node_modules/.bin/next start --hostname 0.0.0.0')
})

test('detected status compares effective settings including commands, output and port', () => {
  const plan = frameworkRecipe('nextjs')
  assert.ok(sameFrameworkPlan(plan, { ...plan, output_directory: '' }))
  for (const change of [
    { port: 8080 },
    { start_command: 'node server.js' },
    { install_command: 'npm ci' },
    { runtime: 'static' as const, output_directory: 'out', start_command: '' },
  ]) {
    assert.equal(sameFrameworkPlan(plan, { ...plan, ...change }), false)
  }
})
