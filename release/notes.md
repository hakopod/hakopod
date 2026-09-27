Hakopod 0.1.0-alpha.36 fixes the error returned when an API request is missing its JSON body (#117).

Requests with an empty or whitespace-only body now return HTTP 400 with code `invalid_request` and the message `request body is required and must be a JSON object`. Previously, they returned the Go decoder error `EOF`.

The fix applies to all endpoints using the shared request decoder, including retained-storage deletion. Unknown fields, malformed JSON, multiple JSON values and oversized bodies retain their existing validation. Required fields such as `confirm_name` are still checked by the endpoint and store.

## Upgrade

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.36
```

Direct upgrades are supported from alpha.34 and alpha.35. Older installations must upgrade through a supported intermediate release. This release adds no database migrations or configuration changes.

## Validation

Eleven decoder regression cases cover missing and whitespace-only bodies, valid objects, unknown fields, malformed and truncated JSON, multiple objects and the request-size limit. The full Go suite and dashboard build passed for the fix. Publication is gated on the release workflow's source checks, packaged smoke tests and native installation and upgrade acceptance on amd64 and arm64.
