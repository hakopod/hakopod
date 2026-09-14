import { createFileRoute } from '@tanstack/react-router'
import { GitConnectionEditor } from '../components/git-connections'
export const Route = createFileRoute('/settings/git/connections/new')({
  component: () => <GitConnectionEditor />,
})
