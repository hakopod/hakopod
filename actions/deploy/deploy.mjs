import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';

const MAX_INPUT_BYTES = 256 * 1024;
const MAX_RESPONSE_BYTES = 2 * 1024 * 1024;
const NAME = /^(?:[a-z][a-z0-9-]{0,38}[a-z0-9]|[a-z])$/;
const ID = /^[A-Za-z0-9_-]{1,128}$/;
const VARIABLE = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;
const STATUSES = new Set(['queued', 'running', 'succeeded', 'failed', 'cancelled', 'superseded']);

// Only these errors may reach workflow logs. Never print response bodies or fetch errors.
export class ActionError extends Error {
  constructor(message, { status, ambiguous = false } = {}) {
    super(message);
    this.status = status;
    this.ambiguous = ambiguous;
  }
}

const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
function check(condition, message) {
  if (!condition) throw new ActionError(message);
}

function json(raw, name) {
  check(Buffer.byteLength(raw) <= MAX_INPUT_BYTES, `${name} exceeds the 256 KiB input limit.`);
  try { return JSON.parse(raw); }
  catch { throw new ActionError(`${name} must be valid JSON.`); }
}

function patch(value, label, secret = false) {
  check(object(value) && Object.keys(value).length <= 128, `${label} must be an object with at most 128 variables.`);
  for (const [key, entry] of Object.entries(value)) {
    check(VARIABLE.test(key), `${label} contains an invalid environment variable name.`);
    check(entry === null || (typeof entry === 'string' && !entry.includes('\0') &&
      (secret ? NAME.test(entry) : Buffer.byteLength(entry) <= 4096)),
    secret ? `${label} values must be existing secret reference names or null.` : `${label} values must be strings of at most 4096 bytes or null.`);
  }
  return value;
}

function pinnedImage(value) {
  check(typeof value === 'string' && value.length <= 512 &&
    /^[a-zA-Z0-9][a-zA-Z0-9._/:\-]*@sha256:[a-f0-9]{64}$/.test(value),
  'Images must use a repository@sha256:<64 lowercase hex characters> reference. Build and push the image before deployment.');
  return value;
}

