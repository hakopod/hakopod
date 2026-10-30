Hakopod 0.1.0-alpha.73 lets an installation keep its dashboard and API off the public internet, adds VPN access for administrators, fixes `hakopod exec` after a CLI login and allows API keys that never expire.

- Settings → Network access limits the dashboard, the API, the CLI, the SDK and MCP to the networks you list. Signed GitHub and GitLab webhooks stay reachable and SSH to the server always works. A saved policy reverts after five minutes unless you confirm it from a network it allows, and `hakopod-server access-reset` on the server turns it off. It needs a Pro license with the Private management access feature.
- Settings → VPN can run a WireGuard server on the server for administrators, with one profile per device generated in the browser, or connect the server to a VPN you already run: WireGuard, Tailscale or Headscale, OpenVPN, or IPsec. Admit only people on the VPN, and close the dashboard port and the Kubernetes API in the server firewall. The VPN and the firewall run in hakopod-vpn, a separate agent that `sudo python3 ./installer/modules.py private-access` downloads from this release only when you install it.
- `hakopod login --allow-exec` now requests the pods:exec permission, so `hakopod exec` works after the browser approval. `--allow-sql` and `--allow-sql-write` request the SQL permissions. A session without the permission now gets an error that names the login command. Log in again once to use exec.
- Administrators can create API keys that never expire for CI and other long-lived integrations, from Settings or with `hakopod key-create --never-expires` in a browser-approved session. Revoke them when the integration no longer needs them.
- The sign-in, account and password screens now use the dashboard's own controls and theme.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.73/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.73
```

Upgrade an alpha.71 or alpha.72 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.73/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.73
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
