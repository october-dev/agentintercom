package bus

import (
	"context"
	"time"
)

const (
	maxExpiryMS int64 = 30 * 24 * 60 * 60 * 1000
	// Inbox waits stay below the server and default client timeout of 30 seconds.
	maxInboxWaitMS int64 = 25 * 1000
	maxEventWaitMS int64 = 25 * 1000

	DefaultA2APrincipalMessageLimit int64 = 1000
	DefaultA2APrincipalByteLimit    int64 = 16 * 1024 * 1024
)

type Runtime struct {
	store              storageBackend
	signals            *runtimeSignals
	inboxWaiters       inboxWaitBudget
	a2aPrincipalLimits A2APrincipalLimits
}

type RuntimeOptions struct {
	A2APrincipalMessageLimit int64
	A2APrincipalByteLimit    int64
}

func Open(source string) (*Runtime, error) {
	return OpenWithOptions(source, RuntimeOptions{})
}

func OpenWithOptions(source string, options RuntimeOptions) (*Runtime, error) {
	limits, err := normalizedA2APrincipalLimits(options)
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(source)
	if err != nil {
		return nil, err
	}
	return openWithStorage(store, limits), nil
}

func openWithStorage(store storageBackend, limits A2APrincipalLimits) *Runtime {
	return &Runtime{store: store, signals: newRuntimeSignals(), a2aPrincipalLimits: limits}
}

func normalizedA2APrincipalLimits(options RuntimeOptions) (A2APrincipalLimits, error) {
	messageLimit := options.A2APrincipalMessageLimit
	if messageLimit == 0 {
		messageLimit = DefaultA2APrincipalMessageLimit
	}
	if messageLimit < 1 || messageLimit >= messageBacklogCap {
		return A2APrincipalLimits{}, Errorf(CodeInvalidArgument, "A2A principal message limit must be between 1 and 9999")
	}
	byteLimit := options.A2APrincipalByteLimit
	if byteLimit == 0 {
		byteLimit = DefaultA2APrincipalByteLimit
	}
	maxBytes := int64(messageBacklogCap-1) * 65536
	if byteLimit < 1 || byteLimit > maxBytes {
		return A2APrincipalLimits{}, Errorf(CodeInvalidArgument, "A2A principal byte limit must be between 1 and 655294464")
	}
	return A2APrincipalLimits{MessageLimit: messageLimit, ByteLimit: byteLimit}, nil
}

func (r *Runtime) Close() error { return r.store.Close() }

func (r *Runtime) StorageHealth(ctx context.Context) StorageHealth {
	health := StorageHealth{Backend: r.store.Backend(), Status: StorageAvailable}
	if err := r.store.Ping(ctx); err != nil {
		health.Status = StorageUnavailable
	}
	return health
}

func (r *Runtime) CreateScope(ctx context.Context, input CreateScopeInput) (CreateScopeResult, error) {
	if err := validateIdentity(input.ID, "id", true); err != nil {
		return CreateScopeResult{}, err
	}
	return r.store.CreateScope(ctx, input.ID)
}

func (r *Runtime) RegisterAgent(ctx context.Context, scopeToken string, input RegisterAgentInput) (RegisterAgentResult, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	if err := validateIdentity(input.ID, "id", true); err != nil {
		return RegisterAgentResult{}, err
	}
	if err := validateText(input.DisplayName, "displayName", 256, false); err != nil {
		return RegisterAgentResult{}, err
	}
	if err := validateCapabilities(input.Capabilities); err != nil {
		return RegisterAgentResult{}, err
	}
	if len(input.ConnectTo) > 128 {
		return RegisterAgentResult{}, Errorf(CodeInvalidArgument, "connectTo exceeds 128 items")
	}
	for _, peer := range input.ConnectTo {
		if err := validateIdentity(peer, "connectTo", false); err != nil {
			return RegisterAgentResult{}, err
		}
	}
	input.LeaseMS, err = normalizedLease(input.LeaseMS)
	if err != nil {
		return RegisterAgentResult{}, err
	}
	if input.Capabilities == nil {
		input.Capabilities = []AgentCapability{}
	}
	result, err := r.store.RegisterAgent(ctx, scopeID, input)
	if err == nil {
		r.signals.notify(signalKey{scopeID: result.ScopeID, consumerID: result.AgentID})
		r.notifyScope(result.ScopeID)
	}
	return result, err
}

