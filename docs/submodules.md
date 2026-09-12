# Component, issuer and cloud repositories

Hakopod uses three separately versioned Git repositories:

| Path | Purpose | Distribution |
| --- | --- | --- |
| `packages/ui` | Hatch monorepo; consumer components in `packages/ui/packages/ui` | Public |
| `private/license-issuer` | Offline license issuance tools | Private |
| `private/cloud` | AWS, GCP and Azure provisioning and managed-deployment workflows | Private, commercial |

The parent tracks Git commit references. The public component repository is
[hakopod/hatch-ui](https://github.com/hakopod/hatch-ui).
The license issuer lives in the private `hakopod/hakopod-license-issuer`
repository and requires separate access. The commercial cloud toolkit lives in
the private `hakopod/hakopod-cloud` repository. Relative URLs in `.gitmodules` resolve
to these sibling repositories when cloning from GitHub. Keep both private sources private.

Ordinary dashboard/server builds do not need private source. The parent includes
a deterministic, checksummed public UI snapshot under `third_party/ui`:

```sh
python3 scripts/ui-source.py restore
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
```

Restore fills an absent/empty `packages/ui` directory. It preserves an existing
working copy. The bundle contains only Hatch’s consumer package and licenses; the
documentation site and example dashboard are not shipped. Archive extraction
rejects traversal, symlinks, duplicates, unexpected repository metadata and
excessive size. CI uses this public path and
never requests private submodule credentials. A source archive may already contain
the restored UI files.

To work on the components, clone the public submodule before restoring the bundle:

```sh
git clone https://github.com/hakopod/hakopod.git
cd hakopod
git submodule update --init packages/ui
```

Public contributors should initialize only `packages/ui`; a recursive submodule
checkout also requests the private repositories. If you already restored the public
snapshot, preserve any edits and move `packages/ui` aside before initializing its
submodule. Authorized issuer operators can initialize `private/license-issuer`
separately with their own GitHub credentials.

Authorized cloud operators can initialize `private/cloud` separately. Its Terraform
roots and installation workflow have their own tests, lockfiles and proprietary
license. Cloud changes are committed in that repository first, followed by its
gitlink in the parent. See [cloud deployments](cloud-deployments.md).

For component changes, work in the UI repository, run the dashboard checks, and
commit the component change there. Then refresh the public snapshot and commit
the gitlink plus snapshot in the parent:

```sh
python3 scripts/ui-source.py pack
```

The snapshot is source, not a second hand-maintained implementation. Preserve the
fonts’ OFL licenses and the package's component notices. Private issuer changes
are committed only in its own repository; the parent stores the resulting gitlink.
Never include issuer/cloud source, private keys, installation secrets or activation
tokens in public source bundles, dashboard archives or container images.
