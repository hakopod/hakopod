# Linux installer

Managed-database backups use the exact owned PostgreSQL container and its pinned
client tools. Existing local or external databases use host PostgreSQL clients;
`pg_dump` must be at least the server major version before enabling backups. The installer creates a private restore staging directory at
`/var/lib/hakopod/backups` and allows only the API service to write there.
Configure an S3-compatible destination in the dashboard after setup. Preserve
`/etc/hakopod`, cluster state and application volumes separately.

This installer installs a dedicated, single-server Hakopod on **Ubuntu 24.04/26.04 or Debian 12/13, amd64 or arm64**, with systemd, cgroup v2 and swap disabled. Missing Python 3.10+ and other distro prerequisites are installed automatically. It is implementation scope, not a claim that every host/architecture combination has passed a full installation. Read the verification record below. At least 4 GiB RAM and 30 GiB free disk are required; allow additional capacity for application pods, rollouts, and optional modules. Runtime caps are conservative, configurable bounds, not measured idle usage or a guarantee that every workload fits.

Use a **fresh dedicated server**. Existing K3s, kubelet, RKE2, Kubernetes state, conflicting ports, service accounts or installer paths are refused. An existing installation can be resumed only with its original configuration, ownership marker and artifact bytes. The installer does not adopt another cluster or Helm installation. Existing PostgreSQL is an explicit option: provide a dedicated empty database owned by its connection role; unrelated databases and services remain untouched. It does not change DNS, your firewall, swap configuration, SSH access, or OS package repositories. No uninstaller or automatic data deletion is provided.

## Prebuilt releases

`scripts/installer.sh` is the POSIX bootstrap for prebuilt releases. The first
planned release is `0.1.0-alpha.2`. The bootstrap pins that version by default;
it does not use GitHub's `latest` endpoint, which excludes prereleases. A tag,
successful release workflow and published assets are required before downloads
work. The `hakopod.com` endpoint also requires separate website deployment.

Once those publication steps are complete, the intended command on a fresh,
root-owned Linux server is:

```sh
curl -fsSL https://hakopod.com/scripts/installer.sh | sh
```

This URL is a deployment target, not a claim that the domain currently serves
the installer. Until then, use the checked-out script with a published release,
or the local artifact workflow below. To inspect a published bootstrap before
running it:

```sh
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.2/installer.sh \
  -o installer.sh
sh installer.sh --help
sh installer.sh --version 0.1.0-alpha.2 --config /path/to/install.json --dry-run
sudo sh installer.sh --version 0.1.0-alpha.2 --config /path/to/install.json
```

The bootstrap supports Linux amd64 and arm64. If Python, Bash, curl or system CA
certificates are missing, it first installs their missing packages from the
configured distro repositories. This small prerequisite phase precedes the
Python configuration review; `--dry-run` prints it without installing anything.
If even curl is absent, download the script from another machine and run it
with the existing POSIX shell. It downloads only the selected architecture, the dashboard and
the small installer kit. Downloads are sequential with size/time bounds; the
SHA256 manifest is verified before extracting or executing the kit. All
redirects stay on HTTPS. The extractor rejects traversal, links, special files
and oversized kits. Temporary downloads are removed when the installer exits.
No compiler or language package manager is used on the target to build Hakopod.

Interactive input comes from `/dev/tty`, so the script pipe is never consumed
as answers. Without a terminal, pass an explicit `--config` and `--dry-run` or
`--yes`. The config version must match `--version`; when omitted, the bootstrap
uses the version in the config. `--resume --config /etc/hakopod/config.json`
downloads that same version and delegates to the existing ownership checks.
Resume does not upgrade or replace installation inputs.

Checksums downloaded with the archive establish consistency, not publisher
identity. Releases also publish GitHub provenance attestations. For an
independent provenance check, download an asset and run
`gh attestation verify FILE --repo hakopod/hakopod` on an operator machine.
The small bootstrap does not install GitHub CLI. See
[release preparation](../release/README.md) for the workflow and remaining host
acceptance requirements.

