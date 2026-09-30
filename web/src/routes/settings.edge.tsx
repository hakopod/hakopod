import { createFileRoute } from '@tanstack/react-router'
import { ProxyEditor } from '../components/proxy-settings'

export const Route = createFileRoute('/settings/edge')({ component: ProxyEditor })
