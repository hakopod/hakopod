import { createFileRoute } from '@tanstack/react-router'
import { GitConnectionEditor } from '../components/git-connections'
export const Route = createFileRoute('/settings/git/connections/$connectionId')({
  component: Editor,
})
function Editor() {
  return <GitConnectionEditor id={Route.useParams().connectionId} />
}
