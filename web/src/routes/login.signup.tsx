import { createFileRoute } from '@tanstack/react-router'
import { AuthScreen } from '../components/auth-screen'

export const Route = createFileRoute('/login/signup')({
  component: () => <AuthScreen signedIn onSuccess={() => window.location.assign('/')} />,
})
