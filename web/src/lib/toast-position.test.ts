import assert from 'node:assert/strict'
import test from 'node:test'
import { toastPosition } from './toast-position.ts'

const viewport = { top: 0, bottom: 844, left: 0, right: 320 }
const toastArea = { ...viewport, left: 16, right: 304 }

test('a mobile toast clears the action bar and the page footer below it', () => {
  const footer = { top: 604, bottom: 744, left: 16, right: 304 }
  const position = toastPosition(toastArea, viewport, [footer])
  assert.equal(position.footerSpace, 240)
  assert.equal(viewport.bottom - 16 - position.footerSpace, footer.top - 16)
  assert.equal(position.maxHeight, 572)
})

test('scrolling a footer above or below the viewport restores normal toast placement', () => {
  for (const footer of [
    { top: -140, bottom: 0, left: 16, right: 304 },
    { top: 844, bottom: 984, left: 16, right: 304 },
    { top: 0, bottom: 0, left: 0, right: 0 },
    { top: 604, bottom: 744, left: 400, right: 700 },
  ])
    assert.deepEqual(toastPosition(toastArea, viewport, [footer]), {
      footerSpace: 0,
      maxHeight: 812,
    })
  assert.deepEqual(toastPosition(toastArea, viewport, []), {
    footerSpace: 0,
    maxHeight: 812,
  })
})

test('partly visible and resized action bars keep their entire visible top clear', () => {
  const position = toastPosition(toastArea, viewport, [
    { top: 760, bottom: 900, left: 16, right: 304 },
  ])
  assert.deepEqual(position, { footerSpace: 84, maxHeight: 728 })
  assert.deepEqual(
    toastPosition(toastArea, viewport, [
      { top: 700, bottom: 840, left: 16, right: 304 },
      { top: 620, bottom: 700, left: 16, right: 304 },
    ]),
    { footerSpace: 224, maxHeight: 588 },
  )
})

test('an action bar leaving through the top does not hide the persistent error', () => {
  assert.deepEqual(
    toastPosition(toastArea, viewport, [{ top: -20, bottom: 100, left: 16, right: 304 }]),
    { footerSpace: 0, maxHeight: 712 },
  )
})

test('dialog toasts use their own containing area instead of the page bottom', () => {
  const dialog = { top: 150, bottom: 700, left: 28, right: 292 }
  const position = toastPosition(dialog, viewport, [
    { top: 600, bottom: 699, left: 12, right: 308 },
    { top: 730, bottom: 840, left: 16, right: 304 },
  ])
  assert.deepEqual(position, { footerSpace: 100, maxHeight: 418 })
  assert.equal(dialog.bottom - 16 - position.footerSpace, 584)
})

test('visual viewport changes leave toast content above the keyboard and inside view', () => {
  const visible = { ...viewport, top: 80, bottom: 500 }
  assert.deepEqual(toastPosition(toastArea, visible, []), {
    footerSpace: 344,
    maxHeight: 388,
  })
  assert.deepEqual(
    toastPosition(toastArea, visible, [{ top: 440, bottom: 600, left: 16, right: 304 }]),
    { footerSpace: 404, maxHeight: 328 },
  )
})
