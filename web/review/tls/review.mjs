import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath, pathToFileURL } from 'node:url'

const project = fileURLToPath(new URL('../../../', import.meta.url))
const output = `${project}/.local/tls-review`
const base = 'http://127.0.0.1:4199'
const applicationID = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
const deploymentID = 'dddddddddddddddddddddddddddddddd'
const appURL = `${base}/applications/${applicationID}`
const serviceURL = `${appURL}?service=api&tab=network`
const modulePath = process.env.HAKOPOD_PLAYWRIGHT_MODULE
assert(modulePath, 'Set HAKOPOD_PLAYWRIGHT_MODULE to the installed Playwright index.mjs')
const { chromium } = await import(pathToFileURL(modulePath).href)
await mkdir(`${output}/screenshots`, { recursive: true })
await mkdir(`${output}/diagnostics`, { recursive: true })
const browser = await chromium.launch({ headless: true, args: ['--disable-dev-shm-usage'] })
const results = []
const screenshots = []
const sizes = [{ width: 1484, height: 1044 }, { width: 390, height: 844 }, { width: 320, height: 844 }]
const defaultLabel = 'shared-acme · Default issuer · ready'
const applicationLabel = 'shared-acme · Application issuer · ready'
const newLabel = 'review-issuer · Application issuer · not ready'
const issuerFailure = 'Synthetic fixture: the issuer could not be created. Your draft was not applied.'
const tlsFailure = 'Synthetic fixture: the TLS change could not be submitted. Your draft was not applied.'
const pem = 'SYNTHETIC REVIEW CERTIFICATE BYTES; NOT A CERTIFICATE'
const key = 'SYNTHETIC REVIEW KEY BYTES; NOT A PRIVATE KEY'
const certificateName = 'synthetic-review-certificate-chain.pem'
const keyName = 'synthetic-review-private-key.pem'

async function settled(page, expected, options = {}) {
  await page.waitForFunction(() => window.__tlsFixture?.ready === true)
  await page.locator('main').waitFor()
  await expected.waitFor({ state: 'visible' })
  if (!options.loading) await page.locator('.hako-loading-stack').first().waitFor({ state: 'hidden' })
  await page.evaluate(() => document.fonts.ready)
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
}

