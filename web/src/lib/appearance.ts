import { useSyncExternalStore } from 'react'

export type Theme = 'dark' | 'light'
const themeEvent = 'hakopod-theme-change'

function currentTheme(): Theme {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark'
}

function applyTheme(theme: Theme) {
  const root = document.documentElement
  root.classList.remove('dark', 'light')
  root.classList.add(theme)
  root.dataset.theme = theme
  // Remove custom colours left by an older console in this browser session.
  for (const token of ['--accent', '--accent-strong', '--accent-wash', '--accent-border'])
    root.style.removeProperty(token)
}

export function setTheme(theme: Theme) {
  applyTheme(theme)
  try {
    localStorage.setItem('hakopod-theme', theme)
  } catch {}
  window.dispatchEvent(new Event(themeEvent))
}

function subscribe(listener: () => void) {
  const onStorage = (event: StorageEvent) => {
    if (event.key === 'hakopod-theme' || event.key === null) {
      applyTheme(event.newValue === 'light' ? 'light' : 'dark')
      listener()
    }
  }
  window.addEventListener(themeEvent, listener)
  window.addEventListener('storage', onStorage)
  return () => {
    window.removeEventListener(themeEvent, listener)
    window.removeEventListener('storage', onStorage)
  }
}

export function useTheme() {
  const theme = useSyncExternalStore(subscribe, currentTheme, (): Theme => 'dark')
  return [theme, setTheme] as const
}