## Build and review local artifacts

Build the Go release first, then the dashboard/installer kit from stable source:

```sh
pnpm --dir web install --frozen-lockfile
python3 release/build.py --version 0.1.0-dev
python3 release/build-installer.py --version 0.1.0-dev
```

The second builder writes `.local/installer-artifacts/0.1.0-dev/`: both Linux Go archives, one dashboard archive with the actual pure-JavaScript SSR runtime dependency closure, the installer kit, provenance, and `SHA256SUMS`. It preserves installed package license files and emits `runtime-inventory.json` in the dashboard. The dashboard archive contains no package-manager installation step, build toolchain or native addons. Server-rendered imports determine its dependency roots; required runtime dependencies and peers are included with their pnpm peer contexts. Native runtime additions fail packaging and require a future platform-specific bundle. Build in a stable working tree; concurrent source changes cause an explicit failure.

The kit also carries the original Go/dependency-lock SBOMs and collected notice archive with their original scope. The dashboard preserves those collected frontend notices, local component notices and the font's OFL text. `react-remove-scroll-bar` omits a license file from its npm distribution; the builder includes its upstream MIT text and provenance describing the available repository commit, without claiming an exact source-commit match for that npm version. Missing text for any new runtime dependency fails packaging.

`--use-existing-dist` exists only for development packaging/smoke tests. Its provenance explicitly says source freshness is unknown. Build normally again from final source before distributing anything. Installer output is separate from `release/build.py` output, so adding the kit does not alter the original Go verification/SBOM scope. The runtime inventory is an actual package list, not a vulnerability audit or a full OS/image SBOM.

Transfer the artifact directory and verify `SHA256SUMS` through a trusted channel. A checksum delivered with an archive establishes consistency, **not publisher identity**. Upstream K3s, Helm and Node binary URLs and SHA256 values are checked in at `installer/pins.json`; their source URLs and verification date are retained. Downloads are sequential, bounded in time, and verified before execution. The K3s distribution manages its bundled system images; independently installed PostgreSQL, ingress, storage and certificate images use repository digest pins. Internet access to the pinned release hosts and image registries is required; this is not an air-gapped installer.

Extract the installer kit locally and run the included entrypoint. With no `--config` it asks for the inputs below. For a repeatable installation, copy `installer/example.json`, replace the documentation-only domains/address and save it as your own JSON file. JSON is parsed strictly; it is never sourced by Bash. Unknown and duplicate keys fail.

```sh
bash scripts/install.sh --artifact-dir /path/to/artifacts --config /path/to/install.json --dry-run --arch arm64
sudo bash scripts/install.sh --artifact-dir /path/to/artifacts --config /path/to/install.json
```

`--dry-run` is safe on macOS and on disposable Linux containers; it validates input and both local archive checksums and prints the intended paths, routing, modules and memory limits. It does not create installation state, credentials or services. Interactive review may use a temporary input file that is removed at exit. A real install rejects unsupported distributions, insufficient resources and ordinary non-systemd containers before installing the remaining OS packages. The full preflight then checks ports, cluster ownership and routes before creating Hakopod state. Missing Bash, Python 3, curl, CA certificates, OpenSSL, iproute2, util-linux, passwd, kmod and coreutils are installed from existing distro repositories. No global package upgrade or repository change occurs. K3s supplies containerd. `install_docker=true` optionally installs Docker Engine when no existing daemon or client installation would be replaced; the default is false.

The final prompt requires typing `install`. `--yes` accepts that printed plan for an unattended run with explicit config. It never supplies a user identity, owner email, password or OAuth account. Root must trust and review the installer code it executes. Root-controlled local files are outside the hostile-tenant boundary.

## Operator inputs and network access

