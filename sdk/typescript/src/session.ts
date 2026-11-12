import { AgentIntercomAdminClient, AgentIntercomClient, AgentIntercomScopeClient } from './client.js'
import { BusError } from './errors.js'
import type {
  Agent,
  AgentLifecycle,
  BusMessage,
  BusTask,
  RegisterAgentInput,
  RegisterAgentResult
} from './protocol.js'

export interface AgentSessionOptions {
  address: string
  scopeToken: string
  registration: RegisterAgentInput
  heartbeatIntervalMs?: number
  initialLifecycle?: AgentLifecycle
  initialReady?: boolean
  signal?: AbortSignal
}

export interface InboxPollingOptions {
  limit?: number
  waitMs?: number
  signal?: AbortSignal
}

export interface ClaimedTaskResult<T> {
  task: BusTask
  value: T
}

export function requiredEnvironmentValue(
  environment: Readonly<Record<string, string | undefined>>,
  name: string
): string {
  const value = environment[name]
  if (!value) throw new Error(`${name} is required`)
  return value
}

export function newIdempotencyKey(): string {
  return crypto.randomUUID()
}

export class AgentIntercomAgentSession {
  readonly client: AgentIntercomClient
  readonly registration: RegisterAgentResult
  readonly done: Promise<void>

  private readonly leaseMs: number
  private readonly heartbeatIntervalMs: number
  private readonly signal: AbortSignal | undefined
  private readonly lifecycleAbort = new AbortController()
  private readonly abortListener = (): void => { void this.close() }
  private lifecycle: AgentLifecycle
  private ready: boolean
  private timer: ReturnType<typeof setTimeout> | undefined
  private lifecycleQueue: Promise<void> = Promise.resolve()
  private closePromise: Promise<void> | undefined
  private resolveDone!: () => void
  private closed = false
  private sessionError: unknown

  private constructor(options: AgentSessionOptions, registration: RegisterAgentResult) {
    this.registration = registration
    this.client = new AgentIntercomClient(options.address, registration.agentToken)
    this.leaseMs = options.registration.leaseMs || 300_000
    this.heartbeatIntervalMs = options.heartbeatIntervalMs ?? Math.floor(this.leaseMs / 3)
    this.lifecycle = options.initialLifecycle ?? 'starting'
    this.ready = options.initialReady ?? false
    this.signal = options.signal
    this.done = new Promise((resolve) => { this.resolveDone = resolve })
    this.signal?.addEventListener('abort', this.abortListener, { once: true })
  }

  static async start(options: AgentSessionOptions): Promise<AgentIntercomAgentSession> {
    if (options.signal?.aborted) throw options.signal.reason ?? new Error('Operation aborted')
    const leaseMs = options.registration.leaseMs || 300_000
    const interval = options.heartbeatIntervalMs ?? Math.floor(leaseMs / 3)
    const lifecycle = options.initialLifecycle ?? 'starting'
    const ready = options.initialReady ?? false
    if (!Number.isFinite(interval) || interval <= 0 || interval >= leaseMs) {
      throw new Error('heartbeatIntervalMs must be shorter than the execution lease')
    }
    if (lifecycle === 'offline' && ready) throw new Error('offline agents cannot be ready')
    const health = await new AgentIntercomAdminClient(options.address, '').health(
      options.signal === undefined ? {} : { signal: options.signal }
    )
    if (health.name !== 'october-bus' || health.protocolVersion !== '0.1' ||
        health.status !== 'ready' || !Array.isArray(health.features) || !health.features.includes('session-retirement')) {
      throw new BusError('CONFLICT', 'Managed sessions require a ready protocol 0.1 runtime advertising session-retirement; upgrade the daemon before registering')
    }
    if (options.signal?.aborted) throw options.signal.reason ?? new Error('Operation aborted')
    const scope = new AgentIntercomScopeClient(options.address, options.scopeToken)
    // Do not abort registration halfway through an ambiguous committed response.
    // Once its result arrives, cancellation retires the returned execution.
    const registration = await scope.registerAgent({ ...options.registration, leaseMs })
    const session = new AgentIntercomAgentSession(options, registration)
    try {
      if (options.signal?.aborted) throw options.signal.reason ?? new Error('Operation aborted')
      await session.enqueueHeartbeat(lifecycle, ready)
      if (options.signal?.aborted) throw options.signal.reason ?? new Error('Operation aborted')
      session.scheduleHeartbeat()
      return session
    } catch (error) {
      await session.close()
      throw error
    }
  }