function apiURL(raw) {
  let url;
  try { url = new URL(raw); } catch { throw new ActionError('api-url must be an absolute HTTPS URL.'); }
  const loopback = url.hostname === 'localhost' || url.hostname === '[::1]' || /^127\.\d+\.\d+\.\d+$/.test(url.hostname);
  check((url.protocol === 'https:' || (url.protocol === 'http:' && loopback)) &&
    !url.username && !url.password && !/[? #%\\\s]/.test(raw) &&
    /^https?:\/\/[^/?#]+(?:\/api\/v1)?\/*$/i.test(raw) &&
    ['', '/api/v1'].includes(url.pathname.replace(/\/+$/, '')),
  'api-url must use HTTPS without credentials, query, fragment, or encoded path. HTTP is allowed only on loopback for development.');
  const base = url.href.replace(/\/+$/, '');
  return base.endsWith('/api/v1') ? base : `${base}/api/v1`;
}

export function parseInputs(env, mask = () => {}) {
  const read = (name, fallback = '') => env[`INPUT_${name.toUpperCase()}`] ?? fallback;
  const token = read('api-token').trim();
  if (token) mask(token);
  check(token.length > 0 && token.length <= 8192 && !/[\s\x00-\x1f\x7f]/.test(token), 'api-token is required and must be a single bearer token.');
  const applicationId = read('application-id').trim();
  check(ID.test(applicationId), 'application-id must identify an existing application.');
  const rawServices = read('services');
  const services = json(rawServices, 'services');
  check(Array.isArray(services) && services.length >= 1 && services.length <= 20, 'services must be a JSON array of 1 to 20 existing services.');
  const seen = new Set();
  const selected = services.map(entry => {
    const service = typeof entry === 'string' ? { name: entry } : entry;
    check(object(service) && Object.keys(service).every(key => ['name', 'image', 'env', 'secrets'].includes(key)), 'Each service must be a name or an object containing only name, image, env, and secrets.');
    check(typeof service.name === 'string' && NAME.test(service.name) && !seen.has(service.name), 'Service names must be valid, unique Hakopod service names.');
    seen.add(service.name);
    if (Object.hasOwn(service, 'image')) pinnedImage(service.image);
    if (Object.hasOwn(service, 'env')) patch(service.env, 'Service env');
    if (Object.hasOwn(service, 'secrets')) patch(service.secrets, 'Service secrets', true);
    return service;
  });
  const image = read('image').trim();
  if (image) pinnedImage(image);
  const rawEnv = read('env', '{}');
  check(Buffer.byteLength(rawServices) + Buffer.byteLength(rawEnv) <= MAX_INPUT_BYTES, 'Combined services and env inputs exceed 256 KiB.');
  const variables = patch(json(rawEnv, 'env'), 'env');
  const workspace = read('workspace').trim();
  check(!workspace || /^[a-f0-9]{32}$/.test(workspace), 'workspace must be a 32-character Hakopod Cloud workspace ID.');
  const wait = read('wait', 'true').trim();
  check(wait === 'true' || wait === 'false', 'wait must be true or false.');
  const number = (name, fallback, max) => {
    const raw = read(name, fallback).trim();
    check(/^\d+$/.test(raw) && Number(raw) >= 1 && Number(raw) <= max, `${name} must be an integer from 1 to ${max}.`);
    return Number(raw);
  };
  return { apiUrl: apiURL(read('api-url').trim()), token, applicationId,
    services: selected, image, env: variables, workspace, wait: wait === 'true',
    timeout: number('timeout', '600', 3600), pollInterval: number('poll-interval', '5', 60) };
}

function applyPatch(original, changes, secrets = false) {
  const result = new Map(Object.entries(original ?? {}));
  for (const [key, value] of Object.entries(changes)) {
    if (value === null) result.delete(key);
    else result.set(key, secrets ? { ref: value } : value);
  }
  return Object.fromEntries(result);
}

function httpError(status, stage) {
  const advice = {
    401: 'Check the API token and its expiry.',
    403: 'Check token scope, workspace, and deployments:read/write permissions. Complete any required approval in Hakopod.',
    404: 'Check the application ID, API URL, and token scope.',
    409: 'The application changed or its current release is unresolved. Inspect it and rerun with a fresh plan.',
    429: 'Hakopod rate limited this request. Retry later.',
  }[status] ?? 'Inspect the deployment or configuration in Hakopod for details.';
  return new ActionError(`${stage} failed (HTTP ${status}). ${advice}`, { status, ambiguous: status >= 500 || status === 408 });
}

export async function deploy(options, {
  fetch = globalThis.fetch, sleep = ms => delay(ms), now = Date.now,
  output = () => {}, log = () => {},
} = {}) {
  const deadline = now() + options.timeout * 1000;
  let acceptedId;
  const remaining = () => {
    const value = deadline - now();
    check(value > 0, acceptedId
      ? `Timed out waiting for deployment ${acceptedId}. The accepted deployment continues in Hakopod; inspect it before retrying.`
      : 'The action timed out. If submission started, recover it using the idempotency-key output before retrying.');
    return value;
  };
  const pause = async ms => { await sleep(Math.min(ms, remaining())); remaining(); };

  async function request(method, path, body, key, stage) {
    const bodyText = body === undefined ? undefined : JSON.stringify(body);
    check(bodyText === undefined || Buffer.byteLength(bodyText) <= 512 * 1024, 'The deployment request exceeds 512 KiB.');
    const attempts = method === 'GET' ? 3 : 1;
    for (let attempt = 0; attempt < attempts; attempt++) {
      let reader;
      try {
        const headers = { Accept: 'application/json', Authorization: `Bearer ${options.token}` };
        if (bodyText !== undefined) headers['Content-Type'] = 'application/json';
        if (options.workspace) headers['X-Hakopod-Workspace'] = options.workspace;
        if (key) headers['Idempotency-Key'] = key;
        const response = await fetch(`${options.apiUrl}${path}`, {
          method, headers, body: bodyText, redirect: 'error',
          signal: AbortSignal.timeout(Math.max(1, Math.min(30000, remaining()))),
        });
        if (response.redirected || (response.status >= 300 && response.status < 400)) {
          await response.body?.cancel();
          throw new ActionError(`${stage} returned a redirect. Use the final HTTPS API URL.`, { ambiguous: method === 'POST' });
        }
        if (!response.ok) {
          await response.body?.cancel();
          throw httpError(response.status, stage);
        }
        if (Number(response.headers.get('content-length')) > MAX_RESPONSE_BYTES) {
          await response.body?.cancel();
          throw new ActionError(`${stage} response exceeded 2 MiB.`, { ambiguous: true });
        }
        reader = response.body?.getReader();
        if (!reader) throw new ActionError(`${stage} returned an empty response.`, { ambiguous: true });
        const chunks = [];
        let bytes = 0;
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          bytes += value.byteLength;
          if (bytes > MAX_RESPONSE_BYTES) throw new ActionError(`${stage} response exceeded 2 MiB.`, { ambiguous: true });
          chunks.push(value);
        }
        try {
          const parsed = JSON.parse(Buffer.concat(chunks).toString('utf8'));
          if (!object(parsed)) throw new Error();
          return parsed;
        } catch { throw new ActionError(`${stage} returned invalid JSON.`, { ambiguous: true }); }
      } catch (error) {
        const safe = error instanceof ActionError ? error : new ActionError(`${stage} could not be completed. Check API connectivity.`, { ambiguous: true });
        if (attempt + 1 === attempts || !(safe.ambiguous || safe.status === 429)) throw safe;
        await pause((attempt + 1) * 1000);
      } finally {
        if (reader) { try { await reader.cancel(); } catch {} reader.releaseLock(); }
      }
    }
  }

  const app = await request('GET', `/applications/${options.applicationId}`, undefined, undefined, 'Application fetch');
  check(app.id === options.applicationId && Number.isSafeInteger(app.revision) && app.revision >= 1 &&
    typeof app.project === 'string' && NAME.test(app.project) && typeof app.environment === 'string' && NAME.test(app.environment) &&
    object(app.spec) && app.spec.schema_version === 1 && typeof app.spec.name === 'string' && NAME.test(app.spec.name) && object(app.spec.services),
  'Application fetch returned an invalid application or revision.');
  const next = structuredClone(app.spec);
  for (const selected of options.services) {
    check(Object.hasOwn(next.services, selected.name) && object(next.services[selected.name]), `Service ${selected.name} is not in this application.`);
    const service = next.services[selected.name];
    if (selected.image || options.image) service.image = selected.image || options.image;
    const changes = { ...options.env, ...selected.env };
    if (Object.keys(changes).length) service.env = applyPatch(service.env, changes);
    if (selected.secrets) service.secrets = applyPatch(service.secrets, selected.secrets, true);
    check(!Object.keys(service.env ?? {}).some(key => Object.hasOwn(service.secrets ?? {}, key)),
      `Service ${selected.name} has conflicting env and secret bindings. Explicitly remove the old binding with null.`);
  }
  const scope = { project: app.project, environment: app.environment };
  const plan = await request('POST', '/plan', { ...scope, spec: next, services: options.services.map(service => service.name), expected_revision: app.revision }, undefined, 'Deployment plan');
  check(plan.application_id === app.id, 'The deployment plan targets a different application.');
  check(plan.expected_revision === app.revision, 'The application revision changed while planning. Rerun to review the current application.');
  check(object(plan.spec) && plan.spec.schema_version === 1 && plan.spec.name === app.spec.name && object(plan.spec.services) &&
    Object.keys(plan.spec.services).length === Object.keys(next.services).length && Object.keys(next.services).every(name => Object.hasOwn(plan.spec.services, name)),
  'The deployment plan returned an invalid application specification.');
  check(plan.missing_secrets === undefined || Array.isArray(plan.missing_secrets), 'The deployment plan returned invalid secret requirements.');
  check(!plan.missing_secrets?.length, 'The deployment needs missing secrets. Save them in Hakopod, then rerun the action.');
  if (Array.isArray(plan.warnings) && plan.warnings.length) log('The plan has warnings. Review them in Hakopod; response bodies are omitted from workflow logs.');

  const key = randomUUID();
  output('idempotency-key', key);
  output('application-id', app.id);
  log(`Submitting ${options.services.length} service(s) at application revision ${app.revision + 1}.`);
  // Submit the canonical full plan, as the Go CLI does. Reapplying a service selector
  // here would merge the reviewed snapshot into whatever state exists on a retry.
  const body = { ...scope, spec: plan.spec, expected_revision: plan.expected_revision };
  if (plan.provenance) body.provenance = plan.provenance;
  let deployment;
  try {
    deployment = await request('POST', '/deployments', body, key, 'Deployment submission');
  } catch (error) {
    if (!(error instanceof ActionError) || !error.ambiguous) throw error;
    log('Submission response was uncertain. Looking up the request without resubmitting it.');
    try { deployment = await request('GET', `/idempotency/${key}`, undefined, undefined, 'Deployment recovery'); }
    catch { throw new ActionError(`Deployment acceptance could not be confirmed. Recover with idempotency key ${key} before starting another deployment.`); }
  }

  function observe(value) {
    check(object(value) && typeof value.id === 'string' && ID.test(value.id) && value.application_id === app.id &&
      Number.isSafeInteger(value.revision) && value.revision === app.revision + 1 && (!acceptedId || value.id === acceptedId),
    'Hakopod returned an unexpected deployment. Use the idempotency-key output to inspect the request.');
    check(STATUSES.has(value.status), 'Hakopod returned an unknown deployment status. Inspect it before retrying.');
    const first = !acceptedId;
    acceptedId = value.id;
    if (first) {
      output('deployment-id', value.id);
      output('revision', String(value.revision));
    }
    output('status', value.status);
    if (['failed', 'cancelled', 'superseded'].includes(value.status)) throw new ActionError(`Deployment ${value.id} ${value.status}. Inspect its events in Hakopod.`);
    return value;
  }
  observe(deployment);
  if (!options.wait || deployment.status === 'succeeded') return deployment;
  while (true) {
    await pause(options.pollInterval * 1000);
    deployment = observe(await request('GET', `/deployments/${acceptedId}`, undefined, undefined, 'Deployment status'));
    if (deployment.status === 'succeeded') return deployment;
  }
}
