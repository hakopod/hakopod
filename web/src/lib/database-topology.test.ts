import assert from 'node:assert/strict'
import test from 'node:test'
import { connectedApplications, databaseTopologyLayout, bindingEvidence, endpointTargetsMember } from './database-topology'
import type { DatabaseConnectionReference } from './databases'

test('15 applications and six replicas retain every non-overlapping target inside the canvas', () => {
  for (const [apps, members, primary] of [
    [15, 7, true],
    [0, 1, true],
    [15, 48, false],
    [1, 2, true],
  ] as const) {
    const layout = databaseTopologyLayout(apps, members, primary)
    assert.equal(layout.appPositions.length, apps)
    assert.equal(layout.memberPositions.length, members)
    const rectangles = [
      ...layout.appPositions.map((p) => ({ x: p.x, y: p.y, width: 164, height: 70 })),
      ...layout.memberPositions.map((p) => ({ x: p.x - 82, y: p.y - 67, width: 164, height: 174 })),
    ]
    for (const [i, a] of rectangles.entries()) {
      assert.ok(
        a.x >= 0 && a.y >= 0 && a.x + a.width <= layout.width && a.y + a.height <= layout.height,
      )
      for (const b of rectangles.slice(i + 1))
        assert.ok(
          a.x + a.width <= b.x ||
            b.x + b.width <= a.x ||
            a.y + a.height <= b.y ||
            b.y + b.height <= a.y,
          'node targets overlap',
        )
    }
  }
})

test('connections group by application identity and retain old and new endpoint evidence', () => {
  const base: DatabaseConnectionReference = {
    application_id: 'a',
    application_name: 'same-name',
    application_display_name: 'Checkout',
    project: 'p',
    environment: 'dev',
    service: 'api',
    variable: 'DATABASE_URL',
    endpoint: 'read_write',
    saved_revision: 3,
    last_successful_revision: 0,
    latest_attempt_revision: 3,
    latest_attempt_status: 'failed',
  }
  const old = {
    ...base,
    endpoint: 'read_only',
    saved_revision: 0,
    last_successful_revision: 2,
    latest_attempt_revision: 0,
  }
  const groups = connectedApplications([base, old, { ...base, application_id: 'b' }])
  assert.equal(groups.length, 2)
  assert.equal(groups[0].bindings.length, 2)
  assert.equal(groups[0].displayName, 'Checkout')
  assert.match(bindingEvidence(old), /Last successful deployment · r2/)
  assert.doesNotMatch(bindingEvidence(old), /Saved/)
  assert.match(bindingEvidence(base), /failed attempt · r3/)
})


test('pooled topology retains every endpoint and routes poolers to the right members', () => {
  for (const count of [1, 7]) {
    const layout = databaseTopologyLayout(15, count, true, 4, 6)
    for (const p of [...layout.endpointPositions, ...layout.poolerPositions]) assert.ok(p.y + 70 < layout.height)
    assert.ok(layout.poolerX + 164 < layout.memberStart)
  }
  assert.equal(endpointTargetsMember('pooled_read_write', 'primary'), true)
  assert.equal(endpointTargetsMember('pooled_read_write', 'replica'), false)
  assert.equal(endpointTargetsMember('pooled_read_only', 'primary'), false)
  assert.equal(endpointTargetsMember('pooled_read_only', 'replica'), true)
})

test('MongoDB discovery includes every voting member without changing Redis seed routing', () => {
  assert.equal(endpointTargetsMember('cluster', 'primary', 'mongodb'), true)
  assert.equal(endpointTargetsMember('cluster', 'replica', 'mongodb'), true)
  assert.equal(endpointTargetsMember('cluster', 'router', 'mongodb'), false)
  assert.equal(endpointTargetsMember('cluster', 'replica', 'redis'), false)
})

test('ClickHouse routes to data replicas and separates Keeper coordination from application traffic', () => {
  assert.equal(endpointTargetsMember('cluster', 'replica', 'clickhouse'), true)
  for (const role of ['leader', 'follower', 'unknown']) assert.equal(endpointTargetsMember('cluster', role, 'clickhouse'), false)
  for (const members of [2, 6]) {
    const layout = databaseTopologyLayout(15, members, false, 1, 0, 3)
    assert.equal(layout.coordinatorPositions.length, 3)
    const lastData = layout.memberPositions.at(-1)!
    for (const at of layout.coordinatorPositions) {
      assert.ok(at.y > lastData.y + 110)
      assert.ok(at.y + 70 < layout.height && at.x + 164 < layout.width)
    }
  }
})
