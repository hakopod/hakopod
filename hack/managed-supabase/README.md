# Public Supavisor candidate

This directory prepares the patched Supavisor `v2.9.12` pooler for publication as `ghcr.io/hakopod/managed-supabase-pooler:v2.9.12-hakopod.2`. It does not publish or qualify the image.

The Dockerfile pins the frontend, builder and runtime images; the upstream commit archive; the Hakopod TLS patch; rustup-init; Rust; Hex; Rebar; and the Debian snapshot. The resulting config identifies the canonical Hakopod source and records the upstream Supavisor repository, commit and source digest plus the patch and rustup-init digests.

Run `build-pooler.py` only on the resized 32 CPU acceptance VM after the native lane is explicitly released. Use a fresh absolute output directory on `/srv/hakopod-build`. The helper runs BuildKit with four CPUs, 12 GiB memory, no additional swap, a 20 GiB free-space reserve, bounded logs and a two-hour deadline per export. It produces two cached exports and requires an identical manifest, config and layer graph. This checks deterministic re-export from one build cache; it is not an independent no-cache reproducibility claim.

The generated `build-receipt.json` records the new immutable archive, manifest, config and layer hashes. Publication, anonymous pull verification, runtime identity inspection and the full 25-case native acceptance remain separate gates.

`Dockerfile.realtime` builds the pinned Realtime candidate with the authoritative
`patches/realtime-v2.134.10-hakopod-tls.patch` from the repository root. The
patch enforces verified TLS for both the control repository and tenant or
replication connections. Its provenance record is
`patches/realtime-v2.134.10-hakopod-tls.provenance.json`. Keep the image
unqualified until both paths pass correct-CA, wrong-CA, wrong-host and plaintext
native checks.