`app_domain` is your wildcard application domain, for example `apps.your-company.example`. `node_ip` is an IPv4 address assigned to this server and reachable by workers. `supervisor_host` is the stable DNS name or IPv4 address workers use on TCP 6443. `node_name` defaults to `hakopod-server`. Pod/service CIDRs are 10.42.0.0/16 and 10.43.0.0/16; conflicting host routes are refused. IPv6-only host installation and CIDR migration are outside this installer.

The ingress profile has one controller, pinned to the initial node, with host ports 80/443. Traefik and K3s ServiceLB are disabled. The Service remains ClusterIP; administrative/metrics ports are not published on the host. Ingress changes use a Recreate strategy because one node cannot bind the same host ports during a surge; expect a short routing interruption during a controller replacement. This is a single public endpoint, not HA.

Point `*.app_domain` and requested custom domains at your server/public NAT. Allow inbound TCP 80/443 to this node, and forward those same ports through NAT. Restrict TCP 6443 to administrators and enrolled workers, TCP 10250 to cluster nodes, and UDP 8472 to nodes only (Flannel VXLAN must not be exposed publicly). Keep SSH under your normal controls. The installer enables the K3s network-policy controller and does not claim encrypted pod traffic or hostile-tenant isolation. The dedicated same-node database is not an internet-published service. Local host traffic has the CNI limitations described in the networking documentation.

Dashboard access is explicit:

* `dashboard_mode=ssh` (default): binds 127.0.0.1:3000, origin `http://localhost:3000`. Open the printed SSH tunnel and then that exact localhost origin. API binds 127.0.0.1:8080. Use a corresponding SSH tunnel for remote CLI access.
* `dashboard_mode=https`: supply `dashboard_origin=https://console.your-company.example:8443`, `dashboard_port=8443`, and absolute PEM certificate/key paths. The dashboard binds `node_ip` directly with TLS on that separate port. Allow it only to intended operators. The origin must be outside the application domain; the private key must have mode 0600/0400 and match a certificate valid for that hostname. Certificate validity and key matching are checked before installation, and HTTPS readiness uses system trust. The installer does not obtain or renew this operator-supplied dashboard certificate. Renew the copied `/etc/hakopod/dashboard.crt` and `.key` atomically, retain dashboard-user ownership/mode 0400, then restart `hakopod-dashboard`.

There is no public API listener or implicitly trusted reverse proxy. Untrusted application origins receive no dashboard credentials. SMTP and OAuth are disabled unless separately configured with explicit operator credentials after installation.

The interactive installer defaults to `acme=production` for Let's Encrypt application HTTPS and asks for a contact `acme_email`; it requires public DNS and reachable port 80. The included example deliberately sets `off` for a safe review/test configuration: that choice keeps cert-manager off and public applications initially use HTTP until TLS is configured. `staging` installs pinned cert-manager plus a staging ClusterIssuer; its test certificates are **not browser-trusted**. The email is an ACME contact, not a Hakopod owner identity. HTTP-01 handles individual names; no wildcard DNS-01 automation is installed. Inspect actual issuer/certificate Ready conditions in Hakopod. cert-manager renews application certificates it manages. Local automated tests explicitly disable issuance.

`storage=false` is the default. When enabled, the separately pinned local-path module supplies node-local application volumes under `/var/lib/hakopod/application-volumes`; these use Delete reclaim and are not replicated backups. In managed database mode, platform PostgreSQL uses its own static 20 GiB PV with Retain regardless of this option. The static PV's declared size is scheduling metadata, not a filesystem quota. Database data stays under `/var/lib/hakopod/postgres` on the initial node.

## PostgreSQL selection

`database_mode` defaults to `managed`: install one dedicated PostgreSQL 17 pod
inside the new K3s cluster. Its data, namespace, password and storage remain
installer-owned. No PostgreSQL server package is installed on the host.

