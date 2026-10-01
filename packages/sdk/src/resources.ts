import { APIError, HakopodError, OperationError, WaitError } from "./errors.js";
import {
  abortable,
  delay,
  idempotencyKey,
  integer,
  required,
  Transport,
} from "./transport.js";
import type {
  ApplicationSpec,
  DatabaseOperation,
  DatabasePublicEndpointCapabilities,
  DatabasePublicEndpointOperation,
  DatabasePublicEndpointSpec,
  DatabaseSpec,
  Deployment,
  ManagedDatabase,
  NetworkSpec,
  Plan,
  RequestOptions,
  Schema,
  Scope,
  ServiceSpec,
  WaitOptions,
} from "./types.js";

export type ApplicationInput = Omit<ApplicationSpec, "name" | "schema_version">;
export type DatabaseInput = {
  engine: DatabaseSpec["engine"];
  version?: string;
  mode?: "standalone" | "cluster";
  replicas?: number;
  shards?: number;
  cpu: string;
  memory: string;
  storageGiB: number;
  placement?: DatabaseSpec["placement"];
  pooling?: DatabaseSpec["pooling"];
  oracle?: DatabaseSpec["oracle"];
  vitess?: DatabaseSpec["vitess"];
};
export type NetworkInput = Pick<NetworkSpec, "segments"> & {
  description?: string;
};
export type DeepReadonly<T> = T extends object
  ? { readonly [K in keyof T]: DeepReadonly<T[K]> }
  : T;

function snapshot<T>(value: T): T {
  let copy: T;
  try {
    copy = JSON.parse(JSON.stringify(value));
  } catch {
    throw new HakopodError(
      "Configuration must contain JSON values.",
      "invalid_body",
    );
  }
  function freeze(value: unknown): void {
    if (value && typeof value === "object") {
      Object.freeze(value);
      for (const child of Object.values(value)) freeze(child);
    }
  }
  freeze(copy);
  return copy;
}

/** Define one service inside an application. This performs no network requests. */
export function service(input: ServiceSpec): ServiceSpec {
  return snapshot(input);
}
/** Define a complete application revision. The server resolves image tags and validates it. */
export function app(
  input: ApplicationInput & { name: string },
): ApplicationSpec {
  return snapshot({ ...input, schema_version: 1 });
}
/** Resource amounts are explicit and apply to every database member. */
export function db(input: DatabaseInput & { name: string }): DatabaseSpec {
  const mode = input.mode ?? "standalone";
  return snapshot({
    schema_version: 1,
    name: input.name,
    engine: input.engine,
    version:
      input.version ??
      { postgresql: "18", redis: "8", mysql: "8.4", mongodb: "8.0", clickhouse: "26.3", oracle: "23.26", vitess: "23" }[input.engine],
    mode,
    replicas:
      input.replicas ??
      (mode === "cluster" ? (["mysql", "mongodb"].includes(input.engine) ? 2 : 1) : 0),
    shards:
      input.shards ?? (mode === "cluster" && input.engine === "redis" ? 3 : 1),
    cpu: input.cpu,
    memory: input.memory,
    storage_gib: input.storageGiB,
    tls: { mode: "required" },
    ...(input.placement ? { placement: input.placement } : {}),
    ...(input.pooling ? { pooling: input.pooling } : {}),
    ...(input.engine === "oracle" ? { oracle: input.oracle ?? { edition: "free" } } : {}),
    ...(input.engine === "vitess" && input.vitess ? { vitess: input.vitess } : {}),
  });
}
export function network(input: NetworkInput & { name: string }): NetworkSpec {
  return snapshot({
    ...input,
    schema_version: 1,
    description: input.description ?? "",
  });
}

export type Context = { transport: Transport; scope?: Readonly<Scope> };
export function scope(context: Context): Readonly<Scope> {
  if (!context.scope)
    throw new HakopodError(
      "Select both project and environment with client.in({ project, environment }).",
      "missing_scope",
    );
  return context.scope;
}
export function assertScope(context: Context, resource: Scope): void {
  if (
    context.scope &&
    (context.scope.project !== resource.project ||
      context.scope.environment !== resource.environment)
  )
    throw new HakopodError(
      "This resource belongs to a different project or environment.",
      "scope_mismatch",
    );
}
function revision(value: number): number {
  return integer(value, 0, Number.MAX_SAFE_INTEGER, "expectedRevision");
}
function assertRevision(plan: Plan, expected?: number): void {
  if (expected !== undefined && plan.expected_revision !== expected)
    throw new APIError(
      "The application changed while planning. Fetch it again and review a new plan.",
      "revision_conflict",
      409,
    );
}

/** The submitted snapshot is frozen. Editing a displayed plan never changes an accepted payload. */
export class Review<T, Result> {
  readonly plan: DeepReadonly<T>;
  readonly idempotencyKey: string;
  #submit: (plan: T, options: RequestOptions) => Promise<Result>;
  #value: T;
  constructor(
    plan: T,
    submit: (plan: T, options: RequestOptions) => Promise<Result>,
  ) {
    this.#value = snapshot(plan);
    this.plan = this.#value as DeepReadonly<T>;
    this.idempotencyKey = idempotencyKey();
    this.#submit = submit;
    Object.freeze(this);
  }
  apply(options: RequestOptions = {}): Promise<Result> {
    return this.#submit(this.#value, {
      ...options,
      idempotencyKey: options.idempotencyKey ?? this.idempotencyKey,
    });
  }
}

