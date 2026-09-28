import type { components, paths } from "./api.generated.js";

export type Schema = components["schemas"];
export type ApplicationSpec = Schema["Spec"];
export type ServiceSpec = Schema["Service"];
export type DatabaseSpec = Schema["ManagedDatabaseSpec"];
export type NetworkSpec = Schema["VirtualNetworkSpec"];
export type Deployment = Schema["Deployment"];
export type ManagedDatabase = Schema["ManagedDatabase"];
export type DatabaseOperation = Schema["DatabaseOperation"];
export type Plan = Schema["Plan"];
export type Scope = { project: string; environment: string };
export type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
type Lower<M extends Method> = Lowercase<M>;
export type PathFor<M extends Method> = {
  [P in keyof paths]: Lower<M> extends keyof paths[P]
    ? NonNullable<paths[P][Lower<M>]> extends never
      ? never
      : P
    : never;
}[keyof paths];
export type Operation<M extends Method, P extends PathFor<M>> =
  Lower<M> extends keyof paths[P] ? NonNullable<paths[P][Lower<M>]> : never;
type Parameter<O, K extends string> = O extends { parameters: infer P }
  ? K extends keyof P
    ? NonNullable<P[K]>
    : never
  : never;
type JSONContent<T> = T extends { content: { "application/json": infer D } }
  ? D
  : never;
export type ResponseFor<O> = O extends { responses: infer R }
  ? JSONContent<R[keyof R & (200 | 201 | 202 | 204)]>
  : never;
export type BodyFor<O> = O extends { requestBody: infer B }
  ? JSONContent<B>
  : never;
type Field<K extends string, T> = [T] extends [never]
  ? { [N in K]?: never }
  : {} extends T
    ? { [N in K]?: T }
    : { [N in K]: T };
export type RequestOptions = { signal?: AbortSignal; idempotencyKey?: string };
export type OptionsFor<O> = RequestOptions &
  Field<"params", Parameter<O, "path">> &
  Field<"query", Parameter<O, "query">> &
  Field<"body", BodyFor<O>>;
export type RequestArgs<O> =
  {} extends OptionsFor<O>
    ? [options?: OptionsFor<O>]
    : [options: OptionsFor<O>];

export interface WaitOptions<T> {
  signal?: AbortSignal;
  timeoutMs?: number;
  pollIntervalMs?: number;
  /** Receives server observations, including terminal failures. No invented percentages. */
  onProgress?: (snapshot: Readonly<T>) => void | Promise<void>;
}
