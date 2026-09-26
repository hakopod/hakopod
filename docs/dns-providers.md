# DNS providers

A DNS provider is a saved credential for the service that hosts your zones, plus
the list of zones that credential is allowed to write to. With one saved, Hakopod
can create the ownership and routing records for a custom domain instead of asking
you to copy them by hand. Cloudflare is the only kind today.

A provider is installation-wide, or it belongs to one project and one environment.
There is no project-only scope: a half-scoped credential would be reachable from
places nobody intended.

## The zone filter is the boundary

Hakopod cannot see what a provider token actually reaches. A Cloudflare token may
be restricted to one zone or hold edit rights across every zone in the account,
and nothing in the token tells us which. So the zone filter you enter, not the
token, is the boundary this feature enforces. Every hostname is checked against
that list before any request is made, and a hostname outside it is refused whatever
the token itself could have done.

Because the filter is the boundary, widening it means entering the token again.
Changing the provider's name or kind also means entering it again, because both are
bound into the credential's encryption. Moving a provider between scopes does not:
the stored token carries forward.

## Scoping a token

Give the token the smallest reach that does the job, and then set the zone filter
to match:

- Create the token with zone-level DNS edit rights on only the zones you intend
  Hakopod to write to.
- List exactly those zones in the zone filter. Between one and 32 entries; a
  credential permitted everywhere is not a boundary.
- Do not enable the provider's proxy on records Hakopod manages. Proxying
  terminates TLS at the provider's edge, which changes what certificate issuance
  and verification observe, so Hakopod refuses to write a proxied record.
- Prefer one provider per scope over one installation-wide credential, if
  different teams own different zones.

The token is sealed with the installation's persistent authentication encryption
key before it is stored, and only the server's own record writer ever opens it. It
is never returned by the API, never logged and never included in an error. If that
key is not configured, saving and using a provider is refused rather than
downgraded.

## Who can manage a provider

Installation administrators, and project owners delegated one entire
project/environment. Machine keys are refused. A machine key is issued for a
narrow job, usually repository access, and reading that as permission to point a
domain somewhere new would widen a key its holder never asked to widen. Admitting
one would need a DNS permission of its own, which does not exist yet.

Administrators see the whole row, including the zone filter. A delegated project
owner sees only the name and kind of the providers their scope may use, because the
zone filter is the boundary the administrator set and is not theirs to read or
edit.

Every save and delete writes an audit event, `dns.provider.configure` or
`dns.provider.delete`, in the same transaction as the row. A provider that still
owns records cannot be deleted.

## Creating records

`POST /api/v1/applications/{id}/domains/dns-records` takes a provider, up to 20
hostnames and a `replace_existing` flag, and answers with one result per hostname:
`created`, `exists`, `conflict`, `failed` or `skipped`. It answers 200 even when
some hostnames failed, because no transaction spans a third party: if the fourth of
twelve hostnames is refused after eight records already landed, a 500 would leave
you unable to tell what exists.

Existing records are read before anything is written. An identical record is left
alone and reported as `exists`. A different value at the same name and type is a
`conflict` and is left alone unless you asked for `replace_existing`, and then only
the exact record that was read is replaced. Nothing is ever blind-written.

Each hostname has six seconds and the whole request has 60, since the hostnames are
written one after another; the provider client holds a single connection, so
running them in parallel would queue on the same socket. Hostnames still unwritten
when the request's budget is gone come back as `skipped`, and retrying is safe: the
records that landed are then reported as existing.

Creating a record is not verifying it. See
[configure domains after deployment](pending-domains.md).

Nothing a provider says reaches an API response. Provider failures collapse to one
opaque value inside Hakopod and are reported as fixed prose, because providers
quote a rejected token back inside their own error messages.

## Verified behavior

The management routes, the application-scoped provider listing and the record
endpoint are covered by tests against a real PostgreSQL with an injected DNS
client: a mixed bulk over four hostnames returning four different statuses with a
200, a hostname the application does not own refused, a provider out of scope
refused, the idempotency header enforced, and no provider text or stored token in
any response body. Nothing here has been exercised against a real Cloudflare
account.
