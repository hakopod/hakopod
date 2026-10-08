# Run a predefined job

Use this API for a fixed report, notebook or build template. Callers supply JSON
inputs; the reviewed service owns its image, command, limits and network policy.

1. Deploy a job with `job.invocation.allowed_identities` and `input_keys`.
2. Use a dedicated service identity or an administrator identity. Create an
   application-scoped machine key with the required permissions:
   `jobs:invoke`, `jobs:read`, `jobs:cancel`, `jobs:logs`.
3. Send the exact successful revision and image digest. Set
   `X-Hakopod-Owner-Scope` from your authenticated tenant context.

```http
POST /api/v1/applications/{app}/services/{service}/invocations
Authorization: Bearer <app-scoped-key>
Idempotency-Key: <unique-request-id>
X-Hakopod-Owner-Scope: team-123
Content-Type: application/json

{
  "expected_revision": 3,
  "expected_image": "registry.example/report@sha256:<64-hex-digest>",
  "correlation_id": "report-456",
  "owner_scope": "team-123",
  "inputs": {"payload": {"report_id": "456"}}
}
```

Relative to that endpoint:

| Request | Result |
| --- | --- |
| `GET /{invocation}` | Current receipt |
| `GET ?correlation_id=report-456&active=true` | Active matching receipts |
| `POST /{invocation}/cancel` | Cancellation requested |
| `GET /{invocation}/logs` | Terminal `{text,truncated}` output |

Include the owner header on every request. A lost create response can be recovered
with the same `Idempotency-Key` header and exact correlation filter; no new create
is needed. Changed input under the same key returns 409.

One invocation runs per application, with four worker lanes per management
process. Each owner can queue four jobs per application; each service defaults
to 16 pending jobs. Deployments wait for active jobs and cleanup. A changed
revision cancels queued jobs. Stop/resume controls template admission.

Inputs and terminal logs are encrypted with the management authentication key.
Receipts remain for seven days, capped at 1,000 per application. New requests
return 409 at the cap until expired history is removed; existing idempotency
receipts remain available. Input is erased after cleanup. Cleanup retains a
one-minute reconciliation window and checks recent tombstones for ten minutes.
`running` with `cleanup_pending=true` is not a completed cancellation.

Job retries are disabled. Kubernetes can replace a disrupted pod, so downstream
side effects must still use their own idempotency key. This is not exactly-once
execution. Live logs, arbitrary images and arbitrary commands are unsupported.
