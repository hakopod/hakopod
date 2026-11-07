# SaaS custom hostnames

A multi-tenant SaaS on Hakopod often lets each of its own customers bring a
domain, for example `shop.customer.com`, that serves the customer's store from
one shared service. Custom hostnames do that at the scale of thousands of
domains: your backend attaches a hostname through the API, the customer adds a
CNAME record, and Hakopod routes the hostname and issues its certificate. No
application revision is created per hostname.

This is an API and CLI feature for now. There is no dashboard page yet.

## Custom hostnames or `[domains]`

Use [`[domains]`](pending-domains.md) for the handful of names you own: the
product's own `www`, `api` or `app`. Those live in the application TOML, prove
ownership with a TXT record that is valid for an hour, activate with a reviewed
revision, and are capped at 20 per application.

Use custom hostnames for names your customers own and add on their schedule.
They never enter the TOML, need no TXT record and no deployment, and a service
can hold up to 1000 of them by default.

A hostname can be one or the other, never both, anywhere on the installation.

## Turn it on

Opt a public HTTP service in and deploy:

```toml
[services.web]
image = "ghcr.io/acme/storefront@sha256:..."
port = 8080
public = true

[services.web.custom_hostnames]
enabled = true
max = 5000                       # optional, default 1000
target = "stores.acme-cdn.com"   # optional, see below
```

| Field | Meaning |
|---|---|
| `enabled` | Route the service's attached hostnames. `false` behaves like removing the table. |
| `max` | Most hostnames this service may hold, 1–50000, default 1000. Acceptance refuses a value above the installation's `max_per_service`. |
| `target` | The name customers point their CNAME at. Defaults to the service's generated hostname, such as `web-0123456789ab.apps.example.com`. Set it when you publish your own name that itself points at that hostname, so customers never see the installation's domain. |

The service needs `public = true` and a `port`. Jobs, serverless services,
Actions runners and sandbox sessions are refused, as for `backend_http2` and
`ingress_timeout_seconds`.

## What customers add

Each customer adds one record at their DNS provider:

```
shop.customer.com.  CNAME  web-0123456789ab.apps.example.com.
```

Every API response carries that record as `cname` (and the target alone as
`target`), so your onboarding screen can show it. A CNAME can sit behind
further CNAMEs as long as the chain ends at the target or at the service's
generated hostname.

An apex domain such as `customer.com` cannot carry a CNAME, so the customer
uses their provider's ALIAS record or CNAME flattening, which publishes the
target's addresses instead. Addresses alone do not show which service the
domain is meant for, because every service on the installation shares them,
so such a hostname also needs the TXT record returned as `verification`:

```
_hakopod-challenge.customer.com.  TXT  "4f9c2a7e0b13d58c..."   # the full 64-character value
```

The value is random and belongs to that one hostname; it stays the same if
the hostname fails and is added again. A hostname with a plain CNAME needs no
TXT record. `status_reason` says which of the two is missing.

## Lifecycle

| Status | What is happening |
|---|---|
| `pending_dns` | Waiting for a CNAME to the target, or for the target's address plus the TXT verification. Not routed. |
| `issuing` | DNS matched. The hostname is routed and a certificate is requested. |
| `active` | The certificate is ready, or the installation has no TLS issuer and the hostname is served over HTTP. |
| `failed` | Hakopod stopped trying. `status_reason` says why. Add the hostname again to start over. |
| `deleting` | Removal was requested. The route, certificate and secret are being removed; then the record disappears. |

DNS is checked about 15 seconds after the hostname is added, then after 1
minute, 5 minutes, 30 minutes and every 6 hours. A hostname that does not
point at the target within 7 days becomes `failed`. `status_reason` names what
was found and what to change. CNAME, address and TXT lookups use Hakopod's
own resolver without a cache, so a check made before the customer's record existed does not hide the
record later; the customer's provider and any resolver in front of the
installation can still cache an earlier negative answer for its TTL.

Once DNS matches, the hostname is added to the service's routing and a
cert-manager Certificate is requested for it alone with HTTP-01, using the
service's `tls.issuer` if it names one and the installation's issuer
otherwise. A hostname whose certificate is not ready 3 hours after DNS
matched becomes `failed` with cert-manager's last message.

