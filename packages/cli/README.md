# Hakopod CLI

Deploy a committed Git repository to Hakopod Cloud or your own Hakopod installation.
Requires Node.js 22.12+ and Git. This package uses the shared engine API; it does not
download or execute another binary during npm installation.

```sh
npx @hakopod/cli deploy
# Or point it at your own dashboard:
npx @hakopod/cli deploy --url https://hakopod.example.com
```

The first run opens browser sign-in. Create an account if needed, compare the
terminal code, and choose one project/environment or ready Cloud workspace.
If no destination is ready, open dashboard setup from the consent page, finish
setup, return, and refresh destinations. Codes expire after ten minutes; repeat
login if setup takes longer. Cloud admission, email verification, membership and
MFA requirements still apply. A project administrator or Cloud workspace owner
must connect Git and authorize a new application source.

The deploy command checks that your working tree is clean and the current branch
is pushed to `origin`, detects the framework remotely, and lets you review or edit
its commands, service port and public access. GitHub.com and GitLab.com are supported.
It shows the exact generated workflow and requirements before writing it, then
writes to the repository default branch. When deploying that branch, it also
fast-forwards your local checkout to the reviewed workflow commit. Concurrent remote
or local edits stop this step for review. Builds run in your Git provider's CI.

You review the deployment plan before its digest-pinned image is deployed. The CLI
waits for the deployment's terminal status. Failed builds, failed deployments and
Cloud Team approvals do not report success. Approval must be completed in Cloud.
Run the same command after interruption to resume an accepted build/deployment.

```sh
npx @hakopod/cli login --url https://cloud.hakopod.com
npx @hakopod/cli deploy --name my-app --context apps/web
npx @hakopod/cli deploy --build BUILD_ID  # link an existing build for this exact source/scope
npx @hakopod/cli status
npx @hakopod/cli logout
```

`--no-browser` prints the sign-in URL without opening it. `--yes` accepts the
printed configuration, workflow and deployment reviews; it does not bypass browser
consent or server policy. Noninteractive defaults use Cloud, a small private web
service and the detected commands. Advanced runtime variables, secret bindings,
multiple services, architecture and resource settings remain editable in the
shared dashboard; linked builds preserve those settings.

Credentials and resumable deployment state live outside the source repository in
`~/.config/hakopod/npm`, separately for each exact installation origin. Set
`HAKOPOD_CLI_HOME` to use another protected directory. Files are written atomically
with owner-only permissions; Windows uses the account profile's directory ACLs.
Sessions expire after twelve hours and can be revoked in account security. Logout
attempts server revocation and always removes local credentials. No passwords or
provider tokens are requested by the terminal. Never commit CLI state to Git.

Use an engine/Cloud server release containing npm CLI device-scope support. Older
servers may expose only the image-based Go CLI API. That CLI remains available for
TOML, machine API keys, logs and infrastructure administration.

For local development: `npm test`, `npm pack`, then `npx --package ./hakopod-cli-*.tgz hakopod --help`.
The package archive contains only public JavaScript, this documentation and the
Apache-2.0 license. CI tests it and inspects its package contents.
