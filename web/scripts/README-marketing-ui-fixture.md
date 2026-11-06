# Marketing screenshot fixture

`marketing-ui-fixture.html`, `marketing-ui-fixture-harness.tsx` and `marketing-ui-fixture.vite.config.ts` are development-only sources for README and website screenshots. Product routes do not import them, and they are not part of the production dashboard build.

The fixture renders the real dashboard shell and the real application, catalog and deployment routes, but every `/api/*` request is answered with artificial data in the browser. It does not authenticate, contact a Hakopod API, or reach a cluster. The shop application, its revisions, runtime metrics and managed database are invented. The catalog is the real embedded `templates/catalog.json`, served the way `GET /api/v1/templates` serves it. A small "Fixture data" label is shown in the corner by default.

Start it from `web/` after `git submodule update --init packages/ui` and `pnpm install`:

```sh
node ../templates/scripts/sync-assets.mjs public/template-assets
pnpm exec vite --config scripts/marketing-ui-fixture.vite.config.ts
```

The first command copies the template logos so `/template-assets/<id>/logo.svg` resolves. Open `http://127.0.0.1:14596/scripts/marketing-ui-fixture.html` with these query parameters:

- `view=app` (default): the `shop` application overview in `demo` / `prod`
- `view=topology`: the same application with the service graph and its managed PostgreSQL
- `view=catalog`: the template catalog; `q=` and `category=` pass through to the page's own search and filter
- `view=deployment`: revision 42 of `shop` with its log phases opened
- `view=deployment&tab=changes`: the configuration diff for revision 42
- `theme=paper` (default) or `theme=dark`
- `label=0`: hide the "Fixture data" label for a marketing capture

Any `/api/*` request the fixture does not answer is logged with `console.warn` and collected in `window.fixtureUnhandled`. Keep that list empty before capturing.

Capture with headless Chrome, for example:

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --hide-scrollbars \
  --force-device-scale-factor=2 --window-size=1600,1000 --virtual-time-budget=8000 \
  --screenshot=app.png 'http://127.0.0.1:14596/scripts/marketing-ui-fixture.html?view=app&label=0'
```

Screenshots from this fixture are illustrations of the interface. They are not evidence of cluster behaviour, health or performance.
