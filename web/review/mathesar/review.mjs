import assert from 'node:assert/strict'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || '../../../.local/template-review/node_modules/playwright/index.mjs')
const evidence = fileURLToPath(new URL('../../../.local/mathesar-ui-review/evidence/', import.meta.url))
const catalog = JSON.parse(await readFile(new URL('../../../.local/mathesar-ui-review/catalog-fixture.json', import.meta.url), 'utf8'))
await mkdir(evidence, { recursive: true })
const browser = await chromium.launch({ headless: true, ...(process.env.CHROME_EXECUTABLE ? { executablePath: process.env.CHROME_EXECUTABLE } : {}) })
const results = [], errors = []
let page, label

async function loaded({ allowError = false, allowEmpty = false } = {}) {
  await page.locator('.hako-page-content').waitFor()
  await page.evaluate(() => document.fonts.ready)
  assert.equal(await page.locator('.hako-loading-stack').count(), 0)
  if (!allowError) assert.equal(await page.locator('[data-error-recovery]').count(), 0)
  if (!allowEmpty) assert.equal(await page.locator('.hako-empty-canvas').count(), 0)
  assert.equal(await page.getByText('Something went wrong!', { exact: true }).count(), 0)
  assert.equal(await page.evaluate(() => window.__fixture.requests.some(request => !request.allowed)), false)
  await page.evaluate(() => Promise.allSettled(document.getAnimations()
    .filter(animation => animation.effect?.getComputedTiming().iterations !== Infinity)
    .map(animation => animation.finished)))
}

async function capture(state, options = {}) {
  await loaded(options)
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
      nativeSelects: document.querySelectorAll('select:not([aria-hidden=true])').length,
      brackets: [...document.querySelectorAll('.brackets')].filter(e => getComputedStyle(e).display !== 'none' && e.getBoundingClientRect().width).length,
      fields: [...document.querySelectorAll('.form-card input,.form-card button[role="combobox"]')].filter(visible).map(e => ({ name: e.getAttribute('aria-label') || e.id || e.type, ...rect(e) })),
      labels: [...document.querySelectorAll('.form-card label[for]')].map(e => ({ name: e.textContent.trim(), label: rect(e), control: document.getElementById(e.htmlFor) && rect(document.getElementById(e.htmlFor)) })),
      actions: [...document.querySelectorAll('.form-footer button,.dialog-footer button,.dialog-footer a')].filter(visible).map(e => ({ name: e.textContent.trim(), ...rect(e) })),
      dialog: document.querySelector('[role="dialog"]') && rect(document.querySelector('[role="dialog"]')),
    }
  })
  assert.ok(geometry.scrollWidth <= geometry.width, `${state}: document overflow`)
  assert.equal(geometry.h1, 1)
  assert.equal(geometry.brackets, 0)
  assert.equal(geometry.nativeSelects, 0)
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
  console.log(`Captured ${label} ${state}`)
}

async function select(name, choice, touch = false) {
  const control = page.getByRole('combobox', { name, exact: true })
  if (touch) await control.tap()
  else await control.click()
  await page.getByRole('listbox').waitFor()
  const bounds = await page.getByRole('listbox').boundingBox()
  const width = page.viewportSize().width
  assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= width)
  if (touch) await page.getByRole('option', { name: choice, exact: true }).tap()
  else await page.getByRole('option', { name: choice, exact: true }).click()
  await page.getByRole('listbox').waitFor({ state: 'hidden' })
  await page.waitForFunction(({ name, choice }) =>
    [...document.querySelectorAll('button[role="combobox"]')]
      .find(element => element.getAttribute('aria-label') === name)?.textContent?.trim() === choice,
  { name, choice })
}

