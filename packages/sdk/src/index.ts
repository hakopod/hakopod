import { Transport, environmentVariables, required } from "./transport.js";
import type { ClientOptions } from "./transport.js";
import type {
  ApplicationSpec,
  Method,
  Operation,
  PathFor,
  RequestArgs,
  ResponseFor,
  Scope,
  RequestOptions,
  Schema,
  Deployment,
} from "./types.js";
import {
  ApplicationRef,
  BackupRun,
  DatabaseRef,
  DatabaseRun,
  DatabasePublicEndpointRun,
  DatabaseApplicationProvisioningRun,
  DatabaseMigrationLockRecoveryRun,
  DeploymentRun,
  NetworkRef,
  ManagedPlatformRef,
  ManagedPlatformRun,
  planApplication,
  scope,
} from "./resources.js";
import type { Context, PlanOptions } from "./resources.js";
import { HakopodError } from "./errors.js";
import { ExternalDatabaseRef, ExternalDatabaseRun } from "./external-databases.js";

export { ExternalDatabaseRef, ExternalDatabaseRun } from "./external-databases.js";
export type { ExternalDatabaseCredentials } from "./external-databases.js";

export * from "./errors.js";
export * from "./types.js";
export {
  app,
  db,
  network,
  service,
  ApplicationRef,
  BackupRun,
  DatabaseRef,
  DatabaseRun,
  DatabasePublicEndpointRun,
  DeploymentRun,
  NetworkRef,
  ManagedPlatformRef,
  ManagedPlatformRun,
  Review,
  ServiceRef,
} from "./resources.js";
export type {
  ApplicationInput,
  DatabaseInput,
  NetworkInput,
  DeepReadonly,
  PlanOptions,
} from "./resources.js";
export type { ClientOptions } from "./transport.js";
export type { paths, components, operations } from "./api.generated.js";

