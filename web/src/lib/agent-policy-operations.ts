import { policyOperations } from './agent-policy-operations.generated'
import { humanizeOperation } from './agent-policy-form'

// Operations an API key can call, from the generated contract, for the operation picker.
// Search covers the readable name, the operation id and its summary.
export const operationOptions = policyOperations
  .map((operation) => ({
    value: operation.id,
    label: humanizeOperation(operation.id),
    detail: operation.summary,
  }))
  .sort((a, b) => a.label.localeCompare(b.label))
