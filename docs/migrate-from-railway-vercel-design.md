# Migrate from Railway and Vercel: design

Status: in progress. User-facing docs go in `docs/migrate-from-railway-vercel.md`
once it ships.

## Goal

A user pastes a Railway or Vercel API token, picks a project, reviews what
Hakopod will create, and starts the migration. Hakopod recreates the project as
one Hakopod application:

- each app becomes a service, built from the same GitHub repository and branch;
- variables carry over, sensitive ones as application secrets;
- PostgreSQL and Redis databases become Hakopod managed databases, with their
  data copied;
- MySQL and MongoDB databases are created empty, with a warning;
- custom domains are added as pending, so the user cuts over DNS when ready;
- cron schedules become Hakopod jobs.

The source project is only read. Nothing on Railway or Vercel changes.

## Decisions

- **Builds.** A service built from Git gets a Build configuration. Hakopod opens a
  pull request in the user's repository that adds the build workflow, the same
  as Build from Git. The service deploys after its first build.
- **Tokens.** The source token is used for one migration only. It is sealed with
  the installation auth key in the migration record and erased when the
  migration finishes, fails or is cancelled, or after 24 hours. It is never
  returned by the API or written to logs or audit events.
- **Data.** PostgreSQL and Redis data is copied. MySQL and MongoDB are created
  empty in this version.
- **go.mod stays unchanged.** Provider clients use `net/http` and
  `encoding/json`. Copy jobs run the database's own client tools in a pod.

## Components

1. **Provider readers** (`internal/migrate/railway`, `internal/migrate/vercel`).
   Read-only clients:
   - `New(token string, opts) *Client`;
   - `ListProjects(ctx) ([]migrate.Project, error)`;
   - `Read(ctx, projectID, environmentID string) (migrate.Inventory, error)`.

   They are tested against fake HTTP servers.
2. **Converter** (`internal/migrate/convert.go`).
   - `Convert(inv migrate.Inventory, opts ConvertOptions) (Proposal, error)`.
   - The `Proposal` holds:
     - the application spec, ready for the normal plan;
     - one build plan per Git service;
     - one database plan per database;
     - the secrets to create, as name → value;
     - the domains;
     - the variables the user must fill in;
     - warnings.
   - It is pure and fully unit-tested.
3. **Database copy** (`internal/cluster`). Runs a one-off pod in the
   application's namespace that streams data from the source URL into the new
   managed database:
   - PostgreSQL: `pg_dump | pg_restore`;
   - Redis: dump and restore.

   It reports progress and errors, and its egress is limited to the source host
   and the target database.
4. **Migration record, worker and API.**
   - A `migrations` table with leased steps, in the backup worker style.
   - Routes:
     - `POST /migrations/projects`: list the projects the token can read; the token is not stored.
     - `POST /migrations`: read, convert, and save a planned migration with its proposal.
     - `GET /migrations/{id}`: the proposal and step progress.
     - `PUT /migrations/{id}/variables`: values for unreadable variables.
     - `POST /migrations/{id}/start`, with an Idempotency-Key.
     - `POST /migrations/{id}/cancel`.
   - The worker runs the steps in order:
     1. secrets;
     2. databases, waiting until each is ready;
     3. data copy;
     4. the first deployment;
     5. builds and their pull requests;
     6. domains;
     7. erasing the token.
5. **Dashboard.** A Migrate option on New application. The flow has four steps:
   1. token;
   2. project;
   3. review: services, builds, databases, secrets, domains, warnings, and fill-ins for missing values;
   4. progress.

## Not in this version

- MySQL and MongoDB data.
- Railway volume data.
- Vercel Edge Middleware and Edge Config.
- Preview environments.
- Keeping the source and Hakopod in sync after the copy.
