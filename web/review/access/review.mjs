// Captures Settings → Network access from the synthetic fixture in each state,
// theme and a desktop and phone width. Output: .local/access-review/*.png
import { mkdir } from 'node:fs/promises'

const { chromium } = await import(process.env.HAKOPOD_PLAYWRIGHT_MODULE)
const base = 'http://127.0.0.1:4199'
const out = new URL('../../../.local/access-review/', import.meta.url).pathname
await mkdir(out, { recursive: true })
const browser = await chromium.launch()
let failed = false
for (const theme of ['dark', 'light'])
  for (const [width, height] of [[1440, 1000], [390, 900]])
    for (const fixture of ['off', 'pending', 'restricted', 'unlicensed', 'vpn', 'server']) {
      const name = `${fixture}-${theme}-${width}`
      const page = await browser.newPage({ viewport: { width, height } })
      await page.addInitScript((value) => localStorage.setItem('hakopod-theme', value), theme)
      try {
        await page.goto(`${base}/settings?tab=access&fixture=${fixture}`)
        await page.getByRole('heading', { name: 'Network access', exact: true }).waitFor({ timeout: 20000 })
        await page.waitForTimeout(500)
        await page.screenshot({ path: `${out}${name}.png`, fullPage: true })
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
