import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { DeploymentLog, deploymentPhases, duration, initiallyOpen } from './deployment-log'

const event = (id: number, type: string, service: string, second: number, message = type) => ({
  id,
  type,
  service,
  message,
  time: new Date(Date.UTC(2026, 9, 29, 12, 0, second)).toISOString(),
})
const release = {
  id: 'dep_1',
  revision: 4,
  status: 'failed' as const,
  created_at: '2026-10-29T12:00:00Z',
  error: 'worker did not become ready',
}

test('deployment phases collapse routine success and open the failing service', () => {
  const events = [
    event(1, 'resolved', '', 1),
    event(2, 'applying', 'web', 2),
    event(3, 'ready', 'web', 9),
    event(4, 'applying', 'worker', 10),
    event(5, 'failed', 'worker', 40, 'Back-off restarting failed container'),
    event(6, 'failed', '', 41),
  ]
  const phases = deploymentPhases(release, events)
  assert.deepEqual(
    phases.map((phase) => [phase.label, phase.status]),
    [
      ['Release', 'failed'],
      ['web', 'succeeded'],
      ['worker', 'failed'],
    ],
  )
  assert.equal(phases[2].error?.message, 'Back-off restarting failed container')
  assert.equal(duration(phases[2].start, phases[2].end), '30s')
  assert.deepEqual(initiallyOpen(phases), {
    release: false,
    'service-web': false,
    'service-worker': true,
  })
  const markup = renderToStaticMarkup(
    <DeploymentLog release={{ ...release, events } as never} />,
  )
  assert.match(markup, /worker failed/)
  assert.match(markup, /Back-off restarting failed container/)
  assert.match(markup, /aria-label="Next phase"/)
})

test('a recovered service is not reported as failed; an unfinished one is not success', () => {
  const phases = deploymentPhases({ ...release, status: 'succeeded', error: '' }, [
    event(1, 'applying', 'web', 1),
    event(2, 'failed', 'web', 2),
    event(3, 'ready', 'web', 3),
    event(4, 'applying', 'job', 4),
  ])
  assert.equal(phases[1].status, 'succeeded')
  assert.equal(phases[2].status, 'incomplete')
  assert.equal(phases[0].error, undefined)
})
