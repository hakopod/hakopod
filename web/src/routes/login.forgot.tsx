import { createFileRoute } from '@tanstack/react-router'
import { AuthScreen } from '../components/auth-screen'

export const Route = createFileRoute('/login/forgot')({
  component: () => <AuthScreen onSuccess={() => window.location.assign('/')} />,
})
