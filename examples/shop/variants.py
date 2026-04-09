#!/usr/bin/env python3
"""Generate full application variants from the runnable baseline; writes only .local."""
from pathlib import Path

root = Path(__file__).resolve().parents[2]
original = (root / 'examples/shop/hakopod.toml').read_text()
prefix, api = original.split('[services.api]', 1)
updated_api = api.replace(
    'python:3.13.15-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a',
    'python:3.14.7-alpine@sha256:c6ead215bfd31f1e433d968853b7a769989117115b728874824e6c0a27cb96fc',
)
destination = root / '.local/examples'
destination.mkdir(parents=True, exist_ok=True)
(destination / 'shop-updated.toml').write_text(prefix + '[services.api]' + updated_api)
(destination / 'shop-failed-readiness.toml').write_text(prefix + '[services.api]' + updated_api.replace('healthcheck = "/readyz"', 'healthcheck = "/not-ready"'))
print('Generated .local/examples/shop-updated.toml and shop-failed-readiness.toml')
