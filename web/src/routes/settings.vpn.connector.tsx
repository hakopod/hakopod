import { createFileRoute } from '@tanstack/react-router'
import { VPNConnectorEditor } from '../components/vpn-settings'
export const Route = createFileRoute('/settings/vpn/connector')({ component: VPNConnectorEditor })
