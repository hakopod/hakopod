Hakopod 0.1.0-alpha.28 enables production Pro license activation and explicit lifetime owner grants.

## Pro license activation

Official binaries now include the vendor public verification key. The signing key remains outside the source repository, release artifacts and customer installations. Activate an installation-bound license in **Settings → License**. A trusted issuer alone does not enable paid features.

## Lifetime owner grants

An explicitly signed lifetime Pro grant has no expiry. Subscription licenses continue to require a finite expiry and renewal. Both enforce installation identity, not-before time, signed entitlements and monotonic sequences. Removing a lifetime license disables paid features and prevents reuse of that token; reactivation requires a newly issued higher sequence.

## Upgrade

Back up the installation and run the release installer:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.28
```

Supported sources include alpha.27 and its supported predecessors. Earlier releases cannot verify the new vendor licenses and reject lifetime claims. Existing time-limited v1 licenses remain compatible when their issuer is trusted. This upgrade does not install the optional Managed Actions runtime or change customer workloads.

## Validation

The release is gated by Go/API tests, dashboard checks, artifact inspection and the native installer/upgrade matrix. License tests cover forged signatures, subscription expiry, explicit lifetime validation, installation binding, activation, removal and replay rejection.