  get error(): unknown { return this.sessionError }

  async setState(lifecycle: AgentLifecycle, ready: boolean): Promise<Agent> {
    if (this.closed) throw new Error('agent session is closed')
    if (lifecycle === 'offline' && ready) throw new Error('offline agents cannot be ready')
    return this.enqueueHeartbeat(lifecycle, ready)
  }

  close(): Promise<void> {
    if (this.closePromise !== undefined) return this.closePromise
    this.closed = true
    this.lifecycleAbort.abort(new Error('agent session is closed'))
    if (this.timer !== undefined) clearTimeout(this.timer)
    this.signal?.removeEventListener('abort', this.abortListener)
    this.closePromise = (async () => {
      try {
        await this.lifecycleQueue
        await this.client.retire({ timeoutMs: 5_000 })
      } catch (error) {
        if (this.sessionError === undefined) this.sessionError = error
      } finally {
        this.resolveDone()
      }
    })()
    return this.closePromise
  }

  private enqueueHeartbeat(lifecycle?: AgentLifecycle, ready?: boolean): Promise<Agent> {
    const operation = this.lifecycleQueue.then(async () => {
      if (this.closed) throw new Error('agent session is closed')
      // Background heartbeats read the last confirmed state when their queued
      // turn begins, not while an earlier state write is still in flight.
      const nextLifecycle = lifecycle ?? this.lifecycle
      const nextReady = ready ?? this.ready
      const agent = await this.client.heartbeat(nextLifecycle, nextReady, this.leaseMs, { signal: this.lifecycleAbort.signal })
      this.lifecycle = nextLifecycle
      this.ready = nextReady
      return agent
    })
    // Keep the queue drainable after a failed write so cleanup is never skipped.
    this.lifecycleQueue = operation.then(() => undefined, () => undefined)
    return operation
  }

  private scheduleHeartbeat(): void {
    if (this.closed) return
    this.timer = setTimeout(() => {
      void this.enqueueHeartbeat().then(
        () => this.scheduleHeartbeat(),
        (error: unknown) => {
          // Close intentionally aborts an in-flight heartbeat; that is not a
          // failure of an otherwise clean retirement.
          if (!this.closed) this.sessionError = error
          void this.close()
        }
      )
    }, this.heartbeatIntervalMs)
  }
}

export async function* pollInbox(
  client: AgentIntercomClient,
  options: InboxPollingOptions = {}
): AsyncGenerator<BusMessage[]> {
  const limit = options.limit ?? 50
  const waitMs = options.waitMs ?? 25_000
  if (limit < 1 || limit > 100) throw new Error('limit must be between 1 and 100')
  if (!Number.isInteger(waitMs) || waitMs < 1 || waitMs > 25_000) {
    throw new Error('waitMs must be an integer between 1 and 25000')
  }
  while (!options.signal?.aborted) {
    let messages: BusMessage[]
    try {
      messages = await client.pullInbox(limit, {
        waitMs,
        ...(options.signal === undefined ? {} : { signal: options.signal })
      })
    } catch (error) {
      if (options.signal?.aborted) return
      throw error
    }
    if (messages.length > 0) yield messages
  }
}

export async function withClaimedTask<T>(
  client: AgentIntercomClient,
  taskId: string,
  work: (task: BusTask) => Promise<T>,
  completionNote?: (value: T) => string | undefined
): Promise<ClaimedTaskResult<T>> {
  const claimed = await client.claimTask(taskId)
  try {
    const value = await work(claimed)
    const task = await client.completeTask(taskId, completionNote?.(value))
    return { task, value }
  } catch (error) {
    try {
      await client.releaseTask(taskId)
    } catch {
      // Preserve the work error. Lease recovery remains the final fallback.
    }
    throw error
  }
}
