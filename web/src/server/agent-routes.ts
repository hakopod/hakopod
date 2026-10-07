import { agentRoutes } from './agent-routes.generated.ts'

// Match exact contract segments. Encoded separators never select another route.
export function generatedAgentRoute(path: string) {
  if (path.includes('%') || path.includes('\\') || path.includes('?') || path.includes('#')) return undefined
  const parts = path.split('/')
  if (parts.some((part) => !part || part === '.' || part === '..')) return undefined
  const matched = agentRoutes.filter((route) => {
    const template = route.path.slice(1).split('/')
    return template.length === parts.length && template.every((part, index) =>
      part.startsWith('{') ? /^[A-Za-z0-9][A-Za-z0-9_.@-]{0,255}$/.test(parts[index]) : part === parts[index],
    )
  })
  if (!matched.length) return undefined
  return { methods: matched.map((route) => route.method), operations: matched }
}
