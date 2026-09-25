import assert from 'node:assert/strict'
import test from 'node:test'
import { runnerReservation, runnerReservationLabel } from './runner-resources'

test('runner totals use reservations rather than limits and count each slot once', () => {
  assert.deepEqual(
    runnerReservation(
      {
        cpu_request: '1200m',
        cpu_limit: '4800m',
        memory_request: '2458Mi',
        memory_limit: '4916Mi',
      },
      5,
    ),
    { cpu: 6, memoryMiB: 12290 },
  )
  assert.deepEqual(runnerReservation({ cpu_request: '0.25', memory_request: '1Gi' }, 3), {
    cpu: 0.75,
    memoryMiB: 3072,
  })
  assert.equal(
    runnerReservationLabel({ cpu_request: '250m', memory_request: '1Gi' }, 3),
    '0.75 CPU cores and 3072 MiB memory reserved across 3 slots.',
  )
})

test('runner totals support server quantity units and do not claim invalid input is zero', () => {
  for (const memory of ['1Gi', '1024Mi', '1048576Ki', '1073741824']) {
    assert.equal(
      runnerReservation({ cpu_request: '1', memory_request: memory }, 1)?.memoryMiB,
      1024,
    )
  }
  assert.equal(
    runnerReservation({ cpu_request: '500m', memory_request: '1G' }, 1)?.memoryMiB,
    1e9 / 1024 ** 2,
  )
  for (const cpu of ['', '-1', 'foo', '0', '0.0001', 'NaN'])
    assert.equal(runnerReservation({ cpu_request: cpu, memory_request: '1Gi' }, 1), undefined)
  assert.equal(runnerReservation({ cpu_request: '1', memory_request: '4oops' }, 1), undefined)
})
