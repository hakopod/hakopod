import { useCallback, useEffect, useId, useRef, type ReactNode } from 'react'
import { Card } from '@hakopod/hatch-ui/components/card'
import { HeadingHelp, PageHeader } from './shared'

export function FormPage({
  title,
  description,
  children,
  help,
  keepFocusedControlsVisible = false,
}: {
  title: string
  description: string
  breadcrumbs: { label: string; to?: string }[]
  children: ReactNode
  help?: ReactNode
  icon?: string
  keepFocusedControlsVisible?: boolean
}) {
  const page = useRef<HTMLDivElement>(null)
  const focusFrame = useRef<number | null>(null)
  const revealFocus = useCallback(() => {
    if (focusFrame.current !== null) cancelAnimationFrame(focusFrame.current)
    focusFrame.current = requestAnimationFrame(() => {
      focusFrame.current = null
      const target = document.activeElement
      if (page.current && target instanceof HTMLElement && page.current.contains(target))
        revealFormControl(page.current, target)
    })
  }, [])
  useEffect(() => {
    if (!keepFocusedControlsVisible) return
    const viewport = window.visualViewport
    viewport?.addEventListener('resize', revealFocus)
    window.addEventListener('resize', revealFocus)
    return () => {
      viewport?.removeEventListener('resize', revealFocus)
      window.removeEventListener('resize', revealFocus)
      if (focusFrame.current !== null) cancelAnimationFrame(focusFrame.current)
    }
  }, [keepFocusedControlsVisible, revealFocus])
  return (
    <div
      ref={page}
      className="form-page hako-form-page"
      onFocusCapture={keepFocusedControlsVisible ? revealFocus : undefined}
    >
      <PageHeader title={title} description={description} />
      <div className={`form-page-layout ${help ? 'with-help' : ''}`}>
        <div className="form-page-main">{children}</div>
        {help && <aside className="form-page-help">{help}</aside>}
      </div>
    </div>
  )
}

function revealFormControl(page: HTMLElement, target: HTMLElement) {
  if (
    !target.matches(
      'input:not([type="hidden"]), textarea, button, select, summary, a[href], [contenteditable="true"]',
    )
  )
    return
  const rect = target.getBoundingClientRect()
  if (!rect.width || !rect.height) return
  const viewport = window.visualViewport
  let top = viewport?.offsetTop ?? 0
  let bottom = top + (viewport?.height ?? window.innerHeight)
  let scroller: HTMLElement | null = null
  for (
    let parent = target.parentElement;
    parent && parent !== document.body;
    parent = parent.parentElement
  ) {
    if (
      parent.scrollHeight > parent.clientHeight &&
      /^(auto|scroll|overlay)$/.test(getComputedStyle(parent).overflowY)
    ) {
      scroller = parent
      const bounds = parent.getBoundingClientRect()
      top = Math.max(top, bounds.top + parent.clientTop)
      bottom = Math.min(bottom, bounds.top + parent.clientTop + parent.clientHeight)
      break
    }
  }
  const header = page.closest('.hako-shell')?.querySelector<HTMLElement>('.hako-global-header')
  if (header && ['fixed', 'sticky'].includes(getComputedStyle(header).position)) {
    const bounds = header.getBoundingClientRect()
    if (bounds.bottom > top && bounds.top < bottom) top = Math.max(top, bounds.bottom)
  }
  const footer = (target.closest('form') || page).querySelector<HTMLElement>('.form-footer')
  if (
    footer &&
    !footer.contains(target) &&
    ['fixed', 'sticky'].includes(getComputedStyle(footer).position)
  ) {
    const bounds = footer.getBoundingClientRect()
    if (
      bounds.bottom > top &&
      bounds.top < bottom &&
      bounds.left < rect.right &&
      bounds.right > rect.left
    )
      bottom = Math.min(bottom, bounds.top)
  }
  // Native focus scrolling can reveal only a textarea's caret or stop behind the action bar.
  top += 8
  bottom -= 8
  if (rect.height > bottom - top) return
  const delta = rect.bottom > bottom ? rect.bottom - bottom : rect.top < top ? rect.top - top : 0
  if (delta) (scroller || window).scrollBy({ top: delta, behavior: 'instant' })
}

export function FormSection({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children: ReactNode
  icon?: string
}) {
  const id = useId()
  return (
    <Card className="form-card hako-form-section" role="region" aria-labelledby={id}>
      <header>
        <h2 id={id}>{title}</h2>
        {description && <HeadingHelp title={title}>{description}</HeadingHelp>}
      </header>
      <div className="form-card-content">{children}</div>
    </Card>
  )
}

export function FormHint({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="form-hint hako-form-hint">
      <h3>{title}</h3>
      <div>{children}</div>
    </section>
  )
}