async function capture(page, id, options = {}) {
  const viewport = page.viewportSize()
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
    const style = getComputedStyle(main)
    const dialogs = [...document.querySelectorAll('[role=dialog]')].filter(visible)
    const surface = dialogs.at(-1) || main
    const controls = [...surface.querySelectorAll('input:not([type=hidden]),textarea,button,[role=combobox]')].filter(visible).map((element) => {
      const tabList = element.closest('.tab-list')
      const scrollableInactiveTab = element.matches('[role=tab][data-state=inactive]') && tabList &&
        ['auto', 'scroll'].includes(getComputedStyle(tabList).overflowX) && tabList.scrollWidth > tabList.clientWidth
      return {
        label: element.getAttribute('aria-label') || element.labels?.[0]?.textContent?.trim() || element.textContent?.trim(),
        box: rect(element),
        scrollableInactiveTab: Boolean(scrollableInactiveTab),
      }
    })
    const active = [...document.querySelectorAll('.tab-list [data-state=active]')].filter(visible).map((element) => ({ label: element.textContent, box: rect(element), color: getComputedStyle(element).color, background: getComputedStyle(element).backgroundColor }))
    return {
      width: innerWidth,
      height: innerHeight,
      documentWidth: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
      main: { box: rect(main), padding: [style.paddingLeft, style.paddingRight] },
      h1: [...main.querySelectorAll('h1')].map((element) => element.textContent),
      dialogs: dialogs.map((element) => ({ box: rect(element), title: element.getAttribute('aria-labelledby') })),
      controls,
      active,
      tabLists: [...main.querySelectorAll('.tab-list')].filter(visible).map(rect),
      visibleBrackets: [...document.querySelectorAll('[class*=bracket]')].filter(visible).map((element) => element.className),
      loading: [...document.querySelectorAll('.hako-loading-stack')].filter(visible).length,
      fixture: window.__tlsFixture.synthetic,
      blocked: [...window.__tlsFixture.blocked],
    }
  })
  data.requestedViewport = viewport
  assert(data.fixture, 'Review must use explicit synthetic data')
  assert.deepEqual(data.blocked, [], 'Unexpected API or external request')
  assert(data.documentWidth <= viewport.width + 1, `Document exceeds the requested viewport by ${data.documentWidth - viewport.width}px`)
  assert(Math.abs(data.width - viewport.width) <= 1, `Layout viewport changed from ${viewport.width}px to ${data.width}px`)
  assert.deepEqual(data.main.padding, [viewport.width < 640 ? '16px' : '24px', viewport.width < 640 ? '16px' : '24px'])
  assert.equal(data.h1.length, 1, 'Expected one page heading behind the dialog')
  assert(data.dialogs.length <= 1, 'Issuer creation must not stack two modal dialogs')
  for (const dialog of data.dialogs) {
    assert(dialog.box.x >= -1 && dialog.box.right <= viewport.width + 1, 'Dialog is clipped horizontally')
    assert(dialog.box.y >= -1 && dialog.box.bottom <= viewport.height + 1, 'Dialog is clipped vertically')
  }
  // Inactive tabs may sit outside an intentionally scrollable strip. The strip
  // and selected tab must remain visible; ordinary controls have no exemption.
  for (const control of data.controls) {
    if (!control.scrollableInactiveTab) assert(control.box.x >= -1 && control.box.right <= viewport.width + 1, `Control is clipped horizontally: ${control.label}`)
  }
  for (const tabList of data.tabLists) assert(tabList.x >= -1 && tabList.right <= viewport.width + 1, 'Tab strip is clipped horizontally')
  for (const active of data.active) {
    assert(active.box.x >= -1 && active.box.right <= viewport.width + 1, `Active tab is outside the viewport: ${active.label}`)
    assert.equal(active.background, 'rgba(0, 0, 0, 0)', 'Active tab has a selected background')
  }
  assert.equal(data.visibleBrackets.length, 0, 'Self-hosted review contains decorative corner brackets')
  if (!options.loading) assert.equal(data.loading, 0, 'Unexpected loading placeholder')
  const screenshot = `screenshots/${id}.png`
  await page.screenshot({ path: `${output}/${screenshot}`, fullPage: !data.dialogs.length && !options.viewport })
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
    assert.deepEqual(networkBlocks, [], 'Page attempted an external network request')
    assert.deepEqual(await page.evaluate(() => window.__tlsFixture.blocked), [], 'API request was outside the fixture contract')
    results.push({ id, name, theme, viewport, passed: true, details })
    console.log(`PASS ${id}`)
  } catch (error) {
    const diagnostic = `diagnostics/${id}.png`
    await page.screenshot({ path: `${output}/${diagnostic}`, fullPage: false }).catch(() => {})
    results.push({ id, name, theme, viewport, passed: false, error: error.stack || String(error), errors, networkBlocks, diagnostic })
    console.log(`FAIL ${id}: ${error.message}`)
  } finally {
    await context.close()
  }
}

async function choose(page, label, option, touch, screenshotID) {
  const trigger = page.getByRole('combobox', { name: label, exact: true })
  if (touch) await trigger.tap()
  else {
    await trigger.focus()
    await page.keyboard.press('Enter')
  }
  const item = page.getByRole('option', { name: option, exact: true })
  await item.waitFor()
  const box = await item.boundingBox()
  assert(box && box.x >= 0 && box.x + box.width <= page.viewportSize().width, 'Select option is clipped horizontally')
  if (screenshotID) await capture(page, screenshotID)
  if (touch) await item.tap()
  else await item.click()
  await page.getByRole('listbox').waitFor({ state: 'hidden' })
  assert((await trigger.textContent()).includes(option), `Selection did not keep ${option}`)
}

async function openService(page, scenario = 'ready') {
  await page.goto(`${serviceURL}&fixture=${scenario}`)
  await settled(page, page.getByRole('heading', { name: 'TLS certificate', exact: true }))
}

async function openTLS(page) {
  const button = page.getByRole('button', { name: /^(Configure TLS|Replace certificate)$/ })
  await button.focus()
  await page.keyboard.press('Enter')
  await settled(page, page.getByRole('dialog', { name: 'Configure TLS for api', exact: true }))
}

async function managed(page, touch = false) {
  const method = page.getByRole('combobox', { name: 'Certificate method', exact: true })
  if (!(await method.textContent()).includes('Use a managed ACME issuer')) await choose(page, 'Certificate method', 'Use a managed ACME issuer', touch)
  await page.getByRole('combobox', { name: 'Issuer', exact: true }).waitFor()
}

