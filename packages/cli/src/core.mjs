import { mkdir, open, readFile, rename, stat, unlink } from "node:fs/promises";
import { constants } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { createHash, randomUUID } from "node:crypto";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
export const execute = promisify(execFile);
export const cloud = "https://cloud.hakopod.com";
export const clean = (value) =>
  String(value).replace(/[\x00-\x1f\x7f-\x9f]/g, "");
export function origin(value) {
  const url = new URL(value);
  if (
    url.username ||
    url.password ||
    url.search ||
    url.hash ||
    !["", "/"].includes(url.pathname) ||
    !(
      url.protocol === "https:" ||
      (url.protocol === "http:" &&
        ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname))
    )
  )
    throw new Error(
      "Use an exact HTTPS dashboard origin, or HTTP on localhost.",
    );
  return url.origin;
}
export function remote(value) {
  const match =
    /^(?:git@(github\.com|gitlab\.com):|https:\/\/(github\.com|gitlab\.com)\/|ssh:\/\/git@(github\.com|gitlab\.com)\/)([A-Za-z0-9_.-]+(?:\/[A-Za-z0-9_.-]+)+?)(?:\.git)?$/.exec(
      value,
    );
  if (!match)
    throw new Error(
      "Use a GitHub.com or GitLab.com origin without credentials in its URL.",
    );
  const host = match[1] || match[2] || match[3];
  const repository = match[4].replace(/\.git$/, "");
  if (
    repository.split("/").some((part) => part === "." || part === "..") ||
    (host === "github.com" && repository.split("/").length !== 2)
  )
    throw new Error("Invalid Git repository path.");
  return { provider: host === "github.com" ? "github" : "gitlab", repository };
}
export async function git(args, cwd = process.cwd()) {
  try {
    return (
      await execute("git", args, {
        cwd,
        timeout: 30000,
        maxBuffer: 1024 * 1024,
        env: { ...process.env, GIT_TERMINAL_PROMPT: "0" },
      })
    ).stdout.trim();
  } catch {
    throw new Error(
      "Git could not complete the check. Verify repository access and your local Git credentials.",
    );
  }
}
export async function repository(cwd = process.cwd(), requirePushed = true) {
  const root = await git(["rev-parse", "--show-toplevel"], cwd);
  const source = remote(await git(["remote", "get-url", "origin"], root));
  const branch = await git(["symbolic-ref", "--short", "HEAD"], root);
  const commit = await git(["rev-parse", "HEAD"], root);
  if (requirePushed) {
    if (await git(["status", "--porcelain"], root))
      throw new Error(
        "Commit or stash local changes first. This command deploys only committed, pushed Git files.",
      );
    const published = await git(
      ["ls-remote", "--exit-code", "origin", `refs/heads/${branch}`],
      root,
    );
    if (published.split(/\s/)[0] !== commit)
      throw new Error(
        "Push this branch before deploying. Its remote HEAD must match your local commit.",
      );
  }
  return { root, branch, commit, ...source };
}
export const configRoot = () =>
  process.env.HAKOPOD_CLI_HOME || join(homedir(), ".config", "hakopod", "npm");
export const key = (value) => createHash("sha256").update(value).digest("hex");
export async function readPrivate(file) {
  let handle;
  try {
    handle = await open(file, constants.O_RDONLY | constants.O_NOFOLLOW);
    const info = await handle.stat();
    if (
      (process.getuid && info.uid !== process.getuid()) ||
      !info.isFile() ||
      info.size > 1024 * 1024 ||
      (process.platform !== "win32" && info.mode & 0o077)
    )
      throw new Error("CLI state must be a private file (chmod 600).");
    return JSON.parse(await handle.readFile("utf8"));
  } catch (error) {
    if (error.code === "ENOENT") return null;
    throw error;
  } finally {
    await handle?.close();
  }
}
export async function writePrivate(file, data) {
  await mkdir(dirname(file), { recursive: true, mode: 0o700 });
  const temporary = `${file}.${randomUUID()}`;
  const handle = await open(temporary, "wx", 0o600);
  try {
    await handle.writeFile(JSON.stringify(data, null, 2) + "\n");
    await handle.sync();
  } finally {
    await handle.close();
  }
  try {
    await rename(temporary, file);
  } finally {
    await unlink(temporary).catch(() => {});
  }
}
export const profilePath = (target) =>
  join(configRoot(), `profile-${key(origin(target))}.json`);
export const statePath = (target, scope, root) =>
  join(
    configRoot(),
    `deploy-${key(JSON.stringify([origin(target), scope, root]))}.json`,
  );
