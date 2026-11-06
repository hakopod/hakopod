import { agentRoutes } from '../server/agent-routes.generated'
import { humanizeOperation } from './agent-policy-form'

// Operations an API key can call, from the generated contract, for the operation picker.
export const operationOptions = agentRoutes
  .map((route) => ({
    value: route.id,
    label: humanizeOperation(route.id),
    detail: `${route.method} ${route.path}`,
  }))
  .sort((a, b) => a.label.localeCompare(b.label))
