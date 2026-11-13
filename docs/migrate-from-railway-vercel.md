# Migrate from Railway or Vercel

Move a Railway or Vercel project into one Hakopod application. Hakopod reads the
project with an API token, shows you what it will create, and then recreates
the apps, variables, databases, cron schedules and custom domains. The source
project is only read. Nothing on Railway or Vercel changes, so it keeps serving
traffic until you switch DNS.

Open **New application → Migrate from Railway or Vercel**.

## What comes over

| On Railway or Vercel | On Hakopod |
| --- | --- |
| A service or project built from GitHub | A service with a build configuration. Hakopod opens a pull request that adds the build workflow; merging it builds and starts the service. |
| A service from a Docker image | A service with the same image, port, replicas and health check. |
| Variables | Plain env values, or application secrets for anything sensitive (tokens, passwords, connection strings). |
| PostgreSQL | A managed PostgreSQL database with its data copied. |
| Redis (including Upstash and Vercel KV) | A managed Redis database with its keys and expiry times copied. |
| MySQL, MongoDB | A managed database of the same engine, created empty. Copy the data yourself. |
| A variable that points at a database, like `DATABASE_URL` | The same variable, now bound to the new managed database. |
| A Railway cron schedule | A Hakopod job on the same schedule (UTC). |
| A Vercel cron | A small job that calls the same path on your service on schedule. |
| Custom domains | Domains added as pending, with the DNS records to set. |
| A Railway volume | An empty 5 GiB volume at the same path. Data in the volume is not copied. |

What stays behind: preview environments, Vercel Edge Middleware and Edge Config,
platform domains (`*.up.railway.app`, `*.vercel.app`), and volume data. The
review lists everything else that will not carry over for your project.

## Before you start

- **A token.** Hakopod needs one token to read the project:
  - **Railway:** Account Settings → Tokens. An account or workspace token can
    list all your projects; a project token reads one environment of one project.
  - **Vercel:** Account Settings → Tokens. Choose the team that owns the
    project as the token's scope.
- **A GitHub connection** in Hakopod, for services built from GitHub. Hakopod
  uses it to open the build pull requests. See [Git connections](git-connections.md).
- **A public database endpoint** on Railway, if you want its data copied. A
  Railway database without a TCP proxy (no `DATABASE_PUBLIC_URL`) is created
  empty, with a note in the review.

## Steps

1. **Source.** Pick Railway or Vercel, paste the token and load your projects.
2. **Project.** Pick the project (and, on Railway, the environment), then the
   Hakopod project, environment, application name and GitHub connection.
3. **Review.** Check the services, builds, databases, secret names, domains and
   warnings. Vercel does not return the values of sensitive variables, so the
   review asks you to paste them. You cannot start until every value is filled in.
4. **Progress.** Hakopod works through seven steps and shows each one:
   1. secrets;
   2. databases, waiting until each is ready;
   3. data copy;
   4. the first deployment;
   5. build pull requests;
   6. domains;
   7. finish, which erases the token.

You can cancel while it runs. Anything already created stays, so you can look at
it or delete it.

## After the migration

1. **Merge the build pull requests.** Each one starts the first build of its
   service; the service starts when the build succeeds.
2. **Check the application** on its Hakopod address before you move traffic.
3. **Point DNS** for each custom domain at Hakopod, using the records shown on
   the domain. The domain goes live once it verifies.
4. **Shut down the source project** on Railway or Vercel when you are happy.

Data written to the source after the copy ran is not copied. For a database that
changes often, stop writes on the source (or switch DNS) shortly before you
start the migration.

## Security

- The token is used to read the source project only. It is sealed with the
  installation's encryption key, bound to this one migration, and erased as soon
  as the migration finishes, fails or is cancelled, or after 24 hours. It is
  never returned by the API or written to logs or audit events.
- Values you paste for sensitive variables are sealed the same way and erased at
  the same time. They become application secrets; the review shows only names.
- The data copy runs as a one-off job next to the new database. It can reach
  only the source database's public address and the new database, and refuses a
  source on a private or internal address.
- Starting, planning and supplying values are dashboard actions. Agents with API
  keys can read migrations but cannot start one, because that takes a
  third-party token.

## Limits

- One Railway environment or the Vercel production environment per migration.
- Up to 20 services, and 32 secrets per service.
- PostgreSQL data copies with `pg_dump` and `pg_restore` in one transaction. A
  source newer than Hakopod's supported versions is refused. Extensions your
  source uses must exist on Hakopod's PostgreSQL, or the restore stops and names
  the missing one.
- Redis copies key by key. Very large keyspaces take a while, and keys written
  during the copy may be missed.
- Railway and Vercel API details were built from their published documentation.
  If a provider changes its API, the review shows an error rather than a partial
  plan.

## API

The dashboard uses these routes; see the OpenAPI document for shapes.

- `POST /api/v1/migrations/projects`: list the projects a token can read.
- `POST /api/v1/migrations`: plan a migration and return its proposal.
- `GET /api/v1/migrations?project=&environment=`, `GET /api/v1/migrations/{id}`.
- `PUT /api/v1/migrations/{id}/variables`: values for sensitive variables.
- `POST /api/v1/migrations/{id}/start`, with an `Idempotency-Key` header.
- `POST /api/v1/migrations/{id}/cancel`.
