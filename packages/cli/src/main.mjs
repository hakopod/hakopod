#!/usr/bin/env node
import { Writable } from "node:stream";
import { parseArgs } from "node:util";
import { createInterface } from "node:readline/promises";
import { stdin, stdout } from "node:process";
import { basename } from "node:path";
import { unlink } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import {
  API,
  APIError,
  cloud,
  clean,
  origin,
  repository,
  git,
  profilePath,
  statePath,
  readPrivate,
  writePrivate,
  deviceLogin,
  openBrowser,
  sleep,
} from "./core.mjs";

const help = `Hakopod — deploy committed Git repositories

  npx @hakopod/cli deploy
  npx @hakopod/cli login --url https://hakopod.example.com
  npx @hakopod/cli status --url https://hakopod.example.com
  npx @hakopod/cli logout --url https://hakopod.example.com

Options: --url <dashboard origin>  --name <application>  --context <directory>
         --build <existing build ID>  --yes (accept displayed reviews)  --no-browser  --help

Requires Node 22.12+, Git, and a pushed GitHub.com or GitLab.com branch.
Cloud is the default for noninteractive use. Self-hosted uses the same APIs.
Credentials are saved per installation with owner-only file permissions.
`;
let rl;
const say = (value) => stdout.write(`${clean(value)}\n`);
const show = (value) =>
  stdout.write(
    String(value).replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]/g, "") + "\n",
  );
