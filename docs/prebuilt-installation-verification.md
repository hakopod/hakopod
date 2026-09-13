# Prebuilt installation verification

The first published prerelease is
[`v0.1.0-alpha.2`](https://github.com/hakopod/hakopod/releases/tag/v0.1.0-alpha.2),
from source `0c9a46e3c0fde76251ab62f0b7bf9db09f785d8f`.
[Release run 34776648368](https://github.com/hakopod/hakopod/actions/runs/34776648368)
passed the build, packaged smoke on both architectures, all four native host
checks and publication. It published on September 13, 2026 UTC
(September 14 in Asia/Kolkata).

The candidate also passed
[run 34776024042](https://github.com/hakopod/hakopod/actions/runs/34776024042)
before PR #13 was rebased onto main. Release acceptance rebuilt and checked the
exact tagged source, including the dashboard favicon change.

| Host | PostgreSQL mode | Result |
| --- | --- | --- |
| Ubuntu 24.04, native amd64 | Installer-owned K3s pod | Passed |
| Ubuntu 24.04, native arm64 | Installer-owned K3s pod | Passed |
| Ubuntu 24.04, native amd64 | Existing local PostgreSQL over loopback TCP | Passed |
| Ubuntu 24.04, native amd64 | Existing PostgreSQL with verified TLS | Passed |

Each job installed the exact downloaded, checksum-verified archives on a fresh
GitHub-hosted VM with systemd and cgroup v2. Both the API and dashboard became
healthy, the Kubernetes node reached Ready, first-user setup remained open,
and the public signup status remained disabled.
Resume retained the installation identity and every saved secret. Restarting the
API and dashboard services recovered without creating an account.

The existing-database cases used a separate PostgreSQL 16.15 fixture on the same
VM. Both refused a database containing existing application objects before
creating installation state, and preserved a separate sentinel database through
installation and resume. The external mode connected using a verified test CA
and refused an untrusted certificate before cluster mutation.

The first native run found a startup race: the Kubernetes API became ready before
the kubelet registered its node. The installer now waits for node creation before
checking its Ready condition. The passing run above includes that fix.

The jobs used the default configured limits: K3s 2048 MiB, API 256 MiB, dashboard
320 MiB, managed PostgreSQL 256 MiB, and HAProxy 256 MiB. These are configured hard
limits, not measured idle-memory figures or workload capacity guarantees. Docker
installation, optional application storage and ACME were disabled for these runs.

This evidence does not cover a host reboot, public DNS or ACME issuance, a physical
AWS RDS instance, a restore drill, other supported Linux distributions, or the
existing-database modes on arm64. The bootstrap's missing-prerequisite and pipe
handoff cases are covered by isolated tests; these host jobs ran the verified
installer kit directly and do not prove the public website download endpoint.

The release includes `host-acceptance.json`, both native smoke reports,
`SHA256SUMS` and GitHub build-provenance attestations. Reports are bound to the
release revision and artifact hashes. The public binaries carry the explicit
`hakopod_selfhosted` build tag; environment variables and operator TOML cannot
enable public signup in them. First-owner setup and licensed invitations remain
available through their separate authorization paths.

## Website bootstrap

The website serves the release's exact `installer.sh` at
<https://hakopod.com/scripts/installer.sh>. Its SHA-256 is:

```text
d2241f2d8be7a280430b08a095ff4023d08f347fe522c0965e766d0198e0a49d
```

This endpoint is deployed separately from GitHub releases and pins
`0.1.0-alpha.2`. A future tag does not update it automatically. The served script
is checked against the published manifest and signed provenance before website
deployment. HTTPS, the response type and exact bytes are checked after deployment.

The public endpoint check proves download identity and the bootstrap handoff;
the native runner evidence above covers full installation. It does not imply a
production deployment on a customer's server.
