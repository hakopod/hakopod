Hakopod 0.1.0-alpha.59 improves how applications connect to managed databases,
how deployments handle migrations, and how database failures are explained.

- **Binding changes replace the affected pods.** Database credentials and public
  CA files enter the new pod template together. Existing jobs and retained
  ReplicaSets keep their original settings, including workloads created before
  immutable environment snapshots were introduced.
- **Supported templates configure database TLS.** The pinned Infisical and
  GlitchTip templates declare the driver settings they need for managed
  PostgreSQL and Redis. Certificate and hostname verification stay enabled.
- **Test a connection from the application.** The dashboard, CLI and SDK can
  check the running container's environment, DNS, TCP connection, TLS,
  authentication and a small read query. Results identify the tested pod and
  distinguish stale configuration from a working current connection.
- **Give migrations time to finish.** Services can declare a startup budget of
  10 to 900 seconds. Deployment progress separates explicit migration jobs from
  service startup, and the affected catalog templates declare their budgets.
- **Keep the reason a database failed.** Memory-limit failures retain their
  Kubernetes evidence, timestamp, member identity and configured limit after a
  later healthy observation.
- **Review resource recommendations before deployment.** Database creation and
  resize planning show per-member resources, total allocation, connected
  applications, observed usage and available capacity. Recommendations are
  advice; they do not add another deployment restriction.

Connection tests currently support PostgreSQL, MySQL and Redis wire protocols,
including compatible Vitess and MyDuck endpoints. MongoDB, ClickHouse and Oracle
return an explicit unsupported result. A passed test covers one container and a
small read query; it does not verify every application permission, migration or
replica. Test execution requires deployment permission and a configured,
digest-pinned connection helper. Older bound services need redeployment to
receive that helper. Cloud distributes its helper through its separate release.

The release includes guides for
[binding rollouts and connection tests](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.59/docs/database-binding-reliability.md),
[application TLS](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.59/docs/application-database-tls.md),
[startup and migration handling](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.59/docs/application-lifecycle.md)
and [resource recommendations](https://github.com/hakopod/hakopod/blob/v0.1.0-alpha.59/docs/database-resource-recommendations.md).

Install this prerelease with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.59/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.59
```

Upgrade an existing alpha.57 or alpha.58 installation with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.59/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.59
```

Older installations need a supported intermediate release. The upgrade backs up
PostgreSQL and configuration, then restarts the management API and dashboard.
Keep those backups; replacing binaries does not reverse database migrations.
The OSS release and the Cloud production rollout have separate verification.
