import { createFileRoute } from '@tanstack/react-router'
import { CustomRoleEditor } from '../components/pro-access'
export const Route = createFileRoute('/settings/roles/new')({
  component: () => <CustomRoleEditor />,
})
