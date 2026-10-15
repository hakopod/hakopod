# Design: database-managed private egress bindings

Status: proposed (not implemented). Follows the plan warning added for
private database destinations.

## Problem

Approving a private destination today needs SSH: write
`/etc/hakopod/private-egress.toml`, add a systemd drop-in for
`HAKOPOD_PRIVATE_EGRESS_FILE`, restart `hakopod-api`. Users who do not run
Kubernetes cannot do this, and a restart is disruptive. Private egress stays
closed by default (tenant and VPC isolation); only the approval path changes.

## Proposal

1. **Table `private_egress_bindings`** with the existing `PrivateEgressBinding`
   fields plus `revision`, `created_by`, `updated_at`. Writes go through
   `ValidatePrivateEgressBindings` on the union of file and database bindings,
   so the 128-binding limit and reserved-range checks are unchanged.
2. **Authority**: only installation administrators (same permission that
   manages secret providers) may create, change or delete rows. Project
   owners can read the bindings scoped to their applications so the plan
   warning can name a real binding. Every change is audited.
3. **API**: `GET/PUT/DELETE /api/v1/admin/private-egress-bindings/{name}` with
   `expected_revision` optimistic concurrency, mirroring secret providers.
   Dashboard: Settings > Network > Private destinations.
4. **Resolution**: `resolvePrivateEgress` reads file bindings plus database
   bindings on each plan, accept and reconcile (no restart). Name collision
   between the file and the database is a startup/validation error; the file
   remains a supported, read-only legacy source shown as "managed by file".
5. **Revocation**: deleting a row marks referencing applications for
   reconcile so owned NetworkPolicies drop the rule without a redeploy.
   Referencing deployments then fail planning with the existing message.

## Optional: AWS discovery

When the installation has an AWS identity, an admin-only "Discover" action
calls `rds:DescribeDBClusters`/`rds:DescribeDBInstances` and
`ec2:DescribeSubnets` for the DB subnet group, and proposes CIDRs and ports.
Subnets containing any cluster node address are excluded (incident case:
the node's 172.31.38.0/24). Discovery only pre-fills a form; an admin still
saves the binding. No automatic refresh; a failover outside the proposed
subnets needs an explicit update.

## Out of scope

Opening private egress by default; inspecting secret values at plan time
(would require reading decrypted secrets in the API path).
