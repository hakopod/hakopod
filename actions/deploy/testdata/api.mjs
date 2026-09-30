// Development fixture for the real Go API/PostgreSQL contract test. No runner or
// Kubernetes result is fabricated here: wait=false checks durable acceptance.
import { deploy, parseInputs } from '../deploy.mjs';

const outputs = {};
try {
  const result = await deploy(parseInputs(process.env), {
    output: (name, value) => { outputs[name] = value; },
  });
  process.stdout.write(JSON.stringify({
    id: result.id,
    application_id: result.application_id,
    revision: result.revision,
    status: result.status,
    outputs,
  }));
} catch (error) {
  // ActionError messages are safe for workflow logs; never serialize responses.
  process.stdout.write(JSON.stringify({ error: error.message, outputs }));
  process.exitCode = 1;
}
