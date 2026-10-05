import assert from 'node:assert/strict'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { chromium } from '../../../.local/template-review/node_modules/playwright/index.mjs'

// Supplemental presentation checks only. The same real Go plans and synthetic
// identity/secret metadata as the full review are used; no deployment occurs.
const evidence = fileURLToPath(new URL('../../../.local/template-review/evidence/', import.meta.url))
const catalog = JSON.parse(await readFile(new URL('./catalog-fixture.json', import.meta.url), 'utf8'))
await mkdir(evidence, { recursive: true })
const browser = await chromium.launch({ headless: true })
const results = [], errors = []
let page, label

async function loaded() {
  await page.getByRole('heading', { name: 'Review fixture-outpost', exact: true }).waitFor()
  await page.evaluate(() => document.fonts.ready)
  assert.equal(await page.locator('.hako-loading-stack,.hako-empty-canvas,[data-error-recovery]').count(), 0)
  assert.equal(await page.getByText('Something went wrong!', { exact: true }).count(), 0)
  assert.equal(await page.evaluate(() => window.__fixture.requests.some(request => !request.allowed)), false)
  await page.evaluate(() => Promise.allSettled(document.getAnimations()
    .filter(animation => animation.effect?.getComputedTiming().iterations !== Infinity)
    .map(animation => animation.finished)))
}

async function capture(state, target) {
  await loaded()
  await target.scrollIntoViewIfNeeded()
  const bounds = await target.evaluate(element => {
    const rect = element.getBoundingClientRect()
    const style = getComputedStyle(element)
    return { x: rect.x, y: rect.y, right: rect.right, width: rect.width, height: rect.height,
      documentWidth: document.documentElement.scrollWidth, viewportWidth: innerWidth,
      clientWidth: element.clientWidth, scrollWidth: element.scrollWidth,
      clientHeight: element.clientHeight, scrollHeight: element.scrollHeight,
      scrollLeft: element.scrollLeft, scrollTop: element.scrollTop,
      overflowX: style.overflowX, outlineStyle: style.outlineStyle,
      outlineWidth: style.outlineWidth, focused: element === document.activeElement }
  })
  assert.equal(bounds.documentWidth, bounds.viewportWidth, `${state}: document overflow`)
  assert.ok(bounds.x >= 0 && bounds.right <= bounds.viewportWidth, `${state}: target bounds`)
  const filename = `${label}-${state}.png`
  // The viewport retains surrounding review context. Full canonical documents
  // are intentionally not expanded into enormous full-page screenshots.
  await page.screenshot({ path: `${evidence}${filename}` })
  results.push({ label, state, bounds, screenshot: filename })
}

function row(field) {
  return page.locator('.diff-row')
    .filter({ has: page.locator('.diff-field > span', { hasText: /^migrate$/ }) })
    .filter({ has: page.locator('.diff-field > strong', { hasText: new RegExp(`^${field}$`) }) })
}

async function showDiff(field, expected) {
  for (let attempt = 0; attempt < 12; attempt++) {
    if (await row(field).count()) {
      assert.equal(await row(field).locator('.diff-after code').textContent(), JSON.stringify(expected))
      return row(field)
    }
    const next = page.locator('.diff-pagination').getByRole('button', { name: 'Next', exact: true })
    assert.equal(await next.isEnabled(), true, `Missing migration ${field} row`)
    const before = await page.locator('.diff-pagination > span').textContent()
    if (label.includes('-320-')) await next.tap()
    else { await next.focus(); await page.keyboard.press('Enter') }
    await page.waitForFunction(previous => document.querySelector('.diff-pagination > span')?.textContent !== previous, before)
  }
  assert.fail(`Migration ${field} row exceeded the bounded pagination search`)
}

async function scrollCanonicalToCommand(pre) {
  return pre.evaluate(element => {
    const text = element.textContent
    const section = text.indexOf('[services.migrate]')
    if (section < 0) throw new Error('Missing migration TOML section')
    const match = /^\s*command\s*=/m.exec(text.slice(section))
    if (!match) throw new Error('Missing migration command in TOML')
    const offset = section + match.index + match[0].indexOf('command')
    const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT)
    let node, consumed = 0
    while ((node = walker.nextNode())) {
      if (consumed + node.length > offset) {
        const range = document.createRange()
        range.setStart(node, offset - consumed)
        range.setEnd(node, Math.min(node.length, offset - consumed + 1))
        element.scrollLeft = 0
        element.scrollTop += range.getBoundingClientRect().top - element.getBoundingClientRect().top - 20
        return { offset, scrollTop: element.scrollTop }
      }
      consumed += node.length
    }
    throw new Error('Migration command range is missing')
  })
}

