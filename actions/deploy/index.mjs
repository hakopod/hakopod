import { appendFileSync } from 'node:fs';
import { ActionError, deploy, parseInputs } from './deploy.mjs';

const escapeCommand = value => String(value).replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A');
const mask = value => process.stdout.write(`::add-mask::${escapeCommand(value)}\n`);

try {
  const options = parseInputs(process.env, mask);
  if (!process.env.GITHUB_OUTPUT) throw new ActionError('GITHUB_OUTPUT is required. Run this entry point as a GitHub Actions step.');
  const result = await deploy(options, {
    output: (name, value) => {
      // All output values are validated identifiers, numbers, or known status names.
      appendFileSync(process.env.GITHUB_OUTPUT, `${name}=${value}\n`);
    },
    log: message => process.stdout.write(`${message}\n`),
  });
  process.stdout.write(result.status === 'succeeded'
    ? `Deployment ${result.id} succeeded.\n`
    : `Deployment ${result.id} accepted (${result.status}). Rollout success has not been verified.\n`);
} catch (error) {
  const message = error instanceof ActionError ? error.message : 'The deployment action could not complete. Inspect Hakopod and the runner configuration.';
  process.stdout.write(`::error::${escapeCommand(message)}\n`);
  process.exitCode = 1;
}
