# Prebuilt installation verification

The prebuilt installer passed native host acceptance on September 13, 2026 in
[GitHub Actions run 34772511894](https://github.com/hakopod/hakopod/actions/runs/34772511894).
The tested candidate was `0.1.0-alpha.1`, from branch commit
`150d2bce1ab02740b65a92740f1685f509dbebdd` in PR #11. GitHub tested the corresponding
PR merge revision `830c046f4fc0d2938e35e6fbb2a82b60b4ebfb5b`.

| Host | PostgreSQL mode | Result |
| --- | --- | --- |
| Ubuntu 24.04, native amd64 | Installer-owned K3s pod | Passed |
| Ubuntu 24.04, native arm64 | Installer-owned K3s pod | Passed |
| Ubuntu 24.04, native amd64 | Existing local PostgreSQL over loopback TCP | Passed |
| Ubuntu 24.04, native amd64 | Existing PostgreSQL with verified TLS | Passed |

Each job installed the exact downloaded, checksum-verified archives on a fresh
GitHub-hosted VM with systemd and cgroup v2. Both the API and dashboard became
healthy, the Kubernetes node reached Ready, and first-user setup remained open.
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

The release workflow rebuilds tagged source and repeats native host acceptance
and container smoke checks before publication. It verifies that the reports
match the release revision and artifact hashes, then includes them with the
checksummed and attested release assets. A passing candidate run is not itself
a published release.
