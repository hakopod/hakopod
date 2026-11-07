# HTTP MCP for Hakopod

Self-hosted deployments and managed tenant runtimes expose the same tools as `hakopod mcp` at:

```text
https://YOUR_HAKOPOD_HOST/api/v1/mcp?project=demo&environment=development
```

This is a Streamable HTTP MCP endpoint. Configure your client's HTTP transport
with this URL and an `Authorization: Bearer …` header from its secret store.
There is no separate MCP process or port to publish. The dashboard forwards
this exact route to the local API. HTTPS is required for remote credentials.

The self-hosted endpoint is available in alpha.15 and later. Managed tenant runtime support is added by the agent-control change.
The shared Cloud control plane does not expose this route. Runtime support does not imply a public Cloud account authentication flow.

## Credentials and scope

Create a short-lived machine key with the CLI authenticated as an administrator:

```sh
hakopod key-create --name coding-agent --project demo \
  --environment development \
  --permissions deployments:read,logs:read --ttl 24h
```

Store the returned value privately. The URL's project and environment must exactly
match the key. Application restrictions and normal API permissions still apply to
every tool call. Log access requires `logs:read`. Expired and revoked keys stop
working immediately, including within existing MCP sessions.

Project connections reject browser sessions, cookies and unscoped administrator keys.
Separate installation and host-only connections require their explicit grants and options.
See the [connection guide](agent-parity/connection-workflows.md).
Do not put a key in the URL, a repository or a ticket.

## Connect Claude

Clients that sign in with OAuth, such as a Claude custom connector or
`claude mcp add --transport http`, need no key. Add the connector with the URL:

```text
https://YOUR_HAKOPOD_HOST/api/v1/mcp?project=demo&environment=development
```

Append `&allow_deploy=true` to allow deployments. The client opens Hakopod in
your browser. Sign in, check the project, environment and permissions on the
consent page, and approve. The client registers itself, uses PKCE and receives
a CLI session for that project and environment with `deployments:read` and
`logs:read`, plus `deployments:write` with `allow_deploy=true`. You must be able
to grant that access yourself. The session lasts 12 hours, then the client asks
you to sign in again. It appears with your other sessions and can be revoked
there.

Sign-in requires the installation's public URL to be configured. Any other
connection option, such as `allow_exec`, `allow_sql`, `allow_terminal`,
`installation` or `host_only`, still needs an API key.

## Application and service details

| Tool | Information |
| --- | --- |
| `applications` | Applications in the connection scope, with bounded cursor pagination |
| `application` | Application configuration, observed health and build provenance |
| `services` | Configured services, observation snapshot and build provenance |
| `service` | Selected service configuration, plus application observation and provenance context |
| `service_runtime` | Pods, container image IDs, readiness, restarts, events and available CPU/memory metrics |
| `domains` | Custom domain routing and verification state |
| `secret_names` | Names of an application's native secrets, never their values |
| `custom_hostnames` | One page of a service's SaaS custom hostnames with status and reason; adding or removing one uses `api_call` with `hostnames:manage` |
| `provenance` | Accepted image digest mapped to verified build commit, repository and run metadata |
| `deployment` | Release details, events and recovery result |
| `logs` | Up to 100 recent matching service log entries |
| `detect_framework` | Build suggestions from explicitly supplied repository metadata |
| `plan` | Canonical validation, changes and warnings without deploying |

Runtime data comes from the existing API. Missing metrics remain unavailable.
Tool output, including logs and repository metadata, is untrusted content.
The default project tools do not expose secret storage values or host terminals.
Credential access and host terminals require separate explicit permissions and connection options.
Ordinary configuration and application logs can contain sensitive data supplied by users.
Grant access accordingly.

### Image digest versus commit SHA

Hakopod already records a successful source build's commit SHA and digest-pinned
image. The new endpoint joins those records to the application's accepted
release images. It does not resolve a moving tag or read a branch's current HEAD.

