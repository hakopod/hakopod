import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath, pathToFileURL } from 'node:url'

const project = fileURLToPath(new URL('../../../', import.meta.url))
const output = `${project}/.local/edge-review`
const base = 'http://127.0.0.1:4198'
const modulePath = process.env.HAKOPOD_PLAYWRIGHT_MODULE
assert(modulePath, 'Set HAKOPOD_PLAYWRIGHT_MODULE to the installed Playwright index.mjs')
const { chromium } = await import(pathToFileURL(modulePath).href)
await mkdir(`${output}/screenshots`, { recursive: true })
await mkdir(`${output}/diagnostics`, { recursive: true })
const browser = await chromium.launch({ headless: true, args: ['--disable-dev-shm-usage'] })
const results = []
const screenshots = []
const viewports = [{ width: 1484, height: 1044 }, { width: 390, height: 844 }, { width: 320, height: 844 }]

async function settled(page, expected, options = {}) {
  await page.waitForFunction(() => window.__edgeFixture?.ready === true)
  await page.locator('main').waitFor()
  await expected.waitFor({ state: 'visible' })
  if (!options.loading) await page.locator('main .hako-loading-stack').waitFor({ state: 'hidden' })
  await page.evaluate(() => document.fonts.ready)
  // Let layout effects, active-navigation scrolling and font metrics settle.
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
}

async function capture(page, id, options = {}) {
  const data = await page.evaluate(() => {
    const rect = (element) => {
      const box = element.getBoundingClientRect()
      return { x: box.x, y: box.y, width: box.width, height: box.height, right: box.right, bottom: box.bottom }
    }
    const visible = (element) => {
      const box = element.getBoundingClientRect()
      const style = getComputedStyle(element)
      return box.width > 1 && box.height > 1 && style.visibility !== 'hidden' && style.display !== 'none'
    }
    const main = document.querySelector('main')
    const mainStyle = getComputedStyle(main)
    const wrappers = [...document.querySelectorAll('.settings-page,.settings-content,.form-page,.form-page-layout')].filter(visible).map((element) => {
      const style = getComputedStyle(element)
      return { className: element.className, box: rect(element), padding: [style.paddingLeft, style.paddingRight], maxWidth: style.maxWidth }
    })
    const controls = [...main.querySelectorAll('input:not([type=hidden]),textarea,button,[role=combobox]')].filter(visible).filter((element) => !element.closest('.settings-nav,.tab-list')).map((element) => ({
      label: element.getAttribute('aria-label') || element.labels?.[0]?.textContent?.trim().slice(0, 100) || element.textContent?.trim(),
      box: rect(element),
    }))
    const headings = [...main.querySelectorAll('.hako-page-heading')].filter(visible).map((element) => {
      const style = getComputedStyle(element)
      return { box: rect(element), top: style.paddingTop, bottom: style.paddingBottom }
    })
    const active = [...document.querySelectorAll('.settings-nav [aria-current=page],.tab-list [data-state=active]')].filter(visible).map((element) => {
      const style = getComputedStyle(element)
      return { label: element.textContent, box: rect(element), color: style.color, background: style.backgroundColor, border: style.borderBottomWidth }
    })
    return {
      width: innerWidth,
      documentWidth: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
      main: { box: rect(main), padding: [mainStyle.paddingLeft, mainStyle.paddingRight] },
      wrappers, controls, headings, active,
      h1: [...main.querySelectorAll('h1')].map((element) => element.textContent),
      loading: main.querySelectorAll('.hako-loading-stack').length,
      errors: [...main.querySelectorAll('.hako-error-state')].map((element) => element.textContent),
      visibleBrackets: [...document.querySelectorAll('[class*=bracket]')].filter(visible).map((element) => element.className),
      blockedRequests: [...window.__edgeFixture.blocked],
      fixture: window.__edgeFixture.synthetic,
      form: main.querySelector('form')?.getAttribute('aria-label'),
    }
  })
  assert(data.fixture, 'The page must use the explicit synthetic fixture')
  assert.deepEqual(data.blockedRequests, [], 'Unexpected API or external request')
  assert(data.documentWidth <= data.width + 1, `Document overflows by ${data.documentWidth - data.width}px`)
  assert.deepEqual(data.main.padding, [data.width < 640 ? '16px' : '24px', data.width < 640 ? '16px' : '24px'], 'Shared page inset is incorrect')
  for (const wrapper of data.wrappers) {
    assert.deepEqual(wrapper.padding, ['0px', '0px'], `${wrapper.className} adds a duplicate horizontal inset`)
    assert(wrapper.box.x >= -1 && wrapper.box.right <= data.width + 1, `${wrapper.className} extends outside the viewport`)
  }
  for (const control of data.controls) assert(control.box.x >= -1 && control.box.right <= data.width + 1, `Control is clipped: ${control.label}`)
  for (const heading of data.headings) {
    assert(Math.abs(heading.box.x) <= 1 && Math.abs(heading.box.right - data.width) <= 1, 'Page divider does not span the viewport')
    assert.equal(heading.top, heading.bottom, 'Page-heading vertical padding is not balanced')
  }
  for (const active of data.active) {
    assert(active.box.x >= -1 && active.box.right <= data.width + 1, `Selected navigation is off-screen: ${active.label}`)
    assert.equal(active.background, 'rgba(0, 0, 0, 0)', 'Selected navigation has a background fill')
  }
  assert.equal(data.visibleBrackets.length, 0, 'Self-hosted UI contains visible decorative corner brackets')
  if (!options.loading) assert.equal(data.loading, 0, 'Unexpected loading placeholder')
  if (!options.error) assert.deepEqual(data.errors, [], 'Unexpected rendered error state')
  if (!options.noHeading) assert.equal(data.h1.length, 1, 'Expected exactly one page heading')
  const screenshot = `screenshots/${id}.png`
  await page.screenshot({ path: `${output}/${screenshot}`, fullPage: !options.viewport })
  screenshots.push({ id, screenshot, ...data })
  return data
}