try {
  for (const theme of ['dark', 'light']) for (const width of [1440, 320]) {
    for (const database of ['bundled', 'external']) {
      label = `${theme}-${width}-${database}`
      const plan = catalog.plans[`${database}-bundled-bundled`]
      const migration = plan.spec.services.migrate
      assert.deepEqual(migration.command, ['/bin/sh', '-ec'])
      assert.ok(migration.args[0].includes('until /usr/local/bin/outpost migrate plan; do'))
      assert.ok(migration.args[0].includes('(exec -a busybox /bin/sh sleep 5)'))
      assert.ok(migration.args[0].includes('exec /usr/local/bin/outpost migrate apply --yes'))
      assert.equal(migration.job.retries, 0)
      for (const name of ['main', 'delivery', 'log', 'migrate']) {
        assert.equal(plan.spec.services[name].env.PGSSLMODE, database === 'bundled' ? 'disable' : undefined)
      }
      assert.equal(migration.secrets.AES_ENCRYPTION_SECRET.ref, 'encryption-secret')
      assert.ok(!('API_KEY' in migration.secrets) && !('API_JWT_SECRET' in migration.secrets))
      const context = await browser.newContext({ viewport: { width, height: 1000 }, hasTouch: width < 640, reducedMotion: 'reduce' })
      page = await context.newPage()
      page.on('pageerror', error => errors.push({ label, message: error.message }))
      await page.goto(`http://127.0.0.1:4194/templates/outpost?theme=${theme}`)
      await page.getByRole('heading', { name: 'Configure Outpost', exact: true }).waitFor()
      await page.getByRole('textbox', { name: 'Application name', exact: true }).fill('fixture-outpost')
      await page.getByRole('combobox', { name: 'Target architecture', exact: true }).click()
      await page.getByRole('option', { name: 'Linux ARM64', exact: true }).click()
      if (database === 'external') {
        await page.getByRole('combobox', { name: 'PostgreSQL', exact: true }).click()
        await page.getByRole('option', { name: 'Existing PostgreSQL', exact: true }).click()
      }
      await page.getByRole('button', { name: 'Review template', exact: true }).click()
      await loaded()
      const command = await showDiff('command', migration.command)
      const commandBounds = await command.boundingBox()
      assert.ok(commandBounds.x >= 0 && commandBounds.x + commandBounds.width <= width)
      await capture('migration-command', command)
      const args = await showDiff('args', migration.args)
      await capture('migration-args', args)
      const summary = page.locator('.config-details > summary')
      if (width < 640) await summary.tap()
      else { await summary.focus(); await page.keyboard.press('Enter') }
      await page.waitForFunction(() => document.querySelector('.config-details')?.open === true)
      const pre = page.getByLabel('TOML configuration', { exact: true })
      assert.equal(await pre.textContent(), plan.toml)
      // Start with programmatic focus on the native disclosure, then use Tab
      // to enter its native scrollable code block. This is targeted navigation,
      // not a claim of complete keyboard traversal from the start of the page.
      // Scroll positioning below selects the changed passage for inspection;
      // it is not counted as a keyboard or touch interaction.
      await summary.focus()
      await page.keyboard.press('Tab')
      assert.equal(await pre.evaluate(element => document.activeElement === element), true)
      const passage = await scrollCanonicalToCommand(pre)
      await capture('canonical-command', pre)
      assert.equal(await pre.evaluate(element => getComputedStyle(element).outlineStyle), 'solid')
      const before = await pre.evaluate(element => element.scrollLeft)
      const canScrollHorizontally = await pre.evaluate(element => element.scrollWidth > element.clientWidth)
      if (canScrollHorizontally) {
        await page.keyboard.press('ArrowRight')
        await page.waitForFunction(() => document.querySelector('.config-details pre')?.scrollLeft > 0)
      }
      const after = await pre.evaluate(element => element.scrollLeft)
      if (canScrollHorizontally) assert.ok(after > before)
      assert.equal(await page.evaluate(() => scrollX), 0)
      results.push({ label, database, commandBounds, passage,
        interactions: [width < 640 ? 'touch diff pagination and disclosure' : 'keyboard diff pagination and disclosure',
          'Tab enters canonical code from programmatically focused disclosure',
          ...(canScrollHorizontally ? ['ArrowRight scrolls canonical code without moving the document'] : [])],
        horizontalScroll: { available: canScrollHorizontally, before, after }, canonicalTextMatchesPlan: true,
        migrationRetries: migration.job.retries, postgresTLSOverride: database === 'bundled' ? 'disable' : 'absent' })
      await summary.focus()
      await page.keyboard.press('Enter')
      await page.waitForFunction(() => document.querySelector('.config-details')?.open === false)
      await context.close()
    }
  }
} catch (error) {
  errors.push({ label, message: error.message, stack: error.stack })
  await page?.screenshot({ path: `${evidence}${label}-failure.png` }).catch(() => {})
} finally {
  await browser.close()
  await writeFile(`${evidence}results.json`, JSON.stringify({
    fixture: 'synthetic UI state with real Go plans; no deployment performed',
    scope: 'migration', results, errors,
  }, null, 2))
}
console.log(JSON.stringify({ observations: results.length, errors }, null, 2))
if (errors.length) process.exitCode = 1
