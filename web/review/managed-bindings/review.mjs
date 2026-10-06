import assert from 'node:assert/strict'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { fileURLToPath, pathToFileURL } from 'node:url'

const project = fileURLToPath(new URL('../../../', import.meta.url))
const output = process.env.HAKOPOD_BINDING_REVIEW_OUTPUT || `${project}/.local/managed-binding-review/evidence`
const base = `http://127.0.0.1:${Number(process.env.HAKOPOD_BINDING_REVIEW_PORT || 4198)}`
const modulePath = process.env.HAKOPOD_PLAYWRIGHT_MODULE
assert(modulePath, 'Set HAKOPOD_PLAYWRIGHT_MODULE to the installed Playwright index.mjs')
const { chromium } = await import(pathToFileURL(modulePath).href)
await mkdir(`${output}/screenshots`, { recursive: true })
const browser = await chromium.launch({ headless: true, ...(process.env.CHROME_EXECUTABLE ? { executablePath: process.env.CHROME_EXECUTABLE } : {}), args: ['--disable-dev-shm-usage'] })
const cases = []
const captures = []
const sizes = [{ width: 1484, height: 1044 }, { width: 390, height: 844 }, { width: 320, height: 844 }]
const applicationID = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
const databaseID = 'cccccccccccccccccccccccccccccccc'
const connectPath = `/databases/${databaseID}/connect`
const appPath = `/applications/${applicationID}`
const syntheticPassword = 'ARTIFICIAL-UI-PASSWORD-NOT-A-CREDENTIAL'
const caseFilter = process.env.HAKOPOD_BINDING_REVIEW_CASES ? new RegExp(process.env.HAKOPOD_BINDING_REVIEW_CASES) : null
const reviewedFiles = ['web/src/lib/database-binding.ts', 'web/src/components/database-binding-options.tsx', 'web/src/components/managed-database-connections.tsx', 'web/src/routes/databases.$databaseId.connect.tsx']
async function sourceHashes() {
  return Object.fromEntries(await Promise.all(reviewedFiles.map(async (path) => [path, createHash('sha256').update(await readFile(`${project}/${path}`)).digest('hex')])))
}
const initialHashes = await sourceHashes()

async function settle(page, expected, loading = false) {
  await page.waitForFunction(() => window.__bindingFixture?.ready === true)
  await page.locator('main').waitFor()
  await expected.waitFor({ state: 'visible' })
  if (!loading) await page.locator('.hako-loading-stack').first().waitFor({ state: 'hidden' })
  await page.evaluate(() => document.fonts.ready)
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
}

async function choose(page, label, option, keyboard = false) {
  const control = page.getByRole('combobox', { name: label, exact: true })
  await control.scrollIntoViewIfNeeded()
  if (keyboard) {
    await control.focus()
    await control.press('ArrowDown')
    const options = page.getByRole('option')
    await options.first().waitFor()
    const names = await options.allTextContents()
    const index = names.findIndex((name) => name.trim() === option)
    assert(index >= 0, `Missing select option: ${label} / ${option}`)
    await options.nth(index).focus()
    await page.keyboard.press('Enter')
  } else {
    await control.click()
    await page.getByRole('option', { name: option, exact: true }).click()
  }
  await page.getByRole('listbox').waitFor({ state: 'hidden' })
  await control.filter({ hasText: option }).waitFor({ state: 'visible' })
}

async function selectApplication(page, keyboard = false) {
  await choose(page, 'Application', 'binding-review', keyboard)
  await choose(page, 'Service', 'api', keyboard)
  await page.getByRole('combobox', { name: 'Password source', exact: true }).waitFor()
}

async function customDraft(page, source = 'Saved application secret') {
  await selectApplication(page)
  await page.getByLabel('Username', { exact: true }).fill('existing_application_user')
  await page.getByLabel('Database name', { exact: true }).fill('existing_application_database')
  await choose(page, 'Password source', source)
  if (source === 'Saved application secret') await choose(page, 'Saved password', 'existing-database-password')
  else await page.getByLabel('Existing password', { exact: true }).fill(syntheticPassword)
  await choose(page, 'SSL mode', 'Verify certificate and hostname (verify-full)')
}

