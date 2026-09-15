import assert from 'node:assert/strict'
import test from 'node:test'
import {
  applicationRuntimeHealth,
  currentDeploymentRuntime,
  runtimeObservationMaxAge,
  runtimeReplicaSummary,
  serviceRuntimeHealth,
} from './runtime-health'
import type { Application, ServiceStatus } from './types'

const now = Date.parse('2026-09-13T10:00:00Z')
const observedAt = new Date(now).toISOString()
const unschedulable =
  'Unschedulable: 0/1 nodes are available: 1 node(s) had untolerated taint {node.kubernetes.io/disk-pressure: }.'
const service = (name: string, changes: Partial<ServiceStatus> = {}): ServiceStatus => ({
  name,
  status: 'deploying',
  ready: 0,
  desired: 1,
  message: unschedulable,
  ...changes,
})
const application = (services = [service('api'), service('web')]): Application => ({
  id: 'example',
  name: 'Example',
  project: 'demo',
  environment: 'production',
  revision: 18,
  status: 'healthy',
  created_at: observedAt,
  updated_at: observedAt,
  spec: {
    schema_version: 1,
    name: 'example',
    services: { api: { image: 'example/api' }, web: { image: 'example/web' } },
  },
  observed: { status: 'pending', observed_at: observedAt, services },
  deployments: [
    {
      id: 'release-18',
      application_id: 'example',
      revision: 18,
      status: 'succeeded',
      result: { status: 'healthy', observed_at: observedAt },
      error: '',
      created_at: observedAt,
    },
  ],
})

test('a successful deployment cannot hide currently unschedulable services', () => {
  const app = application()
  const before = structuredClone(app)
  const health = applicationRuntimeHealth(app, now)
  assert.equal(health.status, 'blocked')
  assert.equal(health.ready, 0)
  assert.equal(health.desired, 2)
  assert.deepEqual(
    health.issues.map((issue) => issue.service),
    ['api', 'web'],
  )
  assert.ok(
    health.issues.every((issue) => issue.inspect === 'nodes' && issue.message === unschedulable),
  )
  assert.equal(currentDeploymentRuntime(app, 18, now)?.status, 'blocked')
  assert.equal(currentDeploymentRuntime(app, 17, now), undefined)
  assert.deepEqual(
    app,
    before,
    'runtime presentation must not rewrite stored status or release history',
  )
})

test('unavailable observations never inherit an application or deployment success', () => {
  const app = application()
  app.observed = {}
  const health = applicationRuntimeHealth(app, now)
  assert.equal(health.status, 'not observed')
  assert.equal(health.observed, false)
  assert.equal(health.ready, undefined)
  assert.equal(health.desired, undefined)
  assert.match(health.note!, /has not been observed/)
  assert.equal(currentDeploymentRuntime(app, 18, now)?.status, 'not observed')
  assert.equal(applicationRuntimeHealth(undefined, now).status, 'not observed')
})

test('all configured services must be observed ready before an application is healthy', () => {
  const app = application([service('api', { status: 'ready', ready: 1, message: undefined })])
  app.observed.status = 'healthy'
  const health = applicationRuntimeHealth(app, now)
  assert.equal(health.status, 'partial')
  assert.equal(health.ready, undefined)
  assert.match(health.note!, /1 of 2 services/)
  app.observed.services = []
  assert.equal(applicationRuntimeHealth(app, now).status, 'not observed')
})

test('recovery uses current readiness without retaining old blocking diagnostics', () => {
  const app = application(
    ['api', 'web'].map((name) => service(name, { status: 'ready', ready: 1 })),
  )
  const health = applicationRuntimeHealth(app, now)
  assert.equal(health.status, 'healthy')
  assert.equal(health.ready, 2)
  assert.equal(health.desired, 2)
  assert.deepEqual(health.issues, [])
  assert.equal(app.deployments![0].status, 'succeeded')
})

test('stale and undated observations cannot claim current healthy or blocked runtime', () => {
  for (const services of [
    undefined,
    ['api', 'web'].map((name) => service(name, { status: 'ready', ready: 1, message: undefined })),
  ]) {
    const app = application(services)
    const stale = applicationRuntimeHealth(app, now + runtimeObservationMaxAge + 1)
    assert.equal(stale.status, 'stale')
    assert.equal(stale.ready, undefined)
    assert.deepEqual(stale.issues, [])
    assert.match(stale.note!, /Current health is unknown/)
    for (const time of [undefined, 'invalid', new Date(now + 120_000).toISOString()]) {
      app.observed.observed_at = time
      assert.equal(applicationRuntimeHealth(app, now).status, 'unknown')
    }
  }
})

