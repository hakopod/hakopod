import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { exportDeployAction } from './export-deploy-action.mjs';

const SOURCE_FILES = {
  'actions/deploy/action.yml': 'name: Synthetic deployment action\nruns:\n  using: node24\n  main: index.mjs\n',
  'actions/deploy/deploy.mjs': 'export const syntheticFixture = true;\n',
  'actions/deploy/index.mjs': "import './deploy.mjs';\n",
  'actions/deploy/test/deploy.test.mjs': '// Synthetic deployment test fixture.\n',
  'actions/deploy/test/index.test.mjs': '// Synthetic entrypoint test fixture.\n',
  'actions/deploy/testdata/runner.mjs': '// Synthetic GitHub runner API fixture.\n',
  'actions/deploy/README.md': '# Synthetic action\n\n[API setup](../../docs/ci-api.md)\n',
  LICENSE: 'Synthetic license fixture\n',
  NOTICE: 'Synthetic notice fixture\n',
};

function git(sourceRoot, ...args) {
  return execFileSync('git', ['-C', sourceRoot, '-c', 'core.hooksPath=/dev/null', ...args], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  }).trim();
}

function commitMain(sourceRoot) {
  git(sourceRoot, 'add', '--all', '--force');
  git(sourceRoot, '-c', 'user.name=Synthetic Fixture', '-c', 'user.email=fixture@example.test', 'commit', '--quiet', '--no-gpg-sign', '-m', 'Synthetic exporter fixture');
  const commit = git(sourceRoot, 'rev-parse', 'HEAD');
  git(sourceRoot, 'update-ref', 'refs/remotes/origin/main', commit);
  return commit;
}

function fixture(t, { notice = true } = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'hakopod-export-fixture-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const sourceRoot = join(directory, 'source');
  mkdirSync(sourceRoot);
  git(sourceRoot, 'init', '--quiet', '--template=');
  for (const [path, content] of Object.entries(SOURCE_FILES)) {
    if (!notice && path === 'NOTICE') continue;
    mkdirSync(dirname(join(sourceRoot, path)), { recursive: true });
    writeFileSync(join(sourceRoot, path), content);
  }
  // Even committed files outside the allowlist must never reach the export.
  writeFileSync(join(sourceRoot, '.env'), 'SYNTHETIC_SECRET=must-not-be-exported\n');
  mkdirSync(join(sourceRoot, 'actions/deploy/node_modules'), { recursive: true });
  writeFileSync(join(sourceRoot, 'actions/deploy/node_modules/unwanted.mjs'), 'synthetic dependency fixture\n');
  const commit = commitMain(sourceRoot);
  return { sourceRoot, commit, output: join(directory, 'standalone') };
}

function listFiles(root, relative = '') {
  return readdirSync(join(root, relative), { withFileTypes: true }).flatMap(entry => {
    const path = relative ? `${relative}/${entry.name}` : entry.name;
    return entry.isDirectory() ? listFiles(root, path) : [path];
  }).sort();
}

