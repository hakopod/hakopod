FROM ghcr.io/actions/actions-runner:2.337.0@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4

USER root
ARG TARGETARCH
ARG GH_VERSION=2.101.0
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       build-essential cmake ninja-build pkg-config python3-dev python3-venv \
       ca-certificates curl git jq tar zstd unzip postgresql-client libpq-dev libzbar0 \
    && rm -rf /var/lib/apt/lists/*

# Release checksums are pinned independently for both native architectures.
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) checksum=9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8 ;; \
      arm64) checksum=b57e8063f18862647c9d22727c32e9da1b963f8bf9db648fe123a6975695640f ;; \
      *) echo "Unsupported runner architecture" >&2; exit 1 ;; \
    esac; \
    archive="gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    curl --fail --location --retry 3 "https://github.com/cli/cli/releases/download/v${GH_VERSION}/${archive}" -o /tmp/gh.tar.gz; \
    echo "$checksum  /tmp/gh.tar.gz" | sha256sum --check --strict; \
    tar -xzf /tmp/gh.tar.gz -C /tmp; \
    install -m 0755 "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /usr/local/bin/gh; \
    rm -rf /tmp/gh.tar.gz "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}"

COPY observe.py /usr/local/lib/hakopod/observe.py
COPY --chmod=0755 job-started.sh /usr/local/bin/hakopod-job-started.sh

USER runner
WORKDIR /home/runner
