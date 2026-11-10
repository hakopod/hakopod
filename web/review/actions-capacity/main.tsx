import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '../../src/styles.css'

// Browser-only, synthetic test data. This module never calls the original fetch.
// ?fixture=empty shows an installation without capacities.
const empty = new URLSearchParams(location.search).get('fixture') === 'empty'
const settings = {
  external_id: 'hakopod-0123456789abcdef0123456789abcdef',
  installation_id: 'fedcba9876543210fedcba9876543210',
  revision: 3,
  images: {
    'gcp-spot': {
      capacity: 'gcp-spot',
      state: 'building',
      requested_at: '2026-11-10T10:00:00Z',
      updated_at: '2026-11-10T10:04:00Z',
    },
  },
  capacities: empty
    ? []
    : [
        {
          name: 'aws-spot',
          cloud: 'aws',
          region: 'eu-central-1',
          role_arn: 'arn:aws:iam::123456789012:role/hakopod-actions',
          subnet_ids: ['subnet-0abc1234'],
          security_group_ids: ['sg-0abc1234'],
          instance_types: ['c7i.xlarge', 'c6i.xlarge'],
          architecture: 'amd64',
          spot: true,
          disk_gib: 100,
          max_instances: 20,
          environments: ['*'],
          warm_pool: 2,
        },
        {
          name: 'gcp-spot',
          cloud: 'gcp',
          region: 'us-central1',
          project: 'review-project',
          zones: ['us-central1-a', 'us-central1-b'],
          service_account: 'hakopod-actions@review-project.iam.gserviceaccount.com',
          subnet_ids: ['default'],
          instance_types: ['t2a-standard-2'],
          architecture: 'arm64',
          spot: true,
          max_instances: 5,
          environments: ['shop/production'],
          image: 'hakopod-minimal',
        },
      ],
}
const fixture = { requests: [] as { method: string; path: string; body?: unknown }[] }
Object.assign(window, { __capacityFixture: fixture })

window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  const body = method === 'GET' ? undefined : await request.json().catch(() => undefined)
  fixture.requests.push({ method, path: url.pathname, body })
  let data: unknown = { error: { code: 'not_found', message: 'Synthetic fixture: no such request.' } }
  let status = 404
  if (url.pathname === '/api/settings/actions-capacity' && method === 'GET') [data, status] = [settings, 200]
  if (url.pathname === '/api/settings/actions-capacity' && method === 'PUT') {
    settings.capacities = body.capacities
    settings.revision++
    ;[data, status] = [settings, 200]
  }
  if (url.pathname.endsWith('/test'))
    [data, status] = [{ capacity: 'aws-spot', external_id: settings.external_id, warnings: ['sg-0abc1234 allows inbound 0.0.0.0/0 on port 22'] }, 200]
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

const { default: ActionsCapacitySettings } = await import('../../src/components/actions-capacity-settings')
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={queryClient}>
    <main className="settings-page" style={{ padding: 24 }}>
      <div className="app-settings-panel settings-hub-panel">
        <ActionsCapacitySettings />
      </div>
    </main>
  </QueryClientProvider>,
)
