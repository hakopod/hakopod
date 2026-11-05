# alpha.78 app catalog post

Social image announcing the eight presets added in 0.1.0-alpha.78. It is
16:9, which X and LinkedIn show uncropped.

- `hakopod-alpha78-apps-3200x1800.png`: the image to post
- `post.html`: the source, laid out at 1600x900

This is a one-off campaign image. It is HTML rather than an outlined SVG from
`source/build.py`, and it is not listed in the kit's `manifest.json` or
`SHA256SUMS`.

## Render

Chrome renders it at 2x from this directory:

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new \
  --hide-scrollbars --force-device-scale-factor=2 --window-size=1600,900 \
  --allow-file-access-from-files \
  --screenshot="$PWD/hakopod-alpha78-apps-3200x1800.png" "file://$PWD/post.html"
```

## Third-party logos

The logos in `logos/` belong to their projects and are used only to name the
apps in the catalog. They are not covered by this repository's license.

| File | Source |
|---|---|
| `radicale.svg` | Kozea/Radicale, `radicale/web/internal_data/css/logo.svg` |
| `loki.png` | grafana/loki, `docs/sources/logo.png` |
| `victoriametrics.svg` | Simple Icons 13.21.0, filled with the project's purple #621773 |
| `beszel.svg` | henrygd/beszel, `internal/site/public/static/icon.svg` |
| `pgadmin.png` | pgadmin-org/pgadmin4, `web/pgadmin/static/img/logo-256.png` |
| `listmonk.svg` | The ring from knadh/listmonk's `logo.svg`, without the wordmark |
| `opentelemetry.svg` | cncf/artwork, `projects/opentelemetry/icon/color` |
| `prometheus.svg` | cncf/artwork, `projects/prometheus/icon/color`, used for Alertmanager |