async function runCase(name, theme, viewport, run) {
  const id = `${name}-${theme}-${viewport.width}`
  const context = await browser.newContext({ viewport, reducedMotion: 'reduce', hasTouch: viewport.width < 640, isMobile: viewport.width < 640 })
  await context.addInitScript((value) => localStorage.setItem('hakopod-theme', value), theme)
  const networkBlocks = []
  await context.route('**/*', (route) => {
    if (new URL(route.request().url()).origin === base) return route.continue()
    networkBlocks.push(route.request().url())
    return route.abort()
  })
  const page = await context.newPage()
  page.setDefaultTimeout(15000)
  page.setDefaultNavigationTimeout(45000)
  const errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  try {
    const details = await run(page, id, viewport)
    assert.deepEqual(errors, [], 'Browser reported a script error')
    assert.deepEqual(networkBlocks, [], 'Page attempted a non-fixture network request')
    const blocked = await page.evaluate(() => window.__edgeFixture.blocked)
    assert.deepEqual(blocked, [], 'Page used an API without a fixture contract')
    results.push({ id, name, theme, viewport, passed: true, details })
    console.log(`PASS ${id}`)
  } catch (error) {
    const diagnostic = `diagnostics/${id}.png`
    await page.screenshot({ path: `${output}/${diagnostic}`, fullPage: true }).catch(() => {})
    results.push({ id, name, theme, viewport, passed: false, error: error.stack || String(error), errors, networkBlocks, diagnostic })
    console.log(`FAIL ${id}: ${error.message}`)
  } finally {
    await context.close()
  }
}

const patchCount = (page) => page.evaluate(() => window.__edgeFixture.patches.length)
const labels = (page, name) => page.getByRole('textbox', { name: new RegExp(`^${name}`) })

async function choose(page, label, value, touch) {
  const trigger = page.getByRole('combobox', { name: label, exact: true })
  if (touch) await trigger.tap()
  else await trigger.click()
  const option = page.getByRole('option', { name: value, exact: true })
  await option.waitFor()
  if (touch) await option.tap()
  else await option.click()
  await page.getByRole('listbox').waitFor({ state: 'hidden' })
  assert((await trigger.textContent()).includes(value), `Select did not keep ${value}`)
}

async function helpCheck(page, id, touch) {
  const help = page.getByRole('button', { name: 'About Hakopod Edge', exact: true })
  if (touch) await help.tap()
  else {
    await help.focus()
    await page.keyboard.press('Escape')
    await page.keyboard.press('Enter')
  }
  const tooltip = page.getByRole('tooltip')
  await tooltip.waitFor()
  const box = await tooltip.boundingBox()
  assert(box && box.x >= 0 && box.x + box.width <= page.viewportSize().width, 'Help is clipped horizontally')
  assert(box.y >= 0 && box.y + box.height <= page.viewportSize().height, 'Help is clipped vertically')
  await capture(page, `${id}-help`)
  if (touch) await help.tap()
  else await page.keyboard.press('Escape')
  await tooltip.waitFor({ state: 'hidden' })
}