async function review(database, storage) {
  await page.getByRole('button', { name: 'Review template', exact: true }).click()
  await page.getByRole('heading', { name: 'Review fixture-mathesar', exact: true }).waitFor()
  await page.getByRole('heading', { name: 'Required secrets', exact: true }).waitFor()
  await page.waitForFunction(() => window.__fixture.requests.some(request => request.path === '/api/secrets'))
  const request = await page.evaluate(() => window.__fixture.requests.findLast(request => request.path.endsWith('/plan')))
  assert.equal(request.status, 200)
  assert.equal(request.body.values['database-mode'], database)
  assert.equal(request.body.values['media-storage-mode'], storage === 'local' ? 'local' : 'shared')
  assert.equal(request.body.values['media-storage-class'], storage === 'local' ? undefined : 'fixture-rwx')
  const plan = catalog.plans[`${database}-${storage}`]
  const media = plan.spec.volumes.media
  assert.equal(media.access_mode, storage === 'local' ? 'ReadWriteOnce' : 'ReadWriteMany')
  assert.equal(media.storage_class || '', storage === 'local' ? '' : 'fixture-rwx')
  assert.deepEqual((await page.locator('.service-summary-panel .settings-list-row strong').allTextContents()).sort(), ['database-password', 'secret-key'])
  assert.equal(await page.getByRole('button', { name: 'Deploy template', exact: true }).isDisabled(), true)
  await capture(`${database}-${storage}-review`)
  const summary = page.locator('.config-details > summary')
  if (page.viewportSize().width < 640) await summary.tap()
  else { await summary.focus(); await page.keyboard.press('Enter') }
  await page.waitForFunction(() => document.querySelector('.config-details')?.open === true)
  const canonical = page.getByLabel('TOML configuration', { exact: true })
  assert.equal(await canonical.textContent(), plan.toml)
  await summary.focus()
  await page.keyboard.press('Tab')
  assert.equal(await canonical.evaluate(element => element === document.activeElement), true)
  assert.equal(await canonical.evaluate(element => getComputedStyle(element).outlineStyle), 'solid')
  results.push({ label, database, storage, planRequest: request, media, canonicalTextMatchesPlan: true })
  await page.getByRole('button', { name: 'Back to configuration', exact: true }).click()
  await page.getByRole('heading', { name: 'Configure Mathesar', exact: true }).waitFor()
}