test('replica facts omit retained counts when observations are stale or unavailable', () => {
  const ready = service('api', { status: 'ready', ready: 1, message: undefined })
  assert.equal(runtimeReplicaSummary(serviceRuntimeHealth(ready, observedAt, now)), '1 / 1 ready')
  assert.equal(
    runtimeReplicaSummary(
      serviceRuntimeHealth(ready, observedAt, now + runtimeObservationMaxAge + 1),
    ),
    'Unavailable',
  )
  assert.equal(runtimeReplicaSummary(serviceRuntimeHealth(ready, undefined, now)), 'Unavailable')
  assert.equal(
    runtimeReplicaSummary(serviceRuntimeHealth(undefined, observedAt, now)),
    'Not observed',
  )
})

test('readiness counts alone do not hide a blocked rollout or incomplete readiness', () => {
  assert.equal(
    serviceRuntimeHealth(service('api', { ready: 1 }), observedAt, now).status,
    'blocked',
  )
  assert.equal(
    serviceRuntimeHealth(service('api', { status: 'ready', message: undefined }), observedAt, now)
      .status,
    'pending',
  )
  assert.equal(
    serviceRuntimeHealth(
      service('api', { status: 'ready', ready: NaN, message: undefined }),
      observedAt,
      now,
    ).status,
    'pending',
  )
  assert.equal(
    serviceRuntimeHealth(
      service('api', { message: 'ContainerCreating: pulling image' }),
      observedAt,
      now,
    ).status,
    'deploying',
  )
})

test('active container failures direct inspection without treating old events as current state', () => {
  for (const [message, inspect] of [
    ['ImagePullBackOff: verify registry access', 'pods'],
    ['CrashLoopBackOff: application repeatedly exits', 'logs'],
    ['readiness check has not passed; verify port and healthcheck', 'pods'],
  ] as const) {
    const health = serviceRuntimeHealth(service('api', { message }), observedAt, now)
    assert.equal(health.status, 'blocked')
    assert.equal(health.issues[0].inspect, inspect)
  }
})

test('empty applications require a fresh explicit runtime observation', () => {
  const app = application([])
  app.spec.services = {}
  assert.equal(applicationRuntimeHealth(app, now).status, 'not observed')
  app.observed.status = 'empty'
  const health = applicationRuntimeHealth(app, now)
  assert.equal(health.status, 'empty')
  assert.equal(health.desired, 0)
  assert.equal(applicationRuntimeHealth(app, now + runtimeObservationMaxAge + 1).status, 'stale')
})

test('jobs keep completion distinct while contributing successful application health', () => {
  const completed = service('api', {
    status: 'completed',
    ready: 1,
    message: 'Job completed successfully',
  })
  const health = serviceRuntimeHealth(completed, observedAt, now)
  assert.equal(health.status, 'completed')
  assert.equal(runtimeReplicaSummary(health), 'Completed')
  const app = application([
    completed,
    service('web', { status: 'ready', ready: 1, message: undefined }),
  ])
  app.spec.services.api.job = { timeout_seconds: 300, retries: 0 }
  assert.equal(applicationRuntimeHealth(app, now).status, 'healthy')
  assert.equal(
    serviceRuntimeHealth(completed, observedAt, now + runtimeObservationMaxAge + 1).status,
    'stale',
  )
  const running = service('api', { status: 'running', message: undefined })
  assert.equal(runtimeReplicaSummary(serviceRuntimeHealth(running, observedAt, now)), 'Running')
  app.observed.services![0] = running
  assert.equal(applicationRuntimeHealth(app, now).status, 'partial')
  app.observed.services![0] = service('api', {
    status: 'failed',
    message: 'Job failed or exceeded its deadline; inspect service logs',
  })
  assert.equal(applicationRuntimeHealth(app, now).status, 'failed')
  assert.equal(applicationRuntimeHealth(app, now).issues[0].inspect, 'logs')
  assert.equal(
    runtimeReplicaSummary(serviceRuntimeHealth(app.observed.services![0], observedAt, now), true),
    'Failed',
  )
})

