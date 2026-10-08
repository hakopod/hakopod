# One contract for API clients and agent tools

OpenAPI is the shared interface contract. The feature definitions in `api/contracts/` generate `api/openapi.json`.
The dashboard types, SDK types, proxy routes and agent operation catalog use that document.
Swagger UI can display this contract. It does not define authorization or execution behavior.

Every operation has an explicit action summary in its feature contract or `api/operation-summaries.json`.
Generation rejects missing or obsolete summary entries. It does not derive action descriptions from operation IDs.
CLI and MCP discovery preserve this text. Detailed descriptions and policies specify additional conditions.

## Generated and maintained components

| Component | Contract use |
| --- | --- |
| Dashboard and SDK | Generate TypeScript request and response types from OpenAPI. |
| Dashboard proxy | Generate exact routes for operations with generic or installation exposure. |
| CLI and MCP | Load operation IDs, schemas, references and policies from the embedded OpenAPI document. |
| Discovery | Return bounded operation pages, verbatim summaries and descriptions, schema definitions and the contract SHA-256. |
| Coverage inventory | Compare contract operations with literal dashboard call sites. |
| Go API | Enforce current credentials, scope, ownership, revisions, reviews and execution limits. |

The CLI and MCP share the Go operation adapter. There is no separately maintained list of generic MCP endpoints.
The adapter compiles the embedded input schemas once with a pinned JSON Schema 2020-12 library.
It rejects external schema resources and applies composition, conditional, numeric, string and collection constraints.
Schema validation preserves exact JSON numbers. Validation errors omit submitted values and property names.
Format and content keywords remain annotations. The canonical handlers apply domain-specific checks.

Declared object types reject unknown fields. Explicit maps and provider webhook payloads retain their declared open fields.
Object extensions merge their base fields before generation. They do not combine incompatible closed object branches.
Generation declares each route variable as a required path parameter. Explicit parameter schemas retain their constraints.
Generation rejects duplicate parameters and invalid path declarations. Clients use the generated declarations without separate path inference.
Every input has independent bounds: 32 nested levels, 10,000 values and 1,000 items per array.
Numeric literals have at most 128 characters, with exponents from -1000 to 1000.
The request and response limits remain 1 MiB.
Dedicated tools remain for deployment review, pod execution, SQL, terminals, event samples and CSV exports.
These tools handle behavior that a JSON request schema cannot describe completely.

## Agent exposure metadata

Every operation has an explicit `x-hakopod-agent` policy from `api/contracts/agent_policy.py`.
The policy states exposure, connection boundary, required permissions, resource references, sensitive fields and review requirements.
Credential checks consume this policy. Do not create a second credential-policy map in a gateway or client.

An unclassified operation does not become callable automatically.
A permission in the contract does not grant that permission to the caller.
The canonical Go API checks the caller's current authority for every request.
Revocation, scope restrictions and review checks still apply after discovery.

Personal authentication, provider callbacks and approval flows remain prerequisites.
An agent cannot use generated invocation to approve its own access.

## Update procedure

1. Edit the feature contract, its action summary and its agent policy.
2. Run `python3 api/generate.py` to generate OpenAPI and agent proxy routes.
3. Generate dashboard and SDK types with their package scripts.
4. Run `python3 docs/agent-parity/coverage_inventory.py` to refresh the inventory.
5. Check generated-file drift and run affected authorization and adapter tests.
6. Run native acceptance for changed Kubernetes behavior.

CI checks OpenAPI, proxy, SDK, dashboard API types and editor-schema generation for drift.
Tests reject missing exposure metadata and unsafe invocation paths.
Tests also check that each route variable has exactly one required path parameter.
CI also checks literal Go API route declarations against OpenAPI. A new declaration must have a contract or an explained exclusion.
`api/route_coverage_exclusions.json` records internal protocols, acceptance probes, the contract download, a retired route and the Slack OAuth callback.
The check rejects obsolete exclusions. Dynamic route expressions and transports that dispatch several methods still require separate review.
Use an immutable source snapshot for validation. Record its hash with the results.

This process reduces duplicate interface work. It does not generate reconciler logic, prove runtime behavior or authorize deployment.
