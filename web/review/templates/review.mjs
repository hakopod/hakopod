import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { chromium } from '../../../.local/template-review/node_modules/playwright/index.mjs'

const evidence = fileURLToPath(new URL('../../../.local/template-review/evidence/', import.meta.url))
await mkdir(evidence, { recursive: true })
const browser = await chromium.launch({ headless: true })
const results = [], errors = []
let page, label
async function stable({ allowEmpty = false, allowError = false } = {}) {
  await page.locator('.hako-page-content').waitFor()
  await page.evaluate(() => document.fonts.ready)
  assert.equal(await page.locator('.hako-loading-stack').count(), 0)
  if (!allowEmpty) assert.equal(await page.locator('.hako-empty-canvas').count(), 0)
  if (!allowError) assert.equal(await page.locator('[data-error-recovery]').count(), 0)
  assert.equal(await page.getByText('Something went wrong!', { exact: true }).count(), 0)
  assert.equal(await page.getByText('Not Found', { exact: true }).count(), 0)
  await page.evaluate(() => Promise.allSettled(document.getAnimations()
    .filter(animation => animation.effect?.getComputedTiming().iterations !== Infinity)
    .map(animation => animation.finished)))
  assert.equal(await page.evaluate(() => window.__fixture.requests.some(request => !request.allowed)), false)
}
async function capture(state, options = {}) {
  await stable(options)
  const geometry = await page.evaluate(() => {
    const rect = element => { const r = element.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, right: r.right, bottom: r.bottom, height: r.height } }
    const content = document.querySelector('.hako-page-content')
    const heading = document.querySelector('.hako-page-heading')
    const visible = element => element.getBoundingClientRect().width > 0 && element.getBoundingClientRect().height > 0
    return {
      width: innerWidth, scrollWidth: document.documentElement.scrollWidth,
      inset: getComputedStyle(content).paddingLeft,
      content: rect(content),
      heading: heading && { ...rect(heading), paddingTop: getComputedStyle(heading).paddingTop, paddingBottom: getComputedStyle(heading).paddingBottom },
      title: document.querySelector('h1') && rect(document.querySelector('h1')),
      h1: document.querySelectorAll('h1').length,
      brackets: [...document.querySelectorAll('.brackets')].filter(e => getComputedStyle(e).display !== 'none' && e.getBoundingClientRect().width).length,
      fields: [...document.querySelectorAll('.form-card input,.form-card button[role="combobox"],.service-summary-panel input')].filter(visible).map(e => ({ name: e.getAttribute('aria-label') || e.id || e.type, ...rect(e) })),
      labels: [...document.querySelectorAll('.form-card label[for]')].map(e => ({ name: e.textContent.trim(), label: rect(e), control: document.getElementById(e.htmlFor) && rect(document.getElementById(e.htmlFor)) })),
      actions: [...document.querySelectorAll('.form-footer button,.dialog-footer button,.dialog-footer a')].filter(visible).map(e => ({ name: e.textContent.trim(), ...rect(e) })),
      dialog: document.querySelector('[role="dialog"]') && rect(document.querySelector('[role="dialog"]')),
    }
  })
  assert.ok(geometry.scrollWidth <= geometry.width, `${state}: document overflow`)
  assert.equal(geometry.h1, 1)
  assert.equal(geometry.brackets, 0)
  assert.equal(geometry.inset, geometry.width < 640 ? '16px' : '24px')
  if (geometry.heading) {
    assert.ok(Math.abs(geometry.heading.x - geometry.content.x) < 1, `${state}: heading divider left edge`)
    assert.ok(Math.abs(geometry.heading.right - geometry.content.right) < 1, `${state}: heading divider right edge`)
    assert.equal(geometry.heading.paddingTop, geometry.heading.paddingBottom, `${state}: heading vertical balance`)
    assert.ok(Math.abs(geometry.title.x - geometry.content.x - parseFloat(geometry.inset)) < 1, `${state}: title inset`)
  }
  for (const field of geometry.fields) assert.ok(field.x >= 0 && field.right <= geometry.width, `${state}: field overflow`)
  for (const { name, label, control } of geometry.labels) {
    assert.ok(control, `${state}: ${name} has no labeled control`)
    assert.ok(Math.abs(label.x - control.x) < 1 && control.y >= label.bottom, `${state}: ${name} label alignment`)
  }
  for (const action of geometry.actions) assert.ok(action.x >= 0 && action.right <= geometry.width && action.height >= 32, `${state}: action bounds ${action.name}`)
  if (geometry.dialog) assert.ok(geometry.dialog.x >= 0 && geometry.dialog.right <= geometry.width, `${state}: inspector overflow`)
  const filename = `${label}-${state}.png`
  await page.screenshot({ path: `${evidence}${filename}`, fullPage: true })
  results.push({ label, state, geometry, screenshot: filename })
}
async function requiredSecrets(expected) {
  await page.getByRole('heading', { name: 'Required secrets', exact: true }).waitFor()
  await page.waitForFunction(() => window.__fixture.requests.some(request => request.method === 'GET' && request.path === '/api/secrets'))
  const names = await page.locator('.service-summary-panel .settings-list-row strong').allTextContents()
  assert.deepEqual(names.toSorted(), expected.toSorted())
}
async function recordPlan(expected) {
  const plan = await page.evaluate(() => window.__fixture.requests.findLast(request => request.path.endsWith('/plan')))
  assert.equal(plan.status, 200)
  assert.equal(plan.body.name, 'fixture-outpost')
  assert.equal(plan.body.architecture, 'arm64')
  for (const [key, value] of Object.entries(expected)) assert.equal(plan.body.values[key], value)
  results.push({ label, request: plan })
}
async function select(name, choice) {
  await page.getByRole('combobox', { name, exact: true }).click()
  await page.getByRole('option', { name: choice, exact: true }).click()
}
try {
  for (const theme of ['dark', 'light']) for (const width of [1440, 390, 320]) {
    label = `${theme}-${width}`
    const context = await browser.newContext({ viewport: { width, height: 1000 }, hasTouch: width < 640 })
    page = await context.newPage()
    page.on('pageerror', error => errors.push({ label, message: error.message }))
    await page.goto(`http://127.0.0.1:4194/templates?theme=${theme}&q=outpost`)
    await page.getByRole('heading', { name: 'Outpost', exact: true }).waitFor()
    const card = page.locator('.catalog-card').filter({ has: page.getByRole('heading', { name: 'Outpost', exact: true }) })
    assert.equal(await card.locator('img').evaluate(e => e.complete && e.naturalWidth > 0), true)
    await capture('catalog')
    await card.focus()
    await page.keyboard.press('Enter')
    await page.getByRole('dialog', { name: 'Outpost', exact: true }).waitFor()
    await page.getByRole('heading', { name: 'Before you deploy', exact: true }).waitFor()
    if (width !== 390) await capture('inspector')
    await page.keyboard.press('Escape')
    await page.getByRole('dialog').waitFor({ state: 'hidden' })
    assert.equal(await card.evaluate(e => e === document.activeElement), true)

    await page.goto(`http://127.0.0.1:4194/templates/outpost?theme=${theme}`)
    await page.getByRole('heading', { name: 'Configure Outpost', exact: true }).waitFor()
    await page.getByRole('textbox', { name: 'Application name', exact: true }).fill('fixture-outpost')
    await select('Target architecture', 'Linux ARM64')
    assert.equal(await page.getByRole('textbox', { name: 'Redis host', exact: true }).count(), 0)
    await capture('bundled')
    await page.getByRole('button', { name: 'Review template', exact: true }).click()
    await page.getByRole('heading', { name: 'Review fixture-outpost', exact: true }).waitFor()
    await requiredSecrets(['api-key', 'jwt-secret', 'encryption-secret', 'database-password', 'redis-password', 'broker-password'])
    await recordPlan({ 'database-mode': 'bundled', 'redis-mode': 'bundled', 'broker-mode': 'bundled' })
    if (width !== 320) await capture('bundled-review')
    await page.getByRole('button', { name: 'Back to configuration', exact: true }).click()
    assert.equal(await page.getByRole('textbox', { name: 'Application name', exact: true }).inputValue(), 'fixture-outpost')
    await select('PostgreSQL', 'Existing PostgreSQL')
    if (width === 390) {
      await page.getByRole('button', { name: 'Review template', exact: true }).click()
      await page.getByRole('heading', { name: 'Review fixture-outpost', exact: true }).waitFor()
      await requiredSecrets(['api-key', 'jwt-secret', 'encryption-secret', 'database-url', 'redis-password', 'broker-password'])
      await recordPlan({ 'database-mode': 'external', 'redis-mode': 'bundled', 'broker-mode': 'bundled' })
      await capture('mixed-review')
      await page.getByRole('button', { name: 'Back to configuration', exact: true }).click()
    }
    await select('RabbitMQ', 'Existing RabbitMQ')
    const redisMode = page.getByRole('combobox', { name: 'Redis', exact: true })
    await redisMode.focus()
    await page.keyboard.press('ArrowDown')
    await page.getByRole('option', { name: 'Existing Redis', exact: true }).waitFor()
    await page.waitForFunction(() => document.activeElement?.getAttribute('role') === 'option')
    await page.keyboard.press('End')
    await page.waitForFunction(() => document.activeElement?.textContent?.trim() === 'Existing Redis')
    await page.keyboard.press('Enter')
    await page.getByRole('textbox', { name: 'Redis host', exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: 'Review template', exact: true }).isDisabled(), true)
    await page.getByRole('textbox', { name: 'Redis host', exact: true }).fill('redis.example.invalid')
    await capture('external')
    await select('Redis', 'Bundled Redis')
    await select('Redis', 'Existing Redis')
    assert.equal(await page.getByRole('textbox', { name: 'Redis host', exact: true }).inputValue(), 'redis.example.invalid')
    const tls = page.getByRole('combobox', { name: 'Redis TLS', exact: true })
    await tls.click()
    await page.getByRole('listbox').waitFor()
    const bounds = await page.getByRole('listbox').boundingBox()
    assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= width)
    await page.keyboard.press('Escape')
    const help = page.getByRole('button', { name: 'About Application configuration', exact: true })
    await help.scrollIntoViewIfNeeded()
    await help.focus()
    await page.getByRole('tooltip').waitFor()
    await page.keyboard.press('Escape')
    await page.getByRole('tooltip').waitFor({ state: 'hidden' })
    if (width < 640) {
      await help.tap()
      await page.getByRole('tooltip').waitFor()
      const tooltip = await page.getByRole('tooltip').boundingBox()
      assert.ok(tooltip.x >= 0 && tooltip.x + tooltip.width <= width)
      await help.tap()
      await page.getByRole('tooltip').waitFor({ state: 'hidden' })
    }
    await page.evaluate(() => window.__fixture.planMode = 'error')
    await page.getByRole('button', { name: 'Review template', exact: true }).click()
    await page.getByText('Development fixture: review request failed. Your entries are preserved.', { exact: true }).first().waitFor()
    assert.equal(await page.getByRole('textbox', { name: 'Redis host', exact: true }).inputValue(), 'redis.example.invalid')
    if (width === 320) await capture('review-error', { allowError: true })
    await page.evaluate(() => window.__fixture.planMode = 'success')
    await page.getByRole('button', { name: 'Review template', exact: true }).click()
    await page.getByRole('heading', { name: 'Review fixture-outpost', exact: true }).waitFor()
    await requiredSecrets(['api-key', 'jwt-secret', 'encryption-secret', 'database-url', 'redis-password', 'broker-url'])
    await recordPlan({ 'database-mode': 'external', 'redis-mode': 'external', 'broker-mode': 'external', 'redis-host': 'redis.example.invalid' })
    assert.equal(await page.getByRole('button', { name: 'Deploy template', exact: true }).isDisabled(), true)
    assert.equal(await page.getByText('database-password', { exact: true }).count(), 0)
    assert.equal(await page.getByText('broker-password', { exact: true }).count(), 0)
    await capture('review')
    const row = page.locator('.settings-list-row').filter({ has: page.getByText('database-url', { exact: true }) })
    await row.getByRole('button', { name: 'Set value', exact: true }).click()
    await page.locator('input[type=password]').fill('postgres://fixture:private-fixture@db/outpost?sslmode=require')
    assert.equal(await page.getByRole('button', { name: 'Generate value', exact: true }).count(), 0)
    await page.evaluate(() => window.__fixture.secretMode = 'error')
    await page.getByRole('button', { name: 'Save secret', exact: true }).click()
    await page.getByText('Development fixture: secret was not saved. Your entry is preserved.', { exact: true }).first().waitFor()
    assert.equal(await page.locator('input[type=password]').inputValue(), 'postgres://fixture:private-fixture@db/outpost?sslmode=require')
    if (width < 640) await capture('secret-error', { allowError: true })
    assert.equal(await page.evaluate(() => window.__fixture.requests.filter(request => request.method === 'PUT').every(request => request.body?.redacted === true && !('value' in request.body))), true)
    results.push({ label, interactions: ['inspector keyboard and focus return', 'native keyboard selection', 'TLS popup bounds', 'help focus and Escape', ...(width < 640 ? ['touch help toggle'] : []), 'inactive draft preservation', 'failed review preservation', 'provider-secret generation unavailable', 'failed secret preservation', 'deployment blocked on missing secrets'] })
    if ((theme === 'dark' && width === 1440) || (theme === 'light' && width === 390)) {
      await page.goto(`http://127.0.0.1:4194/templates/outpost?theme=${theme}&viewer=1`)
      await page.getByRole('heading', { name: 'Deployment access required', exact: true }).waitFor()
      assert.equal(await page.getByRole('button', { name: 'Review template', exact: true }).count(), 0)
      await capture('denied', { allowEmpty: true })
      await page.goto(`http://127.0.0.1:4194/templates?theme=${theme}&state=empty`)
      await page.getByRole('heading', { name: 'No matching templates', exact: true }).waitFor()
      await capture('empty', { allowEmpty: true })
      await page.goto(`http://127.0.0.1:4194/templates?theme=${theme}&state=error`)
      await page.locator('[data-error-recovery]').getByText('Development fixture: catalog is unavailable.', { exact: true }).waitFor()
      await capture('catalog-error', { allowError: true })
      await page.evaluate(() => window.__fixture.catalogMode = 'success')
      await page.locator('[data-error-recovery]').getByRole('button', { name: 'Retry', exact: true }).click()
      await page.getByRole('heading', { name: 'Outpost', exact: true }).waitFor()
      await stable()
      results.push({ label, interactions: ['viewer deployment controls absent', 'empty catalog readable', 'catalog retry recovers loaded cards'] })
    }
    await context.close()
  }
} catch (error) {
  errors.push({ label, message: error.message })
  await page?.screenshot({ path: `${evidence}${label}-failure.png`, fullPage: true }).catch(() => {})
} finally {
  await browser.close()
  await writeFile(`${evidence}results.json`, JSON.stringify({ fixture: 'synthetic UI state with real Go plans; no deployment performed', results, errors }, null, 2))
}
console.log(JSON.stringify({ observations: results.length, errors }, null, 2))
if (errors.length) process.exitCode = 1
