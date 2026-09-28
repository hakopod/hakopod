import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
const dir = await mkdtemp(join(tmpdir(), "hakopod-sdk-contract-"));
try {
  const output = join(dir, "api.generated.ts");
  execFileSync(
    process.execPath,
    [
      "node_modules/openapi-typescript/bin/cli.js",
      "../../api/openapi.json",
      "-o",
      output,
    ],
    { stdio: "pipe" },
  );
  assert.equal(
    await readFile(output, "utf8"),
    await readFile("src/api.generated.ts", "utf8"),
    "Regenerate SDK types after changing the canonical API contract.",
  );
  console.log("SDK types match the canonical OpenAPI contract.");
} finally {
  await rm(dir, { recursive: true, force: true });
}