export async function waitFor<T extends { id: string; status: string }>(
  id: string,
  read: (signal: AbortSignal) => Promise<T>,
  options: WaitOptions<T> = {},
): Promise<T> {
  const timeout = integer(
    options.timeoutMs ?? 20 * 60_000,
    1,
    24 * 60 * 60_000,
    "timeoutMs",
  );
  const poll = integer(
    options.pollIntervalMs ?? 2000,
    10,
    60_000,
    "pollIntervalMs",
  );
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeout);
  const signal = options.signal
    ? AbortSignal.any([options.signal, controller.signal])
    : controller.signal;
  try {
    while (true) {
      signal.throwIfAborted();
      const current = await read(signal);
      if (current.id !== id)
        throw new HakopodError(
          "The API returned another operation's status.",
          "invalid_response",
        );
      if (options.onProgress)
        await abortable(
          Promise.resolve(options.onProgress(snapshot(current))),
          signal,
        );
      if (current.status === "succeeded") return current;
      if (["failed", "cancelled", "superseded"].includes(current.status))
        throw new OperationError(
          id,
          current.status,
          `Operation ${id} ${current.status}. Inspect the operation for the server's error and recovery details.`,
        );
      if (!["queued", "running"].includes(current.status))
        throw new OperationError(
          id,
          current.status,
          `Unrecognized operation status for ${id}. Inspect the server before continuing.`,
        );
      await delay(poll, signal);
    }
  } catch (error) {
    if (signal.aborted)
      throw new WaitError(
        id,
        options.signal?.aborted ? "wait_aborted" : "wait_timeout",
      );
    throw error;
  } finally {
    clearTimeout(timer);
  }
}

export class DeploymentRun {
  readonly id: string;
  readonly applicationId?: string;
  readonly idempotencyKey?: string;
  #context: Context;
  constructor(
    context: Context,
    id: string,
    accepted?: Deployment,
    key?: string,
  ) {
    this.#context = context;
    this.id = required(id, "deployment ID");
    this.applicationId = accepted?.application_id;
    this.idempotencyKey = key;
  }
  get(options: RequestOptions = {}): Promise<Deployment> {
    return this.#context.transport.request("GET", "/deployments/{id}", {
      ...options,
      params: { id: this.id },
    });
  }
  wait(options: WaitOptions<Deployment> = {}): Promise<Deployment> {
    return waitFor(this.id, (signal) => this.get({ signal }), options);
  }
  cancel(
    options: RequestOptions = {},
  ): Promise<{ id: string; status: string }> {
    return this.#context.transport.request("POST", "/deployments/{id}/cancel", {
      ...options,
      params: { id: this.id },
      body: {},
    });
  }
}

export type PlanOptions = RequestOptions & {
  expectedRevision?: number;
  services?: string[];
};
export async function planApplication(
  context: Context,
  spec: ApplicationSpec,
  options: PlanOptions = {},
): Promise<Review<Plan, DeploymentRun>> {
  const selected = scope(context);
  const input = snapshot(spec);
  const services = options.services ? snapshot(options.services) : undefined;
  if (options.expectedRevision !== undefined)
    revision(options.expectedRevision);
  const plan = await context.transport.request<Plan>("POST", "/plan", {
    ...options,
    body: {
      ...selected,
      spec: input,
      ...(options.expectedRevision === undefined
        ? {}
        : { expected_revision: options.expectedRevision }),
      ...(services ? { services } : {}),
    },
  });
  assertRevision(plan, options.expectedRevision);
  if (plan.spec.name !== spec.name)
    throw new HakopodError(
      "The API planned a different application.",
      "invalid_response",
    );
  return new Review(plan, async (reviewed, applyOptions) => {
    if (reviewed.missing_secrets?.length)
      throw new HakopodError(
        "Save the plan's missing secrets, then request a new plan.",
        "missing_secrets",
      );
    const accepted = await context.transport.request<Deployment>(
      "POST",
      "/deployments",
      {
        ...applyOptions,
        body: {
          ...selected,
          spec: reviewed.spec,
          expected_revision: reviewed.expected_revision,
          ...(reviewed.provenance ? { provenance: reviewed.provenance } : {}),
          ...(services ? { services } : {}),
        },
      },
    );
    return new DeploymentRun(
      context,
      accepted.id,
      accepted,
      applyOptions.idempotencyKey,
    );
  });
}

