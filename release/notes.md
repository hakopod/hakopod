Hakopod 0.1.0-alpha.74 lets installation administrators buy Hakopod Pro from the dashboard, trusts the license key that the hakopod.com billing service signs with, lets an administrator change public TCP ports after installation, lets an ingress certificate mount follow the generated hostname and adds a Maddy SMTP template.

- Settings → License has a **Buy Pro** button for installation administrators, or **Renew Pro** once Pro is active. It opens the Pro checkout on hakopod.com in a new tab with this installation's ID filled in. Paste the license you receive into the same page to activate it.
- Releases now trust a second license verification key, `hakopod-web-2026-11`, which the billing service on hakopod.com uses to sign Pro subscriptions. Licenses signed with the existing `hakopod-2026-09` key keep working. Earlier releases do not trust the new key, so upgrade before activating a license bought on hakopod.com.
- `sudo python3 ./installer/modules.py public-tcp --ports 587,465` changes the public TCP ports after installation. It takes the complete new list, refuses to drop a port an application still claims or to add one already in use on the host, then updates the ingress, the API settings and restarts the API. HTTP ingress pauses briefly while the HAProxy controller restarts, and installer `--resume` no longer applies to that host. See docs/public-tcp.md.
- An ingress certificate mount without a `hostname` now follows the service's generated hostname, so a template can mount the certificate before it knows the application's identity. On a first release the container starts with the certificate mounted. Uploaded certificate mounts still need a hostname. See docs/backend-certificates.md.
- The catalog has a **Maddy SMTP** template: a send-only SMTP submission server for your own apps on port 587 with STARTTLS, using the certificate Hakopod issues for the service's generated hostname. It signs mail with DKIM and delivers it directly or through a relay such as Amazon SES, Mailgun or Postmark. It needs public TCP port 587; direct delivery also needs outbound port 25 and reverse DNS. There is no inbound mail or IMAP.

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