async function ask(label, fallback = "") {
  if (!stdin.isTTY) {
    if (fallback) return fallback;
    throw new Error(`${label}: run in an interactive terminal.`);
  }
  rl ||= createInterface({ input: stdin, output: stdout });
  return (
    (
      await rl.question(`${label}${fallback ? ` [${clean(fallback)}]` : ""}: `)
    ).trim() || fallback
  );
}
async function askSecret(label) {
  if (!stdin.isTTY) throw new Error("Secret input requires an interactive terminal.");
  rl?.close(); rl = undefined;
  let muted = false;
  const output = new Writable({ write(chunk, encoding, done) { if (!muted) stdout.write(chunk, encoding); done(); } });
  const secretInput = createInterface({ input: stdin, output, terminal: true });
  try {
    const result = secretInput.question(`${label} (hidden): `);
    muted = true;
    return await result;
  } finally { secretInput.close(); stdout.write("\n"); }
}
async function setupSecrets(api, config, plan) {
  const missing = plan.missing_secrets || [];
  if (!missing.length) return;
  if (!stdin.isTTY) throw new Error(`Missing application secrets: ${missing.map(clean).join(", ")}. Save them in the dashboard or through POST /api/v1/secrets/{name}, then retry. No deployment was submitted.`);
  for (const name of missing) {
    say(`Missing secret: ${name}`);
    const mode = await choose("Secret value", [
      { label: "Enter an existing value (hidden)", value: "value" },
      { label: "Generate a new random password (not a provider credential)", value: "generate" },
      { label: "Cancel deployment", value: "cancel" },
    ]);
    if (mode === "cancel") throw new Error("Secret setup cancelled. No deployment was submitted.");
    const body = mode === "generate" ? { generate: true } : { value: await askSecret(clean(name)) };
    const query = new URLSearchParams({ project: config.project, environment: config.environment, application: plan.spec.name });
    try { await api.call(`/secrets/${encodeURIComponent(name)}?${query}`, body); }
    finally { delete body.value; }
  }
}
async function choose(label, items) {
  if (!items.length) throw new Error(`No ${label.toLowerCase()} available.`);
  items.forEach((item, index) => say(`${index + 1}. ${item.label}`));
  for (;;) {
    const value = Number(await ask(label, items.length === 1 ? "1" : ""));
    if (Number.isInteger(value) && items[value - 1])
      return items[value - 1].value;
    say("Choose one of the numbers above.");
  }
}
async function confirm(label, yes) {
  if (yes) {
    say(`${label}: accepted with --yes`);
    return;
  }
  if (!/^y(es)?$/i.test(await ask(`${label} (y/N)`, "n")))
    throw new Error(
      "Stopped. Saved configuration remains available for your next attempt.",
    );
}
async function waitFor(read, finished, label, timeout = 30 * 60 * 1000) {
  let previous = "";
  const until = Date.now() + timeout;
  while (Date.now() < until) {
    const item = await read();
    if (item.status !== previous) {
      say(`${label}: ${item.status}`);
      previous = item.status;
    }
    if (finished(item)) return item;
    await sleep(5000);
  }
  throw new Error(
    `${label} is still running. Use the same deploy command to resume, or status to inspect it.`,
  );
}
async function main() {
  const { values: flags, positionals } = parseArgs({
    allowPositionals: true,
    options: {
      url: { type: "string" },
      name: { type: "string" },
      context: { type: "string" },
      build: { type: "string" },
      yes: { type: "boolean" },
      "no-browser": { type: "boolean" },
      help: { type: "boolean" },
    },
  });
  if (flags.help) {
    show(help);
    return;
  }
  const command = positionals[0] || "deploy";
  if (
    positionals.length > 1 ||
    !["deploy", "login", "logout", "status"].includes(command)
  )
    throw new Error(help);
  let target = flags.url;
  if (!target && stdin.isTTY) {
    const kind = await choose("Deployment target", [
      { label: "Hakopod Cloud", value: "cloud" },
      { label: "Self-hosted Hakopod", value: "self" },
    ]);
    target = kind === "cloud" ? cloud : await ask("Self-hosted dashboard URL");
  }
  target = origin(target || cloud);
  const file = profilePath(target);
  let profile = await readPrivate(file);
  if (command === "logout") {
    if (profile) {
      try {
        await new API(target, profile).call("/auth/logout", {});
        say("CLI session revoked.");
      } catch {
        say(
          `Server revocation was unavailable. Revoke this session in ${target}/settings/security.`,
        );
      }
      await unlink(file);
      say("Removed local credentials.");
    } else say("No saved login for this installation.");
    return;
  }
  let api = new API(target, profile);
  if (
    command === "login" ||
    !profile ||
    Date.parse(profile.expires_at) <= Date.now()
  )
    profile = null;
  if (profile) {
    try {
      const me = await api.call("/me");
      if (
        me.credential_type !== "cli" ||
        me.project !== profile.project ||
        me.environment !== profile.environment
      )
        throw new Error(
          "The saved CLI destination no longer matches. Run login again.",
        );
    } catch (error) {
      if (error.status === 401) profile = null;
      else throw error;
    }
  }
  if (!profile) {
    profile = await deviceLogin(new API(target), {
      show,
      launch: flags["no-browser"] ? async () => {} : openBrowser,
    });
    await writePrivate(file, profile);
    api = new API(target, profile);
  }
  say(`Destination: ${target} · ${profile.project} / ${profile.environment}`);
  if (command === "login") return;
  let source = await repository(process.cwd(), command !== "status");
  const local = statePath(
    target,
    [profile.scope_id, profile.project, profile.environment],
    source.root,
  );
  let saved = await readPrivate(local);
  if (flags.build) {
    if (!/^[a-f0-9]{32}$/.test(flags.build))
      throw new Error("Use the build ID shown in the dashboard.");
    if (saved?.build && saved.build !== flags.build)
      throw new Error(
        "This directory already has a linked build for this destination.",
      );
    const linked = await api.call(`/builds/${flags.build}`);
    if (
      linked.project !== profile.project ||
      linked.environment !== profile.environment ||
      linked.repository !== source.repository ||
      linked.provider !== source.provider ||
      linked.branch !== source.branch
    )
      throw new Error(
        "Build does not match this repository and authorized destination.",
      );
    saved ||= { build: linked.id };
    await writePrivate(local, saved);
  }
  if (command === "status") {
    if (!saved?.build)
      throw new Error(
        "No saved deployment in this repository for this destination.",
      );
    const build = await api.call(`/builds/${saved.build}`);
    say(`Application: ${build.name}`);
    if (saved.run) {
      const run = await api.call(`/builds/${saved.build}/runs/${saved.run}`);
      say(`Build: ${run.status} ${run.conclusion}`);
      if (run.deployment_id) {
        const deployment = await api.call(`/deployments/${run.deployment_id}`);
        say(`Deployment: ${deployment.status}`);
      }
    }
    return;
  }
  let config;
  if (saved?.build) {
    config = await api.call(`/builds/${saved.build}`);
    if (
      config.repository !== source.repository ||
      config.provider !== source.provider ||
      config.branch !== source.branch ||
      config.project !== profile.project ||
      config.environment !== profile.environment
    )
      throw new Error(
        "This directory is linked to another source or destination. Use a separate checkout or restore its original branch.",
      );
    if (
      (flags.name && flags.name !== config.name) ||
      (flags.context && flags.context !== config.context_path)
    )
      throw new Error(
        "This deployment is already linked. Edit its build configuration in the dashboard before continuing.",
      );
  } else {
    let connections = (await api.call("/git/connections")).items.filter(
      (item) =>
        item.provider === source.provider &&
        item.enabled &&
        item.capabilities.builds,
    );
    if (!connections.length) {
      const url = `${target}/settings/git/connections/new`;
      show(
        `Connect ${source.provider} in the dashboard for this destination, then return here.\n${url}`,
      );
      if (!flags["no-browser"]) await openBrowser(url);
      await ask("Press Enter after connecting Git", "continue");
      connections = (await api.call("/git/connections")).items.filter(
        (item) =>
          item.provider === source.provider &&
          item.enabled &&
          item.capabilities.builds,
      );
    }
    const connection_id = await choose(
      "Git connection",
      connections.map((item) => ({ label: item.name, value: item.id })),
    );
    const name =
      flags.name ||
      (await ask(
        "Application name",
        basename(source.root)
          .toLowerCase()
          .replace(/[^a-z0-9-]/g, "-")
          .slice(0, 40),
      ));
    const context_path =
      flags.context || (await ask("Directory inside repository", "."));
    let input = {
      project: profile.project,
      environment: profile.environment,
      name,
      provider: source.provider,
      repository: source.repository,
      branch: source.branch,
      connection_id,
      context_path,
      service: "web",
      public: false,
      auto_build: false,
      auto_deploy: false,
    };
    const detected = await api.call("/builds/detect", input);
    if (detected.commit_sha !== source.commit)
      throw new Error(
        "The remote branch changed during detection. Pull and review it, then retry.",
      );
    show(
      `Detected ${detected.framework?.framework || "Dockerfile"} at ${source.commit}\n${(detected.warnings || []).join("\n")}`,
    );
    input = {
      ...input,
      mode: detected.mode,
      framework: detected.framework,
      dockerfile: detected.dockerfile,
      port: detected.framework?.port || 8080,
      size: "small",
    };
    if (input.framework) {
      show(JSON.stringify(input.framework, null, 2));
      if (
        !flags.yes &&
        /^y(es)?$/i.test(await ask("Edit detected commands? (y/N)", "n"))
      ) {
        for (const field of [
          "install_command",
          "build_command",
          "start_command",
          "output_directory",
        ]) {
          if (input.framework[field] !== undefined)
            input.framework[field] = await ask(
              field.replaceAll("_", " "),
              input.framework[field],
            );
        }
      }
    }
    input.port = Number(await ask("Service port", String(input.port)));
    if (!Number.isInteger(input.port) || input.port < 1 || input.port > 65535)
      throw new Error("Choose a service port between 1 and 65535.");
    if (input.framework) input.framework.port = input.port;
    input.public = /^y(es)?$/i.test(
      await ask("Expose this web service publicly? (y/N)", "n"),
    );
    show(`Review build settings\n${JSON.stringify(input, null, 2)}`);
    await confirm("Save these build settings", flags.yes);
    // A create response can be lost. Discover the unique saved name before retrying.
    const existing = (
      await api.call(
        `/builds?project=${encodeURIComponent(profile.project)}&environment=${encodeURIComponent(profile.environment)}`,
      )
    ).items.filter((item) => item.name === name);
    if (existing.length)
      throw new Error(
        "A build with this name already exists. Rerun with --build plus its ID from the dashboard, or choose another application name.",
      );
    config = await api.call("/builds", input);
    saved = { build: config.id };
    await writePrivate(local, saved);
  }
  say(`Application: ${config.name} · ${source.repository} · ${source.branch}`);
  if (config.installed_revision !== config.revision) {
    const preview = await api.call(`/builds/${config.id}/preview`, {});
    show(
      `Workflow to write: ${preview.workflow_path}\n${preview.workflow}\nRequirements:\n${preview.requirements.join("\n")}`,
    );
    await confirm(
      "Commit this workflow to the repository default branch (fast-forward locally if selected)",
      flags.yes,
    );
    const installed = await api.call(`/builds/${config.id}/install`, {
      expected_config_revision: config.revision,
    });
    config = installed.config;
    if (!/^[a-f0-9]{40,64}$/.test(installed.commit_sha))
      throw new Error("The provider returned an invalid workflow commit.");
    if (!installed.workflow_branch)
      throw new Error(
        "The server must report the installed workflow branch. Upgrade this installation before continuing.",
      );
    if (installed.workflow_branch === source.branch) {
      if (await git(["status", "--porcelain"], source.root))
        throw new Error(
          "Local files changed during review. Commit or stash them, pull the workflow commit, and rerun deploy.",
        );
      await git(["fetch", "origin", source.branch], source.root);
      if (
        (await git(["rev-parse", "FETCH_HEAD"], source.root)) !==
        installed.commit_sha
      )
        throw new Error(
          "The remote branch changed after workflow installation. Pull and review it before retrying.",
        );
      await git(["merge", "--ff-only", installed.commit_sha], source.root);
      source = await repository(source.root);
    }
    say(
      `Workflow installed on ${installed.workflow_branch}. Source remains ${source.branch}.`,
    );
    say(`Workflow ready at ${source.commit}.`);
  }
  if (saved.commit !== source.commit || saved.revision !== config.revision) {
    saved = {
      build: config.id,
      commit: source.commit,
      revision: config.revision,
      idempotency: `cli-${randomUUID()}`,
    };
    await writePrivate(local, saved);
  }
  if (!saved.run) {
    await confirm(
      `Build committed source ${source.commit} using your Git provider`,
      flags.yes,
    );
    const run = await api.call(
      `/builds/${config.id}/run`,
      { expected_config_revision: config.revision, commit: source.commit },
      { idempotency: saved.idempotency },
    );
    saved.run = run.id;
    await writePrivate(local, saved);
  }
  const base = `/builds/${config.id}/runs/${saved.run}`;
  const run = await waitFor(
    () => api.call(base),
    (item) => ["completed", "failed", "cancelled"].includes(item.status),
    "Build",
  );
  if (run.status !== "completed" || run.conclusion !== "success" || !run.image)
    throw new Error(
      `Build did not succeed. Inspect ${target}/builds/${config.id}. Fix it and push a new commit to start another build.`,
    );
  let deploymentID = run.deployment_id;
  if (!deploymentID) {
    const plan = await api.call(`${base}/plan`, {});
    show(
      `Review deployment\n${JSON.stringify({ image: run.image, changes: plan.changes, warnings: plan.warnings }, null, 2)}`,
    );
    await setupSecrets(api, config, plan);
    await confirm("Deploy this built image", flags.yes);
    const deployment = await api.call(`${base}/deploy`, {
      expected_revision: plan.expected_revision,
      expected_config_revision: plan.expected_config_revision,
    });
    deploymentID = deployment.id;
  }
  const deployment = await waitFor(
    () => api.call(`/deployments/${deploymentID}`),
    (item) =>
      ["succeeded", "failed", "cancelled", "superseded"].includes(item.status),
    "Deployment",
  );
  if (deployment.status !== "succeeded")
    throw new Error(
      `Deployment ${deployment.status}. Inspect ${target}/applications/${deployment.application_id} for diagnostics.`,
    );
  say(
    `Deployment succeeded. ${target}/applications/${deployment.application_id}`,
  );
}
main()
  .catch((error) => {
    if (error instanceof APIError && error.code === "approval_required") {
      say(
        "Deployment awaits a Cloud Team review. Open the workspace approvals in the dashboard; rerun deploy after approval.",
      );
    } else show(error.message || "The command could not finish.");
    process.exitCode = 1;
  })
  .finally(() => rl?.close());
