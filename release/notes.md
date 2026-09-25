Hakopod 0.1.0-alpha.27 adds Pro Managed Actions runner pools.

## Run GitHub Actions on your installation

Open **Catalog → Automation → Managed Actions** to configure a repository, application-scoped GitHub credential, labels and concurrent job slots. Review the configuration before deploying. The service page reports observed GitHub runner state and retains cleanup status after a service is removed.

Each job uses a fresh runner and a separate Docker daemon inside the dedicated gVisor sandbox. Shell and JavaScript steps, Docker actions, job containers, service containers and Docker builds use that isolated runtime. The host Docker socket and cluster credentials are never exposed. A slot requires at least a 4 GiB memory limit and has bounded temporary storage.

Managed Actions requires an explicit `managed_actions` entitlement in a signed Pro license. After upgrading an installer-managed single-server installation, extract the verified release kit and run:

```sh
sudo python3 ./installer/modules.py managed-actions
```

This optional maintenance operation restarts K3s and preserves its existing runtime configuration. External or multi-node clusters need operator-provisioned sandbox nodes. See `docs/managed-actions.md` for the full setup and limits.

## Pool lifecycle

Replica reductions, configuration changes, pauses and license expiry drain busy runners. Deleting a service cancels its jobs. Registration intent and cleanup state survive control-plane restarts, and provider outages are retried without exceeding the desired slot count. Repository administration credentials stay in control-plane storage; pods receive only single-job registration material.

## Upgrade

Back up the installation and run the release installer:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.27
```

Supported upgrade sources include alpha.26 and its supported predecessors. The optional runner runtime is not installed automatically. Cloud hosted compute uses a separate trusted Team grant; this release does not automatically migrate BYOD nodes or grant them Cloud Team access.

## Validation

Release publication requires the Go and dashboard suites, installer checks, native host installation/upgrade evidence and artifact inspection. Managed Actions has separate real GitHub job acceptance and Docker sandbox checks. The independent UI report records 93 fixture cases across both themes, desktop and mobile, including keyboard/touch controls, failed requests, long values and deletion cleanup.