async function uploadFiles(page) {
  await page.getByLabel(/^Certificate chain/).setInputFiles({ name: certificateName, mimeType: 'application/x-pem-file', buffer: Buffer.from(pem) })
  await page.getByLabel(/^Private key/).setInputFiles({ name: keyName, mimeType: 'application/x-pem-file', buffer: Buffer.from(key) })
  await page.getByText(`Selected: ${keyName}`, { exact: true }).waitFor()
}

async function fieldFocus(page, label) {
  const viewport = page.viewportSize()
  const field = page.getByRole('textbox', { name: label, exact: true })
  await field.focus()
  await page.keyboard.press('Tab')
  await page.keyboard.press('Shift+Tab')
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  const state = await field.evaluate((element) => {
    const box = element.getBoundingClientRect()
    const style = getComputedStyle(element)
    const hit = document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2)
    return { focused: document.activeElement === element, x: box.x, y: box.y, right: box.right, bottom: box.bottom, width: innerWidth, height: innerHeight, hit: hit === element || element.contains(hit), outline: style.outlineStyle, outlineWidth: style.outlineWidth }
  })
  assert(state.focused && state.hit, `Keyboard focus is covered or lost: ${label}`)
  assert(Math.abs(state.width - viewport.width) <= 1, 'Layout viewport changed during keyboard review')
  assert(state.x >= 0 && state.right <= viewport.width && state.y >= 0 && state.bottom <= viewport.height, `Focused field is outside the viewport: ${label}`)
  assert(state.outline !== 'none' && parseFloat(state.outlineWidth) >= 1, `Focused field has no outline: ${label}`)
  return state
}

async function prepareIssuer(page, name = 'review-issuer') {
  await page.getByRole('button', { name: 'Create application issuer', exact: true }).click()
  await settled(page, page.getByRole('dialog', { name: 'Create application issuer', exact: true }))
  await page.getByRole('textbox', { name: 'Issuer name', exact: true }).fill(name)
  await page.getByRole('textbox', { name: 'ACME contact email', exact: true }).fill('tls-review@example.invalid')
}

async function acceptTLS(page) {
  await page.evaluate(() => { window.__tlsFixture.tlsMode = 'success' })
  await page.getByRole('button', { name: 'Review TLS change', exact: true }).click()
  await settled(page, page.getByRole('dialog', { name: 'Review TLS deployment', exact: true }))
  await page.getByRole('button', { name: 'Deploy TLS change', exact: true }).click()
  await page.getByRole('heading', { name: 'Synthetic deployment queued', exact: true }).waitFor()
  assert.equal(new URL(page.url()).pathname, `/deployments/${deploymentID}`)
  return page.evaluate(() => window.__tlsFixture.submissions.at(-1))
}

