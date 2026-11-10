import { BusError } from './errors.js'
import type { BusErrorCode } from './errors.js'
import type {
  Attachment, AttachmentPage, AttachmentContent, UploadAttachmentInput,
  Channel, ChannelPost, CreateChannelInput, PostChannelInput, ChannelHistoryInput, ChannelPostPage,
  Agent,
  AgentCardPublication,
  A2APrincipal,
  A2APrincipalUsage,
  AgentLifecycle,
  AddTaskInput,
  AddTaskProgressInput,
  AskHumanInput,
  BusHealth,
  BusLiveness,
  BusMessage,
  BusTask,
  CreateScopeInput,
  CreateScopeResult,
  CreateA2APrincipalInput,
  CreateOutputPrincipalInput,
  CreateOutputStreamInput,
  DeliveryReceipt,
  EventBatch,
  HumanEscalation,
  InboxReservation,
  NodeStatus,
  IssuedA2APrincipal,
  IssuedOutputPrincipal,
  OutputHistory,
  OutputPrincipal,
  OutputStream,
  OutputValue,
  PruneScopeInput,
  PruneScopeResult,
  PublishAgentCardInput,
  PublishOutputInput,
  RegisterAgentInput,
  RegisterAgentResult,
  ScopeArchive,
  SendMessageInput,
  StorageSummary,
  TaskProgress,
  ImportScopeResult
} from './protocol.js'

interface Success<T> {
  ok: true
  result: T
}

interface Failure {
  ok: false
  error: { code: BusErrorCode; message: string; details?: Record<string, unknown> }
}

export interface OperationOptions {
  signal?: AbortSignal
  timeoutMs?: number
}

export interface DownloadAttachmentOptions extends OperationOptions {
  /** Bytes requested per chunk, 1 to 524288 (the server maximum). Defaults to 65536. */
  chunkSize?: number
}

export interface DownloadedAttachment {
  attachment: Attachment
  /** The complete attachment, verified against its size and SHA-256. */
  data: Uint8Array
}

const MAX_ATTACHMENT_BYTES = 16 * 1024 * 1024
const MAX_ATTACHMENT_CHUNK_BYTES = 512 * 1024

function downloadFailure(reason: string): never {
  throw new BusError('INTERNAL', `Attachment download failed: ${reason}`)
}

function decodeBase64(value: unknown): Uint8Array {
  if (typeof value !== 'string') downloadFailure('chunk data is missing')
  let binary: string
  try {
    binary = atob(value)
  } catch {
    downloadFailure('chunk data is not valid base64')
  }
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index)
  return bytes
}

async function sha256Hex(data: Uint8Array<ArrayBuffer>): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', data))
  return Array.from(digest, (byte) => byte.toString(16).padStart(2, '0')).join('')
}

export interface InboxReservationOptions extends OperationOptions {
  waitMs?: number
}

export interface ListTasksOptions extends OperationOptions {
  ready?: boolean
}

export interface EventOptions extends OperationOptions {
  after?: number
  limit?: number
  waitMs?: number
}

export interface OutputHistoryOptions extends OperationOptions {
  after?: number
  limit?: number
}

