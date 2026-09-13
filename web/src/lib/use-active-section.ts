import { useEffect, useRef } from 'react'

// Reveal the current section inside a horizontal nav without scrolling the page.
export function useActiveSection(section: string) {
  const root = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const navigation = root.current?.querySelector<HTMLElement>('.settings-nav')
    if (!navigation) return
    let mounted = true
    const reveal = () => {
      if (!mounted || navigation.scrollWidth <= navigation.clientWidth) return
      const active = navigation.querySelector<HTMLElement>('[aria-current="page"]')
      if (!active) return
      const container = navigation.getBoundingClientRect()
      const item = active.getBoundingClientRect()
      if (item.left < container.left) navigation.scrollLeft -= container.left - item.left + 12
      else if (item.right > container.right)
        navigation.scrollLeft += item.right - container.right + 12
    }
    reveal()
    const observer = new ResizeObserver(reveal)
    observer.observe(navigation)
    void document.fonts.ready.then(reveal)
    return () => {
      mounted = false
      observer.disconnect()
    }
  }, [section])
  return root
}
