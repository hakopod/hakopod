import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import type { SecurityOverview } from '../lib/security'
import { SecurityBannerView } from './security-banner'

export function routed(node: React.ReactNode) {
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return renderToStaticMarkup(<RouterContextProvider router={router}>{node}</RouterContextProvider>)
}

export function overviewOf(affected: number, critical: number): SecurityOverview {
  return {
    feed: {
      feed_enabled: true,
      osv_enabled: true,
      revision: 3,
      issued_at: null,
      expires_at: null,
      checked_at: null,
      stale: false,
      error: '',
    },
    applications: [],
    summary: { affected_applications: affected, critical, high: 0 },
  }
}

test('security banner renders plural critical copy linking to review', () => {
  const html = routed(<SecurityBannerView overview={overviewOf(2, 2)} />)
  assert.match(html, /role="region"/)
  assert.match(html, /aria-label="Security update"/)
  assert.match(html, /data-critical="true"/)
  assert.match(html, /Critical update/)
  assert.match(html, /A security update affects 2 of your apps\./)
  assert.match(html, /Review affected apps and available fixes\./)
  assert.match(html, /href="\/security"[^>]*>Review update/)
})

test('security banner singular non-critical uses the warning eyebrow', () => {
  const html = routed(<SecurityBannerView overview={overviewOf(1, 0)} />)
  assert.match(html, /affects 1 of your apps\./)
  assert.match(html, /data-critical="false"/)
  assert.match(html, />Security update</)
})

test('security banner is hidden with no affected apps', () => {
  assert.equal(routed(<SecurityBannerView overview={overviewOf(0, 0)} />), '')
  assert.equal(routed(<SecurityBannerView overview={undefined} />), '')
})
