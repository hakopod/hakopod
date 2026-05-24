# Optional certificate controller

Uploaded PEM certificates work with HAProxy without this module. Install this
module only for automatic ACME issuance and renewal. It adds three controllers
with a combined 384 MiB memory limit; HTTP-01 solver pods are limited to two
concurrent challenges and 64 MiB each by issuers created through Hakopod.

The vendored chart is cert-manager v1.21.2 from
`oci://quay.io/jetstack/charts/cert-manager`, OCI chart digest
`sha256:634dce9c13b56677a2c05e2ab76c312d0be2664022d5dd05815da67e1fd5f610`.
`checksums.sha256` verifies the downloaded archive. Runtime controller and solver
images are pinned to multi-platform manifest digests in `values.yaml` and
`images.json` (verified for Linux amd64 and arm64 on 2026-09-12).
The chart and upstream project use the included Apache-2.0 license.

Use an explicit kubeconfig and context:

```sh
bash deploy/cert-manager/install.sh .local/kubeconfig k3d-hakopod-dev
```

For another cluster, supply its actual kubeconfig and context. The script refuses
to adopt an existing certificate installation owned by another Helm release.
CRDs are preserved on uninstall to protect stored certificate resources.

Create a ClusterIssuer from TLS settings or `POST /api/v1/tls/issuers`. The API
defaults to Let's Encrypt staging; set `production: true` explicitly for trusted
certificates. Service attachment creates a deployment revision and cert-manager
Ingress annotation. Issuer account credentials remain in Kubernetes Secrets.

HTTP-01 needs public DNS pointing at HAProxy and inbound public TCP port 80.
Local loopback domains and development ports cannot complete public issuance.
Hakopod opens only the solver's TCP 8089 from the configured HAProxy controller
pods through its default-deny application policy. Solver pods use ClusterIP,
non-root execution, bounded resources, and the upstream restricted container
security settings. External DNS and issuance success are not inferred from
configuration: the API reports actual issuer conditions and whether a current
hostname-matching certificate Secret is attached to the Ingress.

The low-memory default does not install this module or a monitoring stack.
For large certificate inventories, measure these limits and adjust them before
increasing challenge concurrency.