func (r *Runtime) ListAgents(ctx context.Context, scopeToken string) ([]Agent, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return nil, err
	}
	return r.store.ListAgents(ctx, scopeID)
}

func (r *Runtime) LinkAgents(ctx context.Context, scopeToken, left, right string) error {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return err
	}
	if err := validateIdentity(left, "left", false); err != nil {
		return err
	}
	if err := validateIdentity(right, "right", false); err != nil {
		return err
	}
	err = r.store.LinkAgents(ctx, scopeID, left, right)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return err
}

func (r *Runtime) Principal(ctx context.Context, agentToken string) (Principal, error) {
	return r.store.AuthenticateAgent(ctx, agentToken)
}

// Credential classifies a bearer credential with bounded indexed lookups and
// no scope data, so a forwarding gateway can decide admission before it reads
// a request body. Success authorizes nothing: every route still authenticates
// and rechecks inside its own transaction.
//
// The default purpose mirrors scopeAuthority: a scope token, then an unexpired
// current agent token, then an enabled scoped credential. purpose=retire is the
// read-only form of the rule Store.RetireAgent applies: the current token of an
// execution may retire it even when its lease has expired or already ended,
// while a replaced token may not.
func (r *Runtime) Credential(ctx context.Context, token, purpose string) (Credential, error) {
	switch purpose {
	case "":
		if _, err := r.store.AuthenticateScope(ctx, token); err == nil {
			return Credential{Kind: CredentialScope}, nil
		} else if AsBusError(err).Code != CodeUnauthenticated {
			return Credential{}, err
		}
		kind, err := r.store.CredentialKind(ctx, token)
		if err != nil {
			return Credential{}, err
		}
		switch kind {
		case CredentialKindAgent:
			return Credential{Kind: CredentialAgent}, nil
		case CredentialKindScopedPrincipal:
			return Credential{Kind: CredentialPrincipal}, nil
		}
		return Credential{}, Errorf(CodeUnauthenticated, "Invalid credential")
	case "retire":
		if _, err := r.store.CurrentAgentToken(ctx, token); err != nil {
			return Credential{}, err
		}
		return Credential{Kind: CredentialAgent}, nil
	}
	return Credential{}, Errorf(CodeInvalidArgument, "purpose must be omitted or retire")
}

func (r *Runtime) Heartbeat(ctx context.Context, agentToken string, input HeartbeatInput) (Agent, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return Agent{}, err
	}
	if err := validateLifecycle(input.Lifecycle); err != nil {
		return Agent{}, err
	}
	if input.Lifecycle == LifecycleOffline && input.Ready {
		return Agent{}, Errorf(CodeInvalidArgument, "offline agents cannot be ready")
	}
	input.LeaseMS, err = normalizedLease(input.LeaseMS)
	if err != nil {
		return Agent{}, err
	}
	result, stateChanged, becameReady, err := r.store.Heartbeat(ctx, principal, input)
	if err == nil {
		if stateChanged {
			r.notifyScope(principal.ScopeID)
		}
		if becameReady {
			r.signals.notify(signalKey{scopeID: principal.ScopeID, consumerID: principal.AgentID})
		}
	}
	return result, err
}

func (r *Runtime) ListPeers(ctx context.Context, agentToken string) ([]Agent, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return nil, err
	}
	return r.store.ListPeers(ctx, principal)
}

func (r *Runtime) SendMessage(ctx context.Context, agentToken string, input SendMessageInput) (DeliveryReceipt, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	if err := validateIdentity(input.To, "to", false); err != nil {
		return DeliveryReceipt{}, err
	}
	if err := validateText(input.Body, "body", 65536, false); err != nil {
		return DeliveryReceipt{}, err
	}
	if err := validateMessageMode(input.Mode); err != nil {
		return DeliveryReceipt{}, err
	}
	if err := validateIdentity(input.ResponseTo, "responseTo", true); err != nil {
		return DeliveryReceipt{}, err
	}
	if err := validateText(input.IdempotencyKey, "idempotencyKey", 256, true); err != nil {
		return DeliveryReceipt{}, err
	}
	if input.ExpiresInMS < 0 || input.ExpiresInMS > maxExpiryMS {
		return DeliveryReceipt{}, Errorf(CodeInvalidArgument, "expiresInMs is invalid")
	}
	if err := validateContext(input.Context); err != nil {
		return DeliveryReceipt{}, err
	}
	if input.Context == nil {
		input.Context = []ContextItem{}
	}
	result, err := r.store.SendMessage(ctx, principal, input)
	if err == nil {
		r.signals.notify(signalKey{scopeID: principal.ScopeID, consumerID: input.To})
		r.notifyScope(principal.ScopeID)
	}
	return result, err
}

