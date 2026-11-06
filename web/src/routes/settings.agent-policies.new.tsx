import { createFileRoute } from '@tanstack/react-router'
import { AgentPolicyEditor } from '../components/agent-policy-editor'
export const Route = createFileRoute('/settings/agent-policies/new')({
  component: () => <AgentPolicyEditor />,
})
