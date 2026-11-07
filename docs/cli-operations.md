# Everyday CLI operations

These commands cover the routine work that used to need `hakopod api call`, a
hand-converted specification or the dashboard. They all take the usual
`--project` and `--environment` context and accept an application name or ID.

## See what is running

```sh
hakopod apps                 # one line per application: ID, name, revision, status
hakopod apps --json
hakopod bindings shop        # every service's database and service bindings
hakopod secret list shop     # secret names only; values never leave the server
hakopod virtual-networks     # the environment's virtual networks and their segments
hakopod virtual-networks backend          # one network as canonical TOML
hakopod virtual-networks backend --json   # with its current connections
hakopod networks shop        # the networks one application's services join
```

## Change a running application

Export the live specification instead of keeping a copy in the repository that
can drift. The export plans with no changes, and secrets appear only as
references:

```sh
hakopod export shop > hakopod.toml
hakopod plan --file hakopod.toml        # 0 changes
```

`hakopod manifests shop [--service api]` prints the Deployment, Service and
Ingress Hakopod generates for the current revision, with any
[Kubernetes patches](kubernetes-patches.md) applied, without applying anything.

For the common case of shipping a new image there is no file at all. The
command plans the live specification with one image replaced, prints the
reviewed changes to stderr and deploys that plan:

```sh
hakopod set-image shop --service api --image ghcr.io/acme/api@sha256:DIGEST --wait
```

Restart a service, for example so it reads a changed secret, without editing
`restart_nonce`:

```sh
hakopod restart shop --service api --wait
```

`--service` can be left out when the application has a single service. Both
commands create a new revision through the canonical API and accept
`--idempotency-key` for retries.

## Secrets

Secrets belong to one application. The value is read from a file or from
redirected stdin, never from an argument, so it stays out of shell history and
process listings. A trailing newline is dropped.

```sh
hakopod secret set shop STRIPE_KEY --from-file stripe.key
printf '%s' "$TOKEN" | hakopod secret set shop GITHUB_TOKEN --stdin
hakopod secret delete shop STRIPE_KEY
```

Running pods keep the old value until they restart; `secret set` says so and
names the restart command. To share one value between applications, reference
an external secret provider instead of copying it.

## Tenant hostnames

`hakopod hostnames list|add|show|remove APP [--service S] [HOSTNAME]` manages a
service's [SaaS custom hostnames](custom-hostnames.md); `list` takes
`--status pending_dns` and `--json`.

## Run a command in a pod

`exec` picks the service when there is only one and the pod when exactly one is
ready; otherwise it lists the ready pods to choose from. The container defaults
to `app`.

```sh
hakopod exec shop --command-json '["ls","/data"]' --allow-exec
hakopod exec shop --service api --pod api-7f9c-abcde --command-json '["env"]' --allow-exec
```

Commands still run for at most 20 seconds; use `hakopod terminal` for longer
interactive work.

## Agents

The stdio and HTTP MCP servers offer `secret_names` (names only),
`custom_hostnames` (one page of a service's tenant hostnames) and, with
deployment opt-in, `restart_service`. No MCP tool sets a secret value, so
values never pass through an agent.
