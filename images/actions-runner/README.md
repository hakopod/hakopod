# Managed runner image

This image extends the pinned GitHub Actions runner with `make`, GCC/G++, CMake,
Ninja, pkg-config, Python development headers and GitHub CLI. Jobs still run as
UID 1001 inside the existing Managed Actions sandbox. Package installation runs
only while building the image; jobs do not receive sudo or host access.

The native AMD64 and ARM64 checks build `zxing-cpp==2.3.0` from source under
Python 3.12 and verify a barcode round trip. They also check the compiler and
GitHub CLI as the unprivileged runner. No job registration or credentials are
included in the image.

The image workflow publishes only tested main-branch builds. Each architecture
gets a source-SHA tag, followed by one combined manifest. Pin that
manifest's digest in the engine after publication. Existing busy jobs finish
on their original image; replacement runners use the engine's current pin.

