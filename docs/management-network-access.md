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
  sudo systemd-run --wait --pipe --quiet --collect --uid=hakopod-api --gid=hakopod-api --property=EnvironmentFile=/etc/hakopod/api.env --setenv=HAKOPOD_ACCESS_RESET_ACTOR="$USER" /opt/hakopod/current/hakopod-server access-reset
  ```

  It runs the release's own server binary as the API user with the API's
  settings, turns the policy off, records your host user name in the audit
  log, and works without a license or a dashboard session.

Saves, confirmations, automatic reverts and resets are recorded in the audit log
under `installation.access.*`, with the client address that made the change.

## How it is enforced

The Node dashboard is the only public management listener. It asks the API for
a decision on every request before it renders anything, using the socket peer
address, and caches each decision for five seconds. The API makes the same
check itself for any request that reaches it directly. If the policy cannot be
read, requests are refused with `503`.

## Host firewall

The allowlist above is enforced by Hakopod's own processes. To also close the
ports in the server's firewall, install the module once and tick **Also close
the dashboard port and the Kubernetes API** in Settings:

```bash
sudo python3 ./installer/modules.py private-access
```

The module needs `nftables`. It adds one table, `inet hakopod_access`, and a
root timer that brings it in line with the saved policy every ten seconds:

- The dashboard port (in `https` dashboard mode) and the K3s API on `6443`
  accept only the allowed networks, loopback, current cluster nodes and the
  cluster Pod and Service networks (`10.42.0.0/16`, `10.43.0.0/16`).
- HAProxy's `80` and `443`, kubelet, flannel and everything else are untouched.
- A new agent node must come from an allowed network until it has joined.
- An unconfirmed policy reverts in the firewall within ten seconds of reverting
  in the API. A reset turns the firewall off on the next tick; for an instant
  way out, run `sudo nft delete table inet hakopod_access`.
- If the timer cannot read the policy, it keeps the current rules.

## WireGuard connector

The same module can join this server to your WireGuard network as a client,
so administrators reach the dashboard over the VPN. It needs
`wireguard-tools` on the server.

1. In **Settings → VPN**, press **Connect**, choose **WireGuard** and enter the server's tunnel address, your peer's public key
   and endpoint, and the networks to route through the tunnel.
2. Save. Within ten seconds the server generates its own key pair, creates the
   `hakopod-wg` interface and shows its **Server public key**. The private key
   stays in `/etc/hakopod/wireguard/private.key` (mode 0600) and is never sent
   anywhere.
3. Add that public key as a peer on your WireGuard server, with the tunnel
   address as its allowed IP.
4. Add the VPN's client networks to **Allowed networks** so people on the VPN
   are admitted, then confirm from the VPN.

The section shows the last handshake. Only the listed networks are routed
through the tunnel; the default route and the cluster networks
(`10.42.0.0/16`, `10.43.0.0/16`) are refused. Turning the connector off
removes the interface on the next tick. Preshared keys are not supported yet.

### VPN only

**Admit only people on the VPN** turns on `vpn_only`. Every allowed network
must then sit inside a network routed through the tunnel, and it can be turned
on only after a handshake in the last two minutes. With the server firewall:

- the K3s API (`6443`) accepts only the tunnel, cluster nodes and the cluster
  networks;
- the dashboard port stays open so signed Git provider webhooks still arrive,
  and the dashboard refuses every other request from outside the VPN.

If the tunnel goes down while `vpn_only` is on, use SSH or the reset command
above.

## Tailscale, OpenVPN and IPsec connectors

**Settings → VPN → VPN connector** also connects the server to Tailscale (or Headscale),
OpenVPN or IPsec. Only one connector runs at a time; switching removes the
previous one. Install the VPN's own client on the server first: `tailscale`,
`openvpn`, or strongSwan's `swanctl`.

- **Tailscale or Headscale:** an auth key, a hostname, optional tags and,
  for Headscale, the login server. The server joins with
  `--netfilter-mode=off --accept-dns=false --accept-routes=false`, so K3s
  keeps its own firewall rules and DNS. Add your tailnet's addresses
  (normally `100.64.0.0/10`) to **Allowed networks**.
- **OpenVPN:** paste the client profile with inline certificates and keys,
  and list the networks to route. Directives that run programs, load plugins,
  read files or change the default route are refused; pushed routes are
  ignored (`route-nopull`) and `script-security 1` is forced.
- **IPsec:** IKEv2 with a pre-shared key: the remote gateway, local and remote
  identities, and remote networks. Certificate authentication is not supported
  yet.

The auth key, profile and pre-shared key are sealed with the installation's
authentication key, never shown again, and written on the host only to
root-owned 0600 files. Leave the field empty to keep the stored value.
`vpn_only` applies to WireGuard (connector or server) only.

## Hakopod VPN server

If you have no VPN of your own, Hakopod can run one. The module above runs a
WireGuard server on this host:

1. In **Settings → VPN**, press **Set up** under Hakopod VPN server and enter the public endpoint devices dial (for example
   `vpn.example.com:51820`), the UDP port and the VPN network (for example
   `10.100.0.0/24`). Open that UDP port in any cloud or network firewall in
   front of the server.
2. Save. Within ten seconds the server generates its key in
   `/etc/hakopod/wireguard/server.key` (0600) and starts `hakopod-wgs`.
3. Under **Devices**, press **Add device**, name it and press **Create
   profile**, then **Download profile**. The browser generates the device's key pair; only the public key
   is sent, so the profile can be downloaded once. Import it into the
   WireGuard app.
4. On VPN devices, point the dashboard host name at the server's VPN address
   (the network's first address, for example `10.100.0.1`) with split DNS or
   the hosts file. The server forwards the dashboard port and the K3s API from
   that address, so profiles route only the VPN network and never the
   server's public address.
5. Add the VPN network to **Allowed networks**, or turn on **Admit only people
   on the VPN**, and confirm from the VPN.

**Revoke** removes a device on the next tick. Profiles are for installation
administrators in a browser session; agent keys and CLI sessions cannot create
or list them. QR codes for phones are not available yet: send the profile file
to the phone instead.
