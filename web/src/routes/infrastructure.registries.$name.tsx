import { createFileRoute } from '@tanstack/react-router'
import RegistryEditorPage from '../components/registry-editor-page'
export const Route = createFileRoute('/infrastructure/registries/$name')({
  component: RegistryEditor,
})
function RegistryEditor() {
  return <RegistryEditorPage name={Route.useParams().name} />
}
