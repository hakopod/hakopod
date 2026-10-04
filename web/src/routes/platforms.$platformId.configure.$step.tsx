import { createFileRoute } from '@tanstack/react-router'
import { PlatformWizardStep } from './platforms.new'

export const Route = createFileRoute('/platforms/$platformId/configure/$step')({
  component: PlatformWizardStep,
})
