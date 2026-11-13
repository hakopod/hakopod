Hakopod 0.1.0-alpha.91 moves a Railway or Vercel project into Hakopod.

- **Migrate from Railway or Vercel (preview).** New application → Migrate from Railway or Vercel reads a project with a Railway or Vercel API token, shows what Hakopod will create, and recreates it as one application. Services built from GitHub get a build configuration and a pull request in your repository that adds the build workflow; merging it builds and starts the service. Image services keep their image, port, replicas and health check. Variables come over as env, or as application secrets when they look sensitive, and Vercel's sensitive values are pasted in the review. PostgreSQL and Redis (including Upstash and Vercel KV) become managed databases with their data copied; MySQL and MongoDB are created empty. Variables like `DATABASE_URL` are bound to the new databases, crons become jobs, and custom domains are added as pending so you switch DNS when ready. The source project is only read. The token is sealed, used for this one migration and erased when it finishes, fails or is cancelled, or after 24 hours. Agents can read migrations but cannot start one. This has been tested against fake Railway, Vercel and GitHub APIs only: try it on a small project first. See docs/migrate-from-railway-vercel.md.

Install this prerelease:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.91/installer.sh -o installer.sh &&
sudo sh installer.sh --version 0.1.0-alpha.91
```

Upgrade an alpha.89 or alpha.90 installation:

```sh
curl --fail --location \
  https://github.com/hakopod/hakopod/releases/download/v0.1.0-alpha.91/installer.sh -o installer.sh &&
sudo sh installer.sh --upgrade --version 0.1.0-alpha.91
```

Older installations need a supported intermediate release. The upgrade backs up PostgreSQL and configuration before it restarts the management API and dashboard. Keep these backups. Replacing binaries does not reverse database migrations.

The OSS release and Cloud production deployment have separate verification.