func (r *Runtime) Receipt(ctx context.Context, agentToken, messageID string) (DeliveryReceipt, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	if err := validateIdentity(messageID, "messageId", false); err != nil {
		return DeliveryReceipt{}, err
	}
	result, err := r.store.Receipt(ctx, principal, messageID)
	if err == nil {
		r.notifyScope(principal.ScopeID)
	}
	return result, err
}

func normalizedInboxLimit(limit int) (int, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return 0, Errorf(CodeInvalidArgument, "limit must be between 1 and 100")
	}
	return limit, nil
}

// ReserveInbox reserves available messages, waiting for up to waitMS when the
// inbox is empty. A zero wait returns immediately. Callers embedding Runtime
// directly should cancel ctx before closing the Runtime.
func (r *Runtime) ReserveInbox(ctx context.Context, agentToken string, limit int, waitMS int64) (*InboxReservation, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return nil, err
	}
	limit, err = normalizedInboxLimit(limit)
	if err != nil {
		return nil, err
	}
	if waitMS < 0 || waitMS > maxInboxWaitMS {
		return nil, Errorf(CodeInvalidArgument, "waitMs must be between 0 and 25000")
	}
	if waitMS == 0 {
		reservation, err := r.store.ReserveInbox(ctx, principal, limit)
		if err == nil {
			r.notifyScope(principal.ScopeID)
		}
		return reservation, err
	}
	deadline := time.Now().Add(time.Duration(waitMS) * time.Millisecond)
	releaseBudget, ok := r.inboxWaiters.acquire(principal)
	if !ok {
		return nil, Errorf(CodeBackpressure, "Inbox waiter limit is full")
	}
	defer releaseBudget()
	key := signalKey{scopeID: principal.ScopeID, consumerID: principal.AgentID}
	for {
		signal, unsubscribe := r.signals.subscribe(key)
		principal, err = r.Principal(ctx, agentToken)
		if err != nil {
			unsubscribe()
			return nil, err
		}
		reservation, err := r.store.ReserveInbox(ctx, principal, limit)
		if err == nil {
			r.notifyScope(principal.ScopeID)
		}
		if err != nil || reservation != nil {
			unsubscribe()
			return reservation, err
		}

		now := time.Now()
		if !now.Before(deadline) {
			unsubscribe()
			return nil, nil
		}
		wakeAt := deadline
		leaseExpiresAt := time.UnixMilli(principal.LeaseExpiresAt)
		if leaseExpiresAt.Before(wakeAt) {
			wakeAt = leaseExpiresAt
		}
		reservationExpiresAt, err := r.store.NextInboxReservationExpiry(ctx, principal)
		if err != nil {
			unsubscribe()
			return nil, err
		}
		if reservationExpiresAt > 0 {
			expiresAt := time.UnixMilli(reservationExpiresAt)
			if expiresAt.Before(wakeAt) {
				wakeAt = expiresAt
			}
		}
		wait := time.Until(wakeAt)
		if wait <= 0 {
			unsubscribe()
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			unsubscribe()
			return nil, ctx.Err()
		case <-signal:
			stopTimer(timer)
		case <-timer.C:
		}
		unsubscribe()
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (r *Runtime) CommitInbox(ctx context.Context, agentToken, reservationID string) ([]Message, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return nil, err
	}
	if err := validateIdentity(reservationID, "reservationId", false); err != nil {
		return nil, err
	}
	result, err := r.store.CommitInbox(ctx, principal, reservationID)
	if err == nil {
		r.notifyScope(principal.ScopeID)
	}
	return result, err
}

func (r *Runtime) ReleaseInbox(ctx context.Context, agentToken, reservationID string) error {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return err
	}
	if err := validateIdentity(reservationID, "reservationId", false); err != nil {
		return err
	}
	err = r.store.ReleaseInbox(ctx, principal, reservationID)
	if err == nil {
		r.signals.notify(signalKey{scopeID: principal.ScopeID, consumerID: principal.AgentID})
		r.notifyScope(principal.ScopeID)
	}
	return err
}

