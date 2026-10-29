import { createFileRoute } from '@tanstack/react-router'
import { VPNServerEditor } from '../components/vpn-settings'
export const Route = createFileRoute('/settings/vpn/server')({ component: VPNServerEditor })
