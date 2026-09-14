import { createFileRoute } from '@tanstack/react-router'
import { SMTPEditor } from '../components/smtp-settings'
export const Route = createFileRoute('/settings/smtp')({ component: SMTPEditor })
