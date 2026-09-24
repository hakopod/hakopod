import { createFileRoute } from '@tanstack/react-router'
import { CustomRoleEditor } from '../components/pro-access'
export const Route = createFileRoute('/settings/roles/$role/edit')({ component: RoleEdit })
function RoleEdit() {
  return <CustomRoleEditor id={Route.useParams().role} />
}
