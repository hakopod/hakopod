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

The image retains the pinned upstream image and replaces only `buildkitd`. The
Apache-2.0 upstream license is included. Source is pinned to
`991535e0973488b6a429096d21fa13f81f2d89d8`; the workflow records the patch and
compiled binary checksums for each native architecture.

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
