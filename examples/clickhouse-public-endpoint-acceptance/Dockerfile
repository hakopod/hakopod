# Supply an already built Linux /probe from the approved VM build lane.
# Build context is this directory; compiling locally is not part of this example.
FROM docker.io/clickhouse/clickhouse-server:26.3.33.24@sha256:810861a2e2d0188744f5f23b2d3ec9ff95812bcb9ddbb8fed13a377a7f305893
COPY --chmod=0555 probe /probe
USER 101:101
ENTRYPOINT ["/probe"]
CMD ["--mode", "hold"]
