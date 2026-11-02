// Captures the runner pool scale-to-zero controls and the sleeping pool card
// from the synthetic fixture in both themes at desktop, phone and narrow phone
// widths. Output: .local/runner-scaling-review/*.png and results.json
import { mkdir, writeFile } from 'node:fs/promises'

const { chromium } = await import(process.env.HAKOPOD_PLAYWRIGHT_MODULE)
const base = 'http://127.0.0.1:4207'
const out = new URL('../../../.local/runner-scaling-review/', import.meta.url).pathname
await mkdir(out, { recursive: true })
const browser = await chromium.launch()
const results = []
let failed = false

async function measure(page, name) {
  return page.evaluate((name) => {
    const box = (element) => {
      if (!element) return null
      const rect = element.getBoundingClientRect()
      return { x: Math.round(rect.x), width: Math.round(rect.width), height: Math.round(rect.height) }
    }
    const field = (label) => [...document.querySelectorAll('label')].find((item) => item.textContent?.trim() === label)
    const input = (label) => {
      const id = field(label)?.htmlFor
      return id ? document.getElementById(id) : null
    }
    return {
      name,
      overflow: document.documentElement.scrollWidth - window.innerWidth,
      h1: document.querySelectorAll('h1').length,
      idleInput: box(input('Idle minutes')),
      warmInput: box(input('Runners kept online')),
      scaleChoice: box([...document.querySelectorAll('.runner-choice')].find((item) => item.textContent?.includes('Scale to zero'))),
      unknownRequests: window.__runnerFixture?.unknown || [],
    }
  }, name)
}

async function capture(page, name) {
  results.push(await measure(page, name))
  await page.screenshot({ path: `${out}${name}.png`, fullPage: true })
}

async function openCapacity(page, fixture) {
  await page.goto(`${base}/applications/synthetic-runners/configure?service=runner&fixture=${fixture}`)
  await page.getByRole('heading', { name: 'Configure runner pool', exact: true }).waitFor({ timeout: 20000 })
  await page.getByRole('button', { name: 'Choose image' }).click()
  await page.getByRole('button', { name: 'Set capacity' }).click()
  await page.getByText('When no jobs are waiting').waitFor()
}

const cases = {
  async fixed(page, name) {
    await openCapacity(page, 'fixed')
    await capture(page, `${name}-capacity-fixed`)
    // Keyboard selection: focus the fixed choice, then move to Scale to zero.
    await page.getByRole('radio', { name: 'Keep runners online' }).focus()
    await page.keyboard.press('ArrowRight')
    await page.getByLabel('Idle minutes').waitFor()
    await page.getByText('When no jobs are waiting').scrollIntoViewIfNeeded()
    await capture(page, `${name}-capacity-enabled-keyboard`)
    await page.getByRole('button', { name: 'About Idle minutes' }).click()
    await page.waitForTimeout(300)
    await capture(page, `${name}-idle-help`)
    await page.keyboard.press('Escape')
  },
  async invalid(page, name) {
    await openCapacity(page, 'scaled')
    await page.getByLabel('Runners kept online').fill('9')
    await page.getByRole('button', { name: 'Set cache policy' }).click()
    await page.waitForTimeout(300)
    await capture(page, `${name}-capacity-invalid`)
  },
  async review(page, name) {
    await openCapacity(page, 'scaled')
    await page.getByLabel('Idle minutes').fill('15')
    await page.getByRole('button', { name: 'Set cache policy' }).click()
    await page.getByRole('button', { name: 'Review runner pool' }).click()
    await page.getByText('What will change').waitFor({ timeout: 20000 })
    await capture(page, `${name}-review`)
  },
  async sleeping(page, name) {
    await page.goto(`${base}/status?fixture=sleeping`)
    await page.getByText('Sleeping', { exact: true }).waitFor({ timeout: 20000 })
    await capture(page, `${name}-pool-sleeping`)
  },
}

for (const theme of ['dark', 'light'])
  for (const [width, height] of [[1440, 1000], [390, 900], [320, 760]])
    for (const [key, run] of Object.entries(cases)) {
      const name = `${theme}-${width}-${key}`
      const page = await browser.newPage({ viewport: { width, height }, hasTouch: width < 640 })
      await page.addInitScript((value) => localStorage.setItem('hakopod-theme', value), theme)
      try {
        await run(page, name)
        console.log(`PASS ${name}`)
      } catch (error) {
        failed = true
        results.push({ name, error: error.message.split('\n')[0] })
        await page.screenshot({ path: `${out}${name}-failed.png`, fullPage: true }).catch(() => {})
        console.log(`FAIL ${name}: ${error.message.split('\n')[0]}`)
      }
      await page.close()
    }
await writeFile(`${out}results.json`, JSON.stringify(results, null, 2))
for (const result of results)
  if (result.overflow > 0) {
    failed = true
    console.log(`FAIL ${result.name}: document overflows by ${result.overflow}px`)
  }
await browser.close()
process.exit(failed ? 1 : 0)
