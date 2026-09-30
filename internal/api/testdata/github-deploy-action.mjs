// Development bridge for the real Go API/PostgreSQL contract tests. The action
// implementation comes only from an explicitly selected standalone checkout.
import { isAbsolute, join } from 'node:path';
import { pathToFileURL } from 'node:url';

const outputs = {};
let ActionError;
try {
  const actionPath = process.env.HAKOPOD_DEPLOY_ACTION_PATH;
  if (!actionPath || !isAbsolute(actionPath)) throw new Error('Invalid action checkout path.');
  const action = await import(pathToFileURL(join(actionPath, 'deploy.mjs')).href);
  ActionError = action.ActionError;
  const result = await action.deploy(action.parseInputs(process.env), {
    output: (name, value) => { outputs[name] = value; },
  });
  // wait=false proves durable acceptance, not a successful Kubernetes rollout.
  process.stdout.write(JSON.stringify({
    id: result.id,
    application_id: result.application_id,
    revision: result.revision,
    status: result.status,
    outputs,
  }));
} catch (error) {
  const message = typeof ActionError === 'function' && error instanceof ActionError
    ? error.message
    : 'The standalone action could not run. Check HAKOPOD_DEPLOY_ACTION_PATH and the selected Node runtime.';
  process.stdout.write(JSON.stringify({ error: message, outputs }));
  process.exitCode = 1;
}
