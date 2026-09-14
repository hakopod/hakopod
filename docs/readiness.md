# Listener readiness

An HTTP health endpoint can succeed while another listener is unavailable.
For services such as SMTP, configure `readiness` to check that listener too.
When `healthcheck` is present, both checks must pass before Kubernetes marks
the pod ready and includes it in ready Service endpoints.

```toml
[services.mail]
image = "registry.example.com/mail@sha256:YOUR_VERIFIED_IMAGE_DIGEST"
port = 8080
healthcheck = "/health"
ports = [{ name = "smtp", port = 2525, target_port = 2525, protocol = "TCP" }]

[services.mail.readiness]
protocol = "smtp_starttls"
port = 2525
tls_server_name = "smtp.example.com"
period_seconds = 5
timeout_seconds = 3
failure_threshold = 3
```

`readiness.port` is a declared TCP container target port, not the externally
published port. Checks connect only to `127.0.0.1` inside the application pod;
the listener must accept IPv4 loopback connections. Configuration does not
publish a port or change network permissions. Public SMTP remains available only
on self-hosted installations after administrator port provisioning.

| Protocol | What must succeed |
| --- | --- |
| `tcp` | A TCP connection to the listener. |
| `smtp` | A `220` greeting, successful `EHLO`, and `NOOP`. |
| `smtp_starttls` | SMTP greeting and `EHLO`, advertised `STARTTLS`, verified TLS 1.2 or later handshake, another `EHLO`, and `NOOP`. |

SMTP checks never authenticate, submit a message, or test delivery. A healthy
listener does not prove AWS permissions, outbound delivery, DNS, queues or
provider quotas. For those checks, use application metrics and delivery alarms.
STARTTLS readiness rejects expired, untrusted and wrong-hostname certificates;
it has no option to skip verification. Implicit TLS on port 465 is not supported
by these SMTP probes.

`smtp_starttls` requires `tls_server_name`. Public certificate trust comes from
the helper image's root certificate bundle. For a private CA, add
`tls_ca_file = "/certificates/trust/ca.pem"` pointing to a readable PEM bundle
inside the container. This file supplies the complete trust set for the check.
The bundle is read afresh for each probe, never copied into the application
revision. Do not point it at a private key. A fixture may explicitly trust its
self-signed leaf certificate through a certificate mount's `tls.crt`.

The total timeout covers HTTP and SMTP together. The default is three seconds;
allowed values are one through ten seconds, less than the period. The period
defaults to five seconds and accepts three through sixty seconds. One through
ten consecutive failures remove readiness, defaulting to three; one success
restores it. Startup retains the separate TCP startup probe. Readiness failures
do not introduce a liveness restart policy. Existing established connections may
continue after endpoint removal; this does not forcibly close them.

## Helper installation

