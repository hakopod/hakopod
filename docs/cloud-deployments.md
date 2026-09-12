# Bring Your Own Cloud deployments

Hakopod's commercial cloud toolkit is maintained in the private
`hakopod/hakopod-cloud` repository and linked here at `private/cloud`. Authorized
operators can fetch it with:

```sh
git submodule update --init private/cloud
```

Public builds do not need that repository or its credentials. The parent stores
only a Git reference; release archives and container builds exclude private source.
The open-source Linux installer remains available in this repository.

The initial toolkit provisions one Ubuntu 24.04 server on AWS, GCP or Azure,
with amd64 and arm64 presets. Small has 2 vCPU, 8 GiB RAM and a 100 GiB encrypted
disk. The customer owns the cloud account, Terraform state, data and cloud bill.
The dashboard is private over SSH, while application ports 80/443 are public.

The workflow prepares a separate customer workspace, creates a saved Terraform
plan for review, requires that plan's digest before applying, then installs
verified local Hakopod artifacts over SSH with a pinned host key. It refuses
routine deletion/replacement and never embeds application secrets or license
material in cloud-init. First-time setup lets the customer choose the admin account.

This is the foundation for paid provisioning and operations. It is not a completed
managed-service offering or an HA profile. Mock plans and local workflow tests
cover configuration and guardrails; real cloud installation, reboot, public TLS
and recovery acceptance remain outstanding for each provider and architecture.
Upgrades, ongoing backups, monitoring and support need separate operational work
before service guarantees can be offered.

The private toolkit has its own commercial license. Product Pro entitlements and
the private license issuer remain separate; no issuer secrets are distributed
with cloud provisioning code.
