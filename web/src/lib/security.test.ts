import assert from 'node:assert/strict'
import test from 'node:test'
import { bannerCopy, findingBadge, fixLine, type SecurityOverview } from './security'

const overview = (affected: number, critical = 0) =>
  ({ summary: { affected_applications: affected, critical, high: 0 } }) as SecurityOverview

test('security banner copy follows count and severity', () => {
  assert.equal(bannerCopy(overview(0)), null)
  assert.equal(bannerCopy(undefined), null)
  assert.deepEqual(bannerCopy(overview(1)), {
    eyebrow: 'Security update',
    title: 'A security update affects 1 of your apps.',
    body: 'Review affected apps and available fixes.',
  })
  assert.equal(bannerCopy(overview(2, 1))?.title, 'A security update affects 2 of your apps.')
  assert.equal(bannerCopy(overview(2, 1))?.eyebrow, 'Critical update')
})

test('security finding badge and fix line', () => {
  assert.equal(findingBadge({ confidence: 'confirmed' }), 'Affected')
  assert.equal(findingBadge({ confidence: 'possible' }), 'Possibly affected')
  assert.equal(fixLine({ fix: 'manual', evidence: 'node 20.1.0' }), 'Manual change needed')
  assert.equal(fixLine({ fix: 'reresolve', evidence: 'node 20.1.0' }), 'Runtime update available')
  assert.equal(fixLine({ fix: 'rebuild', evidence: 'python 3.11.2' }), 'Runtime update available')
  assert.equal(
    fixLine({ fix: 'reresolve', evidence: 'postgres:16@sha256:abc' }),
    'Image update available',
  )
  assert.equal(fixLine({ fix: 'rebuild', evidence: 'golang:1.22' }), 'Image update available')
})
