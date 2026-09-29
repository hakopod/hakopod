# Managed runner image

This image extends the pinned GitHub Actions runner with `make`, GCC/G++, CMake,
Ninja, pkg-config, Python development headers and GitHub CLI. PostgreSQL client
tools and the libpq and ZBar libraries support database and barcode checks.
GNU tar and zstd support compressed GitHub Actions dependency caches; the image
smoke check verifies an archive round trip as the unprivileged runner.
Jobs still run as
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

The workflow observer forwards complete physical paging-log records up to
16 KiB. It withholds longer records and incomplete suffixes, without splitting
secret values across artificial lines. Any skipped record marks live redaction
state incomplete; the API then withholds that live output and completed logs
remain available from GitHub after the job finishes.
