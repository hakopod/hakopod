# Template TOML editing review

Development-only isolated review of the real template route, form, TOML editor,
secret form and dashboard shell. Go generates the catalog plans, canonical TOML,
required-secret lists and complete application diffs. All identity, project,
saved-secret metadata and API failures are artificial. Every request is
intercepted and external requests are blocked; deployment always fails.

Use the reserved development VM checkout, initialized pinned submodules and
locked dashboard dependencies. No local Mac builds or browser runs are part of
this review. Generate fixture data on that VM:

```sh
GOMAXPROCS=1 go run web/review/template-edits/export-fixture.go > web/review/template-edits/catalog-fixture.json
```

Run `web/review/template-edits/run-vm.sh` under the bounded review systemd unit.
The runner rejects other platforms and directories and shuts its loopback Vite
server down on exit. It uses the reserved scratch browser and font runtime;
no global installation or operator cluster is required.

Environment options:

- `HAKOPOD_TEMPLATE_REVIEW_PORT`: loopback port, default 4198.
- `HAKOPOD_TEMPLATE_REVIEW_OUTPUT`: absolute evidence directory.
- `HAKOPOD_TEMPLATE_REVIEW_CASES`: regular expression for focused reruns.
- `HAKOPOD_PLAYWRIGHT_MODULE`: existing Playwright module path.

The complete create/add-to-existing flow runs at 1484, 390 and 320 pixels in dark
and Paper themes. Additional mobile cases exercise denied access, stale response
rejection and an application revision change during a held review request.
The runner fingerprints product sources and captures control bounds, editor
diagnostics, exact review/deploy requests and redacted secret-write metadata.

Full-page captures begin at scroll position zero to avoid Chromium's stitched
fixed-position artifacts. Inspect the final screenshots critically before
accepting the evidence. This fixture establishes UI behavior only; it does not
verify backend authorization, secret persistence, workload startup or deployment.