async function request<T>(
  address: string,
  token: string | undefined,
  method: string,
  path: string,
  value?: unknown,
  options: OperationOptions = {}
): Promise<T> {
  const timeoutMs = options.timeoutMs ?? 30_000
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
    throw new BusError('INVALID_ARGUMENT', 'timeoutMs must be a positive finite number')
  }
  const controller = new AbortController()
  const onAbort = () => controller.abort(options.signal?.reason ?? new Error('Operation aborted'))
  if (options.signal?.aborted) onAbort()
  else options.signal?.addEventListener('abort', onAbort, { once: true })
  const timeout = setTimeout(
    () => controller.abort(new Error(`Agent Intercom request timed out after ${timeoutMs}ms`)),
    timeoutMs
  )
  let response: Response
  let text: string
  try {
    response = await fetch(`${address}${path}`, {
      method,
      headers: {
        accept: 'application/json',
        ...(value === undefined ? {} : { 'content-type': value instanceof Blob ? 'application/octet-stream' : 'application/json' }),
        ...(token === undefined ? {} : { authorization: `Bearer ${token}` })
      },
      ...(value === undefined ? {} : { body: value instanceof Blob ? value : JSON.stringify(value) }),
      redirect: 'error',
      signal: controller.signal
    })
    text = await response.text()
  } catch (error) {
    const cause = controller.signal.aborted ? controller.signal.reason : error
    const message = cause instanceof Error ? cause.message : 'Network request failed'
    throw new BusError('INTERNAL', `Agent Intercom request failed: ${message}`)
  } finally {
    clearTimeout(timeout)
    options.signal?.removeEventListener('abort', onAbort)
  }
  let payload: Success<T> | Failure | T
  try {
    payload = JSON.parse(text) as Success<T> | Failure | T
  } catch {
    throw new BusError('INTERNAL', `Agent Intercom returned a non-JSON response with HTTP ${response.status}`)
  }
  if (response.ok) {
    if (payload && typeof payload === 'object' && 'ok' in payload && payload.ok === true)
      return (payload as Success<T>).result
    return payload as T
  }
  if (
    payload &&
    typeof payload === 'object' &&
    'ok' in payload &&
    payload.ok === false &&
    'error' in payload &&
    payload.error &&
    typeof payload.error === 'object' &&
    'code' in payload.error &&
    'message' in payload.error
  ) {
    const failure = payload as Failure
    throw new BusError(failure.error.code, failure.error.message, failure.error.details)
  }
  throw new BusError('INTERNAL', `Agent Intercom request failed with HTTP ${response.status}`)
}

export class AgentIntercomAdminClient {
  constructor(
    readonly address: string,
    private readonly adminToken: string
  ) {}

  /** Stream a sensitive, full SQLite disaster-recovery snapshot (not portable JSON). */
  async *backup(options: OperationOptions = {}): AsyncGenerator<Uint8Array> {
    const timeoutMs = options.timeoutMs ?? 300_000
    if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
      throw new BusError('INVALID_ARGUMENT', 'timeoutMs must be a positive finite number')
    }
    const controller = new AbortController()
    const abort = (): void => controller.abort(options.signal?.reason)
    if (options.signal?.aborted) abort()
    else options.signal?.addEventListener('abort', abort, { once: true })
    const timer = setTimeout(() => controller.abort(new Error('Database backup timed out')), timeoutMs)
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
    try {
      const response = await fetch(this.address + '/v1/admin/backup', {
        headers: { Authorization: 'Bearer ' + this.adminToken },
        signal: controller.signal
      })
      if (!response.ok || response.body === null) {
        await response.body?.cancel()
        throw new Error(`Database backup failed with HTTP ${response.status}`)
      }
      reader = response.body.getReader()
      for (;;) {
        const chunk = await reader.read()
        if (chunk.done) return
        yield chunk.value
      }
    } finally {
      clearTimeout(timer)
      options.signal?.removeEventListener('abort', abort)
      await reader?.cancel().catch(() => undefined)
    }
  }

  listScopes(options?: OperationOptions): Promise<Array<{ scopeId: string; createdAt: string }>> {
    return request(this.address, this.adminToken, 'GET', '/v1/admin/scopes', undefined, options)
  }

  rotateScopeToken(scopeId: string, options?: OperationOptions): Promise<CreateScopeResult> {
    return request(this.address, this.adminToken, 'POST', `/v1/admin/scopes/${encodeURIComponent(scopeId)}/rotate-token`, {}, options)
  }

  async deleteScope(scopeId: string, options?: OperationOptions): Promise<void> {
    await request(this.address, this.adminToken, 'DELETE', `/v1/admin/scopes/${encodeURIComponent(scopeId)}`, { confirmScopeId: scopeId }, options)
  }

  health(options?: OperationOptions): Promise<BusHealth> {
    return request(this.address, undefined, 'GET', '/health', undefined, options)
  }

  liveness(options?: OperationOptions): Promise<BusLiveness> {
    return request(this.address, undefined, 'GET', '/health/live', undefined, options)
  }

  createScope(input: CreateScopeInput = {}, options?: OperationOptions): Promise<CreateScopeResult> {
    return request(this.address, this.adminToken, 'POST', '/v1/scopes', input, options)
  }

  exportScope(scopeId: string, options?: OperationOptions): Promise<ScopeArchive> {
    return request(
      this.address,
      this.adminToken,
      'GET',
      `/v1/admin/scopes/${encodeURIComponent(scopeId)}/export`,
      undefined,
      options
    )
  }

  importScope(archive: ScopeArchive, options?: OperationOptions): Promise<ImportScopeResult> {
    return request(this.address, this.adminToken, 'POST', '/v1/admin/scopes/import', archive, options)
  }

  async shutdown(options?: OperationOptions): Promise<void> {
    await request(this.address, this.adminToken, 'POST', '/v1/admin/shutdown', {}, options)
  }
}

