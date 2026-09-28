import { APIError, HakopodError, TransportError } from "./errors.js";
import type { Method, RequestOptions } from "./types.js";

export interface ClientOptions {
  apiUrl?: string;
  apiKey?: string;
  /** Cloud workspace ID. Never inferred from a browser cookie or another workspace. */
  workspace?: string;
  project?: string;
  environment?: string;
  timeoutMs?: number;
  /** Retries apply only to GET requests. */
  maxRetries?: number;
  maxConcurrency?: number;
  maxResponseBytes?: number;
  fetch?: typeof globalThis.fetch;
}
export type WireOptions = RequestOptions & {
  params?: object;
  query?: object;
  body?: unknown;
};

export function integer(
  value: number,
  min: number,
  max: number,
  name: string,
): number {
  if (!Number.isSafeInteger(value) || value < min || value > max)
    throw new HakopodError(
      `${name} must be an integer between ${min} and ${max}.`,
      "invalid_option",
    );
  return value;
}
export function required(value: string | undefined, name: string): string {
  if (
    typeof value !== "string" ||
    !value.trim() ||
    value !== value.trim() ||
    /[\r\n\0]/.test(value)
  )
    throw new HakopodError(`Provide a nonempty ${name}.`, "invalid_option");
  return value;
}
export function idempotencyKey(
  value: string = globalThis.crypto.randomUUID(),
): string {
  if (!/^[A-Za-z0-9_.:-]{8,128}$/.test(value))
    throw new HakopodError(
      "Idempotency keys need 8–128 letters, digits, dots, colons, underscores or hyphens.",
      "invalid_option",
    );
  return value;
}
export function environmentVariables(): Record<string, string | undefined> {
  return (
    (
      globalThis as typeof globalThis & {
        process?: { env?: Record<string, string | undefined> };
      }
    ).process?.env ?? {}
  );
}
function baseURL(raw: string): string {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new HakopodError(
      "Provide an absolute Hakopod API URL.",
      "invalid_url",
    );
  }
  const loopback =
    url.hostname === "localhost" ||
    url.hostname === "[::1]" ||
    /^127\.\d+\.\d+\.\d+$/.test(url.hostname);
  if (
    (url.protocol !== "https:" && !(url.protocol === "http:" && loopback)) ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  )
    throw new HakopodError(
      "Use HTTPS without URL credentials, query or fragment. HTTP is allowed only on loopback.",
      "invalid_url",
    );
  if (/%|\\/.test(url.pathname))
    throw new HakopodError(
      "The API URL path must not be encoded.",
      "invalid_url",
    );
  const base = url.href.replace(/\/+$/, "");
  return base.endsWith("/api/v1") ? base : `${base}/api/v1`;
}
function pathWithParams(path: string, params: object = {}): string {
  if (!path.startsWith("/") || path.startsWith("//") || /[?%#\\]/.test(path))
    throw new HakopodError(
      "Use a relative API route template, without a query string.",
      "invalid_path",
    );
  const values = params as Record<string, unknown>;
  const used = new Set<string>();
  const resolved = path.replace(/\{([^}]+)\}/g, (_, name: string) => {
    const value = values[name];
    if (
      typeof value !== "string" ||
      !value ||
      value.length > 256 ||
      /[/\\?#%\0\r\n]/.test(value) ||
      value === "." ||
      value === ".."
    )
      throw new HakopodError(
        `Provide a valid ${name} path parameter.`,
        "invalid_path",
      );
    used.add(name);
    // Colons are safe inside a path segment and are supported in recovery keys.
    // Keep them literal so the dashboard's explicit route allowlist sees them.
    return encodeURIComponent(value).replaceAll("%3A", ":");
  });
  if (
    Object.keys(values).some((key) => !used.has(key)) ||
    /[{}]/.test(resolved) ||
    resolved.split("/").some((p) => p === "." || p === "..")
  )
    throw new HakopodError("Unexpected path parameters.", "invalid_path");
  return resolved;
}
export async function abortable<T>(
  work: Promise<T>,
  signal: AbortSignal,
): Promise<T> {
  signal.throwIfAborted();
  let stop: (() => void) | undefined;
  try {
    return await Promise.race([
      work,
      new Promise<never>((_, reject) => {
        stop = () => reject(signal.reason);
        signal.addEventListener("abort", stop, { once: true });
      }),
    ]);
  } finally {
    if (stop) signal.removeEventListener("abort", stop);
  }
}
export async function delay(ms: number, signal: AbortSignal): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await abortable(
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, ms);
      }),
      signal,
    );
  } finally {
    clearTimeout(timer);
  }
}