Choose `local` for an existing server reachable by loopback TCP, or `external`
for a remote PostgreSQL service such as RDS. These modes install only missing
PostgreSQL client tools and create no PostgreSQL pod, namespace, PV or host
server. The operator supplies an existing dedicated database and a password
for its owner role. Supported server majors are 14 through 18. First-install
checks require a writable primary, CONNECT and public-schema USAGE/CREATE,
the public default schema, and an empty public schema. System databases and
unrelated application schemas are refused. No database, role or existing
application data is deleted or overwritten.

Store the connection URI in a root-owned mode 0600 or 0400 file, then set:

```json
{
  "database_mode": "external",
  "database_url_file": "/root/hakopod-database-url",
  "database_ca_file": "/root/rds-ca-bundle.pem",
  "install_docker": false
}
```

These are additions to the normal installer JSON, not a complete configuration.
The interactive flow also accepts hidden URI input and immediately writes it
to a private temporary file. Percent-encode reserved characters in the username
and password. Never put a connection URI or password in a shell command or
JSON field; the installer accepts only the protected file path.

External connections require `sslmode=verify-full`; the hostname must match
the server certificate. Supply `database_ca_file` for a private/RDS CA, or use
system trust roots. The installer copies a supplied CA into its own state and
rewrites the protected runtime URI. Local mode requires an explicit password
and a loopback TCP endpoint such as `127.0.0.1`; Unix-socket and OS peer-auth
connections are not supported by the isolated API service. Remote IAM token
rotation, automatic RDS creation, replica management and existing-schema
migration are outside this installer.

Database probes are read-only, time-bounded and redact server diagnostics.
Connection success does not prove that a host `pg_dump` is new enough for
backups: install a compatible client from a trusted distro repository and run
a restore drill before enabling management backups against an external server.

## Runtime limits and credentials

| Component | Default hard limit | Runtime settings |
| --- | ---: | --- |
| K3s service | 2048 MiB | Go soft target 1536 MiB, 2 processors, max 50 pods |
| API + reconciler | 256 MiB | Go soft target 192 MiB, 2 processors, one process |
| Dashboard | 320 MiB | One Node process, 192 MiB JS heap, request/header timeouts |
| PostgreSQL | 256 MiB | shared_buffers 32 MiB, 30 connections, work_mem 2 MiB |
| HAProxy | 256 MiB | 2 threads, max 1024 connections, Go soft target 160 MiB |
| Optional cert-manager | 384 MiB total controllers | 2 concurrent challenges; solver 64 MiB each |

systemd limits are hard limits on each service cgroup; pod limits apply separately. Go/Node soft targets are not RSS promises. OOM events cause restarts and require reducing load or increasing the reviewed caps. The installer does not run builders, Prometheus, Grafana, Redis or a queue service. Docker Engine is installed only when explicitly requested. Optional application images have their own explicit resource requirements.

API and dashboard run under separate non-login users. Only the API receives the protected Kubernetes admin kubeconfig; the management process is privileged within its dedicated cluster. Applications receive no cluster credentials. Database credentials and setup/auth keys use the server's protected-file inputs. A root-only canonical database URL supports resume; the API receives a separate mode 0400 copy. Root-owned EnvironmentFiles supply cookie-encryption secrets. Files are never written into the dashboard client bundle or printed by the installer. Logs do not include generated secret bodies; the dashboard launcher does not log request URLs that might contain OAuth codes.

After health checks pass, the installer prints the dashboard address and a command to privately read `/etc/hakopod/secrets/setup-token`. Enter this proof in setup and choose your own name, email and password. The installer never runs legacy machine-owner bootstrap, selects an owner email or invents a password. After the first owner exists, the API enforces completed setup. Keep the token private even then.

## Resume, backups and recovery

```sh
sudo bash scripts/install.sh --artifact-dir /path/to/original-artifacts \
  --config /etc/hakopod/config.json --resume
```

