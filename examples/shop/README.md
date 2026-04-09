# Public web + private API

`hakopod.toml` is runnable using public, digest-pinned Python Alpine images on arm64 and amd64. It needs no image build or registry credential. Two small Python processes run actual HTTP servers; this demo is not a production HTTP server.

The public web service proxies `/api` to `http://api:8080/`. Only `web` gets an Ingress. Browser JavaScript calls its own public origin. Private service discovery is stable Kubernetes Service DNS, independent of pod addresses.

From the repository root, with the API running and CLI credentials configured:

```sh
hakopod validate --file examples/shop/hakopod.toml
hakopod plan --file examples/shop/hakopod.toml --project demo --environment development
hakopod deploy --file examples/shop/hakopod.toml --project demo --environment development --wait
```

The generated development URL uses `:18080`. DNS for `*.127.0.0.1.sslip.io` should resolve to loopback. If the local resolver blocks rebinding, use `curl --resolve '<generated-host>:18080:127.0.0.1' http://<generated-host>:18080/api`.

Generate complete update and failure examples without copying application code:

```sh
python3 examples/shop/variants.py
hakopod deploy --file .local/examples/shop-updated.toml --project demo --environment development --service api --wait
hakopod deploy --file .local/examples/shop-failed-readiness.toml --project demo --environment development --service api --wait
```

The update changes the private API runtime image from Python 3.13.15 to 3.14.7. `/api` reports the runtime version. The failure variant sets an HTTP readiness path that returns 404. The old ready replica can continue serving when capacity permits; the operation must report failure after its rollout deadline. Recovery and an explicit rollback are separate auditable deployment operations. `scripts/acceptance.py` checks the complete lifecycle against the real API.

Each process handles one request at a time with bounded proxy reads and a three-second upstream timeout. This keeps the example's resource demand small. Application memory limits are platform resource profiles; see the TOML reference for the current mapping.