class Gate {
  #active = 0;
  #queue: Array<{ grant: () => void }> = [];
  constructor(readonly max: number) {}
  async acquire(signal: AbortSignal): Promise<() => void> {
    signal.throwIfAborted();
    if (this.#active >= this.max) {
      if (this.#queue.length >= 64)
        throw new HakopodError(
          "The client's request queue is full.",
          "client_busy",
        );
      let entry: { grant: () => void };
      const pending = new Promise<void>((resolve) => {
        entry = { grant: resolve };
        this.#queue.push(entry);
      });
      try {
        await abortable(pending, signal);
      } catch (error) {
        const index = this.#queue.indexOf(entry!);
        if (index >= 0) this.#queue.splice(index, 1);
        else this.release();
        throw error;
      }
    } else this.#active++;
    return () => this.release();
  }
  release(): void {
    const next = this.#queue.shift();
    if (next) next.grant();
    else this.#active--;
  }
}

export class Transport {
  #base: string;
  #key: string;
  #workspace?: string;
  #fetch: typeof globalThis.fetch;
  #timeout: number;
  #retries: number;
  #bytes: number;
  #gate: Gate;
  constructor(options: ClientOptions) {
    if (typeof window !== "undefined" && typeof window.document !== "undefined")
      throw new HakopodError(
        "Use the Hakopod SDK on your server or in CI. Never put an API key in a browser bundle.",
        "server_only",
      );
    const env = environmentVariables();
    this.#base = baseURL(
      required(
        options.apiUrl ?? env.HAKOPOD_API_URL,
        "apiUrl or HAKOPOD_API_URL",
      ),
    );
    this.#key = required(
      options.apiKey ?? env.HAKOPOD_API_KEY,
      "apiKey or HAKOPOD_API_KEY",
    );
    if (this.#key.length > 8192 || /\s/.test(this.#key))
      throw new HakopodError(
        "The API key has an invalid format.",
        "invalid_option",
      );
    this.#workspace = options.workspace ?? env.HAKOPOD_WORKSPACE;
    if (
      this.#workspace !== undefined &&
      !/^[a-f0-9]{32}$/.test(this.#workspace)
    )
      throw new HakopodError(
        "workspace must be a 32-character Cloud workspace ID.",
        "invalid_option",
      );
    this.#fetch = options.fetch ?? globalThis.fetch;
    this.#timeout = integer(
      options.timeoutMs ?? 30_000,
      1,
      300_000,
      "timeoutMs",
    );
    this.#retries = integer(options.maxRetries ?? 2, 0, 5, "maxRetries");
    this.#bytes = integer(
      options.maxResponseBytes ?? 8 * 1024 * 1024,
      1024,
      16 * 1024 * 1024,
      "maxResponseBytes",
    );
    this.#gate = new Gate(
      integer(options.maxConcurrency ?? 8, 1, 32, "maxConcurrency"),
    );
  }
  #redact(value: string): string {
    return value.replaceAll(this.#key, "[redacted]").slice(0, 4096);
  }
  async request<T>(
    method: Method,
    template: string,
    options: WireOptions = {},
    text = false,
  ): Promise<T> {
    const path = pathWithParams(template, options.params);
    const url = new URL(this.#base + path);
    for (const [name, value] of Object.entries(options.query ?? {})) {
      if (value === undefined) continue;
      for (const item of Array.isArray(value) ? value : [value]) {
        if (
          !["string", "boolean", "number"].includes(typeof item) ||
          (typeof item === "number" && !Number.isFinite(item))
        )
          throw new HakopodError(
            `Invalid ${name} query parameter.`,
            "invalid_option",
          );
        url.searchParams.append(name, String(item));
      }
    }
    const headers = new Headers({
      Accept: text ? "text/plain" : "application/json",
      Authorization: `Bearer ${this.#key}`,
    });
    if (this.#workspace) headers.set("X-Hakopod-Workspace", this.#workspace);
    if (options.idempotencyKey)
      headers.set("Idempotency-Key", idempotencyKey(options.idempotencyKey));
    let body: string | undefined;
    if (options.body !== undefined) {
      if (method === "GET")
        throw new HakopodError(
          "GET requests cannot have a body.",
          "invalid_option",
        );
      try {
        body = JSON.stringify(options.body);
      } catch {
        throw new HakopodError(
          "The request body must be JSON serializable.",
          "invalid_body",
        );
      }
      if (
        body === undefined ||
        new TextEncoder().encode(body).byteLength > 1024 * 1024
      )
        throw new HakopodError(
          "The request body exceeds 1 MiB or is not JSON.",
          "body_limit",
        );
      headers.set("Content-Type", "application/json");
    }
    const timeout = new AbortController();
    const timer = setTimeout(
      () => timeout.abort(new Error("Request timeout")),
      this.#timeout,
    );
    const signal = options.signal
      ? AbortSignal.any([options.signal, timeout.signal])
      : timeout.signal;
    const deadline = Date.now() + this.#timeout;
    let release: (() => void) | undefined;
    let sent = false;
    try {
      release = await this.#gate.acquire(signal);
      for (let attempt = 0; ; attempt++) {
        let response: Response;
        let data: string;
        try {
          sent = true;
          response = await abortable(
            this.#fetch(url, {
              method,
              headers,
              body,
              redirect: "manual",
              credentials: "omit",
              cache: "no-store",
              signal,
            }),
            signal,
          );
          if (
            response.redirected ||
            (response.status >= 300 && response.status < 400)
          ) {
            void response.body?.cancel().catch(() => {});
            throw new HakopodError(
              "The API redirected the request. Use its final API URL; credentials will not follow redirects.",
              "redirect_refused",
            );
          }
          data = await this.#read(response, signal);
        } catch (error) {
          if (
            error instanceof HakopodError ||
            signal.aborted ||
            method !== "GET" ||
            attempt >= this.#retries
          )
            throw error;
          await delay(200 * 2 ** attempt, signal);
          continue;
        }
        if (!response.ok) {
          let message = `Hakopod returned HTTP ${response.status}.`;
          let code = "http_error";
          let approval: { id: string; workspace: string } | undefined;
          try {
            const parsed = JSON.parse(data);
            if (typeof parsed?.error?.message === "string")
              message = this.#redact(parsed.error.message);
            if (typeof parsed?.error?.code === "string")
              code = this.#redact(parsed.error.code);
            if (
              code === "approval_required" &&
              /^[a-f0-9]{32}$/.test(parsed.error.approval_id) &&
              /^[a-f0-9]{32}$/.test(parsed.error.workspace)
            )
              approval = Object.freeze({
                id: parsed.error.approval_id,
                workspace: parsed.error.workspace,
              });
          } catch {
            /* HTML or proxy errors must not become credential-bearing error messages. */
          }
          const retry = response.headers.get("Retry-After");
          const after =
            retry === null
              ? undefined
              : /^\d+$/.test(retry)
                ? Number(retry) * 1000
                : Date.parse(retry) - Date.now();
          const retryAfterMs =
            after !== undefined && Number.isFinite(after)
              ? Math.max(0, after)
              : undefined;
          const error = new APIError(
            message,
            code,
            response.status,
            retryAfterMs,
            options.idempotencyKey,
            approval,
          );
          const backoff = retryAfterMs ?? 200 * 2 ** attempt;
          if (
            method === "GET" &&
            attempt < this.#retries &&
            [429, 502, 503, 504].includes(response.status) &&
            backoff < deadline - Date.now()
          ) {
            await delay(backoff, signal);
            continue;
          }
          throw error;
        }
        if (text) return data as T;
        if (response.status === 204) return undefined as T;
        if (
          !response.headers
            .get("Content-Type")
            ?.toLowerCase()
            .includes("application/json")
        )
          throw new HakopodError(
            "The API returned a non-JSON response. Check the API URL and server version.",
            "invalid_response",
          );
        try {
          return JSON.parse(data) as T;
        } catch {
          throw new HakopodError(
            "The API returned invalid JSON.",
            "invalid_response",
          );
        }
      }
    } catch (error) {
      if (
        sent &&
        method !== "GET" &&
        error instanceof HakopodError &&
        ["invalid_response", "response_limit"].includes(error.code)
      )
        throw new TransportError(
          "The API's write response could not be read. Recover the operation before submitting a new request.",
          "unknown",
          options.idempotencyKey,
        );
      if (error instanceof HakopodError) throw error;
      if (!sent)
        throw new HakopodError(
          "The request was cancelled before it was sent.",
          "request_aborted",
        );
      throw new TransportError(
        signal.aborted
          ? "The request timed out or was cancelled. A submitted operation may still be running."
          : "The API connection failed. A submitted operation may still be running.",
        method === "GET" ? "read_failed" : "unknown",
        options.idempotencyKey,
      );
    } finally {
      clearTimeout(timer);
      release?.();
    }
  }
  async #read(response: Response, signal: AbortSignal): Promise<string> {
    const reader = response.body?.getReader();
    if (!reader) return "";
    let total = 0;
    const chunks: Uint8Array[] = [];
    try {
      const length = response.headers.get("Content-Length");
      if (length && Number(length) > this.#bytes)
        throw new HakopodError(
          "The API response is too large.",
          "response_limit",
        );
      while (true) {
        const { done, value } = await abortable(reader.read(), signal);
        if (done) break;
        total += value.byteLength;
        if (total > this.#bytes || chunks.length >= 16384)
          throw new HakopodError(
            "The API response is too large.",
            "response_limit",
          );
        chunks.push(value);
      }
      const bytes = new Uint8Array(total);
      let offset = 0;
      for (const chunk of chunks) {
        bytes.set(chunk, offset);
        offset += chunk.byteLength;
      }
      return new TextDecoder().decode(bytes);
    } finally {
      void reader.cancel().catch(() => {});
      reader.releaseLock();
    }
  }
}
