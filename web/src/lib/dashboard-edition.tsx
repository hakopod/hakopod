import type { AuthView } from './auth-view'
import type { ReactNode } from 'react'

// Product extensions replace this module in a disposable build directory.
// The public dashboard never imports private product code.
export const dashboardEdition = {
  cloud: false,
  authReturn: (path: string | undefined | null) => Boolean(path?.startsWith('/login/')),
  home: '/',
  label: 'Self-hosted',
  brandSuffix: '',
  releaseChannel: '',
  authAside: false,
  navigation: (_path: string, _features?: { operator: boolean }) => true,
  scopedNavigation: (_path: string) => true,
  settings: (_section: string, _features?: { operator: boolean }) => true,
}

export function EditionControls() {
  return null
}
export function EditionGate({ children }: { children: ReactNode }) {
  return children
}

export function useEditionAuth() {}

export function useEditionFeatures() {
  return {
    git: true,
    terminal: true,
    operator: false,
    hostedFree: false,
    computeURL: undefined as string | undefined,
  }
}

export function EditionAuthAside(_props: { view: AuthView }) {
  return null
}