async function capture(page, id, options = {}) {
  const viewportCapture = id.endsWith('-help')
  if (!viewportCapture) { await page.evaluate(() => window.scrollTo(0, 0)); await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))) }
  const measurement = await page.evaluate(() => {
    const rect = (el) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, right: r.right, bottom: r.bottom, width: r.width, height: r.height } }
    const visible = (el) => { const r = el.getBoundingClientRect(); const s = getComputedStyle(el); return r.width > 0 && r.height > 0 && s.display !== 'none' && s.visibility !== 'hidden' }
    const main = document.querySelector('main')
    const mainStyle = getComputedStyle(main)
    const controls = [...main.querySelectorAll('input:not([type=hidden]),button,[role=combobox],textarea,a')].filter(visible).map((el) => ({
      id: el.id, label: el.getAttribute('aria-label') || el.labels?.[0]?.textContent?.trim() || el.textContent?.trim(), box: rect(el),
      disabled: Boolean(el.disabled), insideScrollableTabs: Boolean(el.closest('.tab-list')), insideTopologyCanvas: Boolean(el.closest('.topology-canvas')),
    }))
    const active = document.activeElement
    const style = active ? getComputedStyle(active) : null
    return {
      viewport: { width: innerWidth, height: innerHeight }, documentWidth: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
      main: { box: rect(main), padding: [mainStyle.paddingLeft, mainStyle.paddingRight] },
      headings: [...main.querySelectorAll('h1')].map((el) => el.textContent),
      topologyScrollers: [...main.querySelectorAll('.topology-viewport')].map(el => ({box: rect(el), scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, overflowX: getComputedStyle(el).overflowX})),
      controls, bindings: [...main.querySelectorAll('[aria-label="Connection options"]')].filter(visible).map((el) => ({ box: rect(el), text: el.textContent })),
      focused: active ? { tag: active.tagName, id: active.id, box: rect(active), outlineWidth: style.outlineWidth, outlineStyle: style.outlineStyle } : null,
      brackets: [...document.querySelectorAll('.brackets')].filter(visible).map((el) => el.className),
      loading: [...document.querySelectorAll('.hako-loading-stack')].filter(visible).length,
      blocked: [...window.__bindingFixture.blocked], synthetic: window.__bindingFixture.synthetic,
    }
  })
  const width = page.viewportSize().width
  assert(measurement.synthetic, 'Missing artificial-data fixture marker')
  assert.deepEqual(measurement.blocked, [], 'Unexpected API request in fixture')
  assert.equal(measurement.viewport.width, width, 'Actual viewport differs from requested width')
  assert(measurement.documentWidth <= width + 1, `Document overflow: ${measurement.documentWidth - width}px`)
  assert.deepEqual(measurement.main.padding, [width < 640 ? '16px' : '24px', width < 640 ? '16px' : '24px'])
  assert.equal(measurement.headings.length, 1, 'Expected one page h1')
  assert.equal(measurement.brackets.length, 0, 'Self-hosted UI contains decorative brackets')
  if (!options.loading) assert.equal(measurement.loading, 0, 'Screenshot captured a loading placeholder')
  for (const control of measurement.controls) {
    if (!control.insideScrollableTabs && !control.insideTopologyCanvas) assert(control.box.x >= -1 && control.box.right <= width + 1, `Clipped control: ${control.label}`)
  }
  for (const scroller of measurement.topologyScrollers) assert(scroller.box.x >= -1 && scroller.box.right <= width + 1 && ['auto', 'scroll'].includes(scroller.overflowX), 'Topology scroller is clipped or cannot scroll')
  if (width >= 640 && await page.locator('#binding-username').isVisible()) {
    const box = (id) => measurement.controls.find((control) => control.id === id)?.box
    assert(Math.abs(box('binding-username').y - box('binding-database').y) <= 1, 'Username and database control rows differ')
    assert(Math.abs(box('binding-password-source').y - box('binding-ssl').y) <= 1, 'Password and SSL control rows differ')
  }
  const screenshot = `screenshots/${id}.png`
  await page.screenshot({ path: `${output}/${screenshot}`, fullPage: !viewportCapture })
  captures.push({ id, screenshot, screenshotMode: viewportCapture ? 'viewport' : 'full-page-from-top', ...measurement })
}

