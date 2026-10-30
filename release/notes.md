Hakopod 0.1.0-alpha.74 lets installation administrators buy Hakopod Pro from the dashboard, trusts the license key that the hakopod.com billing service signs with, lets an administrator change public TCP ports after installation and lets an ingress certificate mount follow the generated hostname.

- Settings → License has a **Buy Pro** button for installation administrators, or **Renew Pro** once Pro is active. It opens the Pro checkout on hakopod.com in a new tab with this installation's ID filled in. Paste the license you receive into the same page to activate it.
- Releases now trust a second license verification key, `hakopod-web-2026-11`, which the billing service on hakopod.com uses to sign Pro subscriptions. Licenses signed with the existing `hakopod-2026-09` key keep working. Earlier releases do not trust the new key, so upgrade before activating a license bought on hakopod.com.
- `sudo python3 ./installer/modules.py public-tcp --ports 587,465` changes the public TCP ports after installation. It takes the complete new list, refuses to drop a port an application still claims or to add one already in use on the host, then updates the ingress, the API settings and restarts the API. HTTP ingress pauses briefly while the HAProxy controller restarts, and installer `--resume` no longer applies to that host. See docs/public-tcp.md.
- An ingress certificate mount without a `hostname` now follows the service's generated hostname, so a template can mount the certificate before it knows the application's identity. On a first release the container starts with the certificate mounted. Uploaded certificate mounts still need a hostname. See docs/backend-certificates.md.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.74/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.74
```

Upgrade an alpha.72 or alpha.73 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.74/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.74
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
