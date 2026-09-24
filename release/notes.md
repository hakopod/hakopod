Hakopod 0.1.0-alpha.22 adds Pro custom project roles and organization-wide MFA enforcement, plus trusted embedding hooks for hosted compute and deployment admission.

## Pro access

Signed licenses with the custom_roles and team_mfa capabilities enable custom project permissions and organization MFA policy. Role changes apply on the next authorization; expired licenses remove custom role authority. Required MFA remains enforced until a verified administrator explicitly disables it. TOTP, recovery codes and user-verified passkeys establish session assurance, including device consent. Required final factors cannot be removed.

Core deployments, rollback, backups, fixed roles and personal MFA remain Free. This public self-hosted release does not include private Cloud deployment approvals or a hosted subscription.

## Authentication and embedding

Email verification recovery is shown instead of silently repeating login. Public embedding interfaces support bounded hosted compute, refreshed workspace authorization, external factor policy and transactional deployment admission. Private Cloud source is not included.

## Upgrade

Supported upgrade sources are alpha.8, alpha.9, alpha.10, alpha.12, alpha.13, alpha.14, alpha.15, alpha.17, alpha.18, alpha.19, alpha.20 and alpha.21. Back up the installation before upgrading. Download this release's installer.sh and run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.22
```

Publishing this release does not upgrade existing installations automatically.

## Validation

The PostgreSQL-backed Go suite, focused role/MFA/device/admission tests, generated API contracts, dashboard type checking, production build and dashboard tests passed locally. Independent UI review covered synthetic role and MFA states in both themes at mobile and desktop widths. Live customer mutations are not claimed. Publication is gated on native package smoke and the declared installation/upgrade matrix.
