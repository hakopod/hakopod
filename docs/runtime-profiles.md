# Workload runtime profiles

A self-hosted operator can grant one service access to an installed Kubernetes
RuntimeClass. The service selects a short alias in its application TOML.

## Operator setup

1. Install and verify the runtime handler on each eligible node.
2. Create the matching RuntimeClass without scheduling rules or pod overhead.
3. Save this binding in `/etc/hakopod/runtime-profiles.toml`, owned by the server user with mode `0600`.

```toml
schema_version = 1

[[bindings]]
name = "bounded-worker"
project = "demo"
environment = "production"
application = "analytics"
service = "worker"
runtime_class = "bounded-worker"
handler = "bounded-runc"
```

Set `HAKOPOD_RUNTIME_PROFILES_FILE` to that path, or reference it in the
[operator configuration](operator-configuration.md), then restart the API:

```toml
[runtime_profiles]
file = "/etc/hakopod/runtime-profiles.toml"
```

Files use strict version-1 TOML, at most 64 KiB and 64 unique bindings.
Each grant names one exact project, environment, application and service.
The file must belong to root or the server user. Parent directories must remain
under trusted operator control. Group and other users must not have write access.

## Application

Add the alias to the existing service definition:

```toml
[services.worker]
runtime_profile = "bounded-worker"
```

The normal deployment review shows this change. Planning, acceptance and
reconciliation check the grant and the installed class's handler. Deployments,
one-time jobs and scheduled jobs use the selected runtime.

Profiles are unavailable for managed-cloud installations, hosted workload
policies, serverless services and Managed Actions. Preview creation and service
transfer cannot copy an existing grant into another application.
RuntimeClasses with pod overhead or scheduling rules are rejected until planning
accounts for them. Use the service's existing placement settings to select nodes.

## Limits and changes

The operator owns the handler's OCI configuration, node access and runtime
plugins. Hakopod selects the approved class; it does not install a handler or
certify its CPU, memory, task or syscall limits. Applications that depend on
those controls must check them at startup and fail if they are absent.

Changing the file requires an API restart. Removing a grant blocks subsequent
deployment acceptance and reconciliation. It does not stop existing pods or
scheduled workloads. Review and deploy an explicit workload change to retire them.

Keep a handler installed while any retained workload or rollback revision needs
it. Verify ordinary routes and workloads after changing node runtime settings.
