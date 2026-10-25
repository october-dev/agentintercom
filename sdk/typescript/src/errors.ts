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

export function asBusError(error: unknown): BusError {
  return error instanceof BusError ? error : new BusError('INTERNAL', 'Internal Agent Intercom error')
}
