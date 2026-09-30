# Certificate issuers

Open a public service's Networking tab to configure TLS. Select the default
issuer, create an application issuer, or upload a matching PEM certificate and
private key. Application Networking also lists its available issuers. Issuer
creation and certificate replacement each have a review step.

Cloud customers can see the operator-configured default and issuers owned by
their application. They cannot create or select another shared ClusterIssuer.
Self-hosted administrators retain installation-wide issuer creation under
Infrastructure, and deployers can also create application issuers.

## Defaults and readiness

The operator sets `server.tls_issuer` in the server configuration, or
`HAKOPOD_TLS_ISSUER`, to an existing cert-manager ClusterIssuer. Public services
without an explicit TLS configuration use that default. Discovery marks it as
the default and reads it directly, so it is available even when the installation
has more than 64 issuers. Cloud discovery omits its operator contact email and
condition details.

A missing default is reported separately from a missing certificate controller.
An application issuer remains an option when cert-manager is installed without
a default. Issuer creation requires a successfully deployed application and
waits until other application runtime work has finished; a failed request
preserves the entered form values for retry.

An issuer's Ready condition describes account configuration. A service's TLS
status checks its current certificate, covered hostnames and expiry. Neither
status proves public DNS or external traffic reachability. Let’s Encrypt
staging certificates are not trusted by browsers. HTTP-01 requires public DNS
and inbound TCP port 80; local development hostnames cannot complete public
ACME validation.

## API and TOML

`GET /api/v1/applications/{id}/tls/issuers` requires `deployments:read` for that
application. `POST` on the same path requires `deployments:write` and accepts:

```json
{"name":"my-issuer","email":"admin@example.com","production":false}
```

Creation uses the installed ingress class and a constrained Let’s Encrypt
HTTP-01 solver. It creates a namespaced cert-manager `Issuer` and an owned,
immutable ACME account key in the application's namespace. There are at most
16 issuers/account reservations per application. Creation is serialized with
deployment and deletion through PostgreSQL. Audit events record the request
and configured issuer; account keys never enter the API response or audit.
Repeating the same name and configuration reuses the issuer and key. To change
the contact email or ACME environment, choose a new name. Application data
retention also retains issuer credentials; reclaiming the namespace deletes
them with the rest of the retained application data.

Every returned issuer has `kind` (`Issuer` or `ClusterIssuer`), `default`, and
its observed `ready` state. Use both name and kind when attaching an application
issuer:

```toml
[services.web.tls]
issuer = "my-issuer"
issuer_kind = "Issuer"
```

Existing TOML with only `issuer` continues to refer to a `ClusterIssuer`.
Explicit `issuer_kind = "ClusterIssuer"` has the same meaning. An issuer kind
without an issuer, unknown kinds, and a combination of issuer and uploaded
certificate are rejected.

`POST /api/v1/applications/{id}/services/{service}/tls` accepts `issuer` and
`issuer_kind` with `expected_revision` and an `Idempotency-Key`. It creates an
immutable deployment revision. Both desired and resolved configurations retain
the reference. Attachment, deployment planning and reconciliation validate the
same application ownership rules, including rollback and TOML deployments.
Switching between issuer kinds or uploaded certificates clears the previous
cert-manager annotation.

`GET /api/v1/tls/issuers` remains available for default discovery in Cloud.
Its `POST` remains installation administration. Operators must configure the
shared default explicitly; customer requests cannot change it.

See the [certificate controller setup](../deploy/cert-manager/README.md) for
installation and bounded solver configuration. The generated OpenAPI schema
and TypeScript SDK expose the same endpoints and issuer kinds.
