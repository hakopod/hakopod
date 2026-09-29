FROM registry.gitlab.com/gitlab-org/gitlab-runner/gitlab-runner-helper:x86_64-v19.4.1@sha256:25db4eaaff9ff231b010eca4e189065b821b637a443897e14e0271a9faf370bf AS helper-amd64
COPY --chmod=0755 bin/amd64/gitlab-runner-helper /usr/bin/gitlab-runner-helper
COPY bin/amd64/transport-source.json /usr/share/hakopod/transport-source.json
COPY LICENSE.upstream /usr/share/licenses/hakopod-gitlab-runner/LICENSE.upstream

FROM registry.gitlab.com/gitlab-org/gitlab-runner/gitlab-runner-helper:arm64-v19.4.1@sha256:49c4d312526b30baeffee8778c99bfd8bd79877dbb52067cb3f3c54e1973fda1 AS helper-arm64
COPY --chmod=0755 bin/arm64/gitlab-runner-helper /usr/bin/gitlab-runner-helper
COPY bin/arm64/transport-source.json /usr/share/hakopod/transport-source.json
COPY LICENSE.upstream /usr/share/licenses/hakopod-gitlab-runner/LICENSE.upstream

FROM docker.io/gitlab/gitlab-runner:alpine-v19.4.1@sha256:4da99e249eb79dc86a27af77b5c5d6cc46445c802b29294441081ae27410159a AS manager
ARG TARGETARCH
COPY --chmod=0755 bin/${TARGETARCH}/gitlab-runner /usr/bin/gitlab-runner
COPY bin/${TARGETARCH}/transport-source.json /usr/share/hakopod/transport-source.json
COPY LICENSE.upstream /usr/share/licenses/hakopod-gitlab-runner/LICENSE.upstream