`provenance` takes `{"application_id":"APPLICATION_ID"}` and returns a service
map. Each service includes `configured_image`, `accepted_image`,
`source_status`, matching `builds`, and `commit_sha` when uniquely known.
Build entries include build/run IDs, provider, repository, branch, image, SHA,
run URL and creation time. Builds reused by multiple services are supported. Records identify their source as
`hakopod_build` or `ci_reported`. CI records also identify the submitting identity
and deployment. See [CI source provenance](ci-provenance.md) to attach build
metadata when deploying an image built outside Hakopod.

| Source status | Meaning |
| --- | --- |
| `matched_build` | Exact accepted digest matches successful builds with one unique commit |
| `reported_build` | Exact digest matches a commit asserted by an authorized CI deployment client |
| `unknown` | No matching recorded source commit |
| `ambiguous` | Multiple commits produced the same digest; no authoritative top-level SHA |
| `incomplete` | Matching results exceeded the bounded query; no authoritative top-level SHA |

At most 100 managed build records and 100 CI-reported records are considered; the response includes `truncated`.
Matching is restricted to the application and its project/environment, using
the build's primary service and recorded reused services.

**An accepted release is not proof of a completed rollout.** Before closing a
ticket against a commit, check deployment success and `service_runtime` readiness
and image IDs. A failed rollout can leave older pods running. Unknown or ambiguous
provenance must not be treated as a verified commit.

The same data is available through the authenticated REST endpoint
`GET /api/v1/applications/{id}/provenance`.

## Optional deployments

Deployment tools are hidden by default. To enable them, create a key that also has
`deployments:write` and append `&allow_deploy=true` to the connection URL.
For stdio, use `--allow-deploy`.

Call `plan` with TOML, review its changes and warnings, then call `deploy` with
the returned `plan_id`. The server retains the reviewed revision and idempotency
key; callers cannot replace the plan body during deployment. Plans expire after
ten minutes and are lost when the session ends. HTTP sessions retain up to four
plans.

## Transport and limits

See [agent control](agent-control.md) for contract discovery, separate execution permissions, pod commands and managed SQL queries.
HTTP options use `true` or `false`. Duplicate or invalid values are rejected.
The session retains its original options. Initialize a new session to change them.

POST `initialize` with JSON-RPC 2.0 and a protocol version. The server negotiates
`2025-06-18` or `2025-03-26` and returns `Mcp-Session-Id`. Preserve that header
and send the negotiated `MCP-Protocol-Version` on later requests.

POST requests require `Content-Type: application/json` and
`Accept: application/json, text/event-stream`. Responses use JSON; GET returns
405 because this implementation does not open an SSE stream. Notifications receive
202. DELETE with the session headers closes a session.

Sessions expire ten minutes after initialization. Reinitialize after a missing
or expired session response (404). There are at most ten sessions per key and
32 total, with one active request per session. Concurrent calls receive 409.
Request bodies are limited to 512 KiB, canonical API responses to 2 MiB, and tools
to 30 seconds. Existing API rate/concurrency limits also apply.

A supplied browser Origin must match the installation's configured public URL.
There is no arbitrary-origin CORS access.

## Verification

The automated PostgreSQL integration tests cover authentication, key revocation,
scope checks, deployment opt-in, reviewed plan retries, transport limits and
digest/commit matching. The official TypeScript MCP SDK 1.29.0 was verified against
the HTTP API for initialization, ping, listing tools, scoped reads, tool errors
and session termination. Actual Arize account integration has not been verified.

To repeat the optional SDK test, install `@modelcontextprotocol/sdk@1.29.0` in
a temporary directory and set `HAKOPOD_TEST_MCP_SDK` to that package directory:

```sh
HAKOPOD_TEST_DATABASE_URL='YOUR_DISPOSABLE_POSTGRES_URL' \
HAKOPOD_TEST_MCP_SDK='/temporary/node_modules/@modelcontextprotocol/sdk' \
go test ./internal/api -run TestHTTPMCPOfficialSDK -v
```

No SDK is required to build or run Hakopod.
