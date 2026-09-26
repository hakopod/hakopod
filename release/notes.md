Hakopod 0.1.0-alpha.32 fixes a deployment review crash that prevented service resizing.

## Deployment review

Reviewing an ordinary application's configuration could fail with “This page could not load” when no secrets were missing. The shared secret setup component incorrectly treated an absent runner credential as a match. The same crash could occur after saving the last missing secret.

Review now works for applications with no secrets, already-saved secrets, and mixed ordinary services and Managed Actions runners. Repository and organization runner credentials still require real GitHub tokens; random secret generation remains unavailable for those credentials.

This fixes the shared review step used by form and TOML configuration, including lowering CPU and memory requests or limits. It does not change existing service allocations or bypass capacity checks. Reload a failed review, re-enter any unsaved changes, and review before applying.

## Upgrade

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.32
```

Direct upgrades are supported from alpha.30 and alpha.31, the last two published versions. Older installations must upgrade through a supported intermediate release.

## Validation

Regression tests reproduce the prior crash and cover empty or omitted secret requirements, saved secrets and mixed runner scopes. Both dashboard editions are built and tested, with independent UI review of form/TOML resource reductions, secret completion and failed-request draft preservation. Publication remains gated by packaged smoke tests and the bounded 12-case native install/upgrade matrix.
