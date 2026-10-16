# Private database access

Availability: `private_egress` is included in the **v0.1.0-alpha.13 candidate**.
Install a published alpha.13 or later release before using it; alpha.12 and
earlier reject this field. Until then, use the operator policy procedure in the
[website troubleshooting guide](https://hakopod.com/docs/private-database-connectivity/).
Managed Cloud does not accept these grants.

A database can be reachable from the VM but blocked from an application pod.
Hakopod allows DNS and declared peers, and allows public internet egress for
non-internal networks. That public exception excludes private, cluster,
loopback and metadata ranges. An RDS endpoint resolving to a private address is
therefore not automatically reachable. A TCP refusal or timeout alone does not
identify the failing layer: also check routes, security groups and the listener.

## Plan warning

`hakopod plan` (and the dashboard review) warns when a service's plain `env`
value contains a `host:port` that is, or resolves to, a private IPv4 address
not covered by a `private_egress` binding the service references. Example:

```text
payments: DATABASE_URL points at private address 172.31.33.237:5432, which
network policy blocks by default. Ask the installation administrator for a
private egress binding ...
```

The warning does not block the plan and does not grant access. Values held in
secrets are not inspected, so a `DATABASE_URL` stored as a secret produces no
warning; a connection refusal from a pod is then the signal. URLs without an
explicit port are not detected.

## Approve a destination

Private egress stays closed by default. Only an installation administrator
approves a destination, either through the admin API (recommended, no SSH or
restart) or the legacy read-only file.

### Admin API

```sh
curl -X PUT "$HAKOPOD_URL/api/v1/admin/private-egress-bindings/orders-db" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"project":"shop","environment":"production","application":"orders",
       "services":["api","setup","worker"],"cidrs":["10.20.30.0/24","10.20.31.0/24"],
       "ports":[5432],"expected_revision":0}'
```

`expected_revision` is `0` to create and the current `revision` to update or
delete (`DELETE` takes `{"expected_revision": N}`); a stale value returns 409.
`GET /api/v1/admin/private-egress-bindings` lists database bindings and, under
`managed_by_file`, the file bindings. Every write is audited and validated
together with the file bindings, so limits and rules below cover both sources
and a name defined in both is rejected. Changes apply to the next plan, accept
and reconcile without restarting `hakopod-api`.

In the dashboard, administrators use **Settings > Private destinations**. Users
with deployment read access can list the bindings for their own project and
environment with `GET /api/v1/private-egress-bindings?project=…&environment=…`
(each item shows `managed_by`: `file` or `database`), so a plan warning can name
a real binding.

#### Discover RDS subnets (optional)

**Discover from AWS** (`POST /api/v1/admin/private-egress-bindings/discover`,
body `{"region": "us-east-1"}`) uses the API process's AWS identity
(environment keys, the projected `hakopod-aws-identity` web identity, or the EC2
instance role over IMDSv2; shared config files are not read) and needs only
`rds:DescribeDBInstances` and `ec2:DescribeSubnets`. It proposes, per Aurora
cluster or RDS instance, the DB subnet group CIDRs and the endpoint port.
Subnets containing any cluster node address, and ranges that fail validation,
are listed as excluded. Discovery only pre-fills the form; an administrator
still saves the binding. It does not refresh: a failover into a subnet outside
the saved CIDRs needs an explicit update.

### Legacy file

The file remains supported as a read-only source. The administrator creates
`/etc/hakopod/private-egress.toml`.
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
its managed policy. Then remove the unused administrator binding.

Deleting a binding through the admin API revokes it without a restart: new
plans that reference it fail at once, and background service maintenance
rewrites the owned NetworkPolicy of referencing services to drop the rule
(it cycles through applications, so allow a few minutes). File changes still
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
