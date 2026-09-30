# Release the GitHub deployment action

The action is maintained in `actions/deploy` in this repository. Its release
distribution is the public [hakopod/deploy](https://github.com/hakopod/deploy)
repository, with `action.yml` at the repository root so GitHub Marketplace can
identify it. Keep runtime changes and tests in this repository, then export the
reviewed source to the distribution repository.

## Export the reviewed main commit

Merge the source pull request using rebase and merge, and confirm that its CI
checks passed. Use a dedicated, clean `hakopod/hakopod` checkout for the export.
Fetch the latest main branch and check the working tree:

```sh
git fetch origin main
git status --short
```

If the status command prints changes, preserve that work before switching
commits, or use another clean checkout. Once the working tree is clean, check
out the fetched main commit and run the exporter tests:

```sh
git switch --detach origin/main
node --test scripts/test-export-deploy-action.mjs
node scripts/export-deploy-action.mjs --output /absolute/new-directory
```

Choose an absolute output path whose final directory does not exist. The
exporter refuses to overwrite an existing directory and requires `HEAD` to
equal the fetched `origin/main` commit. It reads an explicit list of committed
files through `git show`; it does not copy working-tree changes, dependencies
or Git metadata.

The export contains the root `action.yml`, `index.mjs`, `deploy.mjs`, `README.md`
and `test` files, plus the source license and notice when present. It generates
`SOURCE.json` with the exact source commit, a `package.json` test command, a
standalone GitHub Actions CI workflow and `.gitignore`. The JavaScript action
has no package dependencies or generated bundle.

The exporter also changes the README's relative CI API guide link to
`https://github.com/hakopod/hakopod/blob/main/docs/ci-api.md`. Verify that README
links work from the distribution repository. Keep `action.yml`, runtime files
and tests byte-for-byte identical to the recorded source commit, and include
that commit in the distribution commit and release notes.

## Validate the distribution

For an existing `hakopod/deploy` repository, copy only the exported files into a
clean checkout, preserving `.git`, its history and unrelated repository files.
Review the generated CI workflow alongside any existing release workflow. For
the initial distribution, initialize Git in the new export directory. Review
the full diff in either case. From the distribution root with Node.js 24, run:

```sh
npm test
```

Run the same tests in the distribution repository's GitHub Actions CI. Verify
that `action.yml` uses `node24` and resolves `main: index.mjs` from the repository
root. A consumer workflow should exercise `uses: hakopod/deploy@<commit>` with
the exact proposed distribution commit. Synthetic API fixtures verify runner
packaging and the action contract; label them as fixtures. Record a separate
named development-cluster run when validating real deployments.

Source tests, a GitHub runner check and real deployment acceptance are distinct
results. Preserve that distinction in release notes; do not describe a fixture
run as a production deployment.

## Tag and publish

Choose a semantic version for the reviewed distribution commit. For the first
major version, an initial release such as `v1.0.0` and the compatibility tag
`v1` must resolve to that same commit. Verify both remote tags before publishing.
Never move an existing semantic-version tag; only advance `v1` to a tested,
backward-compatible release in that major version.

Create the GitHub release from the semantic-version tag. In the GitHub release
editor, select **Publish this Action to the GitHub Marketplace**, complete the
required action categories and terms, and publish the release. An ordinary
GitHub release alone does not confirm Marketplace publication.

After publication, verify all of the following:

- The public Marketplace listing opens and links to `hakopod/deploy`.
- The release is published under the intended semantic-version tag.
- The semantic-version tag and `v1` resolve to the reviewed distribution commit.
- The root action metadata and entrypoint are present at that commit.
- The consumer example uses `hakopod/deploy@v1`; immutable examples use the full
  reviewed distribution commit SHA.

Record the public listing URL, release URL, distribution commit, source commit,
test results and any unverified deployment states in the release report. If
Marketplace publication is pending, say so separately from the repository or
release status.