export class Hakopod {
  #context: Context;
  /** Resolve an explicitly scoped key. An installation administrator must select a scope. */
  static async connect(
    options: ClientOptions = {},
    request: RequestOptions = {},
  ): Promise<Hakopod> {
    const client = new Hakopod(options);
    const principal = await client.me(request);
    if (client.#context.scope) return client;
    if (!principal.project || !principal.environment)
      throw new HakopodError(
        "This key has no single project/environment. Provide both explicitly or create a scoped API key.",
        "missing_scope",
      );
    return client.in({
      project: principal.project,
      environment: principal.environment,
    });
  }
  constructor(options: ClientOptions = {}, context?: Context) {
    if (context) {
      this.#context = context;
      return;
    }
    const env = environmentVariables();
    const project = options.project ?? env.HAKOPOD_PROJECT;
    const environment = options.environment ?? env.HAKOPOD_ENVIRONMENT;
    if ((project === undefined) !== (environment === undefined))
      throw new HakopodError(
        "Provide both project and environment, or neither.",
        "missing_scope",
      );
    this.#context = {
      transport: new Transport(options),
      ...(project === undefined
        ? {}
        : {
            scope: Object.freeze({
              project: required(project, "project"),
              environment: required(environment, "environment"),
            }),
          }),
    };
  }
  /** A new project/environment view sharing the same bounded transport and Cloud workspace. */
  in(selected: Scope): Hakopod {
    return new Hakopod(
      {},
      {
        ...this.#context,
        scope: Object.freeze({
          project: required(selected.project, "project"),
          environment: required(selected.environment, "environment"),
        }),
      },
    );
  }
  /** Typed access to JSON endpoints from the canonical OpenAPI contract. */
  request<M extends Method, P extends PathFor<M>>(
    method: M,
    path: P,
    ...args: RequestArgs<Operation<M, P>>
  ): Promise<ResponseFor<Operation<M, P>>> {
    return this.#context.transport.request(method, path, args[0]);
  }
  me(options: RequestOptions = {}): Promise<Schema["Principal"]> {
    return this.request("GET", "/me", options);
  }
  capabilities(
    options: RequestOptions = {},
  ): Promise<Schema["CloudCapabilities"]> {
    return this.request("GET", "/cloud/capabilities", {
      ...options,
      query: scope(this.#context),
    });
  }
  projects(options: RequestOptions = {}) {
    return this.request("GET", "/projects", options);
  }
  listApplications(options: RequestOptions & { cursor?: string } = {}) {
    return this.request("GET", "/applications", {
      ...options,
      query: { ...scope(this.#context), cursor: options.cursor },
    });
  }
  listDatabases(options: RequestOptions = {}) {
    return this.request("GET", "/databases", {
      ...options,
      query: scope(this.#context),
    });
  }
  listManagedPlatforms(options: RequestOptions = {}) {
    return this.request("GET", "/managed-platforms", { ...options, query: this.#context.scope ?? {} });
  }
  managedPlatformCatalog(options: RequestOptions = {}) {
    return this.request("GET", "/managed-platforms/catalog", {
      ...options,
      query: scope(this.#context),
    });
  }
  reviewManagedPlatform(spec: Schema["ManagedPlatformSpec"], options: RequestOptions = {}) {
    const selected = scope(this.#context);
    return this.request("POST", "/managed-platforms/reviews", {
      ...options,
      body: { ...selected, expected_revision: 0, kind: "create", spec },
    });
  }
  acceptManagedPlatform(intent: Schema["ManagedPlatformAcceptIntent"], options: RequestOptions & { idempotencyKey: string }) {
    return this.request("POST", "/managed-platforms/operations", {
      ...options,
      idempotencyKey: required(options.idempotencyKey, "idempotencyKey"),
      body: intent,
    });
  }
  managedPlatform(id: string): ManagedPlatformRef {
    return new ManagedPlatformRef(this.#context, id);
  }
  managedPlatformOperation(id: string): ManagedPlatformRun {
    return new ManagedPlatformRun(this.#context, id);
  }
  databasePlacementNodes(options: RequestOptions = {}) {
    return this.request("GET", "/database-placement/nodes", {
      ...options,
      query: scope(this.#context),
    });
  }
  databaseCapacityPlan(spec: Schema["ManagedDatabaseSpec"], databaseId?: string, options: RequestOptions = {}) {
    const selected = scope(this.#context);
    return this.request("POST", "/database-capacity-plan", {
      ...options,
      body: { ...selected, spec, ...(databaseId ? { database_id: databaseId } : {}) },
    });
  }
  listExternalDatabases(options: RequestOptions = {}) {
    return this.request("GET", "/external-databases", { ...options, query: scope(this.#context) });
  }
  listNetworks(options: RequestOptions = {}) {
    return this.request("GET", "/virtual-networks", {
      ...options,
      query: scope(this.#context),
    });
  }
  plan(spec: ApplicationSpec, options: PlanOptions = {}) {
    return planApplication(this.#context, spec, options);
  }
  app(name: string): ApplicationRef {
    scope(this.#context);
    return new ApplicationRef(this.#context, { name });
  }
  application(id: string): ApplicationRef {
    return new ApplicationRef(this.#context, { id });
  }
  service(applicationId: string, name: string) {
    return this.application(applicationId).service(name);
  }
  db(name: string): DatabaseRef {
    scope(this.#context);
    return new DatabaseRef(this.#context, { name });
  }
  database(id: string): DatabaseRef {
    return new DatabaseRef(this.#context, { id });
  }
  externalDB(name: string): ExternalDatabaseRef {
    scope(this.#context);
    return new ExternalDatabaseRef(this.#context, { name });
  }
  externalDatabase(id: string): ExternalDatabaseRef {
    return new ExternalDatabaseRef(this.#context, { id });
  }
  externalDatabaseOperation(databaseId: string, operationId: string): ExternalDatabaseRun {
    return new ExternalDatabaseRun(this.#context, operationId, databaseId);
  }
  network(name: string): NetworkRef {
    scope(this.#context);
    return new NetworkRef(this.#context, name);
  }
  deployment(id: string): DeploymentRun {
    return new DeploymentRun(this.#context, id);
  }
  databaseOperation(databaseId: string, operationId: string): DatabaseRun {
    return new DatabaseRun(this.#context, operationId, databaseId);
  }
  databasePublicEndpointOperation(databaseId: string, endpointId: string, operationId: string): DatabasePublicEndpointRun {
    return new DatabasePublicEndpointRun(this.#context, operationId, databaseId, endpointId);
  }
  backup(id: string): BackupRun {
    return new BackupRun(this.#context, id);
  }
  async recoverDeployment(
    key: string,
    options: RequestOptions = {},
  ): Promise<DeploymentRun> {
    const result: Deployment = await this.request("GET", "/idempotency/{key}", {
      ...options,
      params: { key },
    });
    return new DeploymentRun(this.#context, result.id, result, key);
  }
}
