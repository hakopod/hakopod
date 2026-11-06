// Captures Settings → Agent policies, the policy editor and Approvals from the
// synthetic fixture in each state, theme and a desktop and phone width, and
// fails when a page scrolls sideways. Output: .local/agent-policies-review/*.png
import { mkdir } from 'node:fs/promises'

const { chromium } = await import(process.env.HAKOPOD_PLAYWRIGHT_MODULE)
const base = 'http://127.0.0.1:4207'
const out = new URL('../../../.local/agent-policies-review/', import.meta.url).pathname
await mkdir(out, { recursive: true })
const browser = await chromium.launch()
let failed = false
const editor = '/settings/agent-policies/pol-deploys'
const button = (name) => async (page) => page.getByRole('button', { name, exact: true }).first().click()
const cases = [
  // [name, path, heading that proves the screen rendered, optional step before the capture]
  ...['full', 'empty', 'unlicensed', 'error'].map((f) => [`list-${f}`, `/settings?tab=agent-policies&fixture=${f}`, 'Agent policies']),
  ['editor', editor, 'Production deploys need approval'],
  ['editor-new', '/settings/agent-policies/new', 'New agent policy'],
  ['editor-publish', editor, 'Publish v8', button('Publish v8')],
  ['editor-simulation', editor, 'Run simulation', button('Run simulation')],
  ...['full', 'empty', 'unlicensed', 'error'].map((f) => [`approvals-${f}`, `/approvals?fixture=${f}`, 'Approvals']),
  ['approvals-decided', '/approvals', 'Approvals', async (page) => page.getByRole('tab', { name: 'Decided' }).click()],
  ['approvals-inspect', '/approvals', 'deploy-agent', async (page) => page.getByRole('button', { name: /deploy-agent/ }).first().click()],
  ['approvals-own-key', '/approvals', 'review-owner-ci', async (page) => page.getByRole('button', { name: /review-owner-ci/ }).first().click()],
]
for (const theme of ['dark', 'light'])
  for (const [width, height] of [[1440, 900], [375, 812]])
    for (const [fixture, path, heading, step] of cases) {
      const name = `${fixture}-${theme}-${width}`
      const page = await browser.newPage({ viewport: { width, height } })
      await page.addInitScript((value) => localStorage.setItem('hakopod-theme', value), theme)
      try {
        await page.goto(base + path)
        if (step) {
          await page.getByRole('heading').first().waitFor({ timeout: 20000 })
          await page.waitForTimeout(500)
          await step(page)
        }
        await page.getByRole('heading', { name: heading, exact: true }).last().waitFor({ timeout: 20000 })
        await page.waitForTimeout(500)
        const overflow = await page.evaluate(() => document.scrollingElement.scrollWidth - innerWidth)
        await page.screenshot({ path: `${out}${name}.png`, fullPage: true })
        if (overflow > 0) throw new Error(`page scrolls sideways by ${overflow}px`)
        console.log(`PASS ${name}`)
      } catch (error) {
        failed = true
        await page.screenshot({ path: `${out}${name}-failed.png`, fullPage: true }).catch(() => {})
        console.log(`FAIL ${name}: ${error.message.split('\n')[0]}`)
      }
      await page.close()
    }
await browser.close()
process.exit(failed ? 1 : 0)