try {
  for (const theme of ['dark', 'light']) {
    for (const viewport of sizes) {
      await runCase('service-create-flow', theme, viewport, async (page, id, size) => {
        await openService(page)
        await capture(page, `${id}-networking`)
        await openTLS(page)
        await managed(page, size.width < 640)
        assert((await page.getByRole('combobox', { name: 'Issuer', exact: true }).textContent()).includes(defaultLabel), 'Cloud did not select the configured default')
        await capture(page, `${id}-default`)
        await choose(page, 'Issuer', applicationLabel, size.width < 640, `${id}-issuer-options`)
        await choose(page, 'Certificate method', 'Upload PEM certificate and private key', size.width < 640)
        await uploadFiles(page)
        await choose(page, 'Certificate method', 'Use a managed ACME issuer', size.width < 640)
        assert((await page.getByRole('combobox', { name: 'Issuer', exact: true }).textContent()).includes(applicationLabel))
        await prepareIssuer(page)
        const focus = await fieldFocus(page, 'ACME contact email')
        await capture(page, `${id}-create`)
        await page.getByRole('button', { name: 'Review issuer', exact: true }).click()
        await settled(page, page.getByRole('dialog', { name: 'Review certificate issuer', exact: true }))
        assert.equal(await page.evaluate(() => window.__tlsFixture.creates.length), 0, 'Review submitted an issuer')
        await capture(page, `${id}-issuer-review`)
        await page.getByRole('button', { name: 'Create reviewed issuer', exact: true }).click()
        await page.getByText(issuerFailure, { exact: true }).waitFor()
        assert.equal(await page.evaluate(() => window.__tlsFixture.creates.length), 1)
        await capture(page, `${id}-create-failed`)
        await page.getByRole('button', { name: 'Back', exact: true }).click()
        assert.equal(await page.getByRole('textbox', { name: 'Issuer name', exact: true }).inputValue(), 'review-issuer')
        assert.equal(await page.getByRole('textbox', { name: 'ACME contact email', exact: true }).inputValue(), 'tls-review@example.invalid')
        await page.getByRole('button', { name: 'Review issuer', exact: true }).click()
        await page.evaluate(() => { window.__tlsFixture.createMode = 'hold' })
        await page.getByRole('button', { name: 'Create reviewed issuer', exact: true }).click()
        await page.waitForFunction(() => window.__tlsFixture.creates.length === 2 && typeof window.__tlsFixture.releaseCreate === 'function')
        assert(await page.getByRole('button', { name: 'Creating…', exact: true }).isDisabled())
        await page.keyboard.press('Enter')
        assert.equal(await page.evaluate(() => window.__tlsFixture.creates.length), 2, 'Pending issuer create was duplicated')
        await page.evaluate(() => { window.__tlsFixture.createMode = 'success'; window.__tlsFixture.releaseCreate() })
        await settled(page, page.getByRole('dialog', { name: 'Configure TLS for api', exact: true }))
        assert((await page.getByRole('combobox', { name: 'Issuer', exact: true }).textContent()).includes(newLabel))
        await page.getByText('review-issuer is not ready. Certificates cannot be issued until the issuer becomes ready.', { exact: true }).waitFor()
        await capture(page, `${id}-created-pending`)
        await choose(page, 'Certificate method', 'Upload PEM certificate and private key', size.width < 640)
        await page.getByText(`Selected: ${certificateName}`, { exact: true }).waitFor()
        await page.getByText(`Selected: ${keyName}`, { exact: true }).waitFor()
        await choose(page, 'Certificate method', 'Use a managed ACME issuer', size.width < 640)
        assert((await page.getByRole('combobox', { name: 'Issuer', exact: true }).textContent()).includes(newLabel))
        await page.getByRole('button', { name: 'Review TLS change', exact: true }).click()
        await settled(page, page.getByRole('dialog', { name: 'Review TLS deployment', exact: true }))
        await page.getByText('Application issuer', { exact: true }).waitFor()
        await capture(page, `${id}-tls-review`)
        await page.getByRole('button', { name: 'Deploy TLS change', exact: true }).click()
        await page.getByText(tlsFailure, { exact: true }).waitFor()
        await capture(page, `${id}-tls-failed`)
        await page.getByRole('button', { name: 'Back to configuration', exact: true }).click()
        assert((await page.getByRole('combobox', { name: 'Issuer', exact: true }).textContent()).includes(newLabel))
        await page.getByRole('button', { name: 'Review TLS change', exact: true }).click()
        await page.evaluate(() => { window.__tlsFixture.tlsMode = 'hold' })
        await page.getByRole('button', { name: 'Deploy TLS change', exact: true }).click()
        await page.waitForFunction(() => window.__tlsFixture.submissions.length === 2 && typeof window.__tlsFixture.releaseTLS === 'function')
        assert(await page.getByRole('button', { name: 'Submitting…', exact: true }).isDisabled())
        await page.keyboard.press('Enter')
        assert.equal(await page.evaluate(() => window.__tlsFixture.submissions.length), 2)
        await page.evaluate(() => { window.__tlsFixture.tlsMode = 'success'; window.__tlsFixture.releaseTLS() })
        await page.getByRole('heading', { name: 'Synthetic deployment queued', exact: true }).waitFor()
        const record = await page.evaluate(() => ({ create: window.__tlsFixture.creates.at(-1), submission: window.__tlsFixture.submissions.at(-1), ready: window.__tlsFixture.tls.ready, requests: window.__tlsFixture.requests }))
        assert.deepEqual(record.create.body, { name: 'review-issuer', email: 'tls-review@example.invalid', production: false })
        assert.equal(record.create.path, `/api/applications/${applicationID}/tls/issuers`)
        assert.deepEqual(record.submission.body, { expected_revision: 7, issuer: 'review-issuer', issuer_kind: 'Issuer' })
        assert(record.submission.key, 'TLS submission omitted idempotency key')
        assert.equal(record.ready, false, 'Fixture invented certificate readiness')
        assert(!record.requests.some((request) => request.path === '/api/tls/issuers'), 'Service used installation issuer endpoint')
        return { focus, draftPreserved: true, createdIssuerSelected: true, uploadRetained: true, noRuntimeSuccess: true }
      })
      await runCase('default-attach', theme, viewport, async (page, id) => {
        await openService(page)
        await openTLS(page)
        await managed(page)
        const record = await acceptTLS(page)
        assert.deepEqual(record.body, { expected_revision: 7, issuer: 'shared-acme', issuer_kind: 'ClusterIssuer' })
        return { defaultKind: 'ClusterIssuer', queuedOnly: true }
      })
      await runCase('upload-switching', theme, viewport, async (page, id, size) => {
        await openService(page)
        await openTLS(page)
        await choose(page, 'Certificate method', 'Upload PEM certificate and private key', size.width < 640)
        await uploadFiles(page)
        await choose(page, 'Certificate method', 'Use a managed ACME issuer', size.width < 640)
        await choose(page, 'Issuer', applicationLabel, size.width < 640)
        await choose(page, 'Certificate method', 'Upload PEM certificate and private key', size.width < 640)
        await page.getByText(`Selected: ${keyName}`, { exact: true }).waitFor()
        await capture(page, `${id}-files`)
        await page.getByRole('button', { name: 'Review TLS change', exact: true }).click()
        await settled(page, page.getByRole('dialog', { name: 'Review TLS deployment', exact: true }))
        assert(!(await page.getByRole('dialog').innerText()).includes(key), 'Private key bytes appeared in review')
        await capture(page, `${id}-review`)
        await page.getByRole('button', { name: 'Deploy TLS change', exact: true }).click()
        await page.getByText(tlsFailure, { exact: true }).waitFor()
        await page.getByRole('button', { name: 'Back to configuration', exact: true }).click()
        await page.getByText(`Selected: ${certificateName}`, { exact: true }).waitFor()
        const record = await acceptTLS(page)
        assert.deepEqual(record.body, { expected_revision: 7, certificate_pem: pem, private_key_pem: key })
        return { source: 'upload', filesPreserved: true, noPrivateBytesInReview: true }
      })
      await runCase('application-networking', theme, viewport, async (page, id, size) => {
        await page.goto(`${appURL}?tab=networking`)
        await settled(page, page.getByRole('heading', { name: 'Certificate issuers', exact: true }))
        await page.getByText('Application issuer', { exact: true }).waitFor()
        await page.getByText('Default issuer', { exact: true }).waitFor()
        await capture(page, id)
        const details = page.locator('summary').filter({ hasText: 'Issuer details' }).last()
        if (size.width < 640) await details.tap()
        else { await details.focus(); await page.keyboard.press('Enter') }
        await page.getByText('developer@example.invalid', { exact: true }).waitFor()
        await capture(page, `${id}-details`)
        await capture(page, `${id}-details-viewport`, { viewport: true })
        await prepareIssuer(page, 'application-page-issuer')
        await page.getByRole('checkbox', { name: 'Use production certificate issuance', exact: true }).check()
        await page.getByRole('button', { name: 'Review issuer', exact: true }).click()
        await page.evaluate(() => { window.__tlsFixture.createMode = 'success' })
        await page.getByRole('button', { name: 'Create reviewed issuer', exact: true }).click()
        await page.getByRole('dialog').waitFor({ state: 'hidden' })
        await page.getByText('application-page-issuer', { exact: true }).waitFor()
        await capture(page, `${id}-created`)
        const create = await page.evaluate(() => window.__tlsFixture.creates.at(-1))
        assert.equal(create.path, `/api/applications/${applicationID}/tls/issuers`)
        assert.equal(create.body.production, true)
      })
      await runCase('cloud-installation-list', theme, viewport, async (page, id) => {
        await page.goto(`${base}/infrastructure?tab=tls`)
        await settled(page, page.getByRole('heading', { name: 'Certificate issuers', exact: true }))
        await page.getByText('Default issuer', { exact: true }).waitFor()
        assert.equal(await page.getByText('Application issuer', { exact: true }).count(), 0)
        assert.equal(await page.getByRole('button', { name: 'Create issuer', exact: true }).count(), 0)
        await capture(page, id)
      })
    }
    for (const scenario of ['loading', 'missing-default', 'no-cert-manager', 'forbidden', 'existing-issuer', 'existing-upload']) {
      await runCase(scenario, theme, sizes[1], async (page, id) => {
        await openService(page, scenario)
        if (scenario === 'loading') {
          await page.getByRole('button', { name: 'Configure TLS', exact: true }).click()
          await page.waitForFunction(() => typeof window.__tlsFixture.releaseIssuers === 'function')
          await settled(page, page.getByRole('dialog', { name: 'Configure TLS for api', exact: true }), { loading: true })
          assert(await page.getByRole('button', { name: 'Review TLS change', exact: true }).isDisabled())
          await capture(page, id, { loading: true })
          return
        }
        await openTLS(page)
        if (scenario === 'existing-upload') {
          assert((await page.getByRole('combobox', { name: 'Certificate method', exact: true }).textContent()).includes('Upload PEM'))
        } else {
          await managed(page, true)
          const issuer = page.getByRole('combobox', { name: 'Issuer', exact: true })
          if (scenario === 'existing-issuer') assert((await issuer.textContent()).includes(applicationLabel))
          if (scenario === 'missing-default') {
            assert((await issuer.textContent()).includes('Choose an issuer'))
            assert(await page.getByRole('button', { name: 'Review TLS change', exact: true }).isDisabled())
            await page.getByText('Synthetic fixture: the installation default issuer is unavailable. Use an application issuer or upload a certificate.', { exact: true }).waitFor()
          }
          if (['no-cert-manager', 'forbidden'].includes(scenario)) {
            assert(await issuer.isDisabled())
            assert(await page.getByRole('button', { name: 'Create application issuer', exact: true }).isDisabled())
            assert(await page.getByRole('button', { name: 'Review TLS change', exact: true }).isDisabled())
          }
        }
        await capture(page, id)
      })
    }
    await runCase('readonly', theme, sizes[1], async (page, id) => {
      await openService(page, 'readonly')
      assert.equal(await page.getByRole('button', { name: /^(Configure TLS|Replace certificate)$/ }).count(), 0)
      await capture(page, `${id}-service`)
      await page.goto(`${appURL}?tab=networking&fixture=readonly`)
      await settled(page, page.getByRole('heading', { name: 'Certificate issuers', exact: true }))
      assert.equal(await page.getByRole('button', { name: 'Create application issuer', exact: true }).count(), 0)
      await capture(page, `${id}-application`)
    })
    await runCase('create-forbidden', theme, sizes[1], async (page, id) => {
      await openService(page)
      await openTLS(page)
      await prepareIssuer(page)
      await page.getByRole('button', { name: 'Review issuer', exact: true }).click()
      await page.evaluate(() => { window.__tlsFixture.createMode = 'forbidden' })
      await page.getByRole('button', { name: 'Create reviewed issuer', exact: true }).click()
      await page.getByText('Synthetic fixture: creating this certificate issuer was denied.', { exact: true }).waitFor()
      await capture(page, id)
      await page.getByRole('button', { name: 'Back', exact: true }).click()
      assert.equal(await page.getByRole('textbox', { name: 'Issuer name', exact: true }).inputValue(), 'review-issuer')
    })
    await runCase('operator-create', theme, sizes[0], async (page, id) => {
      await page.goto(`${base}/infrastructure?tab=tls&fixture=operator`)
      await settled(page, page.getByRole('heading', { name: 'Certificate issuers', exact: true }))
      await page.getByRole('button', { name: 'Create issuer', exact: true }).click()
      await settled(page, page.getByRole('dialog', { name: 'Create certificate issuer', exact: true }))
      await page.getByRole('textbox', { name: 'Issuer name', exact: true }).fill('operator-staging')
      await page.getByRole('textbox', { name: 'ACME contact email', exact: true }).fill('operator@example.invalid')
      await page.getByRole('button', { name: 'Review issuer', exact: true }).click()
      await capture(page, `${id}-review`)
      await page.evaluate(() => { window.__tlsFixture.createMode = 'success' })
      await page.getByRole('button', { name: 'Create reviewed issuer', exact: true }).click()
      await page.getByRole('dialog').waitFor({ state: 'hidden' })
      await page.getByText('operator-staging', { exact: true }).waitFor()
      const created = await page.evaluate(() => window.__tlsFixture.creates.at(-1))
      assert.equal(created.path, '/api/tls/issuers')
      await capture(page, id)
    })
  }
} finally {
  await browser.close()
  await writeFile(`${output}/results.json`, `${JSON.stringify({ fixtureOnly: true, reviewedCommit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: project, encoding: 'utf8' }).trim(), generatedAt: new Date().toISOString(), results, screenshots }, null, 2)}\n`)
  const failed = results.filter((result) => !result.passed)
  console.log(`${results.length - failed.length}/${results.length} TLS review cases passed; ${screenshots.length} screenshots captured.`)
  if (failed.length) process.exitCode = 1
}
