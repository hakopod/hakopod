# Mathesar storage UI review

This isolated development harness renders the real catalog, template form and
review components. Catalog metadata, normalized specifications, diffs and
canonical TOML come from `spec.PlanTemplate`. Identity, project, secret metadata
and request failures are explicit artificial fixtures. Every API call is
intercepted, external requests are blocked and deployment always fails.

Generate the fixture from the repository root, in the configured development
build environment:

```sh
mkdir -p .local/mathesar-ui-review
go run web/review/mathesar/export-fixture.go > .local/mathesar-ui-review/catalog-fixture.json
```

The Mathesar runtime workflow also uploads this JSON before cluster acceptance.
It can be downloaded into the same path to review a remote build without local
Go compilation. Use the artifact from the commit being reviewed.

After installing the dashboard's locked dependencies, start the local fixture:

```sh
node web/review/mathesar/server.mjs
```

In a second terminal, set `PLAYWRIGHT_MODULE` to an installed Playwright entry
point. Optionally set `CHROME_EXECUTABLE` to an existing Chrome binary to avoid
downloading a browser, then run:

```sh
node web/review/mathesar/review.mjs
```

The review covers dark and Paper themes at 1440, 390 and 320 pixels: catalog,
inspector, automatic local storage, conditional shared class, bundled and
external PostgreSQL, review, canonical TOML and failed-review preservation. It
checks actual element bounds, labels, visible keyboard focus, keyboard and
touch selection, help, inactive field preservation, omitted inactive values and
deployment blocking on missing secrets.

Screenshots and measurements are written under the ignored
`.local/mathesar-ui-review/evidence/` directory. Inspect the screenshots
critically before recording sign-off. These fixtures do not prove runtime
storage provisioning or successful deployment.
