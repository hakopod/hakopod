Hakopod 0.1.0-alpha.25 adds reviewed offline volume growth and shrinking, plus optional volume deletion when removing a service.

## Resize persistent volumes

Use Resize in a service's storage section. The engine stops affected services, copies into a new volume and verifies contents and metadata before switching mounts. Shrinking never truncates the original filesystem. Both volumes remain reserved until you explicitly confirm deletion of the original after checking the application. Interrupted maintenance can be retried; pre-switch cancellation resumes the original, while post-switch recovery preserves both copies without rolling back new writes.

The shared API and dashboard support self-hosted, Cloud hosted and upgraded BYOD engines. Targets range from 1 to 200 GiB and must fit the data plus free-space headroom. Physical provider capacity and workspace allowances still apply. Single-replica services with compatible file ownership are supported; scheduled jobs, deployment jobs, serverless scaling and block-device volumes are excluded. See `docs/volume-resizing.md` for recovery and provider limitations.

Resizing creates a new named volume and mount subdirectory. Update the resulting volume and mount settings in Git before redeploying.

## Delete unused service volumes explicitly

Service deletion offers an unchecked option to permanently remove its unused volumes. Shared mounts are retained. Cleanup is retryable, requires current management authority, and releases storage reservations only after owned storage is reclaimed. Backups remain intact.

## Upgrade

Back up the installation, download this release's installer.sh, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.25
```

Supported upgrade sources are declared in `release/upgrade-paths.json`, including alpha.24 and its supported predecessors. Schema 42 adds the durable resize journal. No existing customer volume is resized or erased by upgrading. BYOD nodes need the new engine version before they can execute resizing.

## Validation

PostgreSQL-backed tests cover maintenance exclusion, storage accounting, durable transitions and permission revocation. Dashboard builds and tests plus independent rendered reviews cover both editions/themes and mobile/desktop recovery controls. Publication also requires the native installation/upgrade matrix. The development-cluster resize test checks growth, shrink, data/link preservation and rejection of an undersized target; this does not certify every CSI provider or resize customer volumes.
