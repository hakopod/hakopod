import { createFileRoute } from '@tanstack/react-router'
import { SlackIntegrationPage } from '../components/slack-settings'

export const Route = createFileRoute('/settings/integrations/slack')({
  validateSearch: (search: Record<string, unknown>) => ({
    configure:
      search.configure === '1' || search.configure === 1 || search.configure === true
        ? true
        : undefined,
    connected:
      search.connected === '1' || search.connected === 1 || search.connected === true
        ? true
        : undefined,
  }),
  component: SlackIntegrationPage,
})