export class AgentIntercomScopeClient {
  constructor(
    readonly address: string,
    readonly scopeToken: string
  ) {}

  taskPage(after = '', limit = 100, options?: OperationOptions): Promise<{ tasks: BusTask[]; nextCursor?: string }> {
    const query = new URLSearchParams({ after, limit: String(limit) })
    return request(this.address, this.scopeToken, 'GET', `/v1/tasks/page?${query}`, undefined, options)
  }

  registerAgent(input: RegisterAgentInput, options?: OperationOptions): Promise<RegisterAgentResult> {
    return request(this.address, this.scopeToken, 'POST', '/v1/agents', input, options)
  }

  listAgents(options?: OperationOptions): Promise<Agent[]> {
    return request(this.address, this.scopeToken, 'GET', '/v1/agents', undefined, options)
  }

  async linkAgents(left: string, right: string, options?: OperationOptions): Promise<void> {
    await request(this.address, this.scopeToken, 'POST', '/v1/links', { left, right }, options)
  }

  addTask(input: AddTaskInput, options?: OperationOptions): Promise<BusTask> {
    return request(this.address, this.scopeToken, 'POST', '/v1/tasks', input, options)
  }

  listTasks(options: ListTasksOptions = {}): Promise<BusTask[]> {
    const { ready, ...operationOptions } = options
    const query = ready ? '?ready=true' : ''
    return request(this.address, this.scopeToken, 'GET', `/v1/tasks${query}`, undefined, operationOptions)
  }

  listTaskProgress(taskId: string, options?: OperationOptions): Promise<TaskProgress[]> {
    return request(
      this.address,
      this.scopeToken,
      'GET',
      `/v1/tasks/${encodeURIComponent(taskId)}/progress`,
      undefined,
      options
    )
  }

  storageSummary(options?: OperationOptions): Promise<StorageSummary> {
    return request(this.address, this.scopeToken, 'GET', '/v1/scope/storage', undefined, options)
  }

  pruneScope(input: PruneScopeInput, options?: OperationOptions): Promise<PruneScopeResult> {
    return request(this.address, this.scopeToken, 'POST', '/v1/scope/storage/prune', input, options)
  }

  events(options: EventOptions = {}): Promise<EventBatch> {
    const { after = 0, limit = 50, waitMs = 0, ...operationOptions } = options
    const query = new URLSearchParams({
      after: String(after),
      limit: String(limit),
      waitMs: String(waitMs)
    })
    return request(this.address, this.scopeToken, 'GET', `/v1/events?${query}`, undefined, operationOptions)
  }

  async *watchEvents(options: EventOptions = {}): AsyncGenerator<EventBatch> {
    const waitMs = options.waitMs ?? 25_000
    if (!Number.isInteger(waitMs) || waitMs < 1 || waitMs > 25_000) {
      throw new BusError('INVALID_ARGUMENT', 'waitMs must be an integer between 1 and 25000')
    }
    let after = options.after ?? 0
    while (!options.signal?.aborted) {
      let batch: EventBatch
      try {
        batch = await this.events({ ...options, after, waitMs })
      } catch (error) {
        if (options.signal?.aborted) return
        throw error
      }
      if (batch.resyncRequired) {
        yield batch
        return
      }
      after = batch.nextRevision
      if (batch.events.length > 0) yield batch
    }
  }

  createAgentCardPublication(input: PublishAgentCardInput, options?: OperationOptions): Promise<AgentCardPublication> {
    return request(this.address, this.scopeToken, 'POST', '/v1/a2a/publications', input, options)
  }

  listAgentCardPublications(options?: OperationOptions): Promise<AgentCardPublication[]> {
    return request(this.address, this.scopeToken, 'GET', '/v1/a2a/publications', undefined, options)
  }

  setAgentCardPublicationEnabled(
    publicationId: string,
    enabled: boolean,
    options?: OperationOptions
  ): Promise<AgentCardPublication> {
    const action = enabled ? 'enable' : 'disable'
    return request(
      this.address,
      this.scopeToken,
      'POST',
      `/v1/a2a/publications/${encodeURIComponent(publicationId)}/${action}`,
      {},
      options
    )
  }

