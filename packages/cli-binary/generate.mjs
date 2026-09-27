#!/usr/bin/env node
// Generates the seven publishable npm package directories (one wrapper +
// six platform packages) for a given hakopod engine version, from a
// directory of extracted release archives.
//
// Usage:
//   node generate.mjs <version> <extracted-archives-dir> <out-dir>
//
// <extracted-archives-dir> must contain one subdirectory per platform,
// named "<system>_<arch>" (e.g. "linux_amd64", "windows_arm64"), each
// holding the extracted contents of
// hakopod-cli_<version>_<system>_<arch>.tar.gz: the "hakopod" binary
// ("hakopod.exe" on windows), LICENSE, and NOTICE.

import { mkdirSync, readdirSync, copyFileSync, writeFileSync, readFileSync, chmodSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));

// system/arch (release asset naming) -> npm platform package suffix (process.platform/process.arch naming)
const PLATFORMS = [
  { system: "darwin", arch: "amd64", npmOs: "darwin", npmCpu: "x64", pkgSuffix: "darwin-x64" },
  { system: "darwin", arch: "arm64", npmOs: "darwin", npmCpu: "arm64", pkgSuffix: "darwin-arm64" },
  { system: "linux", arch: "amd64", npmOs: "linux", npmCpu: "x64", pkgSuffix: "linux-x64" },
  { system: "linux", arch: "arm64", npmOs: "linux", npmCpu: "arm64", pkgSuffix: "linux-arm64" },
  { system: "windows", arch: "amd64", npmOs: "win32", npmCpu: "x64", pkgSuffix: "win32-x64" },
  { system: "windows", arch: "arm64", npmOs: "win32", npmCpu: "arm64", pkgSuffix: "win32-arm64" },
];

function fail(msg) {
  process.stderr.write(`generate: ${msg}\n`);
  process.exit(1);
}

const [, , version, archivesDir, outDir] = process.argv;
if (!version || !archivesDir || !outDir) {
  fail("usage: node generate.mjs <version> <extracted-archives-dir> <out-dir>");
}

mkdirSync(outDir, { recursive: true });

for (const p of PLATFORMS) {
  const srcDir = join(archivesDir, `${p.system}_${p.arch}`);
  let entries;
  try {
    entries = readdirSync(srcDir);
  } catch {
    fail(`missing extracted archive directory: ${srcDir}`);
  }
  const binName = p.system === "windows" ? "hakopod.exe" : "hakopod";
  if (!entries.includes(binName)) fail(`missing ${binName} in ${srcDir}`);
  if (!entries.includes("LICENSE")) fail(`missing LICENSE in ${srcDir}`);
  if (!entries.includes("NOTICE")) fail(`missing NOTICE in ${srcDir}`);

  const pkgName = `@hakopod/cli-${p.pkgSuffix}`;
  const pkgDir = join(outDir, `cli-${p.pkgSuffix}`);
  mkdirSync(pkgDir, { recursive: true });

  copyFileSync(join(srcDir, binName), join(pkgDir, binName));
  chmodSync(join(pkgDir, binName), 0o755);
  copyFileSync(join(srcDir, "LICENSE"), join(pkgDir, "LICENSE"));
  copyFileSync(join(srcDir, "NOTICE"), join(pkgDir, "NOTICE"));

  const pkgJson = {
    name: pkgName,
    version,
    description: `hakopod CLI binary for ${p.npmOs}/${p.npmCpu}`,
    license: "Apache-2.0",
    os: [p.npmOs],
    cpu: [p.npmCpu],
    files: [binName, "LICENSE", "NOTICE"],
    repository: {
      type: "git",
      url: "git+https://github.com/hakopod/hakopod.git",
      directory: "packages/cli-binary",
    },
    publishConfig: { access: "public" },
  };
  writeFileSync(join(pkgDir, "package.json"), JSON.stringify(pkgJson, null, 2) + "\n");
}

// Wrapper package
const wrapperDir = join(outDir, "cli");
mkdirSync(join(wrapperDir, "bin"), { recursive: true });
copyFileSync(join(HERE, "bin", "hakopod.mjs"), join(wrapperDir, "bin", "hakopod.mjs"));
chmodSync(join(wrapperDir, "bin", "hakopod.mjs"), 0o755);
copyFileSync(join(HERE, "..", "..", "LICENSE"), join(wrapperDir, "LICENSE"));
copyFileSync(join(HERE, "..", "..", "NOTICE"), join(wrapperDir, "NOTICE"));
copyFileSync(join(HERE, "README.md"), join(wrapperDir, "README.md"));

const wrapperTemplate = readFileSync(join(HERE, "wrapper-package.json"), "utf8");
writeFileSync(join(wrapperDir, "package.json"), wrapperTemplate.replaceAll("__VERSION__", version));

process.stdout.write(`generated 7 packages at ${outDir} for version ${version}\n`);