export class ApplicationRef {
  #context: Context;
  #name?: string;
  #id?: string;
  constructor(context: Context, ref: { name: string } | { id: string }) {
    this.#context = context;
    if ("name" in ref) this.#name = required(ref.name, "application name");
    else this.#id = required(ref.id, "application ID");
  }
  async get(options: RequestOptions = {}): Promise<Schema["Application"]> {
    let id = this.#id;
    if (!id) {
      const selected = scope(this.#context);
      let cursor: string | undefined;
      const seen = new Set<string>();
      for (let page = 0; page < 10; page++) {
        const result = await this.#context.transport.request<{
          items: Schema["Application"][];
          next_cursor?: string;
        }>("GET", "/applications", {
          ...options,
          query: { ...selected, cursor },
        });
        const found = result.items.find(
          (item) =>
            item.name === this.#name &&
            item.project === selected.project &&
            item.environment === selected.environment,
        );
        if (found) {
          id = found.id;
          break;
        }
        if (!result.next_cursor)
          throw new APIError(
            "Application not found in the selected scope.",
            "not_found",
            404,
          );
        if (seen.has(result.next_cursor))
          throw new HakopodError(
            "The API repeated a pagination cursor.",
            "invalid_response",
          );
        seen.add(result.next_cursor);
        cursor = result.next_cursor;
      }
      if (!id)
        throw new HakopodError(
          "Application lookup reached its page limit. Use client.application(id) or paginate listApplications explicitly.",
          "listing_limit",
        );
    }
    const result = await this.#context.transport.request<Schema["Application"]>(
      "GET",
      "/applications/{id}",
      { ...options, params: { id } },
    );
    assertScope(this.#context, result);
    if (result.id !== id || (this.#name && result.name !== this.#name))
      throw new HakopodError(
        "The API returned another application.",
        "invalid_response",
      );
    return result;
  }
  async plan(
    input: ApplicationInput,
    options: PlanOptions = {},
  ): Promise<Review<Plan, DeploymentRun>> {
    if (this.#name)
      return planApplication(
        this.#context,
        app({ ...input, name: this.#name }),
        options,
      );
    const current = await this.get(options);
    return planApplication(
      {
        ...this.#context,
        scope: { project: current.project, environment: current.environment },
      },
      app({ ...input, name: current.name }),
      options,
    );
  }
  /** Deploy is an explicit plan-and-apply shortcut. The specification describes the whole application. */
  async deploy(
    input: ApplicationInput,
    options: PlanOptions & {
      review?: (plan: DeepReadonly<Plan>) => boolean | Promise<boolean>;
    } = {},
  ): Promise<DeploymentRun> {
    const reviewed = await this.plan(input, options);
    if (options.review && !(await options.review(reviewed.plan)))
      throw new HakopodError(
        "Deployment was declined before submission.",
        "review_declined",
      );
    return reviewed.apply(options);
  }
  service(name: string): ServiceRef {
    return new ServiceRef(this.#context, this, required(name, "service name"));
  }
  async rollback(
    targetRevision: number,
    options: RequestOptions & { expectedRevision: number },
  ): Promise<DeploymentRun> {
    const current = await this.get(options);
    const key = idempotencyKey(options.idempotencyKey);
    const result = await this.#context.transport.request<Deployment>(
      "POST",
      "/applications/{id}/rollback",
      {
        ...options,
        idempotencyKey: key,
        params: { id: current.id },
        body: {
          revision: revision(targetRevision),
          expected_revision: revision(options.expectedRevision),
        },
      },
    );
    return new DeploymentRun(this.#context, result.id, result, key);
  }
  async delete(
    options: RequestOptions & { confirmName: string; expectedRevision: number },
  ): Promise<{ status: string }> {
    const current = await this.get(options);
    if (options.confirmName !== current.name)
      throw new HakopodError(
        "confirmName must match the application name. Deleted names cannot be reused.",
        "confirmation_required",
      );
    return this.#context.transport.request("DELETE", "/applications/{id}", {
      ...options,
      params: { id: current.id },
      body: {
        confirm_name: options.confirmName,
        expected_revision: revision(options.expectedRevision),
      },
    });
  }
  async setSecret(
    name: string,
    value: string,
    options: RequestOptions = {},
  ): Promise<unknown> {
    const current = await this.get(options);
    return this.#context.transport.request("PUT", "/secrets/{name}", {
      ...options,
      params: { name },
      query: {
        project: current.project,
        environment: current.environment,
        application: current.name,
      },
      body: { value },
    });
  }
}

export class ServiceRef {
  constructor(
    private context: Context,
    private application: ApplicationRef,
    readonly name: string,
  ) {}
  async get(options: RequestOptions = {}): Promise<ServiceSpec> {
    const current = await this.application.get(options);
    if (!Object.hasOwn(current.spec.services, this.name))
      throw new APIError(
        "Service not found in this application.",
        "not_found",
        404,
      );
    return current.spec.services[this.name]!;
  }
  /** Edit one service while preserving the other services from the same revision. */
  async plan(
    changes: Partial<ServiceSpec>,
    options: RequestOptions = {},
  ): Promise<Review<Plan, DeploymentRun>> {
    const current = await this.application.get(options);
    if (!Object.hasOwn(current.spec.services, this.name))
      throw new APIError(
        "Service not found in this application.",
        "not_found",
        404,
      );
    const spec = {
      ...current.spec,
      services: {
        ...current.spec.services,
        [this.name]: { ...current.spec.services[this.name]!, ...changes },
      },
    };
    return planApplication(
      {
        ...this.context,
        scope: { project: current.project, environment: current.environment },
      },
      spec,
      { ...options, expectedRevision: current.revision, services: [this.name] },
    );
  }
  async runtime(
    options: RequestOptions = {},
  ): Promise<Schema["ServiceRuntime"]> {
    const current = await this.application.get(options);
    return this.context.transport.request(
      "GET",
      "/applications/{id}/services/{service}/runtime",
      { ...options, params: { id: current.id, service: this.name } },
    );
  }
  async logs(
    options: RequestOptions & { tail?: number } = {},
  ): Promise<string> {
    const current = await this.application.get(options);
    return this.context.transport.request(
      "GET",
      "/applications/{id}/logs",
      {
        ...options,
        params: { id: current.id },
        query: {
          service: this.name,
          tail: integer(options.tail ?? 100, 1, 1000, "tail"),
          follow: false,
        },
      },
      true,
    );
  }
  async #action(
    action: "scale" | "restart" | "stop" | "resume",
    options: RequestOptions & { expectedRevision: number },
    replicas?: number,
  ): Promise<DeploymentRun> {
    const current = await this.application.get(options);
    const key = idempotencyKey(options.idempotencyKey);
    const accepted = await this.context.transport.request<Deployment>(
      "POST",
      `/applications/{id}/services/{service}/${action}`,
      {
        ...options,
        params: { id: current.id, service: this.name },
        idempotencyKey: key,
        body: {
          expected_revision: revision(options.expectedRevision),
          ...(replicas === undefined ? {} : { replicas }),
        },
      },
    );
    return new DeploymentRun(this.context, accepted.id, accepted, key);
  }
  scale(
    replicas: number,
    options: RequestOptions & { expectedRevision: number },
  ): Promise<DeploymentRun> {
    return this.#action("scale", options, integer(replicas, 1, 20, "replicas"));
  }
  restart(
    options: RequestOptions & { expectedRevision: number },
  ): Promise<DeploymentRun> {
    return this.#action("restart", options);
  }
  stop(
    options: RequestOptions & { expectedRevision: number },
  ): Promise<DeploymentRun> {
    return this.#action("stop", options);
  }
  resume(
    options: RequestOptions & { expectedRevision: number },
  ): Promise<DeploymentRun> {
    return this.#action("resume", options);
  }
}

export class DatabaseRun {
  readonly database: DatabaseRef;
  constructor(
    private context: Context,
    readonly id: string,
    readonly databaseId: string,
    readonly idempotencyKey?: string,
  ) {
    this.database = new DatabaseRef(context, { id: databaseId });
  }
  async get(options: RequestOptions = {}): Promise<DatabaseOperation> {
    const operation = await this.context.transport.request<DatabaseOperation>(
      "GET",
      "/database-operations/{id}",
      { ...options, params: { id: this.id } },
    );
    if (operation.id !== this.id || operation.database_id !== this.databaseId)
      throw new HakopodError(
        "The API returned another database operation.",
        "invalid_response",
      );
    return operation;
  }
  wait(
    options: WaitOptions<DatabaseOperation> = {},
  ): Promise<DatabaseOperation> {
    return waitFor(this.id, (signal) => this.get({ signal }), options);
  }
}

export class DatabasePublicEndpointRun {
  constructor(
    private context: Context,
    readonly id: string,
    readonly databaseId: string,
    readonly endpointId: string,
    readonly idempotencyKey?: string,
  ) {}
  async get(options: RequestOptions = {}): Promise<DatabasePublicEndpointOperation> {
    const operation = await this.context.transport.request<DatabasePublicEndpointOperation>(
      "GET",
      "/database-public-endpoint-operations/{id}",
      { ...options, params: { id: this.id } },
    );
    if (operation.id !== this.id || operation.database_id !== this.databaseId || operation.endpoint_id !== this.endpointId)
      throw new HakopodError("The API returned another database public endpoint operation.", "invalid_response");
    return operation;
  }
  wait(options: WaitOptions<DatabasePublicEndpointOperation> = {}): Promise<DatabasePublicEndpointOperation> {
    return waitFor(this.id, (signal) => this.get({ signal }), options);
  }
}

export class DatabaseRef {
  #context: Context;
  #name?: string;
  #id?: string;
  constructor(context: Context, ref: { name: string } | { id: string }) {
    this.#context = context;
    if ("name" in ref) this.#name = required(ref.name, "database name");
    else this.#id = required(ref.id, "database ID");
  }
  async get(options: RequestOptions = {}): Promise<ManagedDatabase> {
    let id = this.#id;
    if (!id) {
      const result = await this.#context.transport.request<{
        items: ManagedDatabase[];
      }>("GET", "/databases", { ...options, query: scope(this.#context) });
      const found = result.items.find((item) => item.spec.name === this.#name);
      if (!found) {
        if (result.items.length >= 64)
          throw new HakopodError(
            "Database lookup reached the server's listing limit. Use client.database(id).",
            "listing_limit",
          );
        throw new APIError(
          "Database not found in the selected scope.",
          "not_found",
          404,
        );
      }
      id = found.id;
    }
    const result = await this.#context.transport.request<ManagedDatabase>(
      "GET",
      "/databases/{id}",
      { ...options, params: { id } },
    );
    assertScope(this.#context, result);
    if (result.id !== id || (this.#name && result.spec.name !== this.#name))
      throw new HakopodError(
        "The API returned another database.",
        "invalid_response",
      );
    return result;
  }
  async create(
    input: DatabaseInput,
    options: RequestOptions = {},
  ): Promise<DatabaseRun> {
    if (!this.#name)
      throw new HakopodError(
        "Create a database by name with client.db(name).create(...).",
        "invalid_option",
      );
    const key = idempotencyKey(options.idempotencyKey);
    const accepted = await this.#context.transport.request<DatabaseOperation>(
      "POST",
      "/databases",
      {
        ...options,
        idempotencyKey: key,
        body: {
          ...scope(this.#context),
          spec: db({ ...input, name: this.#name }),
        },
      },
    );
    return new DatabaseRun(
      this.#context,
      accepted.id,
      accepted.database_id,
      key,
    );
  }
  /** Public CA material only; private server and issuer keys never leave the cluster. */
  async trust(
    options: RequestOptions = {},
  ): Promise<Schema["DatabasePublicTrust"]> {
    const current = await this.get(options);
    return this.#context.transport.request("GET", "/databases/{id}/trust", {
      ...options,
      params: { id: current.id },
    });
  }
  async metrics(
    range: "1h" | "6h" | "24h" = "1h",
    options: RequestOptions = {},
  ): Promise<Schema["DatabaseMetricHistory"]> {
    const current = await this.get(options);
    return this.#context.transport.request("GET", "/databases/{id}/metrics", {
      ...options,
      params: { id: current.id },
      query: { range },
    });
  }
  async connections(
    options: RequestOptions = {},
  ): Promise<Schema["DatabaseConnections"]> {
    const current = await this.get(options);
    return this.#context.transport.request(
      "GET",
      "/databases/{id}/connections",
      {
        ...options,
        params: { id: current.id },
      },
    );
  }
  async publicEndpoints(options: RequestOptions = {}): Promise<Schema["DatabasePublicEndpoint"][]> {
    const current = await this.get(options);
    const result = await this.#context.transport.request<{ items: Schema["DatabasePublicEndpoint"][] }>(
      "GET",
      "/databases/{id}/public-endpoints",
      { ...options, params: { id: current.id } },
    );
    if (result.items.length > 4 || result.items.some((endpoint) => endpoint.database_id !== current.id))
      throw new HakopodError("The API returned invalid database public endpoint inventory.", "invalid_response");
    return result.items;
  }
  async publicEndpointCapabilities(
    options: RequestOptions = {},
  ): Promise<DatabasePublicEndpointCapabilities> {
    const current = await this.get(options);
    const capabilities = await this.#context.transport.request<DatabasePublicEndpointCapabilities>(
      "GET",
      "/databases/{id}/public-endpoint-capabilities",
      { ...options, params: { id: current.id } },
    );
    if (capabilities.engine !== current.spec.engine || capabilities.routes.length > 4)
      throw new HakopodError(
        "The API returned invalid database public endpoint capabilities.",
        "invalid_response",
      );
    return capabilities;
  }
  /** Review an available server-approved listener. Applying the review closes the old route before publishing the new one. */
  async publicEndpointPlan(
    input: DatabasePublicEndpointSpec,
    options: RequestOptions = {},
  ): Promise<Review<Schema["DatabasePublicEndpointPlan"], DatabasePublicEndpointRun>> {
    const current = await this.get(options);
    const capabilities = await this.#context.transport.request<DatabasePublicEndpointCapabilities>(
      "GET",
      "/databases/{id}/public-endpoint-capabilities",
      { ...options, params: { id: current.id } },
    );
    const requested = snapshot(input);
    const route = capabilities.routes.find((candidate) => candidate.purpose === requested.purpose);
    if (capabilities.engine !== current.spec.engine)
      throw new HakopodError("The API returned capabilities for another database engine.", "invalid_response");
    if (!capabilities.available)
      throw new HakopodError(
        capabilities.unavailable_reason || "Public database endpoints are not available for this database.",
        "invalid_option",
      );
    if (!route)
      throw new HakopodError("The selected public endpoint route is not available for this database.", "invalid_option");
    const result = await this.#context.transport.request<Schema["DatabasePublicEndpointPlan"]>(
      "POST",
      "/databases/{id}/public-endpoint-plan",
      { ...options, params: { id: current.id }, body: requested },
    );
    if (result.plan.database_id !== current.id || result.plan.project !== current.project || result.plan.environment !== current.environment || result.plan.database_revision !== current.revision || result.plan.spec.purpose !== requested.purpose || result.plan.spec.max_connections !== requested.max_connections)
      throw new HakopodError("The API reviewed another database public endpoint.", "invalid_response");
    const reviewedRoute = result.plan.route;
    if (!reviewedRoute || reviewedRoute.purpose !== route.purpose || reviewedRoute.protocol !== route.protocol || reviewedRoute.routing !== route.routing || reviewedRoute.read_only !== route.read_only || reviewedRoute.pooled !== route.pooled)
      throw new HakopodError("The API reviewed another public endpoint route.", "invalid_response");
    return new Review(result, async (reviewed, applyOptions) => {
      const key = idempotencyKey(applyOptions.idempotencyKey);
      const accepted = await this.#context.transport.request<DatabasePublicEndpointOperation>(
        "POST",
        "/databases/{id}/public-endpoints",
        {
          ...applyOptions,
          idempotencyKey: key,
          params: { id: reviewed.plan.database_id },
          body: {
            review_id: reviewed.id,
            expected_database_revision: reviewed.plan.database_revision,
            expected_endpoint_revision: reviewed.plan.endpoint_revision,
          },
        },
      );
      if (accepted.database_id !== reviewed.plan.database_id || accepted.endpoint_id !== reviewed.plan.endpoint_id || accepted.kind !== "publish" || accepted.revision !== reviewed.plan.endpoint_revision + 1)
        throw new HakopodError("The API accepted another database public endpoint operation.", "invalid_response");
      return new DatabasePublicEndpointRun(this.#context, accepted.id, accepted.database_id, accepted.endpoint_id, key);
    });
  }
  async revokePublicEndpoint(
    endpointId: string,
    expectedEndpointRevision: number,
    options: RequestOptions = {},
  ): Promise<DatabasePublicEndpointRun> {
    const current = await this.get(options);
    const id = required(endpointId, "database public endpoint ID");
    const expected = integer(expectedEndpointRevision, 1, Number.MAX_SAFE_INTEGER, "expectedEndpointRevision");
    const key = idempotencyKey(options.idempotencyKey);
    const accepted = await this.#context.transport.request<DatabasePublicEndpointOperation>(
      "DELETE",
      "/databases/{id}/public-endpoints/{endpoint}",
      { ...options, idempotencyKey: key, params: { id: current.id, endpoint: id }, body: { expected_endpoint_revision: expected } },
    );
    if (accepted.database_id !== current.id || accepted.endpoint_id !== id || accepted.kind !== "revoke" || accepted.revision !== expected + 1)
      throw new HakopodError("The API accepted another database public endpoint operation.", "invalid_response");
    return new DatabasePublicEndpointRun(this.#context, accepted.id, accepted.database_id, accepted.endpoint_id, key);
  }
  async credentials(
    options: RequestOptions = {},
  ): Promise<{ username: string; password: string; database: string }> {
    const current = await this.get(options);
    return this.#context.transport.request(
      "POST",
      "/databases/{id}/credentials",
      { ...options, params: { id: current.id }, body: {} },
    );
  }
  async backup(
    destinationId: string,
    options: RequestOptions = {},
  ): Promise<BackupRun> {
    const current = await this.get(options);
    const key = idempotencyKey(options.idempotencyKey);
    const accepted = await this.#context.transport.request<Schema["BackupJob"]>(
      "POST",
      "/backups",
      {
        ...options,
        idempotencyKey: key,
        body: {
          destination_id: required(destinationId, "backup destination ID"),
          source: {
            kind: "managed_database",
            engine: current.spec.engine,
            managed_database_id: current.id,
          },
        },
      },
    );
    return new BackupRun(this.#context, accepted.id, key);
  }
  async schedule(
    input: {
      name: string;
      destinationId: string;
      frequency: "hourly" | "daily";
      retentionCount: number;
    },
    options: RequestOptions = {},
  ): Promise<Schema["BackupSchedule"]> {
    const current = await this.get(options);
    if (!["hourly", "daily"].includes(input.frequency))
      throw new HakopodError(
        "Choose hourly or daily backups.",
        "invalid_option",
      );
    return this.#context.transport.request("POST", "/backup-schedules", {
      ...options,
      body: {
        name: required(input.name, "schedule name"),
        destination_id: required(input.destinationId, "backup destination ID"),
        interval_hours: input.frequency === "hourly" ? 1 : 24,
        retention_count: integer(
          input.retentionCount,
          1,
          100,
          "retentionCount",
        ),
        enabled: true,
        expected_revision: 0,
        source: {
          kind: "managed_database",
          engine: current.spec.engine,
          managed_database_id: current.id,
        },
      },
    });
  }
  /** Restore into this separate, empty database. Existing source data stays available. */
  async restorePlan(
    artifactId: string,
    options: RequestOptions = {},
  ): Promise<Review<Schema["BackupRestorePlan"], BackupRun>> {
    const current = await this.get(options);
    const plan = await this.#context.transport.request<
      Schema["BackupRestorePlan"]
    >("POST", "/databases/{id}/restore-plan", {
      ...options,
      params: { id: current.id },
      body: { artifact_id: required(artifactId, "backup artifact ID") },
    });
    if (
      plan.artifact_id !== artifactId ||
      plan.target.managed_database_id !== current.id
    )
      throw new HakopodError(
        "The API planned a different recovery target.",
        "invalid_response",
      );
    return new Review(plan, async (reviewed, applyOptions) => {
      const accepted = await this.#context.transport.request<
        Schema["BackupJob"]
      >("POST", "/backup-artifacts/{id}/restore", {
        ...applyOptions,
        params: { id: reviewed.artifact_id },
        body: { plan_id: reviewed.id, confirmation: reviewed.confirmation },
      });
      return new BackupRun(
        this.#context,
        accepted.id,
        applyOptions.idempotencyKey,
      );
    });
  }
  /** Call only after checking the recovered data; this records your attestation. */
  async inspectRecovery(
    input: {
      jobId: string;
      confirmName: string;
      expectedRevision: number;
      inspected: true;
    },
    options: RequestOptions = {},
  ): Promise<ManagedDatabase> {
    const current = await this.get(options);
    if (input.confirmName !== current.spec.name || input.inspected !== true)
      throw new HakopodError(
        "Confirm the database name after inspecting its recovered data.",
        "confirmation_required",
      );
    return this.#context.transport.request("POST", "/databases/{id}/inspect", {
      ...options,
      params: { id: current.id },
      body: {
        job_id: input.jobId,
        confirm_name: input.confirmName,
        expected_revision: revision(input.expectedRevision),
        inspected: true,
      },
    });
  }
  /** Review the saved connection replacement and application redeployment. */
  async connectionPlan(
    input: {
      applicationId: string;
      service: string;
      variable: string;
      endpoint?:
        | "read_write"
        | "read_only"
        | "cluster"
        | "pooled_read_write"
        | "pooled_read_only";
      clusterAware?: boolean;
    },
    options: RequestOptions = {},
  ): Promise<Review<Schema["DatabaseConnectionPlan"], DeploymentRun>> {
    const current = await this.get(options);
    const binding = await this.binding({ ...options, ...input });
    const plan = await this.#context.transport.request<
      Schema["DatabaseConnectionPlan"]
    >("POST", "/databases/{id}/connection-plan", {
      ...options,
      params: { id: current.id },
      body: {
        application_id: required(input.applicationId, "application ID"),
        service: required(input.service, "service"),
        variable: required(input.variable, "variable"),
        endpoint: binding.endpoint,
        cluster_aware: input.clusterAware ?? false,
      },
    });
    if (
      plan.database_id !== current.id ||
      plan.application_id !== input.applicationId ||
      plan.service !== input.service ||
      plan.variable !== input.variable
    )
      throw new HakopodError(
        "The API planned a different connection replacement.",
        "invalid_response",
      );
    return new Review(plan, async (reviewed, applyOptions) => {
      const accepted = await this.#context.transport.request<Deployment>(
        "POST",
        "/databases/{id}/connect",
        {
          ...applyOptions,
          params: { id: reviewed.database_id },
          body: {
            review_id: reviewed.id,
            confirm_application: reviewed.application_name,
          },
        },
      );
      return new DeploymentRun(
        this.#context,
        accepted.id,
        accepted,
        applyOptions.idempotencyKey,
      );
    });
  }
  /** Return a server-resolved binding, never a plaintext password or connection URL. */
  async binding(
    options: RequestOptions & {
      endpoint?:
        | "read_write"
        | "read_only"
        | "cluster"
        | "pooled_read_write"
        | "pooled_read_only";
      clusterAware?: boolean;
    } = {},
  ): Promise<Schema["ServiceBinding"]> {
    const current = await this.get(options);
    const discovery = current.spec.engine === "mongodb" || ["redis", "clickhouse"].includes(current.spec.engine) && current.spec.mode === "cluster";
    if (
      discovery &&
      options.clusterAware !== true
    )
      throw new HakopodError(
        current.spec.engine === "mongodb" ? "MongoDB requires replica-set discovery. Set clusterAware only after configuring a compatible driver." : current.spec.engine === "clickhouse" ? "ClickHouse cluster endpoints balance connections. Set clusterAware after configuring Distributed tables or explicit shard routing." : "Redis Cluster requires a cluster-aware client. Set clusterAware only after configuring one.",
        "cluster_client_required",
      );
    const endpoint =
      options.endpoint ??
      (discovery
        ? "cluster"
        : "read_write");
    const routes =
      current.spec.engine === "oracle" ? ["read_write"] : current.spec.engine === "mongodb" ? ["cluster"] : ["redis", "clickhouse"].includes(current.spec.engine)
        ? current.spec.mode === "cluster"
          ? ["cluster"]
          : ["read_write"]
        : [
            "read_write",
            ...(current.spec.replicas > 0 ? ["read_only"] : []),
            ...(current.spec.engine === "postgresql" && current.spec.pooling
              ? [
                  "pooled_read_write",
                  ...(current.spec.pooling.read_only
                    ? ["pooled_read_only"]
                    : []),
                ]
              : []),
          ];
    if (
      !routes.includes(endpoint) ||
      (options.clusterAware && !discovery)
    )
      throw new HakopodError(
        "Choose a connection route supported by this database configuration.",
        "invalid_binding",
      );
    return {
      managed_database: current.id,
      protocol:
        current.spec.engine === "postgresql" ? "postgres" : current.spec.engine === "vitess" ? "mysql" : current.spec.engine,
      endpoint,
      ...(options.clusterAware === undefined
        ? {}
        : { cluster_aware: options.clusterAware }),
    };
  }
  /** Review a graceful role change. Availability still depends on the gated Enterprise runtime. */
  async switchoverPlan(
    targetMember: string,
    options: RequestOptions = {},
  ): Promise<DeepReadonly<Schema["DatabaseOracleSwitchoverPlan"]>> {
    const target = required(targetMember, "physical standby member");
    if (target.length > 253)
      throw new HakopodError("The standby member name is too long.", "invalid_option");
    const current = await this.get(options);
    this.#assertOracleCluster(current);
    const result = await this.#context.transport.request<Schema["DatabaseOracleSwitchoverPlan"]>(
      "POST", "/databases/{id}/switchover-plan", {
        ...options,
        params: { id: current.id },
        body: { target_member: target },
      },
    );
    if (result.plan.database_id !== current.id || result.plan.project !== current.project ||
        result.plan.environment !== current.environment || result.plan.target !== target)
      throw new HakopodError("The API reviewed another database or standby member.", "invalid_response");
    if (result.plan.revision !== current.revision)
      throw new APIError("The database changed while planning. Review it again.", "revision_conflict", 409);
    return snapshot(result);
  }
  /** Submit the exact server review after reading its warnings. Existing connections close. */
  async switchover(
    input: { reviewId: string; expectedRevision: number; confirmName: string },
    options: RequestOptions = {},
  ): Promise<DatabaseRun> {
    const reviewId = required(input.reviewId, "switchover review ID");
    const expectedRevision = integer(input.expectedRevision, 1, Number.MAX_SAFE_INTEGER, "expectedRevision");
    const current = await this.get(options);
    this.#assertOracleCluster(current);
    this.#assertSwitchoverConfirmation(current, input.confirmName, expectedRevision);
    const key = idempotencyKey(options.idempotencyKey);
    const accepted = await this.#context.transport.request<DatabaseOperation>(
      "POST", "/databases/{id}/switchover", {
        ...options,
        idempotencyKey: key,
        params: { id: current.id },
        body: { review_id: reviewId, expected_revision: expectedRevision, confirm_name: input.confirmName },
      },
    );
    this.#assertSwitchoverOperation(accepted, current, expectedRevision);
    return new DatabaseRun(this.#context, accepted.id, accepted.database_id, key);
  }
  /** Resume the same approved operation after a worker timeout; this cannot choose a new target. */
  async retrySwitchover(
    input: { operationId: string; expectedRevision: number; confirmName: string },
    options: RequestOptions = {},
  ): Promise<DatabaseRun> {
    const operationId = required(input.operationId, "switchover operation ID");
    const expectedRevision = integer(input.expectedRevision, 1, Number.MAX_SAFE_INTEGER, "expectedRevision");
    const current = await this.get(options);
    this.#assertOracleCluster(current);
    this.#assertSwitchoverConfirmation(current, input.confirmName, expectedRevision);
    const accepted = await this.#context.transport.request<DatabaseOperation>(
      "POST", "/databases/{id}/switchover-retry", {
        ...options,
        params: { id: current.id },
        body: { operation_id: operationId, expected_revision: expectedRevision, confirm_name: input.confirmName },
      },
    );
    this.#assertSwitchoverOperation(accepted, current, expectedRevision);
    if (accepted.id !== operationId)
      throw new HakopodError("The API resumed another switchover operation.", "invalid_response");
    return new DatabaseRun(this.#context, accepted.id, accepted.database_id);
  }
  #assertOracleCluster(current: ManagedDatabase): void {
    if (current.spec.engine !== "oracle" || current.spec.oracle?.edition !== "enterprise" || current.spec.mode !== "cluster")
      throw new HakopodError("Switchover requires an Oracle Enterprise Data Guard cluster.", "invalid_option");
  }
  #assertSwitchoverConfirmation(current: ManagedDatabase, confirmName: string, expectedRevision: number): void {
    if (confirmName !== current.spec.name)
      throw new HakopodError("confirmName must match the database name. Existing connections will close.", "confirmation_required");
    if (expectedRevision !== current.revision)
      throw new APIError("The database changed. Review its current topology again.", "revision_conflict", 409);
  }
  #assertSwitchoverOperation(accepted: DatabaseOperation, current: ManagedDatabase, expectedRevision: number): void {
    if (accepted.database_id !== current.id || accepted.revision !== expectedRevision || accepted.kind !== "switchover" ||
        accepted.switchover?.database_id !== current.id || accepted.switchover.revision !== expectedRevision ||
        accepted.switchover.project !== current.project || accepted.switchover.environment !== current.environment)
      throw new HakopodError("The API returned another switchover operation.", "invalid_response");
  }
  async resizePlan(
    changes: Partial<
      Pick<
        DatabaseInput,
        "cpu" | "memory" | "storageGiB" | "replicas" | "shards"
      >
    >,
    options: RequestOptions = {},
  ): Promise<
    Review<{ id: string; plan: Schema["DatabaseResizePlan"] }, DatabaseRun>
  > {
    const current = await this.get(options);
    const proposed = {
      ...current.spec,
      ...Object.fromEntries(
        Object.entries(changes).filter(
          ([key, value]) => key !== "storageGiB" && value !== undefined,
        ),
      ),
      ...(changes.storageGiB === undefined
        ? {}
        : { storage_gib: changes.storageGiB }),
    };
    const result = await this.#context.transport.request<{
      id: string;
      plan: Schema["DatabaseResizePlan"];
    }>("POST", "/databases/{id}/resize-plan", {
      ...options,
      params: { id: current.id },
      body: { spec: proposed },
    });
    if (result.plan.expected_revision !== current.revision)
      throw new APIError(
        "The database changed while planning. Review it again.",
        "revision_conflict",
        409,
      );
    return new Review(result, async (reviewed, applyOptions) => {
      if (reviewed.plan.blocked_reasons.length)
        throw new HakopodError(
          "The database resize plan is blocked. Resolve its blocked_reasons and request a fresh plan.",
          "plan_blocked",
        );
      const accepted = await this.#context.transport.request<DatabaseOperation>(
        "POST",
        "/databases/{id}/resize",
        {
          ...applyOptions,
          params: { id: current.id },
          body: {
            review_id: reviewed.id,
            expected_revision: reviewed.plan.expected_revision,
            spec: reviewed.plan.proposed,
          },
        },
      );
      return new DatabaseRun(
        this.#context,
        accepted.id,
        accepted.database_id,
        applyOptions.idempotencyKey,
      );
    });
  }
  async delete(
    options: RequestOptions & { expectedRevision: number; confirmName: string },
  ): Promise<DatabaseRun> {
    const current = await this.get(options);
    if (options.confirmName !== current.spec.name)
      throw new HakopodError(
        "confirmName must match the database name. Deletion removes its data.",
        "confirmation_required",
      );
    const key = idempotencyKey(options.idempotencyKey);
    const accepted = await this.#context.transport.request<DatabaseOperation>(
      "DELETE",
      "/databases/{id}",
      {
        ...options,
        params: { id: current.id },
        idempotencyKey: key,
        body: {
          expected_revision: revision(options.expectedRevision),
          confirm_name: options.confirmName,
        },
      },
    );
    return new DatabaseRun(
      this.#context,
      accepted.id,
      accepted.database_id,
      key,
    );
  }
}