export class APIError extends Error {
  constructor(status, body) {
    super(
      clean(
        body?.error?.message ||
          body?.error ||
          `API request failed (${status}).`,
      ),
    );
    this.status = status;
    this.code = body?.error?.code || body?.error;
    this.details = body?.error;
  }
}
export class API {
  constructor(target, profile, transport = fetch) {
    this.origin = origin(target);
    this.profile = profile;
    this.transport = transport;
  }
  async call(
    path,
    body,
    { method = body === undefined ? "GET" : "POST", idempotency } = {},
  ) {
    if (
      !/^\/[A-Za-z0-9/?=&%_.:-]+$/.test(path) ||
      path.startsWith("//") ||
      path.split("/").includes("..")
    )
      throw new Error("Invalid API path.");
    if (this.profile && this.profile.origin !== this.origin)
      throw new Error(
        "Credentials belong to a different installation. Sign in again.",
      );
    const headers = { Accept: "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (this.profile) {
      headers.Authorization = `Bearer ${this.profile.token}`;
      if (this.profile.scope_id)
        headers["X-Hakopod-Workspace"] = this.profile.scope_id;
    }
    if (idempotency) headers["Idempotency-Key"] = idempotency;
    let response;
    try {
      response = await this.transport(`${this.origin}/api/v1${path}`, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        redirect: "error",
        signal: AbortSignal.timeout(35000),
      });
    } catch {
      throw new Error(
        "Cannot reach this installation. Any accepted work continues; run the command again to resume.",
      );
    }
    const reader = response.body?.getReader();
    const chunks = [];
    let length = 0;
    if (reader)
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        length += value.length;
        if (length > 2 * 1024 * 1024) {
          await reader.cancel();
          throw new Error("API response exceeds the CLI limit.");
        }
        chunks.push(Buffer.from(value));
      }
    let result;
    try {
      result = JSON.parse(Buffer.concat(chunks).toString());
    } catch {
      throw new Error(
        "This installation did not return JSON. Check the dashboard URL and server version.",
      );
    }
    if (!response.ok) throw new APIError(response.status, result);
    return result;
  }
}
export async function openBrowser(url) {
  const command =
    process.platform === "darwin"
      ? "open"
      : process.platform === "win32"
        ? "rundll32"
        : "xdg-open";
  const args =
    process.platform === "win32" ? ["url.dll,FileProtocolHandler", url] : [url];
  await execute(command, args, { timeout: 5000 }).catch(() => {});
}
export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
export async function deviceLogin(
  api,
  { show, launch = openBrowser, wait = sleep, now = Date.now } = {},
) {
  const challenge = await api.call("/auth/device/start", {});
  const link = new URL(challenge.verification_uri_complete);
  if (
    link.origin !== api.origin ||
    link.pathname !== "/login/device" ||
    link.username ||
    link.password ||
    link.hash ||
    link.searchParams.get("user_code") !== challenge.user_code ||
    !/^[A-Z2-7]{4}-[A-Z2-7]{4}$/.test(challenge.user_code) ||
    !/^[a-f0-9]{64}$/.test(challenge.device_code)
  )
    throw new Error(
      "The server returned an invalid browser authorization link.",
    );
  if (
    !Number.isInteger(challenge.expires_in) ||
    challenge.expires_in < 1 ||
    challenge.expires_in > 900 ||
    !Number.isInteger(challenge.interval) ||
    challenge.interval < 5 ||
    challenge.interval > 30
  )
    throw new Error("The server returned invalid authorization timing.");
  show(
    `Sign in or create an account, choose a destination, and approve code ${challenge.user_code}.\n${link.href}`,
  );
  await launch(link.href);
  const deadline = now() + challenge.expires_in * 1000;
  let interval = challenge.interval;
  while (now() < deadline) {
    await wait(interval * 1000);
    if (now() >= deadline) break;
    try {
      const result = await api.call("/auth/device/token", {
        device_code: challenge.device_code,
      });
      if (
        !/^hs_[a-f0-9]{32}_[a-f0-9]{64}$/.test(result.access_token) ||
        result.user?.credential_type !== "cli" ||
        !result.user.project ||
        !result.user.environment ||
        (result.scope_id && !/^[a-f0-9]{32}$/.test(result.scope_id)) ||
        !(Date.parse(result.expires_at) > now())
      )
        throw new Error("The server returned an invalid scoped CLI session.");
      return {
        origin: api.origin,
        token: result.access_token,
        expires_at: result.expires_at,
        scope_id: result.scope_id || "",
        project: result.user.project,
        environment: result.user.environment,
      };
    } catch (error) {
      if (error.code === "authorization_pending") continue;
      if (error.code === "slow_down") {
        interval = Math.min(30, interval + 5);
        continue;
      }
      throw error;
    }
  }
  throw new Error(
    "Sign-in expired. Run hakopod login again to get a new code.",
  );
}
