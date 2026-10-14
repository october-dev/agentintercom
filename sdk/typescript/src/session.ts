import { OctoberBusAdminClient, OctoberBusClient, OctoberBusScopeClient } from './client.js'
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
  /** Aborting this signal terminates the polling loop entirely. */
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

export class OctoberBusAgentSession {
  readonly client: OctoberBusClient
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
  private wakeController: AbortController = new AbortController()

  /**
   * A signal that fires on every false→true ready transition. The server wakes
   * a blocked inbox reserve on the ready edge, so queued deliveries arrive
   * through the host's in-flight long-poll without any client-side wiring.
   * The signal is replaced after each transition; the replacement is installed
   * BEFORE the previous signal aborts, so a listener that re-subscribes inside
   * its abort callback (reading `session.wake` anew) always lands on a live
   * signal. Callers should read `wake` per use rather than caching it.
   */
  get wake(): AbortSignal {
    return this.wakeController.signal
  }

  private constructor(options: AgentSessionOptions, registration: RegisterAgentResult) {
    this.registration = registration
    this.client = new OctoberBusClient(options.address, registration.agentToken)
    this.leaseMs = options.registration.leaseMs || 300_000
    this.heartbeatIntervalMs = options.heartbeatIntervalMs ?? Math.floor(this.leaseMs / 3)
    this.lifecycle = options.initialLifecycle ?? 'starting'
    this.ready = options.initialReady ?? false
    this.signal = options.signal
    this.done = new Promise((resolve) => { this.resolveDone = resolve })
    this.signal?.addEventListener('abort', this.abortListener, { once: true })
  }

  static async start(options: AgentSessionOptions): Promise<OctoberBusAgentSession> {
    if (options.signal?.aborted) throw options.signal.reason ?? new Error('Operation aborted')
    const leaseMs = options.registration.leaseMs || 300_000
    const interval = options.heartbeatIntervalMs ?? Math.floor(leaseMs / 3)
    const lifecycle = options.initialLifecycle ?? 'starting'
    const ready = options.initialReady ?? false
    if (!Number.isFinite(interval) || interval <= 0 || interval >= leaseMs) {
      throw new Error('heartbeatIntervalMs must be shorter than the execution lease')
    }
    if (lifecycle === 'offline' && ready) throw new Error('offline agents cannot be ready')
    const health = await new OctoberBusAdminClient(options.address, '').health(
      options.signal === undefined ? {} : { signal: options.signal }
    )
    if (health.name !== 'october-bus' || health.protocolVersion !== '0.1' ||
        health.status !== 'ready' || !Array.isArray(health.features) || !health.features.includes('session-retirement')) {
      throw new BusError('CONFLICT', 'Managed sessions require a ready protocol 0.1 runtime advertising session-retirement; upgrade the daemon before registering')
    }
    if (options.signal?.aborted) throw options.signal.reason ?? new Error('Operation aborted')
    const scope = new OctoberBusScopeClient(options.address, options.scopeToken)
    // Do not abort registration halfway through an ambiguous committed response.
    // Once its result arrives, cancellation retires the returned execution.
    const registration = await scope.registerAgent({ ...options.registration, leaseMs })
    const session = new OctoberBusAgentSession(options, registration)
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
      // Only update state on success — failed writes preserve confirmed state.
      const becameReady = nextReady && !this.ready
      this.lifecycle = nextLifecycle
      this.ready = nextReady
      if (becameReady) {
        // Install the replacement BEFORE aborting the previous signal.
        // AbortController.abort() dispatches listeners synchronously; a host
        // that re-subscribes inside its callback reads `session.wake` at that
        // moment and must land on the new, live signal — otherwise the next
        // readiness transition is missed.
        const previous = this.wakeController
        this.wakeController = new AbortController()
        previous.abort()
      }
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
  client: OctoberBusClient,
  options: InboxPollingOptions = {}
): AsyncGenerator<BusMessage[]> {
  const limit = options.limit ?? 50
  const waitMs = options.waitMs ?? 25_000
  if (limit < 1 || limit > 100) throw new Error('limit must be between 1 and 100')
  if (!Number.isInteger(waitMs) || waitMs < 1 || waitMs > 25_000) {
    throw new Error('waitMs must be an integer between 1 and 25000')
  }
  const terminal = options.signal
  // The server wakes the blocked reserve on the ready edge (Runtime.Heartbeat
  // notifies the per-agent signal that ReserveInbox waits on), so a ready host
  // receives its queued deliveries promptly without the client aborting the
  // long-poll. Aborting the pull here would race the server's prompt response
  // and orphan an already-committed reservation, so the in-flight pull is NOT
  // cancelled on a ready edge — the server delivers through it.
  while (!terminal?.aborted) {
    let messages: BusMessage[]
    try {
      messages = await client.pullInbox(limit, {
        waitMs,
        ...(terminal === undefined ? {} : { signal: terminal })
      })
    } catch (error) {
      if (terminal?.aborted) return // terminal abort: stop
      throw error
    }
    // pullInbox has already committed any returned batch. Deliver it before
    // honoring the terminal abort so an abort racing the response cannot drop
    // mail.
    if (messages.length > 0) yield messages
    if (terminal?.aborted) return
  }
}

export async function withClaimedTask<T>(
  client: OctoberBusClient,
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
