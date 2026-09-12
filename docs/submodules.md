# Component and issuer repositories

Hakopod uses two separately versioned Git repositories:

| Path | Purpose | Distribution |
| --- | --- | --- |
| `packages/ui` | Branded shadcn/Radix components, tokens and public assets | Public |
| `private/license-issuer` | Offline license issuance tools | Private |

The parent tracks Git commit references. The public component repository is
[hakopod/hakopod-design-system](https://github.com/hakopod/hakopod-design-system).
The license issuer lives in the private `hakopod/hakopod-license-issuer`
repository and requires separate access. Relative URLs in `.gitmodules` resolve
to these sibling repositories when cloning from GitHub. Keep the issuer private.

Ordinary dashboard/server builds do not need private source. The parent includes
a deterministic, checksummed public UI snapshot under `third_party/ui`:

```sh
python3 scripts/ui-source.py restore
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
```

Restore fills an absent/empty `packages/ui` directory. It preserves an existing
working copy. Archive extraction rejects traversal, symlinks, duplicates,
unexpected repository metadata and excessive size. CI uses this public path and
never requests private submodule credentials. A source archive may already contain
the restored UI files.

To work on the components, clone the public submodule before restoring the bundle:

```sh
git clone https://github.com/hakopod/hakopod.git
cd hakopod
git submodule update --init packages/ui
```

Public contributors should initialize only `packages/ui`; a recursive submodule
checkout also requests the private issuer. If you already restored the public
snapshot, preserve any edits and move `packages/ui` aside before initializing its
submodule. Authorized issuer operators can initialize `private/license-issuer`
separately with their own GitHub credentials.

For component changes, work in the UI repository, run the dashboard checks, and
commit the component change there. Then refresh the public snapshot and commit
the gitlink plus snapshot in the parent:

```sh
python3 scripts/ui-source.py pack
```

The snapshot is source, not a second hand-maintained implementation. Preserve the
font's OFL license and the package's component notices. Private issuer changes
are committed only in its own repository; the parent stores the resulting gitlink.
Never include issuer source, private keys, installation secrets or activation
tokens in public source bundles, dashboard archives or container images.
