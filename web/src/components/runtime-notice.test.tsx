import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import { RuntimeNotice } from './runtime-notice'
import { Status } from './shared'

test('blocked runtime exposes its current cause and scoped pod inspection beside stored outcome', () => {
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory({ initialEntries: ['/applications/example'] }),
  })
  const html = renderToStaticMarkup(
    <RouterContextProvider router={router}>
      <span>Deployment outcome</span>
      <Status value="succeeded" />
      <RuntimeNotice
        applicationId="example"
        canInspectNodes
        health={{
          status: 'blocked',
          observed: true,
          ready: 0,
          desired: 1,
          issues: [
            { service: 'api', message: 'Unschedulable: disk-pressure taint', inspect: 'nodes' },
          ],
        }}
      />
    </RouterContextProvider>,
  )
  assert.match(html, /Deployment outcome/)
  assert.match(html, /Succeeded/)
  assert.match(html, /Runtime blocked/)
  assert.match(html, /0 \/ 1 replicas ready/)
  assert.match(html, /Unschedulable: disk-pressure taint/)
  assert.match(html, /href="\/applications\/example\?service=api&amp;tab=pods"/)
  assert.match(html, /href="\/infrastructure\?tab=nodes"/)
})

test('an unavailable runtime shows uncertainty without a ready replica count', () => {
  const html = renderToStaticMarkup(
    <RuntimeNotice
      applicationId="example"
      health={{
        status: 'not observed',
        observed: false,
        issues: [],
        note: 'Current pod health has not been observed.',
      }}
    />,
  )
  assert.match(html, /Runtime health unavailable/)
  assert.match(html, /has not been observed/)
  assert.doesNotMatch(html, /replicas ready|Healthy|Succeeded/)
})

test('runtime diagnostics retain pod inspection without offering unavailable node or log access', () => {
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory({ initialEntries: ['/applications/example'] }),
  })
  const html = renderToStaticMarkup(
    <RouterContextProvider router={router}>
      <RuntimeNotice
        applicationId="example"
        health={{
          status: 'blocked',
          observed: true,
          ready: 0,
          desired: 2,
          issues: [
            { service: 'api', message: 'Unschedulable: disk pressure', inspect: 'nodes' },
            { service: 'web', message: 'CrashLoopBackOff', inspect: 'logs' },
          ],
        }}
      />
    </RouterContextProvider>,
  )
  assert.match(html, /Unschedulable: disk pressure|CrashLoopBackOff/)
  assert.match(html, /Inspect pods/)
  assert.doesNotMatch(html, /Inspect nodes|Inspect logs/)
})
