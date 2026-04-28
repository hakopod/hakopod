# Licenses and redistribution notes

Hakopod's original code is Apache-2.0. The root `LICENSE` and `NOTICE` apply to that code; dependencies retain their own terms. Public design references are not a license to copy a product's branding. No Railway, Render, Vercel, Coolify or Dokploy brand assets or external fonts are bundled. The dashboard's shadcn/ui attribution and original MIT notice are preserved in `web/THIRD_PARTY_NOTICES.md`.

This review was performed on 2026-09-12 against the actual Go module cache, installed dashboard packages and frozen dependency files. The generated inventory preserves the original available license and notice texts. Automated license hints are review aids; the original texts remain authoritative, including modules that combine differently licensed files.

## Direct application dependencies

| Component | License / action |
| --- | --- |
| Hakopod | Apache-2.0; preserve root LICENSE and NOTICE |
| Go standard library | BSD-3-Clause; compiler-distribution LICENSE included with binary notices |
| pgx, go-toml | MIT; upstream copyright and license files preserved |
| Kubernetes api/apimachinery/client-go | Apache-2.0 with additional upstream notices for included third-party files; preserve those notices |
| React/React DOM, TanStack Start/Router/Query, Radix | MIT; upstream package notices retained where provided |
| Tailwind CSS, Vite, React Vite plugin, openapi-typescript, openapi-fetch, srvx, clsx, tailwind-merge | MIT |
| class-variance-authority, TypeScript | Apache-2.0 |
| React and Node TypeScript declarations | MIT |

The direct license declarations were checked from the installed package metadata and upstream license files. Versions are fixed in `go.mod`/`go.sum` and `web/pnpm-lock.yaml`; the release inventory records versions individually.

## Transitive Go modules

The command import graph contains 45 external modules. Every one has original license/notice files preserved by `release/collect-notices.py`, including nested notices. The source-text review hints group those modules as follows:

| Source-license group | Module count |
| --- | ---: |
| MIT | 11 |
| Apache-2.0 | 11 |
| BSD-3-Clause | 13 |
| ISC | 1 |
| Apache-2.0 and MIT files | 3 |
| Apache-2.0 and BSD-3-Clause files | 4 |
| Apache-2.0, BSD-3-Clause and MIT files | 2 |

Examples include BSD-licensed `golang.org/x/*`, UUID and protobuf; MIT pgx support packages and JSON helpers; Apache-licensed Kubernetes machinery and logging; ISC `go-spew`; and YAML implementations with MIT libyaml-derived files plus Apache files. Mixed-file groups are not presented as a choice of license. The preserved inventory includes the relevant `LICENSE.libyaml` and `NOTICE` files.

## Dashboard transitive dependencies

The installed directory inventory contains 199 package/version records, including native tooling packages. The `pnpm licenses list` view groups 194 package entries; its grouping and optional-tooling coverage differ. The SBOM also includes optional and development dependencies declared in the lockfile, including packages not installed for this host. A directory count is not a count of dependencies shipped in the browser.

Most declarations are MIT, ISC, Apache-2.0 or BSD-3-Clause. The remaining groups need explicit treatment when distributing the applicable package:

| Group | Packages / redistribution consideration |
| --- | --- |
| Python-2.0 | `argparse`; retain its Python-derived license and notices |
| CC-BY-4.0 | `caniuse-lite` browser data; preserve attribution and license information for redistributed data |
| MPL-2.0 | `lightningcss` and its native packages; file-level source obligations apply when distributing covered code or modifications. Running a compiler does not by itself relicense Hakopod or generated CSS |
| Unlicense | `isbot`; preserve the upstream text |
| 0BSD | `tslib`; preserve the upstream declaration |
| MIT OR CC0-1.0 | `type-fest`; retain its expression and original license files |

Six installed npm distributions declare MIT but do not include a license/notice file at their package root: `@redocly/openapi-core@1.34.20`, `@rolldown/binding-darwin-arm64@1.2.8`, `change-case@5.4.4`, `fetchdts@0.1.7`, `react-remove-scroll-bar@2.3.8`, and `uri-js-replace@1.0.1`. The inventory records their declarations and upstream repository URLs, with an empty file list. Before distributing their code in a dashboard artifact, recover and preserve the copyright notices from the matching upstream release/commit. Metadata alone is not treated as a complete notice. The current Go executable archives do not contain these JavaScript packages.

## Platform tools and containers

| Component | License boundary |
| --- | --- |
| K3s | Apache-2.0, plus its bundled components' notices |
| k3d | MIT; development tool only |
| HAProxy Kubernetes Ingress Controller and Helm chart | Apache-2.0; vendored chart remains unmodified, with full upstream license under `deploy/charts/hakopod-platform/licenses/` |
| HAProxy proxy executable | GPLv2 core, LGPL exportable headers and documented OpenSSL exception; distinct from the controller's license |
| PostgreSQL | PostgreSQL License, plus image operating-system packages |
| Python sample image | PSF licensing, plus Alpine and other image packages |
| Syft 1.51.1 | Apache-2.0; checksum-verified developer scanner, not included in application runtime |

These programs run as separate processes/containers. Hakopod does not relicense upstream software. Redistributing an image requires preserving its notices and satisfying its own source/distribution obligations; a top-level container label is not a complete license inventory. No complete image-license or operating-system scan is claimed by the Go/dashboard dependency SBOM.

## Generated evidence and release boundary

`python3 release/build.py` builds local Go archives, preserves available notices, and generates real SPDX 2.3 and CycloneDX JSON with Syft. It scans only staged binaries and dependency manifests, never local credentials. Go license enrichment uses the existing module cache; JavaScript license enrichment reads metadata for pinned versions from the npm registry. The native Syft catalog, a sanitized `dependency-license-inventory.json`, source/tool provenance and SHA256SUMS accompany the artifacts under `.local/releases/<CLI-version>/`.

The 2026-09-12 development build produced 373 SPDX packages and 379 CycloneDX components. Syft's native catalog contains 242 npm entries and 130 Go entries across the platform binaries (47 unique Go module/version pairs, including Hakopod and the standard library). It reports no license for the main Go module or three optional npm package/version pairs: `lightningcss-freebsd-x64@1.32.0`, `lightningcss-freebsd-x64@1.33.0` and `lightningcss-linux-arm-gnueabihf@1.32.0`. Hakopod's Apache-2.0 text accompanies every archive; the optional packages still require upstream metadata review before redistribution. Some filename-based Go notice matches produce opaque license references, which are preserved rather than silently reclassified.

License declarations and `NOASSERTION` values in those SBOMs are kept as reported. Machine-specific staging, cache and home paths are normalized to documented placeholders; package identities and license data are retained. The SBOM is not a vulnerability clearance, a legal approval, a finished-container inventory or evidence that every declared build dependency appears in the production bundle. Release publication remains gated on the exact final artifacts, resolution of missing redistributed notices, applicable source obligations, image scans and signatures. Nothing has been published or uploaded by this workflow.

Sources:

- https://www.apache.org/licenses/LICENSE-2.0
- https://github.com/shadcn-ui/ui/blob/main/LICENSE.md
- https://github.com/haproxy/haproxy/blob/master/LICENSE
- https://github.com/haproxytech/helm-charts/blob/main/LICENSE
- https://www.postgresql.org/about/licence/
- https://www.mozilla.org/en-US/MPL/2.0/
- https://creativecommons.org/licenses/by/4.0/
- https://github.com/anchore/syft/releases/tag/v1.51.1
