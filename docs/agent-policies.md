# Agent policies

Agent policies put rules in front of requests made with machine keys. A policy
can block an operation, or hold it until a person approves it. Use them to keep
an automation key, a CI job or an AI agent away from production changes without
taking the key's permissions away entirely.

Agent policies are a Pro feature. See [Licence](#licence) below.

## What is governed

A policy applies to requests authenticated with a machine key: API keys and
automation keys. That includes the CLI and the MCP server when they are
configured with such a key.

Policies do not apply to:

- browser sessions in the dashboard;
- people signed in to the CLI with their own account.

A policy never grants access. If a key lacks the permission for an operation,
the request is refused by normal authorization before any policy is evaluated.
Policies can only narrow what a key may already do.

## The policy document

Each policy is a JSON document with three parts.

`when` is a list of conditions. All of them must match. Each condition has a
`field`, an `operator` and a list of `values`.

| Field | Matches |
| --- | --- |
| `team` | The team that owns the key |
| `key` | The key ID |
| `category` | The operation category from the API contract |
| `operation` | The operation ID from the API contract |
| `project` | The project of the request |
| `environment` | The environment of the request |

Operators are `is_any_of`, `is_none_of` and `matches`. `matches` accepts `*` as
a wildcard, so `deploy*` matches every operation ID that starts with `deploy`.

Operation IDs and categories come from `api/openapi.json`; the category is the
`x-hakopod-agent.category` value of each operation. `hakopod api operations`
lists them. An empty value (an operation without a category, an unknown
route, a request without a project or environment, a key owner in no team)
never equals a listed value: `is_none_of` matches it, `is_any_of` and
`matches` do not. A policy on "every category except `read`" therefore also
covers unclassified operations.

`then` says what happens on a match:

- `effect` is `block` or `require_approval`.
- `approvers` lists who may approve: `project_admins`, a `team` (with `id`) or
  an `identity` (with `id`). An empty list means installation administrators.
- `approval_ttl_seconds` is how long an approval request stays open, from 300
  to 86400 seconds.
- `max_credential_ttl_seconds` caps the expiry of keys created or rotated by
  `createKey` and `rotateKey` when the policy matches.

`except` holds exceptions. With `active_incident` set, a request that would
otherwise be blocked or held is allowed while an alarm is active in the same
project and environment. Each such request sends the Slack event
`agent.policy.exception`.

## Example: production deploys need approval

This policy holds every production deploy until a project administrator
approves it. Approvals expire after 15 minutes. During an active incident the
deploy goes through and Slack is told.

```json
{
  "schema_version": 1,
  "description": "Production deploys need approval",
  "when": [
    {
      "field": "operation",
      "operator": "matches",
      "values": ["deploy*", "createDeployment"]
    },
    {
      "field": "environment",
      "operator": "is_any_of",
      "values": ["production"]
    }
  ],
  "then": {
    "effect": "require_approval",
    "approvers": [{ "kind": "project_admins" }],
    "approval_ttl_seconds": 900
  },
  "except": {
    "active_incident": true
  }
}
```

## Approval flow

A request that matches an enforced `require_approval` policy is refused with
HTTP 403:

```sh
curl -sS -X POST "$HAKOPOD_URL/api/v1/..." \
  -H "Authorization: Bearer $HAKOPOD_KEY" \
  -H "Content-Type: application/json" \
  --data @deploy.json
```

```json
{"error":{"code":"approval_required","message":"..."}}
```

The message and the `Hakopod-Approval-Id` response header carry the approval
id. The `Hakopod-Approval-Expires` header gives the expiry in RFC 3339.

An approver then approves or denies it in Dashboard, Approvals. Approval
decisions are made only in a browser session; the approve and deny routes
refuse CLI and key credentials. The identity that made the request cannot
approve it.

After approval, the client repeats the identical request with the approval id:

```sh
curl -sS -X POST "$HAKOPOD_URL/api/v1/..." \
  -H "Authorization: Bearer $HAKOPOD_KEY" \
  -H "Hakopod-Approval: $APPROVAL_ID" \
  -H "Content-Type: application/json" \
  --data @deploy.json
```

The approved request runs once. A changed body, path or query, a different
key, or a different `X-Hakopod-Workspace` header does not match the approval
and is evaluated again from scratch. If an enforced `block` policy matches the
request when it is retried, the request is refused with `policy_blocked` and
the approval is not spent.

One key may have at most 20 approval requests open at once. Repeating an open
request reuses it. `GET /api/v1/agent-approvals/{id}` is never gated, so a
held client can always read its approval.

In an interactive terminal the CLI prints the approval id and expiry, then
waits until the request is approved, denied or expired. On approval it retries
the identical request once with `Hakopod-Approval`. In a non-interactive run it
prints the id and exits non-zero. `hakopod approval <id>` shows the status.

Built-in MCP tools are evaluated like direct API requests with the same key.
In MCP, tool results carry the approval id and the `approval_status` tool reads
its state. The generic `api_call` tool accepts an optional `approval_id` to
retry an approved request.

New approval requests send the Slack event `agent.approval.requested`.

## Other refusals

| Status | Code | Cause |
| --- | --- | --- |
| 403 | `policy_blocked` | An enforced `block` policy matched |
| 403 | `policy_credential_ttl` | A key was requested with an expiry above `max_credential_ttl_seconds` |
| 403 | `approval_required` | An enforced `require_approval` policy matched |
| 403 | `approval_invalid` | `Hakopod-Approval` names an approval that is missing, not yet approved, expired, already used, or for a different request |
| 414 | `uri_too_long` | The request path is over 4096 bytes and matched an approval policy |
| 429 | `approval_limit` | The key already has 20 approval requests open |
| 413 | `approval_body_too_large` | A request body over 1 MiB matched an approval policy; such requests cannot be approved |
| 402 | `license_required` | Policy management without the entitlement |
| 503 | `agent_policy_unavailable` | Policies could not be loaded, the resource's project could not be looked up, the policy was deleted while the approval was created, or the approval could not be stored |

## Precedence

When several policies match one request, the strongest result wins:

1. enforced `block`;
2. enforced `require_approval`;
3. an exception that allows the request;
4. monitor policies, which only record what they would have done;
5. allow.

A request on a resource (an application, deployment, database, build, backup,
backup artifact, external database, key, preview or project) is evaluated in
the project and environment that own it. Otherwise each distinct scope the
request names, in the query, the body and the key's own scope, is evaluated
and the strongest result wins.

## Monitor first, then enforce

A published policy has the status Enforced or Monitor. A Monitor policy is
evaluated on every matching request and its decision is recorded, but the
request is not blocked or held.

Publish a new policy as Monitor, leave it for a few days of normal traffic,
then check the impact preview. When the recorded decisions are what you
expected, publish it again as Enforced.

## Impact preview and simulation

The impact preview replays recorded decisions against a draft and shows which
past requests it would have blocked or held. Decisions are kept for 30 days.
The preview shows nothing until requests have been recorded. It never
estimates.

Simulation evaluates a single hypothetical request against the policies and
returns the decision and the policies that matched. Use it to check a rule before any traffic exists.

## Versions

Each publish creates an immutable version. Edits are saved as a draft and have
no effect on enforcement until published.

## Limits

- 8 conditions per policy.
- 16 values per condition.
- 100 published policies per installation.
- 30 days of recorded decisions.

## Failure behaviour

If policies cannot be loaded and none were loaded in the last minute, every
machine-key request fails closed with HTTP 503 `agent_policy_unavailable`. The
same happens when the team or alarm lookup fails while policies are published,
or when a held request cannot be stored. Requests are not let through because
a check could not run.

The project and environment a request is matched against come from the
resource it acts on (the application, deployment, database or build in the
path, or `application_id` in the body). Only when there is no such resource are
the `project` and `environment` query or body fields used, then the key's own
scope. Some operations act on resources that are not resolved this way, such
as backup destinations, Git connections and the host terminal; for those, a
condition on project or environment sees the key's own scope.

## Management API

Management requires an installation administrator in a browser session.
Machine keys cannot change policies.

| Route | Purpose |
| --- | --- |
| `/api/v1/agent-policies` | List and create policies |
| `/api/v1/agent-policies/preview` | Impact preview |
| `/api/v1/agent-policies/simulate` | Simulate a request |
| `/api/v1/agent-policies/{id}` | Read one policy |
| `/api/v1/agent-policies/{id}/draft` | Save a draft |
| `/api/v1/agent-policies/{id}/publish` | Publish the draft |
| `/api/v1/agent-policies/{id}/versions` | List versions |
| `/api/v1/agent-approvals` | List approvals |
| `/api/v1/agent-approvals/{id}` | Read one approval |
| `/api/v1/agent-approvals/{id}/approve` | Approve (browser session only) |
| `/api/v1/agent-approvals/{id}/deny` | Deny (browser session only) |

To receive the Slack events, select `agent.approval.requested` and
`agent.policy.exception` in Settings, Integrations, Slack.

## Licence

Agent policies need the `agent_policies` licence entitlement. Without it,
published policies have no effect and the management routes return 402
`license_required`. Licences issued before this feature do not include the
entitlement; ask for a reissued Pro licence. See [licenses](licenses.md).
