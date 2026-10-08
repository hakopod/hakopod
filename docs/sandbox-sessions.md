# Sandbox sessions

Status: development-qualified on Linux amd64 with gVisor 20261005.0. Qualify the installed runtime before production use.

Use a session when a worker must keep memory and files between calls, such as a notebook kernel. Deploy a fixed worker image, helper command and readiness command. Callers cannot choose a command or receive cluster credentials.

1. Configure a digest-pinned worker with an approved sandbox runtime profile. Set `session.helper_command`, `session.ready_command` and exact machine identity IDs in `session.allowed_identities`.
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

The authenticated machine caller supplies `X-Hakopod-Owner-Scope`; it is not an end-user authentication token. Your trusted application must derive this scope from its authenticated user. Keep the application machine key on the server: a holder can select any owner scope allowed under that machine identity.

Every request requires the creating identity and owner scope. Calls and heartbeats also require the expected generation. A repeated call ID is rejected: an interrupted connection can leave execution uncertain, so clients must not replay code automatically.

Limits: eight active sessions per application, one active session per owner/runtime key, one active call per session, two calls per API process, 48 MiB input, 52 MiB combined stdout/stderr output and 150 seconds per call. Sessions expire after at most one hour; idle timeout is at most 15 minutes. Closed receipts remain for 24 hours.

`closing` with `cleanup_pending=true` means deletion is not confirmed. `closed` confirms cleanup. Credentials, expiry, missing containers and changed generations fail closed; they do not silently recreate a kernel.

The process guard uses Linux `RLIMIT_NPROC`, which counts tasks for a real UID. It is not a Kubernetes per-Pod PIDs control. The native gVisor test saturates one session’s UID limit while another tenant starts a helper and child process. Ordinary shared-kernel execution is not an accepted fallback.

The operator sets `HAKOPOD_SESSION_GUARD_IMAGE` to a digest-pinned Hakopod server image containing `/hakopod-session-guard`. TOML configuration uses `[sandbox_sessions]` with `guard_image`. Each template requires a runtime profile bound to the `runsc` handler. The capacity plan includes eight concurrent workers per template; the application admission limit remains eight sessions in total. Each worker reserves the larger of its request and the guard initialization request (50m CPU, 32 MiB), plus the RuntimeClass overhead (20m CPU, 50 MiB).

Private worker images can use `registry_credential`. The kubelet receives an immutable pull secret; the worker cannot read it. Credential rotation invalidates the session. Create a new session after rotation.

Namespace cleanup stops if it finds unexpected resources or cannot complete resource discovery. Operators must resolve these conditions before the receipt can become `closed`.

The guarded readiness command must confirm that the kernel can accept calls. A startup probe runs it until success or the two-minute startup deadline. The probe does not run notebook code.

Temporary mounts require `memory=true` and share the container memory limit. Their combined configured capacity is at most 128 MiB. `/run/hakopod-session` is reserved for the read-only process guard.

Before executing worker code, the guard permits only UNIX sockets and denies IP sockets and io_uring. Kubernetes network policy adds a second boundary. Helper input remains blocked until the trusted guard confirms the expected Pod UID and generation. Kubernetes exec has no atomic UID precondition; session namespaces therefore allow no tenant Kubernetes access.

Validation covers the full Go suite, guarded binary streams, container identity, resource accounting and cleanup. The native TLS/HTTP gate passed in 88.02 seconds with two persistent Python kernels, file/variable retention, tenant isolation, replay rejection and cancellation. Both container records used `io.containerd.runsc.v1`; all fixture namespaces and volumes were removed. ARM64 native execution remains unverified.