test('fresh sleeping services explain automatic wake without claiming ready replicas', () => {
  const sleeping = service('api', {
    status: 'sleeping',
    ready: 0,
    desired: 0,
    message: 'Sleeping after HTTP inactivity; the next request wakes this service',
  })
  const health = serviceRuntimeHealth(sleeping, observedAt, now)
  assert.equal(health.status, 'sleeping')
  assert.equal(health.ready, 0)
  assert.equal(health.desired, 0)
  assert.equal(runtimeReplicaSummary(health), 'Wakes on request')
  assert.deepEqual(health.issues, [])

  const app = application([sleeping, { ...sleeping, name: 'web' }])
  const before = structuredClone(app)
  const aggregate = applicationRuntimeHealth(app, now)
  assert.equal(aggregate.status, 'sleeping')
  assert.equal(runtimeReplicaSummary(aggregate), 'Wakes on request')
  assert.deepEqual(aggregate.issues, [])
  assert.deepEqual(app, before, 'sleep presentation must not rewrite saved replicas or releases')
})

test('sleeping peers are expected but cannot hide missing or blocked services', () => {
  const sleeping = service('api', {
    status: 'sleeping',
    ready: 0,
    desired: 0,
    message: undefined,
  })
  const app = application([
    sleeping,
    service('web', { status: 'ready', ready: 1, message: undefined }),
  ])
  const healthy = applicationRuntimeHealth(app, now)
  assert.equal(healthy.status, 'healthy')
  assert.equal(healthy.ready, 1)
  assert.equal(healthy.desired, 1)
  app.observed.services = [sleeping]
  assert.equal(applicationRuntimeHealth(app, now).status, 'partial')
  app.observed.services = [sleeping, service('web')]
  const blocked = applicationRuntimeHealth(app, now)
  assert.equal(blocked.status, 'blocked')
  assert.deepEqual(
    blocked.issues.map((issue) => issue.service),
    ['web'],
  )
})

test('stale or undated sleep observations never promise automatic wake', () => {
  const sleeping = service('api', {
    status: 'sleeping',
    ready: 0,
    desired: 0,
    message: undefined,
  })
  const app = application([sleeping, { ...sleeping, name: 'web' }])
  for (const [time, clock, status] of [
    [observedAt, now + runtimeObservationMaxAge + 1, 'stale'],
    [undefined, now, 'unknown'],
  ] as const) {
    app.observed.observed_at = time
    for (const health of [
      serviceRuntimeHealth(sleeping, time, clock),
      applicationRuntimeHealth(app, clock),
    ]) {
      assert.equal(health.status, status)
      assert.equal(runtimeReplicaSummary(health), 'Unavailable')
      assert.equal(health.ready, undefined)
      assert.equal(health.desired, undefined)
      assert.deepEqual(health.issues, [])
    }
  }
})

test('wake transitions stop showing sleeping before replicas become ready', () => {
  const waking = service('api', {
    status: 'sleeping',
    ready: 0,
    desired: 1,
    message: undefined,
  })
  const pending = serviceRuntimeHealth(waking, observedAt, now)
  assert.equal(pending.status, 'pending')
  assert.equal(runtimeReplicaSummary(pending), '0 / 1 ready')
  waking.status = 'deploying'
  assert.equal(serviceRuntimeHealth(waking, observedAt, now).status, 'deploying')
  waking.status = 'ready'
  waking.ready = 1
  assert.equal(serviceRuntimeHealth(waking, observedAt, now).status, 'ready')
})

test('manual stops remain stopped and do not promise wake on request', () => {
  const stopped = service('api', {
    status: 'stopped',
    ready: 0,
    desired: 0,
    message: 'Stopped by user; configuration and volumes are retained',
  })
  const health = serviceRuntimeHealth(stopped, observedAt, now)
  assert.equal(health.status, 'stopped')
  assert.equal(runtimeReplicaSummary(health), '0 / 0 ready')
  assert.equal(runtimeReplicaSummary(health, true), 'Paused')
  assert.deepEqual(health.issues, [])
  const app = application([stopped, { ...stopped, name: 'web' }])
  assert.equal(applicationRuntimeHealth(app, now).status, 'stopped')
  assert.notEqual(runtimeReplicaSummary(applicationRuntimeHealth(app, now)), 'Wakes on request')
  assert.equal(applicationRuntimeHealth(app, now + runtimeObservationMaxAge + 1).status, 'stale')
})
