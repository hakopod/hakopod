Hakopod 0.1.0-alpha.54 adds the Outpost webhook delivery template with independently bundled or existing PostgreSQL, Redis and RabbitMQ.

The preset runs Outpost v1.6.0 as separate API, delivery and log services. A deployment job applies upstream PostgreSQL and Redis migrations before the server roles start. Only the API can receive public HTTP ingress; bundled databases and the broker keep private listeners and separate persistent volumes. All images are digest-pinned, and Outpost runs without root or cluster credentials.

Each dependency choice changes the rendered services and required secrets. Private AMQP bindings URL-escape the bundled broker credentials at deployment time. External PostgreSQL and RabbitMQ URLs stay in application-scoped secrets; PostgreSQL requires an explicit TLS policy and external Redis verifies TLS by default. Keep the API, JWT and encryption keys stable with coordinated data backups. This is a new-install preset; switching images or reverting a deployment does not reverse upstream data migrations.

Shared hosted compute does not provide the job and binding capabilities required by this stack. Use self-hosted Hakopod or connect your own server in Cloud. Public TLS, arbitrary external providers, portal integration, scaling and backup restoration require separate operator validation.

This release retains the database network maintenance and bounded Actions drain fixes from [alpha.53](https://github.com/hakopod/hakopod/releases/tag/v0.1.0-alpha.53), and private self-hosted ClickHouse support from [alpha.52](https://github.com/hakopod/hakopod/releases/tag/v0.1.0-alpha.52).

Install alpha.54 with the published installer:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.54/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.54
```

Upgrade an existing installation with:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.54/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.54
```

Direct upgrades are supported from alpha.52 and alpha.53. Older installations need a supported intermediate release. Each upgrade backs up PostgreSQL and configuration and restarts the management API and dashboard. Retain backups because swapping binaries does not undo database migrations.
