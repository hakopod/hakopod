import { createFileRoute } from '@tanstack/react-router'
import { PlatformWizardStep } from './platforms.new'

export const Route = createFileRoute('/platforms/new/$step')({ component: PlatformWizardStep })
