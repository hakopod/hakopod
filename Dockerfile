# syntax=docker/dockerfile:1.7
FROM golang:1.26.8-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS build
ENV GOMEMLIMIT=256MiB GOMAXPROCS=2
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOMAXPROCS=2 go build -p 2 -trimpath -ldflags='-s -w' -o /out/hakopod-server ./cmd/hakopod-server

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/hakopod-server /hakopod-server
COPY --from=build /src/LICENSE /src/NOTICE /licenses/hakopod/
ENV GOMEMLIMIT=192MiB GOMAXPROCS=2 HAKOPOD_LISTEN=0.0.0.0:8080
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hakopod-server"]
