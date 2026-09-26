# Deploying a folder of applications

`hakopod validate`, `plan` and `deploy` accept `--dir ROOT` to work on a folder
of applications and one virtual network together. Use it when several
applications share a network and are kept in one repository.

## Layout

```text
devops/
├── network.toml          # optional: one virtual network, e.g. name = "shop"
├── api/hakopod.toml
├── db-backup/HAKOPOD.toml
├── web/hakopod.toml
└── worker/hakopod.toml
```

- The root may hold one `network.toml`. It uses the same format as
  **Networks → Create network → import**; see [virtual networks](virtual-networks.md#create-a-network).
- Each immediate subfolder that contains `hakopod.toml` is one application. The
  file name is matched case-insensitively, so `HAKOPOD.toml` works. A folder
  holding two case variants, such as `hakopod.toml` and `HAKOPOD.toml`, is an
  error.
- Deeper folders and hidden folders are ignored.

Separately, without `--file` the CLI now finds `hakopod.toml` in the current
folder case-insensitively. `HAKOPOD.toml` used to be missed on case-sensitive
filesystems such as Linux.

## Validate

```sh
hakopod validate --dir devops
```

Validation is local and does not need a login. It checks every application,
the network file, and that every application joining a segment of that network
is granted that segment in `network.toml`. All problems are reported at once.

## Review

```sh
hakopod plan --dir devops --project demo --environment production
```

This reviews the network and every application and deploys nothing. As with a
single-file `plan`, an application's `env_file` references are imported into
its secrets during review. An
application that relies on a grant the network file adds may show a plan error
until the network has been applied. `--no-network` skips the network review as
well, for keys that cannot manage networks.

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

`--only` accepts folder names or application names, separated by commas.

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
