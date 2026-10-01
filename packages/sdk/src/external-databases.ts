import { APIError, HakopodError } from "./errors.js";
import { assertScope, DeploymentRun, Review, scope, waitFor } from "./resources.js";
import type { Context } from "./resources.js";
import { idempotencyKey, integer, required } from "./transport.js";
import type { RequestOptions, Schema, WaitOptions } from "./types.js";

type ExternalDatabase = Schema["ExternalDatabase"];
type ExternalOperation = Schema["ExternalDatabaseOperation"];
export type ExternalDatabaseCredentials = Schema["ExternalDatabaseCredentials"];

/** An accepted connection operation; this never represents provider provisioning. */
export class ExternalDatabaseRun {
  readonly database: ExternalDatabaseRef;
  constructor(
    private context: Context,
    readonly id: string,
    readonly databaseId: string,
    readonly idempotencyKey?: string,
  ) {
    this.database = new ExternalDatabaseRef(context, { id: databaseId });
  }
  async get(options: RequestOptions = {}): Promise<ExternalOperation> {
    const result = await this.context.transport.request<ExternalOperation>("GET", "/external-database-operations/{id}", {
      ...options, params: { id: this.id },
    });
    if (result.id !== this.id || result.database_id !== this.databaseId)
      throw new HakopodError("The API returned another external database operation.", "invalid_response");
    return result;
  }
  wait(options: WaitOptions<ExternalOperation> = {}): Promise<ExternalOperation> {
    return waitFor(this.id, (signal) => this.get({ signal }), options);
  }
}

/** A legacy provider connection retained for inspection, disconnection and deletion. */
export class ExternalDatabaseRef {
  #context: Context;
  #id?: string;
  #name?: string;
  constructor(context: Context, ref: { id: string } | { name: string }) {
    this.#context = context;
    if ("id" in ref) this.#id = required(ref.id, "external database ID");
    else this.#name = required(ref.name, "external database name");
  }
  async get(options: RequestOptions = {}): Promise<ExternalDatabase> {
    let id = this.#id;
    if (!id) {
      const result = await this.#context.transport.request<{ items: ExternalDatabase[] }>("GET", "/external-databases", {
        ...options, query: scope(this.#context),
      });
      const found = result.items.find((item) => item.spec.name === this.#name);
      if (!found) {
        if (result.items.length >= 64)
          throw new HakopodError("External database lookup reached the listing limit. Use client.externalDatabase(id).", "listing_limit");
        throw new APIError("External database not found in the selected scope.", "not_found", 404);
      }
      id = found.id;
    }
    const current = await this.#context.transport.request<ExternalDatabase>("GET", "/external-databases/{id}", {
      ...options, params: { id },
    });
    assertScope(this.#context, current);
    if (current.id !== id || this.#name && current.spec.name !== this.#name)
      throw new HakopodError("The API returned another external database.", "invalid_response");
    return current;
  }
  async rotateCredentials(
    input: { credentials: ExternalDatabaseCredentials; expectedRevision: number; confirmName: string },
    options: RequestOptions = {},
  ): Promise<ExternalDatabaseRun> {
    const current = await this.get(options);
    if (input.confirmName !== current.spec.name)
      throw new HakopodError("Confirm the connection name before rotating credentials.", "confirmation_required");
    const key = idempotencyKey(options.idempotencyKey);
    const result = await this.#context.transport.request<ExternalOperation>("PUT", "/external-databases/{id}", {
      ...options, idempotencyKey: key, params: { id: current.id },
      body: {
        spec: current.spec,
        credentials: input.credentials,
        expected_revision: integer(input.expectedRevision, 1, Number.MAX_SAFE_INTEGER, "expectedRevision"),
        confirm_name: input.confirmName,
      },
    });
    return new ExternalDatabaseRun(this.#context, result.id, result.database_id, key);
  }
  async delete(input: { expectedRevision: number; confirmName: string }, options: RequestOptions = {}): Promise<ExternalDatabaseRun> {
    const current = await this.get(options);
    if (input.confirmName !== current.spec.name)
      throw new HakopodError("Confirm the connection name to remove it from Hakopod.", "confirmation_required");
    const key = idempotencyKey(options.idempotencyKey);
    const result = await this.#context.transport.request<ExternalOperation>("DELETE", "/external-databases/{id}", {
      ...options, idempotencyKey: key, params: { id: current.id },
      body: { expected_revision: integer(input.expectedRevision, 1, Number.MAX_SAFE_INTEGER, "expectedRevision"), confirm_name: input.confirmName },
    });
    return new ExternalDatabaseRun(this.#context, result.id, result.database_id, key);
  }
  async trust(options: RequestOptions = {}): Promise<Schema["ExternalDatabaseTrust"]> {
    const current = await this.get(options);
    return this.#context.transport.request("GET", "/external-databases/{id}/trust", { ...options, params: { id: current.id } });
  }
  async connections(options: RequestOptions = {}): Promise<Schema["DatabaseConnections"]> {
    const current = await this.get(options);
    return this.#context.transport.request("GET", "/external-databases/{id}/connections", { ...options, params: { id: current.id } });
  }
  async connectionPlan(
    input: { applicationId: string; service: string; variable: string; disconnect: boolean },
    options: RequestOptions = {},
  ): Promise<Review<Schema["ExternalDatabaseConnectionPlan"], DeploymentRun>> {
    const current = await this.get(options);
    const plan = await this.#context.transport.request<Schema["ExternalDatabaseConnectionPlan"]>("POST", "/external-databases/{id}/connection-plan", {
      ...options, params: { id: current.id },
      body: { application_id: required(input.applicationId, "application ID"), service: required(input.service, "service"), variable: required(input.variable, "variable"), disconnect: input.disconnect },
    });
    if (plan.database_id !== current.id || plan.database_revision !== current.revision || plan.credential_revision !== current.credential_revision || plan.application_id !== input.applicationId || plan.service !== input.service || plan.variable !== input.variable || plan.kind !== (input.disconnect ? "disconnect" : "refresh"))
      throw new HakopodError("The API planned a different external database connection.", "invalid_response");
    return new Review(plan, async (reviewed, applyOptions) => {
      const deployment = await this.#context.transport.request<Schema["Deployment"]>("POST", "/external-databases/{id}/connect", {
        ...applyOptions, params: { id: reviewed.database_id },
        body: { review_id: reviewed.id, confirm_application: reviewed.application_name },
      });
      return new DeploymentRun(this.#context, deployment.id, deployment, applyOptions.idempotencyKey);
    });
  }
}
