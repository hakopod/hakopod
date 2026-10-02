# Managed Neon source and image preparation

This directory keeps Hakopod's Neon source preparation reproducible. The
upstream revision and patch hashes are fixed in `source-metadata.toml`.
`prepare-source-archive.sh` clones that revision and its pinned PostgreSQL
submodules, applies the existing proxy timeout patch, then applies the provider
ownership patch. It writes a normalized archive twice and requires the two
copies and the globally sorted member list to match.

Run source preparation on the build VM:

```sh
./hack/managed-neon/prepare-source-archive.sh /path/to/output
```

Extract the resulting archive and run image stages one at a time:

```sh
./hack/managed-neon/build-native-images.sh /path/to/neon-provider-source.tar.gz /path/to/output storage
./hack/managed-neon/build-native-images.sh /path/to/neon-provider-source.tar.gz /path/to/output compute-tools
./hack/managed-neon/build-native-images.sh /path/to/neon-provider-source.tar.gz /path/to/output compute-runtime
```

The storage image contains `storage_controller`, `pageserver`, `safekeeper`,
and `proxy`. The compute-tools checkpoint builds `compute_ctl`, `fast_import`,
and `local_proxy`; the final compute phase builds the PG17 runtime with the
upstream extension set. An isolated BuildKit container limits each sequential
build to one CPU and 7 GiB memory with swap disabled. Each phase refuses to start or finish below
12 GiB free disk.
Set `HAKOPOD_NEON_BUILDKIT_ROOT` to a directory on the scratch filesystem when
the Docker data root cannot hold build layers.

These scripts keep images in the VM-local daemon. Building an image does not
publish or qualify it. Record immutable image digests and complete native
acceptance in the named development cluster before changing qualification
gates.
