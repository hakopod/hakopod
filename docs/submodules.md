# Component and issuer repositories

Hakopod uses two separately versioned local Git repositories:

| Path | Purpose | Distribution |
| --- | --- | --- |
| `packages/ui` | Branded shadcn/Radix components, tokens and public assets | Public |
| `private/license-issuer` | Offline license issuance tools | Private |

The parent tracks Git commit references. Local bare origins are named
`../hakopod-design-system.git` and `../hakopod-license-issuer.git`. These are real
local repositories, not hosted GitHub/GitLab URLs. Configure actual authenticated
remote URLs when those repositories are created; no repository was published as
part of this build. Do not expose the private issuer through a public remote.

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
