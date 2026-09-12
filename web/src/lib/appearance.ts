import { useSyncExternalStore } from 'react'
import { accentForeground, accentText, DEFAULT_ACCENT, validAccent } from './appearance-color'

export type Theme = 'dark' | 'light'
const themeEvent = 'hakopod-theme-change'

function currentTheme(): Theme {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark'
}

function currentAccent() {
  return document.documentElement.dataset.accent || DEFAULT_ACCENT
}

function applyAccent(color: string) {
  const root = document.documentElement
  const accent = validAccent(color) ? color.toUpperCase() : DEFAULT_ACCENT
  root.dataset.accent = accent
  root.style.setProperty('--action', accent)
  root.style.setProperty('--action-fg', accentForeground(accent))
  root.style.setProperty('--action-text', accentText(accent, currentTheme()))
}

function applyTheme(theme: Theme) {
  const root = document.documentElement
  root.classList.remove('dark', 'light')
  root.classList.add(theme)
  root.dataset.theme = theme
  // Remove custom colours left by an older console in this browser session.
  for (const token of ['--accent', '--accent-strong', '--accent-wash', '--accent-border'])
    root.style.removeProperty(token)
  if (root.dataset.accent) applyAccent(root.dataset.accent)
}

export function setAccent(color: string) {
  if (!validAccent(color)) return false
  applyAccent(color)
  let saved = true
  try {
    localStorage.setItem('hakopod-accent', color.toUpperCase())
  } catch {
    saved = false
  }
  window.dispatchEvent(new Event(themeEvent))
  return saved
}

export function setTheme(theme: Theme) {
  applyTheme(theme)
  try {
    localStorage.setItem('hakopod-theme', theme)
  } catch {}
  window.dispatchEvent(new Event(themeEvent))
}

function subscribe(listener: () => void) {
  // The connection screen mounts this store before authenticated content appears.
  // Initialize once; a blocked storage write must not reset this visit's selection.
  if (!document.documentElement.dataset.accent) {
    let accent = DEFAULT_ACCENT
    try {
      accent = localStorage.getItem('hakopod-accent') || DEFAULT_ACCENT
    } catch {}
    applyAccent(accent)
  }
  const onStorage = (event: StorageEvent) => {
    if (event.key === 'hakopod-theme' || event.key === null) {
      applyTheme(event.newValue === 'light' ? 'light' : 'dark')
      listener()
    }
    if (event.key === 'hakopod-accent' || event.key === null) {
      applyAccent(event.newValue || DEFAULT_ACCENT)
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

export function useAccent() {
  const accent = useSyncExternalStore(subscribe, currentAccent, () => DEFAULT_ACCENT)
  return [accent, setAccent] as const
}