func (r *Runtime) AcknowledgeMessages(ctx context.Context, agentToken string, messageIDs []string) (int64, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return 0, err
	}
	if len(messageIDs) > 100 {
		return 0, Errorf(CodeInvalidArgument, "messageIds exceeds 100 items")
	}
	for _, messageID := range messageIDs {
		if err := validateIdentity(messageID, "messageIds", false); err != nil {
			return 0, err
		}
	}
	count, err := r.store.AcknowledgeMessages(ctx, principal, messageIDs)
	if err == nil {
		r.notifyScope(principal.ScopeID)
	}
	return count, err
}

func (r *Runtime) AddTask(ctx context.Context, agentToken string, input AddTaskInput) (Task, error) {
	scopeID, principal, err := r.taskAuthority(ctx, agentToken)
	if err != nil {
		return Task{}, err
	}
	if err := validateText(input.Title, "title", 256, false); err != nil {
		return Task{}, err
	}
	if err := validateText(input.Description, "description", 16384, true); err != nil {
		return Task{}, err
	}
	if len(input.Dependencies) > 128 {
		return Task{}, Errorf(CodeInvalidArgument, "dependencies exceeds 128 items")
	}
	for _, dependency := range input.Dependencies {
		if err := validateIdentity(dependency, "dependencies", false); err != nil {
			return Task{}, err
		}
	}
	var result Task
	if principal == nil {
		ctx = withScopeCredential(ctx, agentToken)
		result, err = r.store.AddTask(ctx, scopeID, "", input)
	} else {
		result, err = r.store.AddAgentTask(ctx, *principal, input)
	}
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) ClaimTask(ctx context.Context, agentToken, taskID string) (Task, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return Task{}, err
	}
	if err := validateIdentity(taskID, "taskId", false); err != nil {
		return Task{}, err
	}
	result, err := r.store.ClaimTask(ctx, principal, taskID)
	if err == nil {
		r.notifyScope(principal.ScopeID)
	}
	return result, err
}

func (r *Runtime) ReleaseTask(ctx context.Context, agentToken, taskID string) (Task, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return Task{}, err
	}
	if err := validateIdentity(taskID, "taskId", false); err != nil {
		return Task{}, err
	}
	result, err := r.store.ReleaseTask(ctx, principal, taskID)
	if err == nil {
		r.notifyScope(principal.ScopeID)
	}
	return result, err
}

func (r *Runtime) CompleteTask(ctx context.Context, agentToken, taskID, note string) (Task, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return Task{}, err
	}
	if err := validateIdentity(taskID, "taskId", false); err != nil {
		return Task{}, err
	}
	if err := validateText(note, "note", 16384, true); err != nil {
		return Task{}, err
	}
	result, err := r.store.CompleteTask(ctx, principal, taskID, note)
	if err == nil {
		r.notifyScope(principal.ScopeID)
	}
	return result, err
}

func (r *Runtime) AddTaskProgress(ctx context.Context, agentToken, taskID string, input AddTaskProgressInput) (TaskProgress, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return TaskProgress{}, err
	}
	if err := validateIdentity(taskID, "taskId", false); err != nil {
		return TaskProgress{}, err
	}
	switch input.Kind {
	case "progress", "note", "blocker":
	default:
		return TaskProgress{}, Errorf(CodeInvalidArgument, "kind must be progress, note, or blocker")
	}
	if err := validateText(input.Text, "text", 4000, false); err != nil {
		return TaskProgress{}, err
	}
	result, err := r.store.AddTaskProgress(ctx, principal, taskID, input)
	if err == nil {
		r.notifyScope(principal.ScopeID)
	}
	return result, err
}

