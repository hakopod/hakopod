import { useEffect, useState } from 'react'

// Reveal the current section inside a horizontal nav without scrolling the page.
export function useActiveSection(section: string, selector = '.settings-nav') {
  const [root, setRoot] = useState<HTMLElement | null>(null)
  useEffect(() => {
    const navigation = root?.matches(selector) ? root : root?.querySelector<HTMLElement>(selector)
    if (!navigation) return
    let mounted = true
    const reveal = () => {
      if (!mounted || navigation.scrollWidth <= navigation.clientWidth) return
      const active = navigation.querySelector<HTMLElement>(
        '[aria-current="page"], [data-state="active"]',
      )
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
  }, [root, section, selector])
  return setRoot
}
