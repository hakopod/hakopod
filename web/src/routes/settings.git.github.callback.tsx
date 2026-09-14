import { createFileRoute } from '@tanstack/react-router'
import { GitHubAppCallback } from '../components/git-app-setup'
export const Route = createFileRoute('/settings/git/github/callback')({
  component: GitHubAppCallback,
})
