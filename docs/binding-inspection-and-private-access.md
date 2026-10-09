# Inspect a binding and connect from the right host

A saved binding is not proof that an application connected to its database.
Open **Database connections** on the service page and choose **Inspect binding**.
The inspector separates four questions:

1. **Saved:** does the accepted application revision contain this binding?
2. **Resolved:** can Hakopod resolve its current database and secret references?
3. **Loaded by container:** did a connection test match the running container's
   values and trust settings to the current binding?
4. **Connection verified:** did that container authenticate and run the helper's
   minimal query?

**Test connection** runs the existing fixed helper in one application container.
It requires `deployments:write`. Inspection requires only `deployments:read` and
does not execute a command.

Hakopod retains the latest test for each binding. The inspector also checks the
application revision, pod identity, container identity, immutable environment
Secret and current CA. A replaced container or changed credential makes earlier
evidence stale. Evidence expires after five minutes even when those identities
match. Use **Refresh binding** to obtain a new observation.

The result identifies the tested pod and time. It contains no connection URL,
password, environment values or credential fingerprints. A successful test covers
one container and a minimal query. It does not test every replica or every table
permission. PostgreSQL, MySQL and Redis protocols are supported by the checker.
Other protocols report that the checker cannot verify them.

The CLI uses the same API:

```sh
hakopod inspect-binding APP --service api --variable DATABASE_URL
hakopod test-connection APP --service api --variable DATABASE_URL
```

The TypeScript SDK exposes `service.inspectBinding(variable)` and
`service.testConnection(variable)`.

## Private connection guide

Open a database's **Connections & security** tab and choose **Private connection**.
Select where the database client runs. The guide uses the database's observed
endpoint and explains each hop:

- **On your computer:** start a loopback-only Kubernetes forward on your SSH
  host, then forward that host's loopback port to your computer.
- **On the SSH host:** run the Kubernetes forward and database client there.
- **Inside an application container:** use the private endpoint from an
  application with an accepted database binding and allowed network access.

The guide does not create SSH accounts, distribute kubeconfigs, start tunnels
or verify a connection. The self-hosted operator must already have authorized
SSH and Kubernetes access. Applications must not receive cluster credentials.
Cloud workspace users can use a dedicated public endpoint with their source IP
allowed, or connect from a bound application.

Forwarded PostgreSQL connections use the endpoint hostname for certificate
verification and `PGHOSTADDR=127.0.0.1` for transport. MySQL clients need a local
hostname mapping when they verify a private endpoint through a loopback tunnel.
Download the database's public CA and install it on the machine running the
client. Keep certificate verification enabled.

MongoDB and Redis clusters discover additional members. A single-port forward
does not provide the network path those clients need. The guide directs those
clients to an allowed application container or a supported public endpoint.
For native clients without a generated command, the guide provides endpoint and
trust instructions and states that driver configuration remains necessary.

Kubernetes forwards close when their selected pod stops. Restart the forward
after a rollout or failover. A private `.svc` hostname is for cluster DNS and
does not become reachable from a laptop just because it appears in a URL.

CLI users can save the guide input in a JSON file:

```json
{
  "location": "local",
  "endpoint": "read_write",
  "ssh_host": "operator@your-server",
  "kube_context": "your-installation-context",
  "local_port": 15432
}
```

```sh
hakopod database private-access DATABASE_ID --file client-location.json
```

The SDK exposes `database.privateAccess(input)`. Generating a guide is read-only.
