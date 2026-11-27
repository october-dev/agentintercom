export type BusErrorCode =
  | 'INVALID_ARGUMENT'
  | 'UNAUTHENTICATED'
  | 'PERMISSION_DENIED'
  | 'NOT_FOUND'
  | 'METHOD_NOT_ALLOWED'
  | 'CONFLICT'
  | 'BACKPRESSURE'
  | 'INTERNAL'

const STATUS_BY_CODE: Readonly<Record<BusErrorCode, number>> = {
  INVALID_ARGUMENT: 400,
  UNAUTHENTICATED: 401,
  PERMISSION_DENIED: 403,
  NOT_FOUND: 404,
  METHOD_NOT_ALLOWED: 405,
  CONFLICT: 409,
  BACKPRESSURE: 429,
  INTERNAL: 500
}

export class BusError extends Error {
  readonly status: number

  constructor(
    readonly code: BusErrorCode,
    message: string,
    readonly details?: Record<string, unknown>
  ) {
    super(message)
    this.name = 'BusError'
    this.status = STATUS_BY_CODE[code]
  }
}

/** Why a request failed before the runtime returned a response. */
export type BusTransportFailure = 'network' | 'timeout' | 'aborted'

/**
 * A request that failed without a response from the runtime: the connection
 * failed, the operation timed out, or the caller's signal aborted it. The
 * outcome of a mutation is unknown; retry it with the same idempotency key or
 * read its receipt.
 *
 * For compatibility, `code` remains `'INTERNAL'` and `status` remains 500. Use
 * `instanceof BusTransportError` (or one of its subclasses) or `transport` to
 * tell these apart from an error the runtime reported.
 */
export class BusTransportError extends BusError {
  constructor(
    readonly transport: BusTransportFailure,
    message: string,
    options?: { cause?: unknown }
  ) {
    super('INTERNAL', message)
    this.name = 'BusTransportError'
    if (options && 'cause' in options) {
      Object.defineProperty(this, 'cause', { value: options.cause, configurable: true, writable: true })
    }
  }
}

/** The runtime could not be reached or the connection failed mid-request. */
export class BusNetworkError extends BusTransportError {
  constructor(message: string, options?: { cause?: unknown }) {
    super('network', message, options)
    this.name = 'BusNetworkError'
  }
}

/** The operation's `timeoutMs` elapsed before a response arrived. */
export class BusTimeoutError extends BusTransportError {
  constructor(
    readonly timeoutMs: number,
    message: string,
    options?: { cause?: unknown }
  ) {
    super('timeout', message, options)
    this.name = 'BusTimeoutError'
  }
}

/** The caller's `signal` aborted the operation. `cause` is the signal's reason. */
export class BusAbortError extends BusTransportError {
  constructor(message: string, options?: { cause?: unknown }) {
    super('aborted', message, options)
    this.name = 'BusAbortError'
  }
}

export function asBusError(error: unknown): BusError {
  return error instanceof BusError ? error : new BusError('INTERNAL', 'Internal Agent Intercom error')
}