async function runCase(name, theme, viewport, scenario, run, path = connectPath) {
  const id = `${name}-${theme}-${viewport.width}`
  if (caseFilter && !caseFilter.test(id)) return
  const context = await browser.newContext({ viewport, reducedMotion: 'reduce', hasTouch: viewport.width < 640, isMobile: viewport.width < 640 })
  await context.addInitScript((value) => localStorage.setItem('hakopod-theme', value), theme)
  const blocked = []
  await context.route('**/*', (route) => {
    if (new URL(route.request().url()).origin === base) return route.continue()
    blocked.push(route.request().url())
    return route.abort()
  })
  const page = await context.newPage()
  page.setDefaultTimeout(30000)
  const errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  try {
    await page.goto(`${base}${path}${path.includes('?') ? '&' : '?'}fixture=${scenario}`, { waitUntil: 'domcontentloaded' })
    await run(page, id)
    assert.deepEqual(blocked, [], 'Unexpected external request')
    assert.deepEqual(errors, [], 'Rendered error boundary or runtime error')
    const requests = await page.evaluate(() => window.__bindingFixture.requests)
    assert(!JSON.stringify(requests).includes(syntheticPassword), 'Raw password reached evidence ledger')
    cases.push({ id, status: 'passed', requests })
    console.log(`PASS ${id}`)
  } catch (error) {
    const diagnostic = await page.evaluate(() => ({ body: document.body.innerText, requests: window.__bindingFixture?.requests, blocked: window.__bindingFixture?.blocked })).catch((issue) => ({ error: issue.message }))
    let screenshotError
    await page.screenshot({ path: `${output}/screenshots/${id}-FAILED.png`, fullPage: true, timeout: 15000 }).catch((issue) => { screenshotError = issue.message })
    cases.push({ id, status: 'failed', error: error.message, pageErrors: errors, blocked, diagnostic, screenshotError })
    console.log(`FAIL ${id}: ${error.message}`)
  } finally { await context.close() }
}

