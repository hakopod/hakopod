import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Toast } from './toast'

// Portals stay inside their originating dialog so modal focus and screen-reader
// isolation also apply to notification actions. No application data is stored here.
type Viewport = {
  element: HTMLDivElement
  entries: Map<symbol, () => void>
  stopObserving: () => void
}
const viewports = new Map<HTMLElement, Viewport>()

// Query libraries can temporarily unmount an error while polling. Keep dismissal
// for those recoverable states across remounts; mutation errors remain local.
const dismissedQueries = new Map<string, true>()
function queryKey(scope: string | undefined, message: string) {
  return scope && typeof window !== 'undefined'
    ? `${window.location.pathname}${window.location.search}:${scope}:${message}`
    : undefined
}
export function reopenError(scope: string, message: string) {
  const key = queryKey(scope, message)
  if (key) dismissedQueries.delete(key)
}

function attach(target: HTMLElement, dismiss: () => void) {
  let viewport = viewports.get(target)
  if (!viewport) {
    const element = document.createElement('div')
    element.className = 'hako-toast-viewport'
    element.setAttribute('aria-label', 'Notifications')
    element.setAttribute('role', 'region')
    target.append(element)
    // Reserve the actual action-bar height, including wrapped mobile footers.
    const footers = [...target.querySelectorAll<HTMLElement>('.form-footer, .dialog-footer')]
    const measure = () =>
      element.style.setProperty(
        '--toast-footer-space',
        `${Math.max(0, ...footers.map((footer) => footer.getBoundingClientRect().height))}px`,
      )
    const observer = new ResizeObserver(measure)
    footers.forEach((footer) => observer.observe(footer))
    measure()
    viewport = { element, entries: new Map(), stopObserving: () => observer.disconnect() }
    viewports.set(target, viewport)
  }
  const key = Symbol()
  viewport.entries.set(key, dismiss)
  const current = viewport
  // Settle effect cleanup/re-attachment (including React Strict Mode) before
  // evicting. Evicting during each mount can otherwise dismiss the entire stack.
  queueMicrotask(() => {
    if (viewports.get(target) !== current) return
    while (current.entries.size > 3) {
      const oldest = current.entries.entries().next().value
      if (!oldest) break
      current.entries.delete(oldest[0])
      oldest[1]()
    }
  })
  return {
    element: current.element,
    release() {
      current.entries.delete(key)
      if (!current.entries.size && viewports.get(target) === current) {
        current.stopObserving()
        current.element.remove()
        viewports.delete(target)
      }
    },
  }
}

export function ErrorToast({
  message,
  children,
  remember,
}: {
  message: string
  children?: ReactNode
  remember?: string
}) {
  const key = queryKey(remember, message)
  const anchor = useRef<HTMLSpanElement>(null)
  const notification = useRef<HTMLDivElement>(null)
  const previousFocus = useRef<HTMLElement | null>(null)
  const [dismissed, setDismissed] = useState(() => Boolean(key && dismissedQueries.has(key)))
  const [viewport, setViewport] = useState<HTMLDivElement | null>(null)
  const open = !dismissed
  useEffect(() => setDismissed(Boolean(key && dismissedQueries.has(key))), [message, key])
  const dismiss = useCallback(() => {
    if (
      notification.current?.contains(document.activeElement) &&
      previousFocus.current?.isConnected
    ) {
      previousFocus.current.focus()
    }
    if (key) {
      dismissedQueries.set(key, true)
      if (dismissedQueries.size > 64) dismissedQueries.delete(dismissedQueries.keys().next().value!)
    }
    setDismissed(true)
  }, [key])
  useEffect(() => {
    if (!open || !anchor.current) return
    const target =
      anchor.current.closest<HTMLElement>('[role="dialog"], [role="alertdialog"]') || document.body
    if (
      document.activeElement instanceof HTMLElement &&
      !notification.current?.contains(document.activeElement)
    ) {
      previousFocus.current = document.activeElement
    }
    const registration = attach(target, dismiss)
    setViewport(registration.element)
    return registration.release
  }, [open, dismiss, message])
  return (
    <>
      <span ref={anchor} hidden />
      {open &&
        viewport &&
        createPortal(
          <div ref={notification} className="hako-error-toast" data-error-toast>
            <Toast open onOpenChange={dismiss} message={message} destructive duration={0} />
            {children && <div className="hako-toast-actions">{children}</div>}
          </div>,
          viewport,
        )}
    </>
  )
}
