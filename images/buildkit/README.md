# Managed BuildKit candidate

This candidate corrects an overlay filesystem compatibility issue in BuildKit
v0.32.2 when used in Hakopod's gVisor sandbox. Writable overlays select the user
attribute namespace, while stock BuildKit reads trusted attributes and creates
some read-only views without selecting the user namespace. This can both force
slow filesystem comparisons and retain deleted files after directory replacement.

The patch selects the user namespace consistently for snapshots, read-only
views, merges, imports and exports. Layer imports apply through the mounted
overlay view, preserving directory deletion metadata during cache restoration.
A startup probe verifies directory replacement and
whiteouts on the actual snapshot filesystem. Existing unmarked snapshot stores
cannot switch modes; use a fresh builder volume. Genuine filesystem errors still
fail the optimized differ, and unsupported redirects are rejected.

The image preserves the pinned upstream filesystem and runtime configuration,
replaces only `buildkitd`, and adds the Apache-2.0 upstream license. A multistage
build copies that filesystem into one final layer. Before publishing, CI requires
one layer and compares the configuration and bounded filesystem exports from
containers that are created but never started. It checks file contents, types,
modes, owners, links and extended attributes, allowing only the daemon, license
and its new parent directories, and Docker-generated container files to differ.
Source is pinned to
`991535e0973488b6a429096d21fa13f81f2d89d8`; the workflow records the patch and
compiled binary checksums for each native architecture.

VFS copies each layer's parent filesystem. The original nine-layer candidate's
logical file-payload model estimated about 416 MiB of extra snapshot data from
the daemon and license layers on each architecture. A single final layer removes
roughly 1 GiB of repeated payload in that model. These are not physical disk
allocation measurements and do not establish the cause of the ARM64 workspace
eviction; the flattened image still requires runtime qualification.

Architecture images have run-specific tags. Their manifest is assembled from
the exact pushed digests. The commit tag is a lookup convenience and may change
on a rebuild; acceptance and user workflows must select the manifest digest.
Native kernel test binaries are retained as CI artifacts, outside the image,
and may only run inside the disposable managed sandbox.

Candidate images are for isolated qualification. They are not selected by the
runner image or by upstream `docker/setup-buildx-action`. A qualified release
must be selected explicitly through that action's `driver-opts` image setting,
using the published image digest. There is no global Buildx environment setting
that replaces the action's default builder image.

Before release, require both architectures to pass exported-image, directory
opacity, capability, hardlink, merge, cache-reuse and cancellation tests in the
managed sandbox. Compare cold and warm export timings under matching resource
limits. Keep the normal fallback behavior; forced-overlay mode is a diagnostic,
not a production default.

Metadata qualification requires exact Linux file capabilities in newly generated
OCI layers, including after a fresh builder restores cache and copies up a file.
The seed also contains a user attribute; native filesystem checkpoints require
both attributes throughout import, read-only views, copy and copy-up. Pinned
containerd's `archive.ChangeWriter` emits only `security.capability` into new
layers. Arbitrary user attribute re-export is an upstream limitation and is not
claimed by this candidate.

The pinned gVisor runtime rejects security attribute writes on host-backed gofer
volumes. Imports there can silently lose capabilities through containerd's
unsupported-attribute handling. The development qualification must resolve this
storage limitation and prove disk accounting, sharing, restart and cleanup
before the candidate or a workspace change becomes a production default.