The dashboard links readiness errors to **Infrastructure > Setup**, which shows
the current helper and the exact operator configuration steps. See
[installation maintenance](installation-maintenance.md#smtp-listener-readiness).

TCP-only readiness uses the native kubelet TCP probe. SMTP and combined HTTP plus
TCP checks need the Hakopod probe helper. Releases with `probe-image.txt` include
a prebuilt public image at `ghcr.io/hakopod/hakopod-probe:<release-tag>` for
`linux/amd64` and `linux/arm64`, built from `Dockerfile.probe`. The release tag
includes its `v` prefix. No compiler, Docker daemon or registry login is needed
on the installation target; K3s pulls the helper when an application needs it.

Open the [release matching your installation](https://github.com/hakopod/hakopod/releases),
download `probe-image.txt`, and use the **complete digest-pinned reference** in
that file. The digest identifies the multi-platform index, so the same setting
works on both architectures. Mutable tags and `latest` are not accepted by the
operator configuration. `probe-image.json` records the source revision,
platform image digests and native packaging smoke results. Both files are covered
by the release's `SHA256SUMS` and artifact attestations; verify the downloaded
file with `gh attestation verify probe-image.txt --repo hakopod/hakopod`.
The index also has registry provenance, verifiable with
`gh attestation verify oci://ghcr.io/hakopod/hakopod-probe@sha256:RELEASE_DIGEST --repo hakopod/hakopod`.
Replace the placeholder below with the reference from your verified file:

```toml
[server]
readiness_probe_image = "ghcr.io/hakopod/hakopod-probe@sha256:RELEASE_DIGEST"
```

The equivalent environment setting is `HAKOPOD_READINESS_PROBE_IMAGE`. Plans and
deployments reject helper checks until this setting exists. The digest is an
operator setting, so application TOML cannot replace the executable. Private
registries need node pull access or compatible configured image pull secrets.

For a systemd installation, run `sudo systemctl edit hakopod-api` and add:

```ini
[Service]
Environment="HAKOPOD_READINESS_PROBE_IMAGE=ghcr.io/hakopod/hakopod-probe@sha256:RELEASE_DIGEST"
```

Replace the whole example reference with `probe-image.txt`, then run
`sudo systemctl restart hakopod-api`. Check **Infrastructure > Setup** for the
configured reference, review the application deployment again, and redeploy it.
Changing this setting does not replace helpers in already running pods.
Upgrades remain explicit: review the new release, update the pinned digest,
restart the API and redeploy affected services.

Older releases without `probe-image.txt` still require a local helper build.
For those releases or an offline/private mirror, build `Dockerfile.probe` from
the matching source tag, publish it to a registry reachable by your nodes, and
configure its verified digest. Mirroring can change the index digest; use the
verified destination digest. The public image does not enable public SMTP in
Hakopod Cloud or bypass administrator port provisioning.

A nonroot init container copies the static executable and public trust bundle
into a 40 MiB disk-backed `emptyDir`. The application receives a read-only mount
at `/var/run/secrets/hakopod-probe`; no shell, curl, netcat, extra Kubernetes
credentials or persistent sidecar is required. The init container has a 64 MiB
memory limit and 100m CPU limit. Each probe is a short-lived process within the
application's resource limit, with `GOMAXPROCS=1` and a 24 MiB soft Go
memory target. SMTP replies are capped at 32 lines and 16 KiB total, individual
lines at 4 KiB, and custom CA bundles at 1 MiB. There is no stored probe history
or background polling service. Helper or root-bundle image upgrades apply when
the workload is redeployed.

## Development verification

`TestLiveSMTPReadiness` is opt-in and refuses any context except
`k3d-hakopod-dev`. Import a locally built digest-pinned helper and set
`HAKOPOD_TEST_READINESS_IMAGE`, `HAKOPOD_TEST_KUBECONFIG`, and
`HAKOPOD_READINESS_TEST=1`. The isolated fixture checks HTTP success with SMTP
initially stopped, SMTP listener failure and recovery, HTTP failure and recovery,
verified STARTTLS, wrong-hostname rejection, ready Service endpoint changes,
nonroot execution and absence of workload restarts or a sidecar. It sends no
email and creates no public route. This fixture is protocol acceptance, not
verification of a production mail provider or an application's delivery path.

## Xem self-hosted configuration

Keep Xem's HTTP `/health` check and add `smtp_starttls` readiness on its configured
SMTP container port. Set `tls_server_name` to the SMTP hostname and mount the
matching backend certificate; see [backend certificate renewal](backend-certificates.md).
Configure SMTP settings and secrets according to the deployed Xem version. Its
outbound `SMTP_HOST`/`SMTP_PORT` delivery settings are distinct from the listener
port checked here.

Xem's deployment guide requires one managed-sending runtime and stop-first
updates against a shared queue. Configure the backend service accordingly:

```toml
[services.backend]
# Keep the verified image, HTTP port, listener ports and other existing settings.
replicas = 1
update_strategy = "recreate"

[services.backend.readiness]
protocol = "smtp_starttls"
port = 2525 # Replace with the actual SMTP container listener port.
tls_server_name = "smtp.example.com"
```

`update_strategy` accepts `rolling` (the default) or `recreate`. Recreate stops
the old revision before starting the replacement, including automatic certificate
renewal. It causes a short interruption; clients need retries. Do not enable
autoscaling or multiple replicas for a singleton mail queue. Services with
persistent mounts continue to use Recreate regardless of this setting. Neither
strategy prevents a cluster administrator from manually running another workload
against the same queue. Validate migrations, backups, AWS identity, sending and
feedback handling separately before changing production traffic.