func (r *Runtime) ListTaskProgress(ctx context.Context, token, taskID string) ([]TaskProgress, error) {
	scopeID, _, err := r.taskAuthority(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := validateIdentity(taskID, "taskId", false); err != nil {
		return nil, err
	}
	return r.store.ListTaskProgress(ctx, scopeID, taskID)
}

func (r *Runtime) ListTasks(ctx context.Context, token string, readyOnly bool) ([]Task, error) {
	scopeID, principal, err := r.taskAuthority(ctx, token)
	if err != nil {
		return nil, err
	}
	if principal == nil {
		ctx = withScopeCredential(ctx, token)
	} else {
		ctx = withExecution(ctx, *principal)
	}
	result, err := r.store.ListTasks(ctx, scopeID, readyOnly)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) StorageSummary(ctx context.Context, scopeToken string) (StorageSummary, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return StorageSummary{}, err
	}
	result, err := r.store.StorageSummary(ctx, scopeID)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) PruneScope(ctx context.Context, scopeToken string, input PruneScopeInput) (PruneScopeResult, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return PruneScopeResult{}, err
	}
	before, err := time.Parse(time.RFC3339, input.Before)
	if err != nil {
		return PruneScopeResult{}, Errorf(CodeInvalidArgument, "before must be an RFC 3339 timestamp")
	}
	result, err := r.store.PruneScope(ctx, scopeID, before.UnixMilli(), input.Execute)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) taskAuthority(ctx context.Context, token string) (scopeID string, principal *Principal, err error) {
	value, agentErr := r.Principal(ctx, token)
	if agentErr == nil {
		return value.ScopeID, &value, nil
	}
	scopeID, scopeErr := r.store.AuthenticateScope(ctx, token)
	if scopeErr == nil {
		return scopeID, nil, nil
	}
	return "", nil, agentErr
}

func (r *Runtime) scopeAuthority(ctx context.Context, token string) (string, error) {
	scopeID, err := r.store.AuthenticateScope(ctx, token)
	if err == nil {
		return scopeID, nil
	}
	if AsBusError(err).Code != CodeUnauthenticated {
		return "", err
	}
	kind, kindErr := r.store.CredentialKind(ctx, token)
	if kindErr != nil {
		return "", kindErr
	}
	if kind != CredentialKindUnknown {
		return "", Errorf(CodePermissionDenied, "Scope authority is required")
	}
	return "", err
}

func (r *Runtime) AskHuman(ctx context.Context, agentToken string, input AskHumanInput) (HumanEscalation, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return HumanEscalation{}, err
	}
	if err := validateText(input.Question, "question", 4000, false); err != nil {
		return HumanEscalation{}, err
	}
	if len(input.Options) == 1 || len(input.Options) > 4 {
		return HumanEscalation{}, Errorf(CodeInvalidArgument, "options must be empty or contain between 2 and 4 values")
	}
	for _, option := range input.Options {
		if err := validateText(option, "options", 256, false); err != nil {
			return HumanEscalation{}, err
		}
	}
	result, err := r.store.AskHuman(ctx, principal, input)
	if err == nil {
		r.notifyScope(principal.ScopeID)
	}
	return result, err
}

func (r *Runtime) Escalation(ctx context.Context, agentToken, escalationID string) (HumanEscalation, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return HumanEscalation{}, err
	}
	if err := validateIdentity(escalationID, "escalationId", false); err != nil {
		return HumanEscalation{}, err
	}
	return r.store.Escalation(ctx, principal.ScopeID, escalationID)
}

func (r *Runtime) ListEscalations(ctx context.Context, scopeToken string) ([]HumanEscalation, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return nil, err
	}
	return r.store.ListEscalations(ctx, scopeID)
}

func (r *Runtime) ResolveEscalation(ctx context.Context, scopeToken, escalationID, answer string) (HumanEscalation, error) {
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return HumanEscalation{}, err
	}
	if err := validateIdentity(escalationID, "escalationId", false); err != nil {
		return HumanEscalation{}, err
	}
	if err := validateText(answer, "answer", 16384, false); err != nil {
		return HumanEscalation{}, err
	}
	result, err := r.store.ResolveEscalation(ctx, scopeID, escalationID, answer)
	if err == nil {
		r.notifyScope(scopeID)
	}
	return result, err
}

func (r *Runtime) NodeStatus(ctx context.Context, agentToken string) (NodeStatus, error) {
	principal, err := r.Principal(ctx, agentToken)
	if err != nil {
		return NodeStatus{}, err
	}
	agent, err := r.store.Agent(ctx, principal.ScopeID, principal.AgentID)
	if err != nil {
		return NodeStatus{}, err
	}
	return NodeStatus{
		Identity: NodeIdentity{AgentIdentity: principal.AgentIdentity, LeaseExpiresAt: instant(principal.LeaseExpiresAt)},
		Agent:    agent,
	}, nil
}