  createA2APrincipal(input: CreateA2APrincipalInput, options?: OperationOptions): Promise<IssuedA2APrincipal> {
    return request(this.address, this.scopeToken, 'POST', '/v1/a2a/principals', input, options)
  }

  listA2APrincipals(options?: OperationOptions): Promise<A2APrincipal[]> {
    return request(this.address, this.scopeToken, 'GET', '/v1/a2a/principals', undefined, options)
  }

  listA2APrincipalUsage(options?: OperationOptions): Promise<A2APrincipalUsage[]> {
    return request(this.address, this.scopeToken, 'GET', '/v1/a2a/principals/usage', undefined, options)
  }

  rotateA2APrincipal(principalId: string, options?: OperationOptions): Promise<IssuedA2APrincipal> {
    return request(
      this.address,
      this.scopeToken,
      'POST',
      `/v1/a2a/principals/${encodeURIComponent(principalId)}/rotate`,
      {},
      options
    )
  }

  setA2APrincipalEnabled(
    principalId: string,
    enabled: boolean,
    options?: OperationOptions
  ): Promise<A2APrincipal> {
    const action = enabled ? 'enable' : 'disable'
    return request(
      this.address,
      this.scopeToken,
      'POST',
      `/v1/a2a/principals/${encodeURIComponent(principalId)}/${action}`,
      {},
      options
    )
  }

  createOutputStream(input: CreateOutputStreamInput, options?: OperationOptions): Promise<OutputStream> {
    return request(this.address, this.scopeToken, 'POST', '/v1/output-streams', input, options)
  }

  listOutputStreams(options?: OperationOptions): Promise<OutputStream[]> {
    return request(this.address, this.scopeToken, 'GET', '/v1/output-streams', undefined, options)
  }

  outputStream(streamId: string, options?: OperationOptions): Promise<OutputStream> {
    return request(
      this.address,
      this.scopeToken,
      'GET',
      `/v1/output-streams/${encodeURIComponent(streamId)}`,
      undefined,
      options
    )
  }

  async removeOutputStream(streamId: string, options?: OperationOptions): Promise<void> {
    await request(
      this.address,
      this.scopeToken,
      'DELETE',
      `/v1/output-streams/${encodeURIComponent(streamId)}`,
      undefined,
      options
    )
  }

  setOutputPublisher(
    streamId: string,
    agentId: string,
    allowed: boolean,
    options?: OperationOptions
  ): Promise<OutputStream> {
    return request(
      this.address,
      this.scopeToken,
      allowed ? 'PUT' : 'DELETE',
      `/v1/output-streams/${encodeURIComponent(streamId)}/publishers/${encodeURIComponent(agentId)}`,
      undefined,
      options
    )
  }

  createOutputPrincipal(input: CreateOutputPrincipalInput, options?: OperationOptions): Promise<IssuedOutputPrincipal> {
    return request(this.address, this.scopeToken, 'POST', '/v1/output-principals', input, options)
  }

  listOutputPrincipals(options?: OperationOptions): Promise<OutputPrincipal[]> {
    return request(this.address, this.scopeToken, 'GET', '/v1/output-principals', undefined, options)
  }

  rotateOutputPrincipal(principalId: string, options?: OperationOptions): Promise<IssuedOutputPrincipal> {
    return request(
      this.address,
      this.scopeToken,
      'POST',
      `/v1/output-principals/${encodeURIComponent(principalId)}/rotate`,
      {},
      options
    )
  }

  setOutputPrincipalEnabled(
    principalId: string,
    enabled: boolean,
    options?: OperationOptions
  ): Promise<OutputPrincipal> {
    const action = enabled ? 'enable' : 'disable'
    return request(
      this.address,
      this.scopeToken,
      'POST',
      `/v1/output-principals/${encodeURIComponent(principalId)}/${action}`,
      {},
      options
    )
  }

  latestOutput(streamId: string, options?: OperationOptions): Promise<OutputValue | null> {
    return request(
      this.address,
      this.scopeToken,
      'GET',
      `/outputs/${encodeURIComponent(streamId)}/latest`,
      undefined,
      options
    )
  }

