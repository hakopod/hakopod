Hakopod 0.1.0-alpha.74 lets installation administrators buy Hakopod Pro from the dashboard and trusts the license key that the hakopod.com billing service signs with.

- Settings → License has a **Buy Pro** button for installation administrators, or **Renew Pro** once Pro is active. It opens the Pro checkout on hakopod.com in a new tab with this installation's ID filled in. Paste the license you receive into the same page to activate it.
- Releases now trust a second license verification key, `hakopod-web-2026-11`, which the billing service on hakopod.com uses to sign Pro subscriptions. Licenses signed with the existing `hakopod-2026-09` key keep working. Earlier releases do not trust the new key, so upgrade before activating a license bought on hakopod.com.

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
