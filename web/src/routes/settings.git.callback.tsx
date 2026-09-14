import { createFileRoute } from '@tanstack/react-router'
import { GitOAuthCallback } from '../components/git-oauth'
export const Route = createFileRoute('/settings/git/callback')({ component: GitOAuthCallback })
