# Sandbox sessions

Status: implementation in progress. Native runtime qualification is required before deployment.

Use a session when a worker must keep memory and files between calls, such as a notebook kernel. Deploy a fixed worker image and helper command; callers cannot choose a command or receive cluster credentials.

1. Configure a digest-pinned worker with an approved sandbox runtime profile. Allow exact machine identity IDs in `session.allowed_identities`.
2. Issue an application-scoped key with `sessions:create`, `sessions:read`, `sessions:call` and `sessions:delete`.
3. Create a session with the expected revision, image and runtime key. Set `X-Hakopod-Owner-Scope` and a unique `Idempotency-Key`.
4. Wait for `ready`. Send calls with the returned `X-Hakopod-Session-Generation` and a new `Idempotency-Key` for each call.
5. Renew the idle deadline with `/heartbeat`. Close the session with `DELETE` and wait for `closed`.

Base path:

```text
/api/v1/applications/{id}/services/{service}/sessions
```

| Operation | Request |
|---|---|
| Create | `POST` base path with `expected_revision`, `expected_image`, `runtime_key` |
| Recover | `GET` base path with exact `runtime_key` |
| Inspect | `GET /{session}` |
| Renew | `POST /{session}/heartbeat` |
| Call fixed helper | `POST /{session}/call` with binary input |
| Close | `DELETE /{session}` |

Every request requires the creating identity and owner scope. Calls and heartbeats also require the expected generation. A repeated call ID is rejected: an interrupted connection can leave execution uncertain, so clients must not replay code automatically.

Limits: eight active sessions per application, one active session per owner/runtime key, one active call per session, two calls per API process, 48 MiB input, 52 MiB output and 150 seconds per call. Sessions expire after at most one hour; idle timeout is at most 15 minutes. Closed receipts remain for 24 hours.

`closing` with `cleanup_pending=true` means deletion is not confirmed. `closed` confirms cleanup. Credentials, expiry, missing containers and changed generations fail closed; they do not silently recreate a kernel.

The process guard uses Linux `RLIMIT_NPROC`, which counts tasks for a real UID. It is not a Kubernetes per-Pod PIDs control. The native gVisor qualification must demonstrate that one session reaching its UID limit does not prevent another tenant session from starting processes. Ordinary shared-kernel execution is not an accepted fallback.

The operator sets `HAKOPOD_SESSION_GUARD_IMAGE` to a digest-pinned Hakopod server image containing `/hakopod-session-guard`. TOML configuration uses `[sandbox_sessions]` with `guard_image`. Each template requires a runtime profile bound to the `runsc` handler. The capacity plan includes eight concurrent workers per template; the application admission limit remains eight sessions in total.