  outputHistory(streamId: string, options: OutputHistoryOptions = {}): Promise<OutputHistory> {
    const { after = 0, limit = 50, ...operationOptions } = options
    const query = new URLSearchParams({ after: String(after), limit: String(limit) })
    return request(
      this.address,
      this.scopeToken,
      'GET',
      `/outputs/${encodeURIComponent(streamId)}/values?${query}`,
      undefined,
      operationOptions
    )
  }

  listEscalations(options?: OperationOptions): Promise<HumanEscalation[]> {
    return request(this.address, this.scopeToken, 'GET', '/v1/scope/escalations', undefined, options)
  }

  resolveEscalation(id: string, answer: string, options?: OperationOptions): Promise<HumanEscalation> {
    return request(this.address, this.scopeToken, 'POST', `/v1/scope/escalations/${encodeURIComponent(id)}/resolve`, {
      answer
    }, options)
  }
}

export class AgentIntercomClient {
  constructor(
    readonly address: string,
    readonly agentToken: string
  ) {}

  taskPage(after = '', limit = 100, options?: OperationOptions): Promise<{ tasks: BusTask[]; nextCursor?: string }> {
    const query = new URLSearchParams({ after, limit: String(limit) })
    return request(this.address, this.agentToken, 'GET', `/v1/tasks/page?${query}`, undefined, options)
  }

  async retire(options?: OperationOptions): Promise<void> {
    await request(this.address, this.agentToken, 'POST', '/v1/me/retire', {}, options)
  }

  heartbeat(lifecycle: AgentLifecycle, ready = true, leaseMs?: number, options?: OperationOptions): Promise<Agent> {
    return request(this.address, this.agentToken, 'PATCH', '/v1/me/heartbeat', {
      lifecycle,
      ready,
      ...(leaseMs === undefined ? {} : { leaseMs })
    }, options)
  }

  listPeers(options?: OperationOptions): Promise<Agent[]> {
    return request(this.address, this.agentToken, 'GET', '/v1/peers', undefined, options)
  }

  publishOutput(streamId: string, input: PublishOutputInput, options?: OperationOptions): Promise<OutputValue> {
    return request(
      this.address,
      this.agentToken,
      'POST',
      `/outputs/${encodeURIComponent(streamId)}/values`,
      input,
      options
    )
  }

  listChannels(options?: OperationOptions): Promise<Channel[]> {
    return request(this.address, this.agentToken, 'GET', '/v1/channels', undefined, options)
  }

