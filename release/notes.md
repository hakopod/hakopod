Hakopod 0.1.0-alpha.21 adds service node placement and serverless HTTP functions and containers.

## Node placement

Choose **Run on node** in the dashboard or set `node_name` in a service's TOML to pin all its replicas to an exact Kubernetes node. The scheduler still enforces architecture, taints, resource requests and runtime allocation. Plans check selected-node capacity and reject conflicts with existing local volumes. Pinned services wait for their selected node instead of moving elsewhere.

See [node placement](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.21/docs/node-placement.md).

## Serverless HTTP

Public HTTP services can now sleep at zero replicas when idle and wake on incoming requests. Start an editable JavaScript or Python function in the shared dashboard, or enable Serverless HTTP for an existing HTTP container. Configure startup and request deadlines, concurrency limits and an optional always-warm replica. Requests routing and logs include activation traffic; a manually stopped service remains stopped.

This first version runs zero or one replica. It does not add background/event triggers, a Lambda handler interface or horizontal request autoscaling. Requests have a 4 MiB body limit. Persistent data and background workers should run in regular services.

Fresh native installations configure a private activation listener at the management node's internal IPv4 address on port 8082. Keep this port private to cluster nodes/pods. Existing installations must explicitly enable the gateway before serverless options become available; upgrades preserve their existing settings. Cloud embeddings require separate gateway provisioning. One gateway process owns an installation-wide PostgreSQL lease.

See [serverless configuration and limits](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.21/docs/serverless.md).

## Upgrade

Declared upgrade candidates are alpha.8, alpha.9, alpha.10, alpha.12, alpha.13, alpha.14, alpha.15, alpha.17, alpha.18, alpha.19 and alpha.20. Alpha.11 and alpha.16 have no published installable release.

Download this release's installer.sh, then run:

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.21
```

The installer backs up PostgreSQL and configuration, replaces the API/dashboard and checks readiness. Publishing a release does not upgrade customer VMs or turn existing services into serverless workloads.

## Verification and limitations

The implementation passed the PostgreSQL-backed Go suite, race tests for the gateway/idle lifecycle/cluster, Go vet, command builds, Cloud build tags, all 104 installer tests, dashboard type checking, tests and production build. Independent visual review covered both themes, desktop/mobile, failure/retry and preserved drafts.

A real two-node development K3s test verified exact placement, Python scale-to-zero and wake-up, an always-warm JavaScript function, three concurrent POSTs each reaching the handler once, Requests capture and manual-stop preservation. The final run used automatically generated cross-node network policies without manual edits.

The feature PR's native smoke and installation/upgrade matrix passed. Release asset publication remains gated on fresh artifact builds, native smoke and the complete declared installation/upgrade matrix, including alpha.20. This is an alpha release; load benchmarking, multi-gateway availability and production Cloud provisioning are outside this verification.
