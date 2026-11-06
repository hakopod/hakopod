import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '../components/shared'
import { AgentApprovals } from '../components/agent-approvals'
export const Route = createFileRoute('/approvals')({ component: ApprovalsPage })
function ApprovalsPage() {
  return (
    <div className="ops-page">
      <PageHeader
        title="Approvals"
        description="Requests made with API keys that an agent policy holds until a person approves them. An approval lets that exact request run once before it expires."
      />
      <AgentApprovals />
    </div>
  )
}
