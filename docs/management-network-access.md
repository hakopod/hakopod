# Management network access

Pro installations can limit which networks reach the management surface: the
dashboard, the API, the CLI, the SDK and MCP. Application traffic on HAProxy is
not affected; use [Hakopod Edge](haproxy.md) for that.

Open **Settings → Network access**, list the allowed CIDRs and save. The policy
is off until you turn it on, and only a self-hosted installation owner signed in
through the browser can change it. Automation keys, CLI sessions and agent keys
cannot.

## What stays reachable

- Signed provider webhooks (`/api/v1/webhooks/github`, `/gitlab`, `/git/...`,
  `/github-app/...`). Their signature checks are unchanged.
- `/healthz` and `/readyz` on the API's loopback listener.
- Loopback and the server's own addresses, so SSH tunnels
  (`dashboard_mode=ssh`) and the maintenance readiness probe keep working.

Everything else, including the sign-in page, OAuth callbacks and device login,
is refused before authentication with `403 network_not_allowed`.

## Behind a load balancer

Choose **Header from a trusted proxy or load balancer**, list the proxy's
addresses and pick the header it sets (`X-Forwarded-For`, `X-Real-IP` or
`CF-Connecting-IP`). The header is read only when the connection comes from a
listed proxy. `X-Forwarded-For` is read from the right, skipping trusted hops,
so a client cannot add its own address in front.

## Saving without locking yourself out

- A save that would block the network you are using is refused unless you tick
  **Save even if it blocks my current network**.
- Every save that turns the policy on or changes it is provisional. It reverts
  to the previous policy after five minutes unless you press **Keep this
  policy** from a network the new policy allows.
- If you are locked out anyway, run this on the server:

  ```bash
  sudo python3 /opt/hakopod/maintenance/maintenance.py --access-reset
  ```

  It turns the policy off, records the host user in the audit log, and works
  without a license or a dashboard session.

Saves, confirmations, automatic reverts and resets are recorded in the audit log
under `installation.access.*`, with the client address that made the change.

## How it is enforced

The Node dashboard is the only public management listener. It asks the API for
a decision on every request before it renders anything, using the socket peer
address, and caches each decision for five seconds. The API makes the same
check itself for any request that reaches it directly. If the policy cannot be
read, requests are refused with `503`.

A host firewall layer and VPN connectors are planned as later phases.
