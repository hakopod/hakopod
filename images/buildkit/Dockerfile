FROM docker.io/moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8

LABEL org.opencontainers.image.source="https://github.com/hakopod/hakopod" \
      org.opencontainers.image.licenses="Apache-2.0" \
      io.hakopod.buildkit.upstream-revision="991535e0973488b6a429096d21fa13f81f2d89d8"

# This image explicitly selects one overlay namespace for writable snapshots,
# read-only views, merges and exports. Stock BuildKit behavior remains available
# by using its upstream image; existing builder volumes cannot change namespace.
ENV HAKOPOD_BUILDKIT_USERXATTR=true
COPY --chmod=0755 buildkitd /usr/bin/buildkitd
COPY LICENSE.upstream /usr/share/licenses/hakopod-buildkit/LICENSE
