#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const SOURCE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const REPOSITORY = 'https://github.com/hakopod/hakopod';
const FILES = [
  ['actions/deploy/action.yml', 'action.yml'],
  ['actions/deploy/deploy.mjs', 'deploy.mjs'],
  ['actions/deploy/index.mjs', 'index.mjs'],
  ['actions/deploy/test/deploy.test.mjs', 'test/deploy.test.mjs'],
  ['actions/deploy/test/index.test.mjs', 'test/index.test.mjs'],
  ['actions/deploy/testdata/runner.mjs', 'testdata/runner.mjs'],
  ['actions/deploy/README.md', 'README.md'],
  ['LICENSE', 'LICENSE'],
  ['NOTICE', 'NOTICE'],
];

const CI = `name: CI
on:
  push:
  pull_request:
  workflow_dispatch:
permissions:
  contents: read
concurrency:
  group: ci-\${{ github.workflow }}-\${{ github.ref }}
  cancel-in-progress: true
jobs:
  test:
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020 # v7.0.0
        with:
          node-version: "24"
      - run: npm test
      - name: Start the synthetic deployment API
        run: |
          node testdata/runner.mjs --port 18777 > "$RUNNER_TEMP/hakopod-action-fixture.log" 2>&1 &
          echo "$!" > "$RUNNER_TEMP/hakopod-action-fixture.pid"
          for attempt in {1..30}; do
            if curl --fail --silent --max-time 1 http://127.0.0.1:18777/healthz > /dev/null; then exit 0; fi
            sleep 0.2
          done
          cat "$RUNNER_TEMP/hakopod-action-fixture.log"
          exit 1
      - name: Exercise the action with synthetic API data
        id: smoke
        uses: ./
        with:
          api-url: http://127.0.0.1:18777
          api-token: synthetic-runner-token
          application-id: synthetic-runner-application
          services: '[{"name":"api","env":{"RELEASE":"synthetic-runner"}}]'
          env: '{"SHARED":"synthetic-runner"}'
          image: ghcr.io/example/api@sha256:${'1'.repeat(64)}
          timeout: '10'
      - name: Check action outputs
        env:
          DEPLOYMENT_STATUS: \${{ steps.smoke.outputs.status }}
          DEPLOYMENT_ID: \${{ steps.smoke.outputs.deployment-id }}
          APPLICATION_ID: \${{ steps.smoke.outputs.application-id }}
        run: |
          test "$DEPLOYMENT_STATUS" = succeeded
          test "$DEPLOYMENT_ID" = synthetic-runner-deployment
          test "$APPLICATION_ID" = synthetic-runner-application
      - name: Stop the synthetic API
        if: always()
        run: |
          if [ -f "$RUNNER_TEMP/hakopod-action-fixture.pid" ]; then
            kill "$(cat "$RUNNER_TEMP/hakopod-action-fixture.pid")" 2>/dev/null || true
          fi
`;

function git(sourceRoot, args) {
  try {
    return execFileSync('git', ['-C', sourceRoot, ...args], {
      maxBuffer: 4 * 1024 * 1024,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
  } catch {
    throw new Error('Cannot read the committed source. Fetch origin/main and run this script from its checked-out commit.');
  }
}

function committedFile(sourceRoot, commit, path, optional) {
  const entry = git(sourceRoot, ['ls-tree', '-z', commit, '--', path]).toString('utf8');
  if (!entry && optional) return undefined;
  if (!/^100(?:644|755) blob [a-f0-9]+\t/.test(entry) || entry.split('\0').filter(Boolean).length !== 1) {
    throw new Error(`The committed source must contain a regular file at ${path}.`);
  }
  return git(sourceRoot, ['show', `${commit}:${path}`]);
}

// Export only committed, explicitly listed files. This never copies a working
// directory, Git metadata, dependencies, or untracked files into the action.
export function exportDeployAction({ output, sourceRoot = SOURCE_ROOT }) {
  if (typeof output !== 'string' || !output.trim()) throw new Error('--output must name a new directory.');
  const commit = git(sourceRoot, ['rev-parse', '--verify', 'HEAD^{commit}']).toString('utf8').trim();
  const main = git(sourceRoot, ['rev-parse', '--verify', 'refs/remotes/origin/main^{commit}']).toString('utf8').trim();
  if (!/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(commit) || commit !== main) {
    throw new Error('Export requires HEAD to equal the fetched origin/main commit. Merge, fetch origin, and check out origin/main first.');
  }

  const files = new Map();
  for (const [source, destination] of FILES) {
    const content = committedFile(sourceRoot, commit, source, source === 'NOTICE');
    if (content !== undefined) files.set(destination, content);
  }
  files.set('README.md', files.get('README.md').toString('utf8').replaceAll(
    '../../docs/ci-api.md', `${REPOSITORY}/blob/main/docs/ci-api.md`,
  ));
  files.set('SOURCE.json', `${JSON.stringify({
    schema_version: 1, repository: REPOSITORY, commit, directory: 'actions/deploy',
  }, null, 2)}\n`);
  files.set('package.json', `${JSON.stringify({
    name: 'hakopod-deploy-action', private: true, type: 'module', license: 'Apache-2.0',
    engines: { node: '>=24' }, scripts: { test: 'node --test test/*.test.mjs' },
  }, null, 2)}\n`);
  files.set('.github/workflows/ci.yml', CI);
  files.set('.gitignore', 'node_modules/\n.tmp/\n*.log\n.DS_Store\n');

  const target = resolve(output);
  mkdirSync(dirname(target), { recursive: true });
  try { mkdirSync(target); }
  catch (error) {
    if (error.code === 'EEXIST') throw new Error('The output directory already exists. Choose a new directory; existing content is never overwritten.');
    throw error;
  }
  // Exclusive creation also protects individual output files. If disk writes
  // fail, leave the new directory intact for inspection instead of deleting it.
  for (const [path, content] of files) {
    const destination = join(target, path);
    mkdirSync(dirname(destination), { recursive: true });
    writeFileSync(destination, content, { flag: 'wx', mode: 0o644 });
  }
  return { output: target, commit, files: [...files.keys()].sort() };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length !== 4 || process.argv[2] !== '--output') {
      throw new Error('Usage: node scripts/export-deploy-action.mjs --output /path/to/new-directory');
    }
    const result = exportDeployAction({ output: process.argv[3] });
    process.stdout.write(`Exported ${result.files.length} files from ${result.commit} to ${result.output}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
