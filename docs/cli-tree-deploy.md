# Deploying a folder of applications

`hakopod validate`, `plan` and `deploy` accept `--dir ROOT` to work on a folder
of applications and one virtual network together. Use it when several
applications share a network and are kept in one repository.

Each immediate subfolder is one application, and one file decides how that folder
is read. A folder holding a file named `hakopod.toml` is that document's
application: it takes the same path it always took, its name comes from the
`name` key inside the document, and every other `.toml` file in the folder is
ignored. A folder holding at least one `.toml` file and no `hakopod.toml` is
merged: every `.toml` file inside it becomes part of one application named after
the folder, so one service per file is a valid way to lay out an application.

Merged mode is opt-in by the absence of `hakopod.toml`, not by the number of
files. Dropping a second file beside an existing `hakopod.toml` does not switch
that folder to merged mode, so a `hakopod.old.toml` backup, a
`hakopod-staging.toml` variant or a copy of the shared `network.toml` kept for
reference is ignored rather than silently merged into the live application.

## Layout

```text
devops/
├── network.toml          # optional: one virtual network, e.g. name = "shop"
├── shop/                 # merged: one application named "shop"
│   ├── web.toml          # becomes services.web
│   ├── api.toml          # becomes services.api
│   └── data.toml         # may declare [services.*] tables itself
└── billing/
    ├── hakopod.toml      # the existing path, unchanged; it names the application
    └── hakopod.old.toml  # ignored, because hakopod.toml is present
```

- The root may hold one `network.toml`. It uses the same format as
  **Networks → Create network → import**; see [virtual networks](virtual-networks.md#create-a-network).
- Each immediate subfolder is one application. A folder with a `hakopod.toml`
  keeps its previous behaviour and is named by the `name` key in that document;
  a folder without one is merged and is named after the folder itself.
- The file name `hakopod.toml` is matched case-insensitively, so `HAKOPOD.toml`
  also claims its folder. A folder holding two case variants, such as
  `hakopod.toml` and `HAKOPOD.toml`, is an error.
- Deeper folders and hidden folders are ignored.

Separately, without `--file` the CLI finds `hakopod.toml` in the current folder
case-insensitively. `HAKOPOD.toml` used to be missed on case-sensitive
filesystems such as Linux.

## How files in one folder are merged

This section describes a folder with no `hakopod.toml`. A folder that has one is
read from that document alone, and nothing below applies to it.

Files are read in filename order. Dotfiles and files that do not end in `.toml`
are ignored. `network.toml` is only special at the root of the tree; inside an
application folder it is merged like any other file.

A file that has a `services` key is an application document, and its
`[services.*]` tables are used as written. A file with no `services` key is one
service, named after the file's basename: `web.toml` becomes `services.web`.
Both kinds can sit in the same folder.

The application's name is the folder name. A `name` key may repeat it, but a
`name` that disagrees with the folder is an error.

Because the folder name becomes the application name, a merged folder must be
named legally for an application: 1 to 40 characters, lowercase letters, digits
and dashes, starting with a letter and not ending with a dash. A folder called
`my_app`, `Web` or `api.v2` fails validation:

```text
folder name "my_app" becomes the application name when several files are merged: rename the folder to 1-40 lowercase letters, digits or dashes, or keep one hakopod.toml that names the application itself
```

The constraint applies only to merged folders. A folder with a `hakopod.toml`
may be named anything, because its application name comes from the document.

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

`env_file` is not supported in a merged folder, and a merged folder that uses it
fails validation naming the file, instead of quietly dropping it. `env_file`
expansion happens on the server on the TOML path only, and a merged folder is
submitted as a JSON specification. A folder with a `hakopod.toml` still takes the
TOML path and keeps `env_file` working as it always did. See [environment
files and shared variables](environment-and-build-reuse.md) for the alternatives.

## Folders that hold an unrelated TOML file

A subfolder holding a `.toml` file that is nothing to do with Hakopod, and no
`hakopod.toml` to claim it, is still treated as an application, and it fails
validation naming the file and the unknown key:

```text
mylib: Cargo.toml: unknown TOML fields: package (line 1, column 2)
```

A Rust crate's `Cargo.toml`, a `pyproject.toml` or a `netlify.toml` is therefore
reported rather than silently deployed. The same strictness means a misnamed
`hakopodd.toml` fails loudly instead of vanishing.

`--only` is the way through a repository that mixes applications with other
projects. When `--only` is set, a folder whose name is not listed and that fails
to load is skipped silently instead of failing the run. With no `--only`, every
folder's failure is an error.

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
`plan` on one file, the `env_file` references of a folder with a `hakopod.toml`
are imported into its secrets during review; a merged folder cannot use them. An application that relies on a grant
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
or skipped as a whole. An unlisted folder that fails to load is skipped silently;
see [folders that hold an unrelated TOML
file](#folders-that-hold-an-unrelated-toml-file).

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
