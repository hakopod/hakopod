// Captures the self-hosted sign-in screen from web/scripts/auth-ui-review.html
// in both themes, empty, filled and with the submit hovered.
// Output: .local/auth-review/*.png
import { mkdir } from 'node:fs/promises'

const { chromium } = await import(process.env.HAKOPOD_PLAYWRIGHT_MODULE)
const base = 'http://127.0.0.1:4197/scripts/auth-ui-review.html'
const out = new URL('../../../.local/auth-review/', import.meta.url).pathname
await mkdir(out, { recursive: true })
const browser = await chromium.launch()
let failed = false
for (const theme of ['paper', 'dark'])
  for (const [width, height] of [[1440, 1000], [390, 900]]) {
    const name = `login-${theme}-${width}`
    const page = await browser.newPage({ viewport: { width, height } })
    try {
      await page.goto(`${base}?view=login&theme=${theme}`)
      const submit = page.getByRole('button', { name: 'Sign in', exact: true })
      await submit.waitFor({ timeout: 20000 })
      await page.waitForTimeout(500)
      await page.screenshot({ path: `${out}${name}-empty.png`, fullPage: true })
      await page.getByLabel('Email address').fill('harsh@example.com')
      await page.getByLabel('Password', { exact: true }).fill('correct horse battery staple')
      await submit.hover()
      await page.waitForTimeout(300)
      await page.screenshot({ path: `${out}${name}-filled-hover.png`, fullPage: true })
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
