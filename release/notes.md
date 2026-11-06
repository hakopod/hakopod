Hakopod 0.1.0-alpha.79 adds agent policies, which put rules in front of requests made with API keys and automation keys.

- **Block or hold.** A policy can refuse an operation, or hold it until a named approver (project administrators, a team or a person) approves that exact request. The approved request runs once; a changed body, path, query or key needs a new approval.
- **Monitor, then enforce.** Publish a policy as Monitor to record what it would have done, then as Enforced once the impact preview, which replays the last 30 days of recorded decisions, shows what you expected.
- **Approvals inbox.** Held requests appear in Dashboard, Approvals and announce themselves with the Slack event `agent.approval.requested`.
- **CLI and MCP.** In an interactive terminal the CLI waits for the approval and retries the identical request; `hakopod approval <id>` shows its state. MCP gains the `approval_status` tool and an `approval_id` on `api_call`.

Browser sessions and people signed in to the CLI are never gated. Agent policies are a Pro feature under the new `agent_policies` entitlement; existing Pro licences need to be reissued to include it. See docs/agent-policies.md.

Also in this release:

- New CLI commands: `apps`, `export`, `set-image`, `restart`, `bindings` and `secret list|set|delete`. `exec` accepts an application name.
- New MCP tools `restart_service` and `secret_names`; both MCP servers accept the optional `_meta` object on tool calls.
- `plan` exits 2 on a missing secret and lists only the env keys that changed.
- A healthy rollout is no longer reported as failed just after the wait.
- Log severity is read from a leading level word in plain text and from `levelname` in JSON.
- ClickHouse bindings can reach the HTTPS interface as well as native TLS.
- A SQL query without `databases:query` says which grant is missing, and an expired CLI session says to sign in again.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.79/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.79
```

Upgrade an alpha.77 or alpha.78 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.79/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.79
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
