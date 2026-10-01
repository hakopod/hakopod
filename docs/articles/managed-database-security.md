# A public database endpoint needs more than a hostname

Draft article. Expanded TLS and trust renewal have native development evidence;
dedicated public managed database endpoints remain planned. This is not a
production availability announcement.

Making a database reachable from outside a private network is a security change.
A DNS record identifies a destination; it does not establish encryption,
authentication, authorization or a safe exposure boundary.

TLS has two jobs: encrypt the connection and verify the server's identity. A
client that encrypts traffic while accepting any certificate leaves an important
part of that protection incomplete. PostgreSQL clients should verify the
hostname and the issuing CA, for example with `sslmode=verify-full` and the
appropriate CA trust. Other drivers have their own equivalent settings. Never
solve a certificate failure by turning verification off.

Certificate management is a lifecycle. The platform needs to validate the key
pair, allowed names and validity period, make rotation visible, verify that the
running server loaded the new certificate and retain a safe overlap when trust
changes. The CA certificate can be distributed to clients. The server's private
key must remain private.

Exposure needs a separate lifecycle too. Before enabling an endpoint, review its
route purpose, allowed client networks and resource allocation. Enforce the
source allowlist at the ingress that sees the client address. NAT or another
proxy can change that address, which is why a policy's presence is not enough:
test both an allowed source and a denied source.

Cluster discovery complicates the design. A public MongoDB connection may need
to discover reachable replica members. A Redis Cluster client must reach the
members advertised by its slot map. Opening one TCP port without considering
those advertisements can produce an endpoint that accepts the first connection
and then fails on normal operations.

Revocation and recovery deserve equal attention. Disabling access should have an
observable completion state. Certificate expiry, primary failover and a
partially applied change must not silently reopen an old listener or weaken
source restrictions. Backup recovery remains a separate responsibility; replicas
can reproduce an accidental deletion and do not replace a verified backup.

The [platform expansion plan](../managed-database-platform-plan.md) records the
intended controls and acceptance tests. Consult the [current managed database
guide](../managed-databases.md) for implemented behavior before planning a
production rollout.
