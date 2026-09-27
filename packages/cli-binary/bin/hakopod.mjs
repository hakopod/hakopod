#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);

const PLATFORM_PACKAGES = {
  "darwin-arm64": "@hakopod/cli-darwin-arm64",
  "darwin-x64": "@hakopod/cli-darwin-x64",
  "linux-x64": "@hakopod/cli-linux-x64",
  "linux-arm64": "@hakopod/cli-linux-arm64",
  "win32-x64": "@hakopod/cli-win32-x64",
  "win32-arm64": "@hakopod/cli-win32-arm64",
};

const key = `${process.platform}-${process.arch}`;
const pkgName = PLATFORM_PACKAGES[key];
const binName = process.platform === "win32" ? "hakopod.exe" : "hakopod";

let binPath;
try {
  if (!pkgName) throw new Error("unsupported");
  binPath = require.resolve(`${pkgName}/${binName}`);
} catch {
  process.stderr.write(
    `hakopod: no binary available for platform ${key}; install via https://hakopod.com/scripts/cli.sh instead\n`,
  );
  process.exit(1);
}

const result = spawnSync(binPath, process.argv.slice(2), { stdio: "inherit" });
process.exit(result.status ?? 1);
