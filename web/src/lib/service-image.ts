import type { Service, ServiceStatus } from './types'

// Managed revisions retain their historical pin. Only the runtime's image list
// represents current runner containers; never fall back to the saved revision.
export function observedServiceImage(service: Service, observed?: ServiceStatus) {
  return service.actions ? observed?.images?.join(', ') : observed?.image || service.image
}
