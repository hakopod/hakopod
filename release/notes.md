Hakopod 0.1.0-alpha.69 fixes application logs that showed only `[log output withheld: redaction limit exceeded]`.

- Hakopod hides an application's secrets in its logs by registering each secret value, and each line of a multi-line value, in several encodings. The limit was 512 entries, about 130 secret values or a few certificates and keys. A pod over the limit had every log line withheld. Since alpha.67 the log history keeps one masker per pod, so every stored line of such a pod was withheld. The limit is now 4096 entries and 4 MiB, enough for the 512 KiB of secret data Hakopod accepts per pod.
- Logs are still withheld when a pod goes over the new limit. The server now logs a warning naming the pod, the number of secrets and their total size, never their values.
- Lines already stored as withheld stay withheld. New lines are masked normally.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.69/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.69
```

Upgrade an alpha.67 or alpha.68 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.69/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.69
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
