import { createFileRoute } from '@tanstack/react-router'
import { VPNDeviceEditor } from '../components/vpn-settings'
export const Route = createFileRoute('/settings/vpn/devices/new')({ component: VPNDeviceEditor })
