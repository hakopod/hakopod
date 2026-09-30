type Bounds = { top: number; bottom: number; left: number; right: number }

export function toastPosition(container: Bounds, viewport: Bounds, footers: Bounds[]) {
  const top = Math.max(container.top, viewport.top)
  const bottom = Math.min(container.bottom, viewport.bottom)
  const left = Math.max(container.left, viewport.left)
  const right = Math.min(container.right, viewport.right)
  let availableTop = top
  let availableBottom = bottom
  for (const footer of footers) {
    if (
      footer.bottom > footer.top &&
      footer.right > footer.left &&
      footer.bottom > top &&
      footer.top < bottom &&
      footer.right > left &&
      footer.left < right
    ) {
      // If actions reach the top edge, use the space below them so the
      // notification can still be read and dismissed while scrolling past.
      if (footer.top <= top + 32) availableTop = Math.max(availableTop, footer.bottom)
      else availableBottom = Math.min(availableBottom, footer.top)
    }
  }
  return {
    footerSpace: Math.max(0, container.bottom - availableBottom),
    maxHeight: Math.max(0, availableBottom - availableTop - 32),
  }
}
