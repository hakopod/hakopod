# Terraform provider

The Hakopod Terraform provider manages projects, private networks, applications
and secrets declaratively. It sends the same application TOML the CLI sends, to
the same API, so the server remains the only validator: a specification Terraform
accepts is a specification `hakopod deploy` would accept, and a rejected one fails
the apply with the server's own message.

The provider is published on the Terraform Registry as
[`hakopod/hakopod`](https://registry.terraform.io/providers/hakopod/hakopod/latest).
Its source is at
[hakopod/terraform-provider-hakopod](https://github.com/hakopod/terraform-provider-hakopod).

## Install

Pin the provider in `required_providers` and let `terraform init` fetch it from
the registry. There is nothing to build or copy into a plugin directory.

```hcl
terraform {
  required_providers {
    hakopod = {
      source  = "hakopod/hakopod"
      version = "~> 0.1"
    }
  }
}

provider "hakopod" {}
```

```sh
terraform init
```

The provider reads the same two variables the CLI reads, so an authenticated
shell needs no provider arguments:

```sh
export HAKOPOD_API_URL=https://hakopod.example.com
export HAKOPOD_API_KEY=...
```

Create the key with `hakopod key-create --name terraform --project demo
--environment production --ttl 720h` and keep it in the CI secret store. The key's
project, environment and permission scope bound everything Terraform can change;
the provider cannot exceed them.

## Resources

- `hakopod_project` — a project and its environments.
- `hakopod_virtual_network` — a private network and its segments. See
  [virtual networks](virtual-networks.md) for grants and service connections.
- `hakopod_application` — an application specification, given either as
  application TOML in `config` or as the equivalent JSON in `spec`. Use one or
  the other, not both.
- `hakopod_secret` — a secret value bound to an application or environment.

## A worked example

```hcl
resource "hakopod_project" "demo" {
  name        = "demo"
  environment = "production"
}

resource "hakopod_application" "shop" {
  project     = hakopod_project.demo.name
  environment = "production"
  name        = "shop"

  config = <<-TOML
    schema_version = 1
    name = "shop"

    [services.web]
    image = "nginx:stable-alpine"
    port = 80
    public = true
    replicas = 2
  TOML
}
```

`terraform apply` creates the project, then submits the specification as one
deployment. The [TOML reference](toml.md) documents every field `config` accepts;
[application lifecycle](application-lifecycle.md) covers jobs, file mounts and
connection bindings.

## Deleted names are retired permanently

Deleting an application or a project retires its name forever. The server records
the name in `retired_resource_names` as part of the deleting transaction, and
every later create checks that table: a retired project ID is refused with
`409 retired_project` ("This project ID was deleted. Choose a new ID.") and a
retired application name with `409 retired_application` ("This application name
was deleted. Choose a new name."). There is no release, no reuse and no cooldown.

This matters more under Terraform than under the CLI, because a `terraform
destroy` in a scratch workspace permanently consumes every name in it. Reapplying
the same configuration afterwards fails at create. Use distinct names per
workspace rather than recreating one.

Deletion is also gated, not immediate. The server only deletes an application
that has no services left, whose empty revision has already deployed successfully,
and that has no queued or running deployments, builds, source imports, previews,
enabled backup schedules or pending volume work. A project only deletes once it
holds no applications, builds, runtime resources, retained data or secret-provider
scope grants. A destroy against a live application therefore fails with a conflict
until the workload is drained first.

## CLI or Terraform?

Reach for the CLI when a human is deploying: `hakopod deploy --file` on one
`hakopod.toml` is the shortest path from an edit to a running revision, it streams
logs, and `--wait` gives an exit code a script can branch on. Reach for Terraform
when the estate is the artifact — when projects, networks, applications and
secrets should exist because a reviewed configuration says so, when you want a
plan before a change lands, and when the same definition is instantiated across
environments. Both talk to one API and one validator, so mixing them is safe as
long as each resource has a single owner: an application Terraform manages should
not also be deployed by hand, or the next plan will show drift.
