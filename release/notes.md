Hakopod 0.1.0-alpha.30 adds organization-wide Managed Actions runner pools.

## Organization runner pools

Open **Catalog → Automation → Managed Actions** and choose Organization. A repository is no longer required. Pools use GitHub's default runner group, or an explicit runner group ID; repository access stays controlled by the group's policy in GitHub. Repository remains an alternative, and existing repository pools retain their scope.

Organization pools require a GitHub credential with organization **Self-hosted runners: read and write** permission. Repository pools continue to use **Administration: read and write**. The credential stays in control-plane secret storage; jobs receive only a single-job registration. Scope changes drain old runners and recover or remove registrations using their original saved scope.

The dashboard, TOML/API, catalog, review and status screens support both scopes. Secret setup shows the correct permissions, and users without deployment access now see why submission is unavailable.

## Upgrade

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.30
```

Direct upgrades are supported from alpha.27 and alpha.29, the last two published releases. Alpha.28 was not published. Older installations must upgrade through a supported intermediate release. This release retains production Pro license verification and lifetime owner grants from alpha.29.

Managed Actions requires Pro access and the optional sandbox module installed from a verified release kit. An upgrade does not automatically install that runtime or register runners with GitHub.

## Validation

Engine tests with isolated PostgreSQL cover organization routes, default-group discovery, invalid targets, scope changes, interrupted registration and durable cleanup. Dashboard typecheck/build/tests and independent UI review cover both editions and themes, desktop/mobile, scope switching and failed requests. Real GitHub organization registration and workflow execution have not yet been exercised; the existing runner sandbox and pod specification are unchanged.

Publication remains gated by release artifact checks and native fresh-install/upgrade acceptance. The bounded matrix retains 12 host cases across managed, local and external database modes.