try {
  for (const theme of ['dark', 'light']) for (const width of [1440, 390, 320]) {
    label = `${theme}-${width}`
    const context = await browser.newContext({ viewport: { width, height: 1000 }, hasTouch: width < 640, reducedMotion: 'reduce' })
    page = await context.newPage()
    page.on('pageerror', error => errors.push({ label, message: error.message }))
    try {
      await page.goto(`http://127.0.0.1:4195/templates?theme=${theme}&q=mathesar`)
      await page.getByRole('heading', { name: 'Mathesar', exact: true }).waitFor()
      const card = page.locator('.catalog-card').filter({ has: page.getByRole('heading', { name: 'Mathesar', exact: true }) })
      assert.equal(await card.locator('img').evaluate(element => element.complete && element.naturalWidth > 0), true)
      await capture('catalog')
      await card.focus()
      await page.keyboard.press('Enter')
      await page.getByRole('dialog', { name: 'Mathesar', exact: true }).waitFor()
      await page.getByRole('heading', { name: 'Before you deploy', exact: true }).waitFor()
      await capture('inspector')
      await page.keyboard.press('Escape')
      await page.getByRole('dialog').waitFor({ state: 'hidden' })
      await page.waitForFunction(() => document.activeElement?.matches('.catalog-card'))
      await page.goto(`http://127.0.0.1:4195/templates/mathesar?theme=${theme}`)
      await page.getByRole('heading', { name: 'Configure Mathesar', exact: true }).waitFor()
      const name = page.getByRole('textbox', { name: 'Application name', exact: true })
      await name.fill('fixture-mathesar')
      // Exercise help while entering the form, before review moves the viewport.
      const help = page.getByRole('button', { name: 'About Application configuration', exact: true })
      await page.keyboard.press('Shift+Tab')
      assert.equal(await help.evaluate(element => element === document.activeElement), true)
      await page.getByRole('tooltip').waitFor()
      await page.keyboard.press('Escape')
      await page.getByRole('tooltip').waitFor({ state: 'hidden' })
      await page.keyboard.press('Enter')
      await page.getByRole('tooltip').waitFor()
      await page.keyboard.press('Escape')
      await page.getByRole('tooltip').waitFor({ state: 'hidden' })
      if (width < 640) {
        await name.tap()
        await help.tap()
        await page.getByRole('tooltip').waitFor()
        const bounds = await page.getByRole('tooltip').boundingBox()
        assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= width)
        await help.tap()
        await page.getByRole('tooltip').waitFor({ state: 'hidden' })
      }
      await page.locator('input[type=url]').fill('https://mathesar.example.invalid')
      await select('Target architecture', 'Linux ARM64')
      const storageMode = page.getByRole('combobox', { name: 'Media storage', exact: true })
      assert.match(await storageMode.textContent(), /Automatic local storage/)
      assert.equal(await page.locator('#template-config-media-storage-class').count(), 0)
      assert.equal(await page.getByRole('button', { name: 'Review template', exact: true }).isEnabled(), true)
      const modeHelp = page.locator('#template-help-media-storage-mode')
      assert.match(await modeHelp.textContent(), /same node|one node/i)
      await capture('automatic-configuration')
      await review('bundled', 'local')
      assert.equal(await name.inputValue(), 'fixture-mathesar')

      await storageMode.focus()
      await page.keyboard.press('ArrowDown')
      await page.getByRole('option', { name: 'Existing shared storage', exact: true }).waitFor()
      await page.waitForFunction(() => document.activeElement?.getAttribute('role') === 'option')
      await page.keyboard.press('End')
      await page.waitForFunction(() => document.activeElement?.textContent?.trim() === 'Existing shared storage')
      await page.keyboard.press('Enter')
      await page.getByRole('listbox').waitFor({ state: 'hidden' })
      await page.waitForFunction(() =>
        document.activeElement?.getAttribute('aria-label') === 'Media storage' &&
        document.activeElement?.getAttribute('aria-expanded') === 'false')
      const storageClass = page.locator('#template-config-media-storage-class')
      await storageClass.waitFor()
      assert.equal(await storageClass.getAttribute('required'), '')
      assert.equal(await page.getByRole('button', { name: 'Review template', exact: true }).isDisabled(), true)
      await page.keyboard.press('Tab')
      assert.equal(await storageClass.evaluate(element => element === document.activeElement), true)
      assert.equal(await storageClass.evaluate(element => getComputedStyle(element).outlineStyle), 'solid')
      await storageClass.fill('fixture-rwx')
      await capture('shared-configuration')
      await review('bundled', 'rwx')
      assert.equal(await storageClass.inputValue(), 'fixture-rwx')
      await select('Media storage', 'Automatic local storage', width < 640)
      assert.equal(await storageClass.count(), 0)
      await select('Media storage', 'Existing shared storage', width < 640)
      assert.equal(await storageClass.inputValue(), 'fixture-rwx')
      await select('Media storage', 'Automatic local storage', width < 640)
      await select('Database', 'Existing PostgreSQL', width < 640)
      await page.getByRole('textbox', { name: 'Database host', exact: true }).fill('postgres.example.invalid')
      await capture('external-automatic-configuration')
      await page.evaluate(() => window.__fixture.planMode = 'error')
      await page.getByRole('button', { name: 'Review template', exact: true }).click()
      await page.getByText('Development fixture: review request failed. Your entries are preserved.', { exact: true }).first().waitFor()
      assert.equal(await page.getByRole('textbox', { name: 'Database host', exact: true }).inputValue(), 'postgres.example.invalid')
      assert.match(await storageMode.textContent(), /Automatic local storage/)
      await capture('failed-review', { allowError: true })
      await page.evaluate(() => window.__fixture.planMode = 'success')
      await review('external', 'local')
      await select('Media storage', 'Existing shared storage', width < 640)
      assert.equal(await storageClass.inputValue(), 'fixture-rwx')
      await review('external', 'rwx')
      results.push({ label, interactions: ['catalog keyboard open and focus return', 'keyboard storage selection and visible input focus', 'help opens from initial Shift+Tab and explicit Enter; Escape dismisses', 'canonical disclosure and code focus', 'default local plan without a class', 'shared class required before review', 'inactive class draft preserved and omitted from local request', 'failed review preserves values', 'missing secrets block deployment', ...(width < 640 ? ['touch storage selection and help toggle'] : [])] })
    } catch (error) {
      errors.push({ label, message: error.message, stack: error.stack })
      await page.screenshot({ path: `${evidence}${label}-failure.png`, fullPage: true }).catch(() => {})
    } finally {
      await context.close()
    }
  }
} catch (error) {
  errors.push({ label, message: error.message, stack: error.stack })
  await page?.screenshot({ path: `${evidence}${label}-failure.png`, fullPage: true }).catch(() => {})
} finally {
  await browser.close()
  await writeFile(`${evidence}results.json`, JSON.stringify({ fixture: 'Real template metadata, normalized Go plans and diffs; artificial identity, project, secret metadata and failures. No deployment performed.', results, errors }, null, 2))
}
console.log(JSON.stringify({ observations: results.length, errors }, null, 2))
if (errors.length) process.exitCode = 1
