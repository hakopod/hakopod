type Bounds = { top: number; bottom: number; left: number; right: number }

export function toastPosition(container: Bounds, viewport: Bounds, footers: Bounds[]) {
  const top = Math.max(container.top, viewport.top)
  const bottom = Math.min(container.bottom, viewport.bottom)
  const left = Math.max(container.left, viewport.left)
  const right = Math.min(container.right, viewport.right)
  if (bottom <= top) return { footerSpace: Math.max(0, container.bottom - bottom), maxHeight: 0 }
  const blockers = footers
    .filter(
      (footer) =>
        footer.bottom > footer.top &&
        footer.right > footer.left &&
        footer.bottom > top &&
        footer.top < bottom &&
        footer.right > left &&
        footer.left < right,
    )
    .map((footer) => ({ top: Math.max(top, footer.top), bottom: Math.min(bottom, footer.bottom) }))
    .sort((a, b) => a.top - b.top)
  let availableTop = top
  let availableBottom = top
  let cursor = top
  const considerGap = (start: number, end: number) => {
    // Visit gaps from top to bottom so equal space keeps the notification above.
    if (end - start > availableBottom - availableTop) {
      availableTop = start
      availableBottom = end
    }
  }
  for (const blocker of blockers) {
    considerGap(cursor, blocker.top)
    cursor = Math.max(cursor, blocker.bottom)
  }
  considerGap(cursor, bottom)
  return {
    footerSpace: Math.max(0, container.bottom - availableBottom),
    maxHeight: Math.max(0, availableBottom - availableTop - 32),
  }
}
