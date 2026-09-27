Hakopod 0.1.0-alpha.34 deploys a whole folder of applications with one command, and lets an application be written as one file per service.

## Deploying a folder

`hakopod validate`, `plan` and `deploy` accept `--dir ROOT`. Each immediate subfolder of `ROOT` is one application and an optional `ROOT/network.toml` is one virtual network, so a repository of applications that share a network deploys with a single command. Validation is local and reports every problem at once. Deploy applies the network first, plans every application before writing any of them, then deploys them in folder-name order and stops at the first failure; an application that is healthy with no changes is skipped rather than restarted. `--only` selects folders by folder or application name, and `--no-network` lets a CI key that cannot manage networks deploy the applications alone.

## One file per service

A folder holding a file named `hakopod.toml` keeps behaving exactly as before: that document is the application, its `name` key names it, and every other `.toml` file beside it is ignored. A folder with no `hakopod.toml` is merged instead: every `.toml` file in it becomes part of one application named after the folder. A file that has a `services` key contributes its `[services.*]` tables as written; a file without one is a single service named after the file, so `web.toml` becomes `services.web`.

Merged mode is opt-in by the absence of `hakopod.toml`, never by the number of files, so adding a file beside an existing configuration cannot change how a folder deploys. A `hakopod.old.toml` backup or a reference copy of the shared `network.toml` is ignored rather than merged into the live application.

Conflicts are errors naming both files rather than silent winners: at most one file may set `name`, `recovery`, `inject_env`, `domains` or `volumes`; application-level `networks`, `env` and `secrets` merge key by key; a service defined twice is refused; and `schema_version` may repeat but not disagree. Every merged file is checked for unknown keys, so a misspelled `imagee` is reported with its line instead of being dropped. `env_file` is not supported in a merged folder and says so, because the server expands it only on the TOML path.

Services inside one application already reach each other at their bare short name on a declared port with no configuration at all, so a folder of cooperating services needs no virtual network. A virtual network is only required to cross an application boundary. The trade-off is that a merged application is one release: a failed rollout of any service reverts every service in that application, and the 20-service limit and the shared namespace quota apply to the folder as a whole. Keep services in separate folders when they have independent release cadences.

## Upgrade

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.34
```

Direct upgrades are supported from alpha.32 and alpha.33, the last two published versions. Older installations must upgrade through a supported intermediate release. This release adds no database migrations and changes no API or schema: everything in it is in the command-line tool, so an installation that does not use `--dir` behaves identically to alpha.33.

## Validation

Engine, API and dashboard suites pass. The folder-merge work was reviewed by two independent passes, one against the specification and one for blast radius, and the defects they found are fixed. It was then exercised against a development cluster on a tree of two applications, one merged from three files into four services and one from a single file: plan, deploy, a rerun that reported both unchanged without rolling any pod, and a one-file change that rolled only the service it touched. Sibling addressing by bare short name was confirmed from inside a running pod. The existing single-file `deploy --file` path is unchanged, and a real seven-application tree of single-file folders was confirmed to take the previous path unaltered.
