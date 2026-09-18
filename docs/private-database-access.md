# Private database access

Availability: `private_egress` is implemented on this branch and is **not included
in v0.1.0-alpha.12 or earlier**. Use the operator policy procedure in the
[website troubleshooting guide](https://hakopod.com/docs/private-database-connectivity/)
until a release includes it. Managed Cloud does not accept these grants.

A database can be reachable from the VM but blocked from an application pod.
Hakopod allows DNS and declared peers, and allows public internet egress for
non-internal networks. That public exception excludes private, cluster,
loopback and metadata ranges. An RDS endpoint resolving to a private address is
therefore not automatically reachable. A TCP refusal or timeout alone does not
identify the failing layer: also check routes, security groups and the listener.

## Approve a destination

The installation administrator creates `/etc/hakopod/private-egress.toml`.
Use actual database subnet CIDRs, not the whole VPC. The following addresses
are examples; they must be replaced. Multiple database subnets can cover the
provider's failover addresses. Hakopod does not resolve or refresh CIDRs from DNS.

```toml
schema_version = 1

[[bindings]]
name = "orders-db"
project = "shop"
environment = "production"
application = "orders"
services = ["api", "setup", "worker"]
cidrs = ["10.20.30.0/24", "10.20.31.0/24"]
ports = [5432]
```

This grants TCP to those subnets and that port only, for services that explicitly
reference `orders-db` in the exact project, environment and application. It does
not configure credentials, routes, security groups or TLS. Do not include node,
control-plane or unrelated tenant addresses in an approved subnet.

Keep the file writable only by the administrator and readable by the API:

```sh
sudo chown root:hakopod-api /etc/hakopod/private-egress.toml
sudo chmod 0640 /etc/hakopod/private-egress.toml
sudo mkdir -p /etc/systemd/system/hakopod-api.service.d
sudo tee /etc/systemd/system/hakopod-api.service.d/private-egress.conf >/dev/null <<'UNIT'
[Service]
Environment="HAKOPOD_PRIVATE_EGRESS_FILE=/etc/hakopod/private-egress.toml"
UNIT
sudo systemctl daemon-reload
sudo systemctl restart hakopod-api
```

These commands target the standard installer. Custom deployments set
`HAKOPOD_PRIVATE_EGRESS_FILE` to the equivalent protected file. Startup rejects
unknown fields, duplicate names/ports/CIDRs, wildcard scopes and malformed rules.
Files are bounded to 64 KiB and 128 bindings. A binding contains 1–32 service
names, 1–16 canonical RFC1918 IPv4 CIDRs of /16 or narrower, and 1–16 TCP ports.
The supported installer pod and Service ranges (`10.42.0.0/16`, `10.43.0.0/16`)
are rejected. Public, link-local, metadata, loopback and IPv6 destinations cannot
be granted with this feature. It targets the supported IPv4 installation profile.

## Reference it in application TOML

In each service's existing table, add:

```toml
[services.api]
# Keep the service's image, port and other existing settings.
private_egress = ["orders-db"]
```

The API, CLI and dashboard TOML editor use the same field. Up to 16 destination
references are allowed per service. It applies to ordinary services, deployment
jobs and scheduled jobs, including internal-only networks. Other services gain
no access automatically. Secrets such as `DATABASE_URL` never grant networking.

Review and deploy the configuration. Planning, deployment acceptance and
reconciliation verify the grant; an absent or wrong-scope reference fails with
an administrator instruction. Hakopod includes the reference in the revision
diff and reconciles its owned network policy before starting workloads. Renaming
or moving a service/application requires matching administrator authorization.

## Change or remove access

Remove a service's reference and successfully deploy to remove the grant from
its managed policy. Then remove the unused administrator binding. File changes
require an API restart and a deployment to reconcile existing policies; a restart
alone does **not** revoke running pods' rules. For emergency revocation, the
administrator must also change the existing policy or external firewall.

An operator-created workaround policy is additive and independent. Remove that
specific policy only after the native policy is deployed and verified, otherwise
it can retain access after the application reference is removed.

## Verify the right layer

First test DNS and TCP from the host and the affected pod, without credentials.
Then verify TLS using the database hostname and provider CA. PostgreSQL clients
should use `sslmode=verify-full` with the correct `sslrootcert`; opening a network
path is not a reason to weaken certificate validation. A successful TCP probe
does not prove authentication, migration safety or application readiness.

For an RDS failover, a /32 rule covers only one IP. Prefer the reviewed database
subnets, or explicitly update the operator grant when the destination changes.