async function keyboardFields(page, id) {
  await page.getByRole('form', { name: 'Hakopod Edge configuration', exact: true }).focus()
  const fields = []
  let reachedSubmit = false
  for (let index = 0; index < 80; index += 1) {
    await page.keyboard.press('Tab')
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    const focus = await page.evaluate(() => {
      const element = document.activeElement
      const form = element?.closest('form')
      if (!form) return { outside: true }
      if (element.matches('button[type=submit]')) return { submit: true }
      if (!element.matches('input,textarea,[role=combobox]')) return {}
      const box = element.getBoundingClientRect()
      const x = box.x + box.width / 2
      const y = box.y + box.height / 2
      const hit = document.elementFromPoint(x, y)
      const style = getComputedStyle(element)
      return {
        label: element.getAttribute('aria-label') || element.labels?.[0]?.textContent?.trim().slice(0, 100),
        tag: element.tagName,
        box: { x: box.x, y: box.y, width: box.width, height: box.height, bottom: box.bottom },
        visible: x >= 0 && x <= innerWidth && y >= 0 && y <= innerHeight && (hit === element || element.contains(hit)),
        outline: style.outlineStyle,
        outlineWidth: style.outlineWidth,
      }
    })
    assert(!focus.outside, 'Keyboard focus left the editor before its submit action')
    if (focus.submit) {
      reachedSubmit = true
      break
    }
    if (!focus.tag) continue
    fields.push(focus)
    assert(focus.visible, `Focused field is covered or outside the viewport: ${focus.label}`)
    assert(focus.outline !== 'none' && parseFloat(focus.outlineWidth) >= 1, `Focused field has no visible outline: ${focus.label}`)
    if (focus.tag === 'TEXTAREA' && fields.filter((field) => field.tag === 'TEXTAREA').length === 1) {
      await capture(page, `${id}-keyboard-focus`, { viewport: true })
    }
    if (focus.label?.startsWith('Controller settings')) await capture(page, `${id}-controller-focus`, { viewport: true })
  }
  assert(reachedSubmit, 'Keyboard review did not reach the submit action')
  assert(fields.length >= 14, 'Keyboard review did not reach the editable fields')
  return fields
}