export class BackupRun {
  constructor(
    private context: Context,
    readonly id: string,
    readonly idempotencyKey?: string,
  ) {
    required(id, "backup job ID");
  }
  get(options: RequestOptions = {}): Promise<Schema["BackupJob"]> {
    return this.context.transport.request("GET", "/backups/{id}", {
      ...options,
      params: { id: this.id },
    });
  }
  wait(
    options: WaitOptions<Schema["BackupJob"]> = {},
  ): Promise<Schema["BackupJob"]> {
    return waitFor(this.id, (signal) => this.get({ signal }), options);
  }
}

export class ManagedPlatformRun {
  constructor(private context: Context, readonly id: string) { required(id, "managed platform operation ID"); }
  get(options: RequestOptions = {}): Promise<Schema["ManagedPlatformOperation"]> {
    return this.context.transport.request("GET", "/managed-platform-operations/{id}", { ...options, params: { id: this.id } });
  }
  wait(options: WaitOptions<Schema["ManagedPlatformOperation"]> = {}): Promise<Schema["ManagedPlatformOperation"]> {
    return waitFor(this.id, (signal) => this.get({ signal }), options);
  }
}

export class ManagedPlatformRef {
  constructor(private context: Context, readonly id: string) { required(id, "managed platform ID"); }
  get(options: RequestOptions = {}): Promise<Schema["ManagedPlatform"]> {
    return this.context.transport.request("GET", "/managed-platforms/{id}", { ...options, params: { id: this.id } });
  }
  operations(options: RequestOptions = {}) {
    return this.context.transport.request("GET", "/managed-platforms/{id}/operations", { ...options, params: { id: this.id } });
  }
  async review(spec: Schema["ManagedPlatformSpec"], expectedRevision: number, options: RequestOptions = {}) {
    const current = await this.get(options);
    assertScope(this.context, current);
    return this.context.transport.request("POST", "/managed-platforms/reviews", { ...options, body: { id: this.id, ...scope(this.context), expected_revision: revision(expectedRevision), kind: "update", confirm_name: current.spec.name, spec } });
  }
  async reviewDelete(confirmName: string, options: RequestOptions = {}) {
    const current = await this.get(options);
    assertScope(this.context, current);
    if (required(confirmName, "platform name") !== current.spec.name) {
      throw new Error("platform name must exactly match the current resource");
    }
    return this.context.transport.request("POST", "/managed-platforms/reviews", {
      ...options,
      body: { id: this.id, ...scope(this.context), expected_revision: revision(current.revision),
        kind: "delete", confirm_name: current.spec.name, spec: current.spec },
    });
  }
}

