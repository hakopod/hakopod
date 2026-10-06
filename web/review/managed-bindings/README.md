# Managed binding options UI review

This development harness imports the actual connection route, application route,
service detail and shared dashboard shell. All API responses are artificial,
every fetch is intercepted, password request bodies are discarded and connection
submission always fails. It cannot connect a database or deploy an application.

Run only in the designated development VM scratch checkout with the exact
reviewed source, initialized pinned submodules, installed locked dashboard
dependencies and an existing Playwright Chromium installation. No local Mac
builds or tests are authorized for this review.

```sh
node web/review/managed-bindings/server.mjs
```

The fixture binds loopback port 4198; override with
`HAKOPOD_BINDING_REVIEW_PORT`. Use `HAKOPOD_PLAYWRIGHT_MODULE` for the installed
Playwright `index.mjs`, then run the review script in that same VM checkout.
Screenshots and measurements belong under `.local/managed-binding-review/`.
No existing operator cluster is needed by this browser-only fixture.

The connection route is
`/databases/cccccccccccccccccccccccccccccccc/connect?fixture=postgresql`.
Other scenarios are `postgresql-legacy`, `redis`, `redis-legacy`, `redis-cluster`,
`mysql`, `mongodb`, `denied`, `empty`, `loading`, `error`, `application-error`
and `unready`. The application/service summary route is
`/applications/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?tab=topology` or
`?service=api&tab=environment`.

The browser exposes `window.__bindingFixture` only in this harness. Its bounded
request ledger records reference metadata but never password contents. Modes
allow rejected or held secret, review and connection requests; resource revisions
can be advanced and their query keys invalidated to inspect stale review behavior.

The independent reviewer must inspect full-resolution captures after the route,
lazy imports, queries and fonts settle. Geometry, input preservation and API
assertions supplement screenshot review. Artificial UI evidence does not verify
database provisioning, TLS handshakes, authorization or production availability.
