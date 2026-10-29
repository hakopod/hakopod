// Captures Settings → Network access from the synthetic fixture in each state,
// theme and a desktop and phone width. Output: .local/access-review/*.png
import { mkdir } from 'node:fs/promises'

const { chromium } = await import(process.env.HAKOPOD_PLAYWRIGHT_MODULE)
const base = 'http://127.0.0.1:4199'
const out = new URL('../../../.local/access-review/', import.meta.url).pathname
await mkdir(out, { recursive: true })
const browser = await chromium.launch()
let failed = false
const cases = [
  ...['off', 'pending', 'restricted', 'unlicensed'].map((fixture) => [fixture, `/settings?tab=access&fixture=${fixture}`, 'Network access']),
  ['vpn-agentless', '/settings?tab=vpn&fixture=agentless', 'Hakopod VPN server'],
  ['vpn-empty', '/settings?tab=vpn&fixture=restricted', 'Hakopod VPN server'],
  ['vpn-full', '/settings?tab=vpn&fixture=full', 'Devices'],
  ['vpn-wireguard', '/settings?tab=vpn&fixture=vpn', 'VPN connector'],
  ['vpn-server-editor', '/settings/vpn/server?fixture=full', 'Hakopod VPN server'],
  ['vpn-connector-editor', '/settings/vpn/connector?fixture=full', 'VPN connector'],
  ['vpn-device-new', '/settings/vpn/devices/new?fixture=full', 'Add a VPN device'],
]
for (const theme of ['dark', 'light'])
  for (const [width, height] of [[1440, 1000], [390, 900]])
    for (const [fixture, path, heading] of cases) {
      const name = `${fixture}-${theme}-${width}`
      const page = await browser.newPage({ viewport: { width, height } })
      await page.addInitScript((value) => localStorage.setItem('hakopod-theme', value), theme)
      try {
        await page.goto(base + path)
        await page.getByRole('heading', { name: heading, exact: true }).first().waitFor({ timeout: 20000 })
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