A lock prevents concurrent installation. The marker records a random installation ID and a fingerprint of configuration, pins and verified artifact bytes. Moving identical artifacts does not change their identity. Changed config, versions or artifacts are refused; **resume is not an upgrade or configuration-management command**. Each external step checks its own result and owned resources. Incomplete Kubernetes/Helm creation is retried; Helm refuses foreign resource ownership. An established secret set is never regenerated during resume. Restore missing secrets from backup. The installed dashboard certificate/key are preserved on resume, including operator renewals; original temporary input files are no longer required once those copies exist. Database URL and CA copies are bound to the marker by SHA256; their saved copies are used on resume and their original input paths may be gone. Editing saved database credentials or trust roots requires a separate reviewed rotation procedure, not `--resume`. A failure retains diagnostics/state and says what to inspect; it does not roll back by deleting data.

Use `systemctl status hakopod-k3s hakopod-api hakopod-dashboard` and `journalctl -u <service>` locally. A management outage does not put existing application traffic through the API/database. API writes and new reconciliation stop if PostgreSQL fails; existing K3s workloads continue subject to node/network health. K3s/node loss on this single server affects both apps and management. The database is single-node and does not become highly available merely by adding workers.

Back up PostgreSQL consistently (`pg_dump` or a tested physical backup), `/etc/hakopod` including all secrets and the marker, and K3s data/encryption material in `/var/lib/hakopod/k3s`, using the upstream K3s backup procedure appropriate to its SQLite datastore. Keep the original artifacts and checksums. A raw live filesystem copy is not a tested PostgreSQL backup. Auth-encryption key loss prevents recovery of encrypted authentication state; rotating it by replacing the file is unsupported. Restore drills on a disposable supported Linux host are required before relying on backups. The installer never deletes retained DB storage or attempts an automatic uninstall.

## Verification record

Automated checks live in `installer/test_installer.py`. The separate `release/smoke-installer.py` runs the actual packaged dashboard/Go CLI on a disposable memory-limited Linux container, on both architectures when emulation is available. It never mounts the host Docker socket or runs installation on macOS. Test reports distinguish archive/config/runtime verification from full host installation. **A full systemd/K3s host install, reboot/recovery drill and public ACME issuance must be verified on disposable supported Linux hosts before claiming production support.** Cross-compilation and a container smoke are not evidence of those behaviors.

The JSON setting `deployment_mode` accepts `self-hosted` (the default) or
`managed-cloud`. The interactive installer asks for this first. The chosen mode
is written to the server's `HAKOPOD_DEPLOYMENT_MODE` environment variable; its
operator TOML equivalent is `[server] deployment_mode`. Applications, dashboard
roles and licenses cannot change or override this server setting. Self-hosted
installations may run on cloud VMs whose administrator controls ingress ports.

In self-hosted mode, an administrator can provision any available non-platform
TCP ports from 1 through 65535. Set `public_tcp_ports` to an explicit JSON array
such as `[587, 12345]`, or use the interactive prompt, with at most 256 ports.
Empty is the default. The plan lists the chosen ports, and preflight refuses
conflicts before installation. The installer provisions matching HAProxy ports
and the API allowlist; firewall rules remain operator-owned. Applications still
need reviewed `public_tcp` listeners and never provision additional ports
automatically.

Managed-cloud mode rejects nonempty `public_tcp_ports` and skips the public TCP
prompt. It does not expose public TCP; private service-to-service TCP remains
available. For a later mode change, remove public TCP listeners while still
self-hosted and wait for their removal to succeed, then clear the allowlist and
change the server mode. Startup refuses leftover managed listeners or
unacknowledged removals. Changing an environment variable does not itself close
host ports or cloud firewalls. The installer's `--resume` continues to require
the original configuration; it is not a way to change deployment mode.

Public SMTP servers, SFTP and custom public protocols belong on self-hosted
installations, including customer-owned BYOC. A shared SMTP gateway is outside
Hakopod Cloud's scope. See [product modes](../docs/product-modes.md),
[public TCP](../docs/public-tcp.md) and [SMTP migration](../docs/smtp-migration.md).
