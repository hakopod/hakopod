export class HakopodError extends Error {
  constructor(
    message: string,
    readonly code: string,
  ) {
    super(message);
    this.name = new.target.name;
  }
}

export class APIError extends HakopodError {
  constructor(
    message: string,
    code: string,
    readonly status: number,
    readonly retryAfterMs?: number,
    readonly idempotencyKey?: string,
    readonly approval?: Readonly<{ id: string; workspace: string }>,
  ) {
    super(message, code);
  }
  get isConflict(): boolean {
    return this.status === 409;
  }
  get isUnauthorized(): boolean {
    return this.status === 401;
  }
  get isForbidden(): boolean {
    return this.status === 403;
  }
}

// A failed response is not evidence that a write was rejected. The caller can
// recover deployments by this key, or repeat the exact request with the same key.
export class TransportError extends HakopodError {
  constructor(
    message: string,
    readonly outcome: "unknown" | "read_failed",
    readonly idempotencyKey?: string,
  ) {
    super(message, "transport_error");
  }
}

export class OperationError extends HakopodError {
  constructor(
    readonly operationId: string,
    readonly status: string,
    message: string,
  ) {
    super(message, "operation_failed");
  }
}

export class WaitError extends HakopodError {
  constructor(
    readonly operationId: string,
    code: "wait_timeout" | "wait_aborted",
  ) {
    super(
      `Stopped waiting for ${operationId}. The operation continues on the server; resume waiting with its ID.`,
      code,
    );
  }
}