  listAttachments(after = '', limit = 50, options?: OperationOptions): Promise<AttachmentPage> {
    return request(this.address, this.agentToken, 'GET', `/v1/attachments?after=${encodeURIComponent(after)}&limit=${limit}`, undefined, options)
  }
  uploadAttachment(input: UploadAttachmentInput, data: Uint8Array, options?: OperationOptions): Promise<Attachment> {
    if (data.byteLength > 16 * 1024 * 1024) throw new BusError('INVALID_ARGUMENT', 'Attachment exceeds 16 MiB')
    const query = new URLSearchParams({ name: input.name, mediaType: input.mediaType ?? '', idempotencyKey: input.idempotencyKey ?? '' })
    return request(this.address, this.agentToken, 'POST', `/v1/attachments?${query}`, new Blob([new Uint8Array(data)]), options)
  }
  readAttachment(id: string, offset = 0, limit = 65536, options?: OperationOptions): Promise<AttachmentContent> {
    return request(this.address, this.agentToken, 'GET', `/v1/attachments/${encodeURIComponent(id)}?offset=${offset}&limit=${limit}`, undefined, options)
  }
  /**
   * Downloads a complete attachment with `readAttachment()` chunks and verifies it:
   * the same attachment ID, contiguous offsets, unchanged size and checksum, a
   * continuation that advances exactly, the final byte count, and SHA-256. It
   * refuses an attachment over 16 MiB before reading its bytes, and stops without
   * requesting another chunk once `signal` is aborted. `timeoutMs` applies to each
   * chunk request. Nothing is written to disk.
   */
  async downloadAttachment(id: string, options: DownloadAttachmentOptions = {}): Promise<DownloadedAttachment> {
    const { chunkSize = 65536, ...requestOptions } = options
    if (!Number.isInteger(chunkSize) || chunkSize < 1 || chunkSize > MAX_ATTACHMENT_CHUNK_BYTES) {
      throw new BusError('INVALID_ARGUMENT', `chunkSize must be an integer from 1 to ${MAX_ATTACHMENT_CHUNK_BYTES}`)
    }
    let metadata: Attachment | undefined
    let data: Uint8Array<ArrayBuffer> = new Uint8Array(0)
    let offset = 0
    for (;;) {
      // request() never sends once the signal is aborted, so cancellation stops
      // the loop before another chunk is requested.
      const chunk = await this.readAttachment(id, offset, chunkSize, requestOptions)
      if (chunk.attachment?.id !== id || chunk.offset !== offset) downloadFailure('attachment ID or offset changed')
      if (metadata === undefined) {
        const { size, sha256 } = chunk.attachment
        if (!Number.isSafeInteger(size) || size < 0 || size > MAX_ATTACHMENT_BYTES) {
          downloadFailure('size is outside the 16 MiB attachment bound')
        }
        if (typeof sha256 !== 'string' || !/^[0-9a-f]{64}$/i.test(sha256)) downloadFailure('checksum is malformed')
        metadata = chunk.attachment
        data = new Uint8Array(size)
      } else if (chunk.attachment.size !== metadata.size || chunk.attachment.sha256?.toLowerCase() !== metadata.sha256.toLowerCase()) {
        downloadFailure('size or checksum changed during the download')
      }
      const bytes = decodeBase64(chunk.dataBase64)
      if (offset + bytes.length > metadata.size) downloadFailure('received more bytes than the attachment size')
      data.set(bytes, offset)
      offset += bytes.length
      if (chunk.nextOffset === undefined || chunk.nextOffset === null) break
      if (bytes.length === 0 || chunk.nextOffset !== offset) downloadFailure('invalid continuation offset')
    }
    if (offset !== metadata.size) downloadFailure(`received ${offset} of ${metadata.size} bytes`)
    if ((await sha256Hex(data)) !== metadata.sha256.toLowerCase()) downloadFailure('SHA-256 does not match')
    return { attachment: metadata, data }
  }
  async deleteAttachment(id: string, options?: OperationOptions): Promise<void> {
    await request(this.address, this.agentToken, 'DELETE', `/v1/attachments/${encodeURIComponent(id)}`, undefined, options)
  }
  createChannel(input: CreateChannelInput, options?: OperationOptions): Promise<Channel> {
    return request(this.address, this.agentToken, 'POST', '/v1/channels', input, options)
  }
  async subscribeChannel(channelId: string, subscribe: boolean, options?: OperationOptions): Promise<void> {
    await request(this.address, this.agentToken, 'POST', `/v1/channels/${encodeURIComponent(channelId)}/subscription`, { subscribe }, options)
  }
  postChannel(input: PostChannelInput, options?: OperationOptions): Promise<ChannelPost> {
    return request(this.address, this.agentToken, 'POST', '/v1/channel-posts', input, options)
  }
  readChannel(input: ChannelHistoryInput, options?: OperationOptions): Promise<ChannelPostPage> {
    const query = new URLSearchParams()
    for (const [key, value] of Object.entries(input)) if (value !== undefined) query.set(key, String(value))
    return request(this.address, this.agentToken, 'GET', `/v1/channel-posts?${query}`, undefined, options)
  }

  sendMessage(input: SendMessageInput, options?: OperationOptions): Promise<DeliveryReceipt> {
    return request(this.address, this.agentToken, 'POST', '/v1/messages', input, options)
  }

  receipt(messageId: string, options?: OperationOptions): Promise<DeliveryReceipt> {
    return request(this.address, this.agentToken, 'GET', `/v1/messages/${encodeURIComponent(messageId)}`, undefined, options)
  }

  reserveInbox(limit = 50, options: InboxReservationOptions = {}): Promise<InboxReservation | null> {
    const { waitMs, ...operationOptions } = options
    return request(
      this.address,
      this.agentToken,
      'POST',
      '/v1/inbox/reserve',
      { limit, ...(waitMs === undefined ? {} : { waitMs }) },
      operationOptions
    )
  }

  commitInbox(reservationId: string, options?: OperationOptions): Promise<BusMessage[]> {
    return request(
      this.address,
      this.agentToken,
      'POST',
      `/v1/inbox/${encodeURIComponent(reservationId)}/commit`,
      {},
      options
    )
  }