try {
  for (const theme of ['dark', 'light']) {
    for (const viewport of viewports) {
      await runCase('settings', theme, viewport, async (page, id, size) => {
        await page.goto(`${base}/settings?tab=edge`)
        await settled(page, page.getByRole('heading', { name: 'Saved traffic configuration', exact: true }))
        const selected = page.getByRole('navigation', { name: 'Settings sections' }).getByRole('button', { name: 'Hakopod Edge', exact: true })
        assert.equal(await selected.getAttribute('aria-current'), 'page')
        await capture(page, id)
        await helpCheck(page, id, size.width < 640)
        const edit = page.getByRole('link', { name: 'Edit configuration', exact: true })
        await edit.focus()
        await page.keyboard.press('Enter')
        await settled(page, page.getByRole('form', { name: 'Hakopod Edge configuration', exact: true }))
        assert.equal(new URL(page.url()).pathname, '/settings/edge')
        return { navigation: 'keyboard', help: size.width < 640 ? 'touch toggle' : 'keyboard and Escape' }
      })
      await runCase('infrastructure-alias', theme, viewport, async (page, id) => {
        await page.goto(`${base}/infrastructure?tab=proxy`)
        await settled(page, page.getByRole('heading', { name: 'Saved traffic configuration', exact: true }))
        assert.equal(await page.getByRole('tab', { name: 'Hakopod Edge', exact: true }).getAttribute('data-state'), 'active')
        await capture(page, id)
      })
      await runCase('editor-flow', theme, viewport, async (page, id, size) => {
        await page.goto(`${base}/settings/edge`)
        await settled(page, page.getByRole('form', { name: 'Hakopod Edge configuration', exact: true }))
        await capture(page, `${id}-initial`)
        const keyboard = await keyboardFields(page, id)
        assert.equal(await page.getByRole('dialog').count(), 0, 'Editor belongs on its nested page')
        assert.deepEqual(await labels(page, 'Rule ID').evaluateAll((elements) => elements.map((element) => element.value)), ['private-api', 'public-site'])
        await page.getByRole('button', { name: 'Move rule public-site earlier', exact: true }).click()
        assert.deepEqual(await labels(page, 'Rule ID').evaluateAll((elements) => elements.map((element) => element.value)), ['public-site', 'private-api'])
        await page.getByRole('button', { name: 'Add rule', exact: true }).click()
        assert.equal(await labels(page, 'Rule ID').count(), 3)
        await page.getByRole('button', { name: 'Remove rule rule-1', exact: true }).click()
        assert.equal(await labels(page, 'Rule ID').count(), 2)
        await choose(page, 'Client IP source', 'Trusted proxy header', size.width < 640)
        await labels(page, 'Trusted proxy networks').fill('192.0.2.0/24\n2001:db8::/32')
        await choose(page, 'Client IP header', 'X-Real-IP', size.width < 640)
        await choose(page, 'Country header', 'CF-IPCountry', size.width < 640)
        await labels(page, 'Allowed country codes').first().fill('US, DE')
        await page.getByRole('spinbutton', { name: /^Requests per second/ }).first().fill('21')
        await labels(page, 'Controller settings').fill(JSON.stringify({ maxconn: '1024', 'timeout-client': '45s', 'timeout-server': '30s' }, null, 2))
        await capture(page, `${id}-trusted`)
        assert.equal(await patchCount(page), 0)
        await page.getByRole('button', { name: 'Review changes', exact: true }).click()
        await settled(page, page.getByRole('form', { name: 'Hakopod Edge review', exact: true }))
        assert.equal(await patchCount(page), 0, 'Review must not submit the change')
        assert.equal(await page.getByRole('textbox').count(), 0, 'Review must show frozen values')
        const reviewFocus = await page.getByRole('form', { name: 'Hakopod Edge review', exact: true }).evaluate((element) => ({ focused: document.activeElement === element, outline: getComputedStyle(element).outlineStyle, width: getComputedStyle(element).outlineWidth }))
        assert(reviewFocus.focused, 'Review did not receive keyboard focus')
        await capture(page, `${id}-review`)
        await capture(page, `${id}-review-viewport`, { viewport: true })
        await page.getByRole('button', { name: 'Apply configuration', exact: true }).click()
        await page.getByText('Synthetic fixture: the change could not be saved; your draft was not applied.', { exact: true }).waitFor()
        assert.equal(await patchCount(page), 1)
        await capture(page, `${id}-failed-save`)
        await page.getByRole('button', { name: 'Back to editor', exact: true }).click()
        await settled(page, page.getByRole('form', { name: 'Hakopod Edge configuration', exact: true }))
        assert.equal(await page.getByRole('spinbutton', { name: /^Requests per second/ }).first().inputValue(), '21')
        assert.equal(await labels(page, 'Allowed country codes').first().inputValue(), 'US, DE')
        assert.equal(JSON.parse(await labels(page, 'Controller settings').inputValue())['timeout-client'], '45s')
        await page.getByRole('button', { name: 'Review changes', exact: true }).click()
        await settled(page, page.getByRole('form', { name: 'Hakopod Edge review', exact: true }))
        await page.evaluate(() => window.__edgeFixture.advance())
        await page.getByRole('button', { name: 'Apply configuration', exact: true }).click()
        await page.getByRole('button', { name: 'Compare latest settings', exact: true }).waitFor()
        assert.equal(await patchCount(page), 2)
        assert(await page.getByRole('button', { name: 'Apply configuration', exact: true }).isDisabled())
        await capture(page, `${id}-conflict`)
        await page.getByRole('button', { name: 'Compare latest settings', exact: true }).click()
        await settled(page, page.getByRole('form', { name: 'Hakopod Edge configuration', exact: true }))
        assert.equal(JSON.parse(await labels(page, 'Controller settings').inputValue())['timeout-client'], '45s')
        assert.equal(JSON.parse(await labels(page, 'Controller settings').inputValue())['timeout-server'], '55s')
        assert.deepEqual(await labels(page, 'Rule ID').evaluateAll((elements) => elements.map((element) => element.value)), ['public-site', 'private-api'])
        assert.equal(await page.getByRole('spinbutton', { name: /^Requests per second/ }).first().inputValue(), '21')
        assert.equal(await patchCount(page), 2, 'Comparison must require a new review')
        await page.getByRole('button', { name: 'Review changes', exact: true }).click()
        await settled(page, page.getByRole('form', { name: 'Hakopod Edge review', exact: true }))
        await page.evaluate(() => { window.__edgeFixture.saveMode = 'hold' })
        await page.getByRole('button', { name: 'Apply configuration', exact: true }).click()
        await page.waitForFunction(() => window.__edgeFixture.patches.length === 3 && typeof window.__edgeFixture.releaseSave === 'function')
        assert(await page.getByRole('button', { name: 'Saving…', exact: true }).isDisabled())
        await page.keyboard.press('Enter')
        assert.equal(await patchCount(page), 3, 'Pending submission was duplicated')
        const submitted = await page.evaluate(() => window.__edgeFixture.patches.at(-1))
        assert.equal(submitted.expected_revision, 8)
        assert.equal(submitted.expected_resource_version, 'synthetic-rv-8')
        assert.deepEqual(submitted.settings, { 'timeout-client': '45s' })
        assert.deepEqual(submitted.edge.rules.map((rule) => rule.id), ['public-site', 'private-api'])
        await page.evaluate(() => { window.__edgeFixture.saveMode = 'success'; window.__edgeFixture.releaseSave() })
        await settled(page, page.getByRole('heading', { name: 'Saved traffic configuration', exact: true }))
        await page.getByText('The change is queued. Wait for HAProxy to report the new configuration before treating it as applied.', { exact: true }).waitFor()
        assert(await page.getByRole('button', { name: 'Edit configuration', exact: true }).isDisabled())
        const unchanged = await page.evaluate(() => window.__edgeFixture.status.observed.edge.rules[0].id)
        assert.equal(unchanged, 'private-api', 'Synthetic acceptance must not invent runtime success')
        await capture(page, `${id}-queued`)
        return { keyboard, reviewFocus, patches: 3, reorder: true, draftPreserved: true, conflictCompared: true, noRuntimeSuccess: true }
      })
    }
    for (const viewport of viewports.slice(0, 2)) {
      for (const scenario of ['readonly', 'cloud']) {
        await runCase(scenario, theme, viewport, async (page, id) => {
          await page.goto(`${base}/settings/edge?fixture=${scenario}`)
          await settled(page, page.getByRole('heading', { name: scenario === 'readonly' ? 'Administrator access required' : 'Managed by Hakopod Cloud', exact: true }))
          assert.equal(await page.getByRole('form').count(), 0)
          assert.equal(await page.getByRole('button', { name: 'Apply configuration', exact: true }).count(), 0)
          assert.equal(await page.evaluate(() => window.__edgeFixture.requests.filter((request) => request.path === '/api/settings/haproxy').length), 0, 'Denied view queried installation policy')
          await capture(page, id)
        })
      }
    }
    for (const scenario of ['empty', 'queued', 'drift', 'error', 'loading']) {
      await runCase(scenario, theme, viewports[1], async (page, id) => {
        await page.goto(`${base}/settings?tab=edge&fixture=${scenario}`)
        if (scenario === 'loading') {
          await page.waitForFunction(() => typeof window.__edgeFixture?.releaseRead === 'function')
          await page.getByRole('heading', { name: 'Hakopod Edge', exact: true }).waitFor()
        }
        const expected = scenario === 'loading'
          ? page.locator('main .hako-loading-stack')
          : scenario === 'error'
            ? page.getByText('Synthetic fixture: HAProxy observations are unavailable.', { exact: true })
            : page.getByRole('heading', { name: 'Saved traffic configuration', exact: true })
        await settled(page, expected, { loading: scenario === 'loading' })
        if (scenario === 'empty') await page.getByText('No traffic rules.', { exact: true }).waitFor()
        if (scenario === 'queued') assert(await page.getByRole('button', { name: 'Edit configuration', exact: true }).isDisabled())
        if (scenario === 'drift') await page.getByText('Observed configuration differs from the last applied change. Review the current values before editing.', { exact: true }).waitFor()
        await capture(page, id, { loading: scenario === 'loading', error: scenario === 'error' })
      })
    }
  }
} finally {
  await browser.close()
  const report = {
    fixtureOnly: true,
    reviewedCommit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: project, encoding: 'utf8' }).trim(),
    generatedAt: new Date().toISOString(),
    results,
    screenshots,
  }
  await writeFile(`${output}/results.json`, `${JSON.stringify(report, null, 2)}\n`)
  const failed = results.filter((result) => !result.passed)
  console.log(`${results.length - failed.length}/${results.length} review cases passed; ${screenshots.length} loaded screenshots captured.`)
  if (failed.length) process.exitCode = 1
}