test('exports only the committed action allowlist with standalone metadata and pinned CI', (t) => {
  const f = fixture(t);
  writeFileSync(join(f.sourceRoot, 'actions/deploy/deploy.mjs'), 'uncommitted changes must not be exported\n');
  const result = exportDeployAction(f);
  const expected = [
    '.github/workflows/ci.yml', '.gitignore', 'LICENSE', 'NOTICE', 'README.md', 'SOURCE.json',
    'action.yml', 'deploy.mjs', 'index.mjs', 'package.json', 'test/deploy.test.mjs', 'test/index.test.mjs', 'testdata/runner.mjs',
  ].sort();
  assert.deepEqual(result.files, expected);
  assert.deepEqual(listFiles(f.output), expected);
  for (const [source, content] of Object.entries(SOURCE_FILES)) {
    if (source.endsWith('README.md')) continue;
    const destination = source.replace(/^actions\/deploy\//, '');
    assert.equal(readFileSync(join(f.output, destination), 'utf8'), content);
  }
  assert.equal(readFileSync(join(f.output, 'README.md'), 'utf8'), '# Synthetic action\n\n[API setup](https://github.com/hakopod/hakopod/blob/main/docs/ci-api.md)\n');
  assert.deepEqual(JSON.parse(readFileSync(join(f.output, 'SOURCE.json'), 'utf8')), {
    schema_version: 1, repository: 'https://github.com/hakopod/hakopod', commit: f.commit, directory: 'actions/deploy',
  });
  const pkg = JSON.parse(readFileSync(join(f.output, 'package.json'), 'utf8'));
  assert.equal(pkg.private, true);
  assert.equal(pkg.scripts.test, 'node --test test/*.test.mjs');
  assert.equal(pkg.engines.node, '>=24');
  assert.equal(pkg.dependencies, undefined);
  assert.equal(pkg.devDependencies, undefined);
  const ci = readFileSync(join(f.output, '.github/workflows/ci.yml'), 'utf8');
  assert.match(ci, /\n  push:\n  pull_request:\n  workflow_dispatch:\n/);
  assert.match(ci, /node-version: "24"/);
  assert.match(ci, /contents: read/);
  assert.match(ci, /run: npm test/);
  assert.match(ci, /node testdata\/runner\.mjs --port 18777/);
  assert.match(ci, /uses: \.\//);
  assert.match(ci, /DEPLOYMENT_STATUS: \$\{\{ steps\.smoke\.outputs\.status }}/);
  assert.match(ci, /test "\$DEPLOYMENT_STATUS" = succeeded/);
  assert.match(ci, /if: always\(\)/);
  const actions = [...ci.matchAll(/uses: (\S+)/g)].map(match => match[1]);
  assert.equal(actions.length, 3);
  assert.equal(actions.at(-1), './');
  assert.ok(actions.slice(0, 2).every(action => /^actions\/[a-z-]+@[a-f0-9]{40}$/.test(action)));
});

test('NOTICE is optional and exported bytes are deterministic for the same source commit', (t) => {
  const f = fixture(t, { notice: false });
  const first = exportDeployAction(f);
  const second = exportDeployAction({ ...f, output: `${f.output}-again` });
  assert.equal(existsSync(join(f.output, 'NOTICE')), false);
  assert.deepEqual(first.files, second.files);
  for (const path of first.files) {
    assert.deepEqual(readFileSync(join(first.output, path)), readFileSync(join(second.output, path)));
  }
});

test('refuses existing output directories and preserves their contents', (t) => {
  const f = fixture(t);
  mkdirSync(f.output);
  writeFileSync(join(f.output, 'keep.txt'), 'existing user content\n');
  assert.throws(() => exportDeployAction(f), /already exists/);
  assert.deepEqual(listFiles(f.output), ['keep.txt']);
  assert.equal(readFileSync(join(f.output, 'keep.txt'), 'utf8'), 'existing user content\n');
  const emptyOutput = `${f.output}-empty`;
  mkdirSync(emptyOutput);
  assert.throws(() => exportDeployAction({ ...f, output: emptyOutput }), /already exists/);
  assert.deepEqual(listFiles(emptyOutput), []);
});

test('refuses a source commit that is not the fetched origin/main before creating output', (t) => {
  const f = fixture(t);
  writeFileSync(join(f.sourceRoot, 'actions/deploy/deploy.mjs'), 'a later feature commit\n');
  git(f.sourceRoot, 'add', '--all');
  git(f.sourceRoot, '-c', 'user.name=Synthetic Fixture', '-c', 'user.email=fixture@example.test', 'commit', '--quiet', '--no-gpg-sign', '-m', 'Synthetic unmerged fixture');
  assert.throws(() => exportDeployAction(f), /HEAD to equal the fetched origin\/main/);
  assert.equal(existsSync(f.output), false);
});

test('refuses a symlink in the committed file allowlist without reading its target', (t) => {
  const f = fixture(t);
  const path = join(f.sourceRoot, 'actions/deploy/index.mjs');
  rmSync(path);
  symlinkSync('../../.env', path);
  commitMain(f.sourceRoot);
  assert.throws(() => exportDeployAction(f), /regular file at actions\/deploy\/index\.mjs/);
  assert.equal(existsSync(f.output), false);
});

test('the packaged runner fixture accepts the actual entrypoint smoke inputs', async (t) => {
  const directory = mkdtempSync(join(tmpdir(), 'hakopod-runner-fixture-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const child = spawn(process.execPath, [
    fileURLToPath(new URL('../actions/deploy/testdata/runner.mjs', import.meta.url)), '--port', '0',
  ], { stdio: ['ignore', 'pipe', 'pipe'] });
  t.after(() => { child.kill('SIGTERM'); });
  const [ready] = await once(child.stdout, 'data', { signal: AbortSignal.timeout(5000) });
  const match = ready.toString('utf8').match(/http:\/\/127\.0\.0\.1:\d+/);
  assert.ok(match, 'fixture reports its loopback URL');
  const outputFile = join(directory, 'github-output');
  writeFileSync(outputFile, '');
  execFileSync(process.execPath, [fileURLToPath(new URL('../actions/deploy/index.mjs', import.meta.url))], {
    encoding: 'utf8', timeout: 10_000,
    env: {
      'INPUT_API-URL': match[0],
      'INPUT_API-TOKEN': 'synthetic-runner-token',
      'INPUT_APPLICATION-ID': 'synthetic-runner-application',
      INPUT_SERVICES: '[{"name":"api","env":{"RELEASE":"synthetic-runner"}}]',
      INPUT_ENV: '{"SHARED":"synthetic-runner"}',
      INPUT_IMAGE: `ghcr.io/example/api@sha256:${'1'.repeat(64)}`,
      INPUT_TIMEOUT: '5',
      GITHUB_OUTPUT: outputFile,
    },
  });
  const output = readFileSync(outputFile, 'utf8');
  assert.match(output, /^status=succeeded$/m);
  assert.match(output, /^deployment-id=synthetic-runner-deployment$/m);
  assert.match(output, /^application-id=synthetic-runner-application$/m);
});