for (const theme of ['dark', 'light']) for (const viewport of sizes) {
  await runCase('default-and-keyboard', theme, viewport, 'postgresql', async (page, id) => {
    await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
    await capture(page, `${id}-empty-draft`)
    await selectApplication(page, true)
    await page.getByLabel('Username', { exact: true }).focus()
    await page.keyboard.press('Tab')
    assert.equal(await page.evaluate(() => document.activeElement?.id), 'binding-database', 'Keyboard field order changed')
    const focus = await page.locator('#binding-database').evaluate((el) => ({ width: getComputedStyle(el).outlineWidth, style: getComputedStyle(el).outlineStyle }))
    assert(focus.width !== '0px' && focus.style !== 'none', 'Keyboard focus is not visible')
    await capture(page, `${id}-keyboard-focus`)
    const help = page.getByRole('button', { name: 'About Connection options', exact: true })
    await help.scrollIntoViewIfNeeded(); await help.focus()
    await page.getByRole('tooltip').waitFor()
    assert(await help.getAttribute('aria-describedby'), 'Help is not associated with its content')
    await capture(page, `${id}-help`)
    await help.press('Escape')
    await page.getByRole('tooltip').waitFor({ state: 'hidden' })
    if (viewport.width < 640) {
      const control = page.getByRole('combobox', { name: 'Password source', exact: true })
      await control.scrollIntoViewIfNeeded(); await control.tap()
      await page.getByRole('option', { name: 'Saved application secret', exact: true }).tap()
      assert((await control.textContent()).includes('Saved application secret'), 'Touch selection failed')
      await capture(page, `${id}-touch-select`)
    }
  })
  await runCase('pooled-guard', theme, viewport, 'postgresql', async (page, id) => {
    await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
    await customDraft(page)
    await page.getByRole('combobox', { name: 'Endpoint', exact: true }).click()
    const pooled = page.getByRole('option').filter({ hasText: /pool/i }).first()
    await pooled.click()
    await page.getByText('Choose a direct endpoint for a custom login or database.', { exact: true }).waitFor()
    assert(!await page.getByRole('button', { name: 'Review connection', exact: true }).isEnabled())
    assert.equal(await page.getByLabel('Username', { exact: true }).inputValue(), 'existing_application_user')
    await capture(page, id)
    await page.getByRole('combobox', { name: 'Endpoint', exact: true }).click()
    await page.getByRole('option').first().click()
    assert(await page.getByRole('button', { name: 'Review connection', exact: true }).isEnabled())
  })
  await runCase('custom-review-rejection', theme, viewport, 'postgresql', async (page, id) => {
    await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
    await customDraft(page)
    await capture(page, `${id}-draft`)
    await page.getByRole('button', { name: 'Review connection', exact: true }).click()
    await settle(page, page.getByRole('heading', { name: 'Review connection and redeployment', exact: true }))
    const input = await page.evaluate(() => window.__bindingFixture.plans.at(-1))
    assert.equal(input.username, 'existing_application_user'); assert.equal(input.database, 'existing_application_database')
    assert.deepEqual(input.password, { ref: 'existing-database-password' }); assert.equal(input.ssl_mode, 'verify-full')
    assert(!await page.getByRole('button', { name: 'Replace connection and redeploy', exact: true }).isEnabled())
    await capture(page, `${id}-review`)
    await page.getByLabel('Type binding-review to confirm redeployment', { exact: true }).fill('binding-review')
    await page.getByRole('button', { name: 'Replace connection and redeploy', exact: true }).click()
    await page.getByText('Artificial fixture: The connection could not be applied. No redeployment was submitted.', { exact: true }).waitFor()
    assert.equal(await page.getByLabel('Type binding-review to confirm redeployment', { exact: true }).inputValue(), 'binding-review')
    assert.equal(await page.getByLabel('Username', { exact: true }).inputValue(), input.username)
    await capture(page, `${id}-rejected`)
    assert(!await page.locator('main').innerText().then((text) => text.includes(syntheticPassword)), 'Review revealed raw password')
  })
  await runCase('secret-and-plan-failure', theme, viewport, 'postgresql', async (page, id) => {
    await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
    await customDraft(page, 'Enter password')
    await page.evaluate(() => { window.__bindingFixture.secretMode = 'error' })
    await page.getByRole('button', { name: 'Review connection', exact: true }).click()
    await page.getByText('Artificial fixture: The password was not saved. Your entry is preserved.', { exact: true }).waitFor()
    assert.equal(await page.getByLabel('Existing password', { exact: true }).inputValue(), syntheticPassword)
    assert.equal(await page.evaluate(() => window.__bindingFixture.plans.length), 0)
    await capture(page, `${id}-secret-error`)
    await page.evaluate(() => { window.__bindingFixture.secretMode = 'success'; window.__bindingFixture.planMode = 'error' })
    await page.getByRole('button', { name: 'Review connection', exact: true }).click()
    await page.getByText('Artificial fixture: The connection review could not be prepared. Your entries are preserved.', { exact: true }).waitFor()
    assert.equal(await page.getByLabel('Existing password', { exact: true }).count(), 0)
    assert.equal(await page.getByLabel('Username', { exact: true }).inputValue(), 'existing_application_user')
    assert((await page.getByRole('combobox', { name: 'Saved password', exact: true }).textContent()).includes('db-'))
    await capture(page, `${id}-plan-error-after-secret`)
    const savedCount = await page.evaluate(() => window.__bindingFixture.secretWrites.length)
    await page.evaluate(() => { window.__bindingFixture.planMode = 'success' })
    await page.getByRole('button', { name: 'Review connection', exact: true }).click()
    await settle(page, page.getByRole('heading', { name: 'Review connection and redeployment', exact: true }))
    assert.equal(await page.evaluate(() => window.__bindingFixture.secretWrites.length), savedCount, 'Retry duplicated saved password')
    await page.evaluate(() => { window.__bindingFixture.database.revision++; window.__bindingFixture.invalidate('managed-database') })
    await page.getByText('The database or application changed. Review the connection again.', { exact: true }).waitFor()
    assert(!await page.getByRole('button', { name: 'Replace connection and redeploy', exact: true }).isEnabled())
    await capture(page, `${id}-stale-review`)
  })
  await runCase('scope-clearing', theme, viewport, 'postgresql', async (page, id) => {
    await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
    await customDraft(page, 'Enter password')
    await choose(page, 'Application', 'another-review-app')
    await page.waitForFunction(() => document.querySelector('#binding-password-source')?.textContent.includes('Managed password'))
    assert.equal(await page.getByLabel('Existing password', { exact: true }).count(), 0)
    await choose(page, 'Service', 'api')
    await choose(page, 'Password source', 'Saved application secret')
    assert(!(await page.getByRole('combobox', { name: 'Saved password', exact: true }).textContent()).includes('existing-database-password'))
    await capture(page, id)
  })
  for (const engine of ['redis', 'redis-cluster', 'mysql', 'mongodb', 'oracle', 'vitess', 'clickhouse', 'postgresql-legacy']) {
    await runCase(`engine-${engine}`, theme, viewport, engine, async (page, id) => {
      await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
      await selectApplication(page)
      if (engine.startsWith('redis')) {
        await choose(page, 'Database index', engine === 'redis-cluster' ? '0' : '15')
        if (engine === 'redis-cluster') {
          await page.getByLabel('This application uses a cluster-aware Redis client.', { exact: true }).check()
          await page.getByRole('combobox', { name: 'Database index', exact: true }).click()
          assert.equal(await page.getByRole('option').count(), 2, 'Redis Cluster exposed nonzero indexes')
          await page.keyboard.press('Escape')
        }
      }
      if (engine === 'mongodb') await page.getByLabel('This application uses a MongoDB driver that supports replica-set discovery.', { exact: true }).check()
      if (engine === 'mysql' || engine === 'vitess') assert(!await page.getByRole('combobox', { name: 'SSL mode', exact: true }).isEnabled())
      if (engine === 'postgresql-legacy') await choose(page, 'SSL mode', 'Disable TLS (legacy database)')
      await capture(page, id)
    })
  }
  for (const [state, expected] of [['denied', 'Connecting a database requires project deployment permission.'], ['empty', 'No applications in this environment'], ['error', 'Artificial fixture: The database could not be loaded.'], ['unready', 'Wait for the database to be ready before connecting an application.']]) {
    await runCase(`state-${state}`, theme, viewport, state, async (page, id) => {
      await settle(page, page.locator('main').getByText(expected, { exact: true }).first())
      await capture(page, id)
    })
  }
  await runCase('application-summary', theme, viewport, 'postgresql', async (page, id) => {
    await settle(page, page.getByRole('region', { name: 'Managed database connections', exact: true }))
    await capture(page, id)
    const links = await page.getByRole('region', { name: 'Managed database connections', exact: true }).getByRole('link').evaluateAll((items) => items.map((item) => item.getAttribute('href')))
    assert(links.every((url) => url.includes('project=review-project') && url.includes('environment=development')), 'Binding link lost loaded scope')
  }, `${appPath}?tab=topology`)
  await runCase('service-summary', theme, viewport, 'postgresql', async (page, id) => {
    await settle(page, page.getByRole('region', { name: 'Managed database connections', exact: true }))
    const summary = page.getByRole('region', { name: 'Managed database connections', exact: true })
    assert((await summary.innerText()).includes('DATABASE_URL')); assert(!(await summary.innerText()).includes('CACHE_URL'))
    await capture(page, id)
  }, `${appPath}?service=api&tab=environment`)
}