export class NetworkRef {
  constructor(
    private context: Context,
    readonly name: string,
  ) {
    required(name, "network name");
  }
  async get(
    options: RequestOptions = {},
  ): Promise<Schema["VirtualNetworkDetail"]> {
    return this.context.transport.request("GET", "/virtual-networks/{name}", {
      ...options,
      params: { name: this.name },
      query: scope(this.context),
    });
  }
  async plan(
    input: NetworkInput,
    options: RequestOptions = {},
  ): Promise<
    Review<Schema["VirtualNetworkPlan"], Schema["VirtualNetworkSummary"]>
  > {
    const selected = scope(this.context);
    const plan = await this.context.transport.request<
      Schema["VirtualNetworkPlan"]
    >("POST", "/virtual-networks/plan", {
      ...options,
      body: { ...selected, spec: network({ ...input, name: this.name }) },
    });
    if (plan.spec.name !== this.name)
      throw new HakopodError(
        "The API planned another network.",
        "invalid_response",
      );
    return new Review(plan, (reviewed, applyOptions) =>
      this.context.transport.request(
        reviewed.expected_revision === 0 ? "POST" : "PUT",
        reviewed.expected_revision === 0
          ? "/virtual-networks"
          : "/virtual-networks/{name}",
        {
          signal: applyOptions.signal,
          ...(reviewed.expected_revision === 0
            ? {}
            : { params: { name: this.name } }),
          body: {
            ...selected,
            spec: reviewed.spec,
            expected_id: reviewed.expected_id,
            expected_revision: reviewed.expected_revision,
          },
        },
      ),
    );
  }
  delete(
    options: RequestOptions & {
      expectedId: string;
      expectedRevision: number;
      confirmName: string;
    },
  ): Promise<{ deleted: boolean }> {
    if (options.confirmName !== this.name)
      throw new HakopodError(
        "confirmName must match the network name.",
        "confirmation_required",
      );
    return this.context.transport.request(
      "DELETE",
      "/virtual-networks/{name}",
      {
        ...options,
        params: { name: this.name },
        body: {
          ...scope(this.context),
          expected_id: options.expectedId,
          expected_revision: revision(options.expectedRevision),
          confirmation: options.confirmName,
        },
      },
    );
  }
}
