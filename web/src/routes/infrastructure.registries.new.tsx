import { createFileRoute } from '@tanstack/react-router'
import RegistryEditorPage from '../components/registry-editor-page'
export const Route = createFileRoute('/infrastructure/registries/new')({
  component: () => <RegistryEditorPage />,
})
