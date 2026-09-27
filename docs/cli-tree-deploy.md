# Deploying a folder of applications

`hakopod validate`, `plan` and `deploy` accept `--dir ROOT` to work on a folder
of applications and one virtual network together. Use it when several
applications share a network and are kept in one repository.

Each immediate subfolder is one application. A folder may hold one file or many:
every `.toml` file inside it is merged into that single application's
specification, so one service per file is a valid way to lay out an application.

## Layout

```text
devops/
├── network.toml          # optional: one virtual network, e.g. name = "shop"
├── shop/                 # one application named "shop"
│   ├── web.toml          # becomes services.web
│   ├── api.toml          # becomes services.api
│   └── data.toml         # may declare [services.*] tables itself
└── billing/
    └── hakopod.toml      # a single file: the existing path, unchanged
```

- The root may hold one `network.toml`. It uses the same format as
  **Networks → Create network → import**; see [virtual networks](virtual-networks.md#create-a-network).
- Each immediate subfolder is one application, named after the folder. The
  folder holding exactly one `hakopod.toml` keeps its previous behaviour; the
  file name is matched case-insensitively, so `HAKOPOD.toml` works. A folder
  holding two case variants, such as `hakopod.toml` and `HAKOPOD.toml`, is an
  error.
- Deeper folders and hidden folders are ignored.

Separately, without `--file` the CLI finds `hakopod.toml` in the current folder
case-insensitively. `HAKOPOD.toml` used to be missed on case-sensitive
filesystems such as Linux.

## How files in one folder are merged

Files are read in filename order. Dotfiles and files that do not end in `.toml`
are ignored. `network.toml` is only special at the root of the tree; inside an
application folder it is merged like any other file.

A file that has a `services` key is an application document, and its
`[services.*]` tables are used as written. A file with no `services` key is one
service, named after the file's basename: `web.toml` becomes `services.web`.
Both kinds can sit in the same folder.

The application's name is the folder name. A `name` key may repeat it, but a
`name` that disagrees with the folder is an error.

Four keys exist at both application and service level: `networks`, `env`,
`secrets` and `env_file`. In a bare-service file they are always service fields.
Only a file that has a `services` table can set the application-level forms of
them.

These rules make conflicts errors rather than silent winners:

- At most one file may set each of `name`, `recovery`, `inject_env`, `domains`
  and `volumes`. A second file setting one of them is an error naming both
  files.
- Every file may carry `schema_version = 1`. Equal values agree; differing
  values are an error.
- A service name defined in two files is an error naming both files. There is no
  last-write-wins.
- Application-level `networks`, `env` and `secrets` merge key by key. The same
  key set in two files is an error.

`env_file` is not supported in a merged folder, and a folder with more than one
`.toml` file that uses it fails validation instead of quietly dropping it.
`env_file` expansion happens on the server on the TOML path only, and a merged
folder is submitted as a JSON specification. A folder with exactly one
`hakopod.toml` still takes the TOML path and keeps `env_file`. See [environment
files and shared variables](environment-and-build-reuse.md) for the alternatives.

## Services in one application already talk to each other

Merging cooperating services into one application is usually all the networking
they need. Every application gets an implicit `default` network, every service
joins it, and with no `network_access` block every sibling is allowed. A service
reaches a sibling at its bare short name on a declared port, with no
configuration at all: `http://api:8080` from `web`.

A virtual network is only needed to cross an application boundary. A tree whose
folders each hold a set of cooperating services needs no `network.toml`.

One caveat: a service with no port gets no Service object and is not reachable
by name. Declare a `port`, or a named entry in `ports`, on anything a sibling
calls.

## A merged application is one blast radius

Services merged into one application share a release. A failed rollout of any
one of them reverts every service in that application to the last successful
revision, because the server restores the complete last successful group.
Multi-service updates are not atomic, so a partially applied group is possible
during failure and recovery.

Releases also serialize per application: one deployment is in flight at a time,
the queue holds 20, and two people deploying different services of the same
application at once contend on `expected_revision`.

Prefer separate folders, and therefore separate applications, when services
have independent release cadences, when different teams deploy them, or when one
failing component must not restart the others. Merge services that are deployed
together and would be rolled back together anyway.

## Limits apply to the merged whole

A merged folder is one application, so one application's limits cover all of it:
at most 20 services, 16 declared networks including the implicit `default`, 20
custom domains, and 20 volumes totalling at most 200 GiB. The application also
shares one namespace resource quota: 8 CPU and 8 GiB of requests, 64 pods and 25
Kubernetes services. One service asking for a large profile leaves less for its
siblings, and a merged folder can hit a quota that the same services in separate
folders would not. See [architecture](architecture.md) for the full list and
[TOML schema v1](toml.md) for per-service resource profiles.

## Validate

```sh
hakopod validate --dir devops
```

Validation is local and does not need a login. It checks every application,
including every merge conflict described above, the network file, and that every
application joining a segment of that network is granted that segment in
`network.toml`. All problems are reported at once.

## Review

```sh
hakopod plan --dir devops --project demo --environment production
```

This reviews the network and every application and deploys nothing. As with a
single-file `plan`, a single-file application's `env_file` references are
imported into its secrets during review. An application that relies on a grant
the network file adds may show a plan error until the network has been applied.
`--no-network` skips the network review as well, for keys that cannot manage
networks.

## Deploy

```sh
hakopod deploy --dir devops --project demo --environment production --wait
```

Deploy runs in this order:

1. Validates everything locally, as `validate --dir` does.
2. Applies the network: creates it, updates it with the reviewed revision, or
   skips it when unchanged. A network may grant applications that do not exist
   yet.
3. Plans every application before deploying any.
4. Deploys applications one at a time in folder-name order, and stops at the
   first failure.
5. Prints a summary with one line per application.

An application whose plan has no changes and whose current state is healthy is
skipped and reported as `unchanged`, so a rerun does not restart it. One whose
last release failed is submitted again even without changes.

To deploy applications without touching the network, add `--no-network`
(only valid with `--dir`). `network.toml` is still validated locally, including
the grant checks, but the network is not planned or applied on the server. The
summary shows it as skipped.

Missing secrets are handled per application, exactly as in a single-file
deploy. Each application gets its own retry key.

## Selecting applications

```sh
hakopod deploy --dir devops --project demo --environment production --only web,api
```

`--only` accepts folder names or application names, separated by commas. It
selects folders, and a folder is an application, so a merged folder is selected
or skipped as a whole.

## What it does not do

- It is not atomic across applications. If a later application fails, earlier
  ones stay deployed. Fix the problem and run the command again; healthy
  applications with no changes are skipped.
- It never deletes applications or network grants that are missing from the
  folder. To remove a grant, first deploy the application without that network
  connection, then remove the grant. The server rejects removing a grant that
  is still in use.
- `--idempotency-key` and `--service` are single-file flags and are rejected
  with `--dir`.

Exit codes are unchanged: 0 success, 2 input, 4 revision conflict and 5 failed
or cancelled release, among others listed in `hakopod --help`.

## CI usage

CI machine keys cannot manage virtual networks. Managing grants requires a
project administrator, and the dashboard does not forward the virtual network
routes for machine keys. Split the work:

1. An administrator applies the network with their own login, the first time
   and after each change to `network.toml`:

   ```sh
   hakopod deploy --dir devops --project demo --environment production --wait
   ```

2. CI sets `HAKOPOD_API_URL` and `HAKOPOD_API_KEY` in its secret store and
   deploys the applications only:

   ```sh
   hakopod deploy --dir devops --project demo --environment production --no-network --wait
   ```

Without `--no-network`, a key that cannot manage networks gets an error saying
so, and nothing is deployed. See [CI access](ci-api.md) for machine key setup.