## Limits and permissions

| Limit | Default | Where |
|---|---|---|
| Hostnames per service | 1000 | `custom_hostnames.max` in TOML |
| Installation cap per service | 10000 | `max_per_service` in installation settings, 1–50000 |
| Creations per application | 120 per minute | Fixed. Over it the API answers 429 with `Retry-After`. |

Installation administrators read and change the cap:

```sh
curl -H "Authorization: Bearer $ADMIN_KEY" https://control.example.com/api/v1/settings/custom-hostnames
curl -X PUT -H "Authorization: Bearer $ADMIN_KEY" -H 'Content-Type: application/json' \
  -d '{"max_per_service":20000,"expected_revision":1}' \
  https://control.example.com/api/v1/settings/custom-hostnames
```

Adding and removing hostnames needs the `hostnames:manage` permission;
`deployments:write` is not enough. Project owners and admins have it. Grant it
to the API key your SaaS backend uses, together with `deployments:read`, which
is all listing and reading need. Every creation and removal is in the audit
log as `custom_hostname.create`, `custom_hostname.retry` or
`custom_hostname.delete`.

## Let's Encrypt limits

Every hostname gets its own certificate, and every tenant domain is a
separate registered domain, so the per-domain limit of 50 certificates a week
never binds across tenants. The limits that do matter are per ACME account,
which is the installation's issuer:

- New orders: 300 per account every 3 hours. Onboarding a few thousand tenants
  at once takes several hours; the rate limit of 120 creations a minute does
  not keep you under it, and hostnames over the limit retry until their
  3-hour window ends. Add large batches gradually.
- Failed validations: 5 per hostname per hour. A customer whose DNS points
  elsewhere stays in `pending_dns` and never reaches Let's Encrypt.
- Certificates per registered domain: 50 a week. It matters only when one
  customer brings many subdomains of the same domain.

See <https://letsencrypt.org/docs/rate-limits/> for the current numbers.

## Identifying the tenant

HAProxy passes the original `Host` header to your service unchanged. Look the
tenant up by `Host`, and reject requests for hostnames your own database does
not know: until a hostname is removed, its traffic reaches you even if the
tenant has left.

## Routing at scale

Hostnames are placed in extra Ingresses named `<service>-ch-<n>`, at most 100
hosts each, chosen by a hash of the hostname. They use the same backend,
ingress class, `ingress_timeout_seconds`, `backend_http2` and `haproxy.org/`
annotations as the service's own ingress, and are rewritten only when their
hosts change or a deployment changes the service. Each hostname has its own
TLS secret. `kubectl get ingress -l hakopod.io/custom-hostname` lists them.

## Cleanup

Removing a hostname deletes its route, Certificate and TLS secret. Removing
the `custom_hostnames` table, setting `enabled = false` or removing the
service marks every hostname of that service `deleting` when the revision is
accepted, and the deployment removes the Ingresses, Certificates and secrets.
Deleting the application removes everything with its namespace. Re-enabling
starts with no hostnames.

## Examples

With the API:

```sh
APP=6f1c...   # application ID
BASE=https://control.example.com/api/v1/applications/$APP/services/web/custom-hostnames

curl -X POST -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"hostname":"shop.customer.com"}' "$BASE"           # 201, or 200 if already attached
curl -H "Authorization: Bearer $KEY" "$BASE?status=pending_dns&limit=100"
curl -H "Authorization: Bearer $KEY" "$BASE?cursor=shop.customer.com"   # next page
curl -H "Authorization: Bearer $KEY" "$BASE/shop.customer.com"
curl -X DELETE -H "Authorization: Bearer $KEY" "$BASE/shop.customer.com"  # 202
```

With the CLI, where `--service` can be left out when the application has one
service:

```sh
hakopod hostnames add shop shop.customer.com
hakopod hostnames list shop --status pending_dns
hakopod hostnames show shop shop.customer.com
hakopod hostnames remove shop --service web shop.customer.com
```

Agents read a page of hostnames with the `custom_hostnames` MCP tool and add or
remove them with `api_call` (`createCustomHostname`, `deleteCustomHostname`),
which needs `hostnames:manage` and a connection that allows writes.