  async releaseInbox(reservationId: string, options?: OperationOptions): Promise<void> {
    await request(
      this.address,
      this.agentToken,
      'POST',
      `/v1/inbox/${encodeURIComponent(reservationId)}/release`,
      {},
      options
    )
  }

  async pullInbox(limit = 50, options?: InboxReservationOptions): Promise<BusMessage[]> {
    const reservation = await this.reserveInbox(limit, options)
    return reservation ? this.commitInbox(reservation.id, options) : []
  }

  async acknowledgeMessages(messageIds: string[], options?: OperationOptions): Promise<number> {
    const result = await request<{ acknowledged: number }>(
      this.address,
      this.agentToken,
      'POST',
      '/v1/messages/ack',
      { messageIds },
      options
    )
    return result.acknowledged
  }

  addTask(input: AddTaskInput, options?: OperationOptions): Promise<BusTask> {
    return request(this.address, this.agentToken, 'POST', '/v1/tasks', input, options)
  }

  listTasks(options: ListTasksOptions = {}): Promise<BusTask[]> {
    const { ready, ...operationOptions } = options
    const query = ready ? '?ready=true' : ''
    return request(this.address, this.agentToken, 'GET', `/v1/tasks${query}`, undefined, operationOptions)
  }

  claimTask(taskId: string, options?: OperationOptions): Promise<BusTask> {
    return request(this.address, this.agentToken, 'POST', `/v1/tasks/${encodeURIComponent(taskId)}/claim`, {}, options)
  }

  releaseTask(taskId: string, options?: OperationOptions): Promise<BusTask> {
    return request(this.address, this.agentToken, 'POST', `/v1/tasks/${encodeURIComponent(taskId)}/release`, {}, options)
  }

  completeTask(taskId: string, note?: string, options?: OperationOptions): Promise<BusTask> {
    return request(this.address, this.agentToken, 'POST', `/v1/tasks/${encodeURIComponent(taskId)}/complete`, {
      ...(note === undefined ? {} : { note })
    }, options)
  }

  addTaskProgress(taskId: string, input: AddTaskProgressInput, options?: OperationOptions): Promise<TaskProgress> {
    return request(
      this.address,
      this.agentToken,
      'POST',
      `/v1/tasks/${encodeURIComponent(taskId)}/progress`,
      input,
      options
    )
  }

  listTaskProgress(taskId: string, options?: OperationOptions): Promise<TaskProgress[]> {
    return request(
      this.address,
      this.agentToken,
      'GET',
      `/v1/tasks/${encodeURIComponent(taskId)}/progress`,
      undefined,
      options
    )
  }

  askHuman(input: AskHumanInput, options?: OperationOptions): Promise<HumanEscalation> {
    return request(this.address, this.agentToken, 'POST', '/v1/escalations', input, options)
  }

  escalation(id: string, options?: OperationOptions): Promise<HumanEscalation> {
    return request(this.address, this.agentToken, 'GET', `/v1/escalations/${encodeURIComponent(id)}`, undefined, options)
  }

  mcpEndpoint(): { url: string; headers: Record<string, string> } {
    return { url: `${this.address}/mcp`, headers: { Authorization: `Bearer ${this.agentToken}` } }
  }

  nodeStatus(options?: OperationOptions): Promise<NodeStatus> {
    return request(this.address, this.agentToken, 'GET', '/v1/me', undefined, options)
  }
}

export class AgentIntercomOutputClient {
  constructor(
    readonly address: string,
    private readonly credential: string
  ) {}

  publish(streamId: string, input: PublishOutputInput, options?: OperationOptions): Promise<OutputValue> {
    return request(
      this.address,
      this.credential,
      'POST',
      `/outputs/${encodeURIComponent(streamId)}/values`,
      input,
      options
    )
  }

  latest(streamId: string, options?: OperationOptions): Promise<OutputValue | null> {
    return request(
      this.address,
      this.credential,
      'GET',
      `/outputs/${encodeURIComponent(streamId)}/latest`,
      undefined,
      options
    )
  }

  history(streamId: string, options: OutputHistoryOptions = {}): Promise<OutputHistory> {
    const { after = 0, limit = 50, ...operationOptions } = options
    const query = new URLSearchParams({ after: String(after), limit: String(limit) })
    return request(
      this.address,
      this.credential,
      'GET',
      `/outputs/${encodeURIComponent(streamId)}/values?${query}`,
      undefined,
      operationOptions
    )
  }
}
