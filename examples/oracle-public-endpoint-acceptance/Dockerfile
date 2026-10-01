# Supply an already built static Linux /probe from the approved VM build lane.
# Build context is this directory; compiling locally is outside this example.
FROM scratch
COPY --chmod=0555 probe /probe
USER 65532:65532
ENTRYPOINT ["/probe"]
CMD ["--mode", "hold"]