for (const theme of ['dark', 'light']) {
  await runCase('binding-interactions', theme, sizes[1], 'postgresql', async (page, id) => {
    await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
    const help = page.getByRole('button', { name: 'About Connection options', exact: true })
    await help.scrollIntoViewIfNeeded(); await help.tap()
    const tooltip = page.getByRole('tooltip'); await tooltip.waitFor()
    const tooltipBounds = await tooltip.boundingBox()
    assert(tooltipBounds.x >= 0 && tooltipBounds.x + tooltipBounds.width <= 390, 'Touch help escaped the viewport')
    await capture(page, `${id}-help`)
    await help.tap(); await tooltip.waitFor({ state: 'hidden' })
    await customDraft(page, 'Enter password')
    await page.evaluate(() => { window.__bindingFixture.secretMode = 'hold' })
    await page.getByRole('button', { name: 'Review connection', exact: true }).click()
    await page.waitForFunction(() => Boolean(window.__bindingFixture.releaseSecret))
    const working = page.getByRole('button', { name: 'Working…', exact: true })
    assert(!await working.isEnabled()); await working.evaluate(el => el.click())
    assert.equal(await page.evaluate(() => window.__bindingFixture.secretWrites.length), 1)
    await page.evaluate(() => { window.__bindingFixture.secretMode = 'success'; window.__bindingFixture.releaseSecret() })
    await settle(page, page.getByRole('heading', { name: 'Review connection and redeployment', exact: true }))
    await page.getByLabel('Type binding-review to confirm redeployment', { exact: true }).fill('binding-review')
    await page.evaluate(() => { window.__bindingFixture.connectMode = 'hold' })
    await page.getByRole('button', { name: 'Replace connection and redeploy', exact: true }).click()
    await page.waitForFunction(() => Boolean(window.__bindingFixture.releaseConnect))
    assert(!await working.isEnabled()); await working.evaluate(el => el.click())
    assert.equal(await page.evaluate(() => window.__bindingFixture.submissions.length), 1)
    await page.evaluate(() => { window.__bindingFixture.releaseConnect() })
    await page.locator('main').getByText('Artificial fixture: The connection could not be applied. No redeployment was submitted.', { exact: true }).first().waitFor()
    await capture(page, `${id}-busy-rejected`)
  })
  await runCase('state-application-error', theme, sizes[1], 'application-error', async (page, id) => {
    await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
    await choose(page, 'Application', 'binding-review')
    await page.locator('main').getByText('Artificial fixture: The selected application could not be loaded.', { exact: true }).first().waitFor()
    assert(!await page.getByRole('button', { name: 'Review connection', exact: true }).isEnabled())
    await capture(page, id)
  })
  await runCase('expiry', theme, sizes[1], 'postgresql', async (page, id) => {
    await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
    await selectApplication(page)
    await page.evaluate(() => { window.__bindingFixture.planMode = 'short' })
    await page.getByRole('button', { name: 'Review connection', exact: true }).click()
    await page.getByText('This review expired. Review the connection again.', { exact: true }).waitFor({ timeout: 10000 })
    assert(!await page.getByRole('button', { name: 'Replace connection and redeploy', exact: true }).isEnabled())
    await capture(page, id)
  })
  for (const scenario of ['secrets-error', 'secrets-empty']) await runCase(scenario, theme, sizes[1], scenario, async (page, id) => {
    await settle(page, page.getByRole('heading', { name: 'Connection options', exact: true }))
    await selectApplication(page)
    await choose(page, 'Password source', 'Saved application secret')
    await page.getByText(scenario === 'secrets-error' ? 'Application secrets could not be loaded. Retry before selecting a saved password.' : 'No passwords are saved for this application. Choose Enter password to save one.', { exact: true }).waitFor()
    await capture(page, id)
    if (scenario === 'secrets-error') {
      assert(!await page.getByRole('combobox', { name: 'Saved password', exact: true }).isEnabled())
      await page.evaluate(() => { window.__bindingFixture.secretsMode = 'success' })
      await page.getByRole('button', { name: 'Retry application secrets', exact: true }).click()
      await page.waitForFunction(() => !document.querySelector('#binding-secret').disabled)
      await choose(page, 'Saved password', 'existing-database-password')
      assert(await page.getByRole('button', { name: 'Review connection', exact: true }).isEnabled())
      await capture(page, `${id}-retried`)
    }
  })
  await runCase('loading', theme, sizes[1], 'loading', async (page, id) => {
    await settle(page, page.locator('.hako-loading-stack').first(), true)
    await capture(page, id, { loading: true })
  })
}
await browser.close()
const finalHashes = await sourceHashes()
assert.deepEqual(finalHashes, initialHashes, 'Reviewed source changed during the browser run')
await writeFile(`${output}/results.json`, `${JSON.stringify({ source: process.env.HAKOPOD_REVIEW_COMMIT || 'working tree snapshot', sourceHashes: initialHashes, artificial: true, cases, captures }, null, 2)}\n`)
console.log(JSON.stringify({ cases: cases.length, failed: cases.filter((entry) => entry.status === 'failed').map((entry) => ({ id: entry.id, error: entry.error })), captures: captures.length, output }, null, 2))
if (cases.some((entry) => entry.status === 'failed')) process.exitCode = 1
