package bus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testAgents struct {
	runtime       *Runtime
	scope         CreateScopeResult
	planner       RegisterAgentResult
	reviewer      RegisterAgentResult
	plannerToken  string
	reviewerToken string
}

func sqliteStore(t testing.TB, runtime *Runtime) *Store {
	t.Helper()
	store, ok := runtime.store.(*Store)
	if !ok {
		t.Fatalf("test requires SQLite storage, got %s", runtime.store.Backend())
	}
	return store
}

func setupAgents(t *testing.T, source string) testAgents {
	return setupAgentsWithOptions(t, source, RuntimeOptions{})
}

func setupAgentsWithOptions(t *testing.T, source string, options RuntimeOptions) testAgents {
	t.Helper()
	ctx := context.Background()
	runtimeValue, err := OpenWithOptions(source, options)
	requireNoError(t, err)
	scope, err := runtimeValue.CreateScope(ctx, CreateScopeInput{ID: "test"})
	requireNoError(t, err)
	planner, err := runtimeValue.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "planner", DisplayName: "Planner"})
	requireNoError(t, err)
	reviewer, err := runtimeValue.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "reviewer", DisplayName: "Reviewer", ConnectTo: []string{"planner"}})
	requireNoError(t, err)
	return testAgents{runtime: runtimeValue, scope: scope, planner: planner, reviewer: reviewer, plannerToken: planner.AgentToken, reviewerToken: reviewer.AgentToken}
}

func requireCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	require(t, err != nil, "expected %s", code)
	var failure *BusError
	require(t, errors.As(err, &failure) && failure.Code == code, "expected %s, got %v", code, err)
}

type authorityFailureStore struct {
	storageBackend
	authErr    error
	kindErr    error
	kindCalled bool
}

func (s *authorityFailureStore) AuthenticateScope(ctx context.Context, token string) (string, error) {
	if s.authErr != nil {
		return "", s.authErr
	}
	return s.storageBackend.AuthenticateScope(ctx, token)
}

func (s *authorityFailureStore) CredentialKind(context.Context, string) (CredentialKind, error) {
	s.kindCalled = true
	return CredentialKindUnknown, s.kindErr
}

func TestScopeAuthorityClassifiesCredentialsAndPreservesTaskFallbacks(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	_, a2aPrincipal := setupA2APrincipal(t, agents)
	stream, err := agents.runtime.CreateOutputStream(ctx, agents.scope.ScopeToken, CreateOutputStreamInput{Name: "scope-authority"})
	requireNoError(t, err)
	outputPrincipal, err := agents.runtime.CreateOutputPrincipal(ctx, agents.scope.ScopeToken, CreateOutputPrincipalInput{
		StreamID: stream.ID, Label: "Scope authority test", Permissions: []OutputPermission{OutputRead},
	})
	requireNoError(t, err)

	if _, err := agents.runtime.ListAgents(ctx, agents.scope.ScopeToken); err != nil {
		t.Fatalf("scope credential was rejected: %v", err)
	}
	for name, testCase := range map[string]struct {
		token string
		want  ErrorCode
	}{
		"garbage":          {token: "not-a-credential", want: CodeUnauthenticated},
		"agent":            {token: agents.plannerToken, want: CodePermissionDenied},
		"a2a principal":    {token: a2aPrincipal.Credential, want: CodePermissionDenied},
		"output principal": {token: outputPrincipal.Credential, want: CodePermissionDenied},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := agents.runtime.ListAgents(ctx, testCase.token)
			requireCode(t, err, testCase.want)
		})
	}

	if _, err := agents.runtime.AddTask(ctx, agents.scope.ScopeToken, AddTaskInput{Title: "Scope task"}); err != nil {
		t.Fatalf("scope credential could not create task: %v", err)
	}
	if _, err := agents.runtime.AddTask(ctx, agents.plannerToken, AddTaskInput{Title: "Agent task"}); err != nil {
		t.Fatalf("agent credential could not create task: %v", err)
	}
	for name, token := range map[string]string{
		"garbage":          "not-a-credential",
		"a2a principal":    a2aPrincipal.Credential,
		"output principal": outputPrincipal.Credential,
	} {
		t.Run("task route "+name, func(t *testing.T) {
			_, err := agents.runtime.ListTasks(ctx, token, false)
			requireCode(t, err, CodeUnauthenticated)
		})
	}

	if _, err := sqliteStore(t, agents.runtime).db.ExecContext(ctx, `UPDATE agents SET lease_expires_at=? WHERE token_hash=?`, nowMillis(), tokenDigest(agents.reviewerToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.runtime.ListAgents(ctx, agents.reviewerToken); err == nil {
		t.Fatal("expired agent credential accessed a scope route")
	} else {
		requireCode(t, err, CodeUnauthenticated)
	}
	if _, err := agents.runtime.SetOutputPrincipalEnabled(ctx, agents.scope.ScopeToken, outputPrincipal.Principal.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.runtime.ListAgents(ctx, outputPrincipal.Credential); err == nil {
		t.Fatal("disabled principal credential accessed a scope route")
	} else {
		requireCode(t, err, CodeUnauthenticated)
	}

	server := NewServer(agents.runtime, ServerOptions{})
	request := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
	request.Header.Set("Authorization", "Bearer "+agents.plannerToken)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("agent credential returned HTTP %d, want 403: %s", response.Code, response.Body.String())
	}

}

func TestScopeAuthorityPropagatesAuthenticationAndClassifierErrors(t *testing.T) {
	store, err := OpenStore(":memory:")
	requireNoError(t, err)
	defer store.Close()
	sentinel := errors.New("credential classifier failed")

	classifierFailure := &authorityFailureStore{storageBackend: store, kindErr: sentinel}
	runtimeValue := openWithStorage(classifierFailure, A2APrincipalLimits{})
	if _, err := runtimeValue.ListAgents(context.Background(), "invalid"); !errors.Is(err, sentinel) {
		t.Fatalf("classifier error was not propagated: %v", err)
	}

	authFailure := &authorityFailureStore{
		storageBackend: store,
		authErr:        Errorf(CodeBackpressure, "scope authentication unavailable"),
		kindErr:        errors.New("must not be returned"),
	}
	runtimeValue = openWithStorage(authFailure, A2APrincipalLimits{})
	_, err = runtimeValue.ListAgents(context.Background(), "invalid")
	requireCode(t, err, CodeBackpressure)
	if authFailure.kindCalled {
		t.Fatal("classifier ran after a non-authentication failure")
	}
}

func TestA2APrincipalLimitsLoadFromEnvironment(t *testing.T) {
	t.Setenv("OCTOBER_BUS_A2A_PRINCIPAL_MESSAGE_LIMIT", "12")
	t.Setenv("OCTOBER_BUS_A2A_PRINCIPAL_BYTE_LIMIT", "4096")
	options, err := runtimeOptionsFromEnvironment()
	require(t, err == nil && options.A2APrincipalMessageLimit == 12 && options.A2APrincipalByteLimit == 4096, "unexpected runtime options: %#v, %v", options, err)
	t.Setenv("OCTOBER_BUS_A2A_PRINCIPAL_MESSAGE_LIMIT", "invalid")
	if _, err := runtimeOptionsFromEnvironment(); err == nil {
		t.Fatal("invalid A2A limit was accepted")
	}
}

func TestDurableRequestRedeliveryAcknowledgementAndReply(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	receipt, err := agents.runtime.SendMessage(ctx, agents.plannerToken, SendMessageInput{To: "reviewer", Mode: MessageRequest, Body: "Review this"})
	requireNoError(t, err)
	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil && len(reservation.Messages) == 1, "unexpected reservation: %#v, %v", reservation, err)
	first, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, reservation.ID)
	require(t, err == nil && first[0].State == DeliveryDelivered, "unexpected delivery: %#v, %v", first, err)
	redelivery, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && redelivery != nil && redelivery.Messages[0].ID == receipt.MessageID, "unexpected redelivery: %#v, %v", redelivery, err)
	if _, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, redelivery.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := agents.runtime.AcknowledgeMessages(ctx, agents.reviewerToken, []string{receipt.MessageID}); err != nil || count != 1 {
		t.Fatalf("unexpected acknowledgement: %d, %v", count, err)
	}
	replyInput := SendMessageInput{To: "planner", Mode: MessageResponse, ResponseTo: receipt.MessageID, Body: "Found an issue", IdempotencyKey: "reply-1"}
	reply, err := agents.runtime.SendMessage(ctx, agents.reviewerToken, replyInput)
	requireNoError(t, err)
	retry, err := agents.runtime.SendMessage(ctx, agents.reviewerToken, replyInput)
	require(t, err == nil && retry.MessageID == reply.MessageID, "response retry did not return the original receipt: %#v, %v", retry, err)
	updated, err := agents.runtime.Receipt(ctx, agents.plannerToken, receipt.MessageID)
	require(t, err == nil && updated.ResponseMessageID == reply.MessageID && updated.RepliedAt != "", "unexpected reply receipt: %#v, %v", updated, err)
	_, err = agents.runtime.SendMessage(ctx, agents.reviewerToken, SendMessageInput{To: "planner", Mode: MessageResponse, ResponseTo: receipt.MessageID, Body: "Second reply"})
	requireCode(t, err, CodeConflict)
}

func TestMessageIdempotencyRejectsPayloadChanges(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	input := SendMessageInput{To: "reviewer", Mode: MessageRequest, Body: "Review this", IdempotencyKey: "review-42"}
	first, err := agents.runtime.SendMessage(ctx, agents.plannerToken, input)
	requireNoError(t, err)
	retry, err := agents.runtime.SendMessage(ctx, agents.plannerToken, input)
	require(t, err == nil && retry.MessageID == first.MessageID, "retry did not return the original receipt: %#v, %v", retry, err)
	input.Body = "Review something else"
	_, err = agents.runtime.SendMessage(ctx, agents.plannerToken, input)
	requireCode(t, err, CodeConflict)
	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil && len(reservation.Messages) == 1, "idempotent retry created duplicate work: %#v, %v", reservation, err)
}

// Portable primitive behind a per-recipient fan-out adapter: each recipient is
// its own send under its own key, so one refusal cannot undo another's commit.
// It does not model a hosted stale generation or a lost aggregate response.
func TestFanOutSendsAreIndependentAndRecipientScopedForDeduplication(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	accepted := SendMessageInput{To: "reviewer", Mode: MessageRequest, Body: "Fan this out", IdempotencyKey: "fanout-7:reviewer"}
	refused := SendMessageInput{To: "absent", Mode: MessageRequest, Body: "Fan this out", IdempotencyKey: "fanout-7:absent"}

	sent, err := agents.runtime.SendMessage(ctx, agents.plannerToken, accepted)
	requireNoError(t, err)
	_, err = agents.runtime.SendMessage(ctx, agents.plannerToken, refused)
	requireCode(t, err, CodePermissionDenied)

	retry, err := agents.runtime.SendMessage(ctx, agents.plannerToken, accepted)
	require(t, err == nil && retry.MessageID == sent.MessageID && retry.AcceptedAt == sent.AcceptedAt, "retry did not return the original receipt: %#v, %v", retry, err)
	_, err = agents.runtime.SendMessage(ctx, agents.plannerToken, refused)
	requireCode(t, err, CodePermissionDenied)

	redirected := accepted
	redirected.To = "absent"
	_, err = agents.runtime.SendMessage(ctx, agents.plannerToken, redirected)
	requireCode(t, err, CodeConflict)

	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil && len(reservation.Messages) == 1 && reservation.Messages[0].ID == sent.MessageID, "fan-out did not deliver exactly one message: %#v, %v", reservation, err)
}

func TestResponsesRequireDeliveryButMayFinishAfterExpiry(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()

	undelivered, err := agents.runtime.SendMessage(ctx, agents.plannerToken, SendMessageInput{
		To: "reviewer", Mode: MessageRequest, Body: "Do not deliver", ExpiresInMS: 40,
	})
	requireNoError(t, err)
	time.Sleep(60 * time.Millisecond)
	_, err = agents.runtime.SendMessage(ctx, agents.reviewerToken, SendMessageInput{
		To: "planner", Mode: MessageResponse, ResponseTo: undelivered.MessageID, Body: "Too late",
	})
	requireCode(t, err, CodeConflict)

	delivered, err := agents.runtime.SendMessage(ctx, agents.plannerToken, SendMessageInput{
		To: "reviewer", Mode: MessageRequest, Body: "Deliver before expiry", ExpiresInMS: 40,
	})
	requireNoError(t, err)
	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil, "unexpected reservation: %#v, %v", reservation, err)
	if _, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, reservation.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	reply, err := agents.runtime.SendMessage(ctx, agents.reviewerToken, SendMessageInput{
		To: "planner", Mode: MessageResponse, ResponseTo: delivered.MessageID, Body: "Finished after expiry",
	})
	requireNoError(t, err)
	receipt, err := agents.runtime.Receipt(ctx, agents.plannerToken, delivered.MessageID)
	require(t, err == nil && receipt.State == DeliveryExpired && receipt.ResponseMessageID == reply.MessageID && receipt.RepliedAt != "", "late delivered reply is not observable: %#v, %v", receipt, err)
}

func TestExpiredReservationCannotDeliverOrResurrectMessage(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	receipt, err := agents.runtime.SendMessage(ctx, agents.plannerToken, SendMessageInput{
		To: "reviewer", Body: "Short lived", ExpiresInMS: 40,
	})
	requireNoError(t, err)
	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil, "unexpected reservation: %#v, %v", reservation, err)
	time.Sleep(60 * time.Millisecond)
	requireNoError(t, agents.runtime.ReleaseInbox(ctx, agents.reviewerToken, reservation.ID))
	state, err := agents.runtime.Receipt(ctx, agents.plannerToken, receipt.MessageID)
	require(t, err == nil && state.State == DeliveryExpired, "release resurrected an expired message: %#v, %v", state, err)
	receipt, err = agents.runtime.SendMessage(ctx, agents.plannerToken, SendMessageInput{
		To: "reviewer", Body: "Also short lived", ExpiresInMS: 40,
	})
	requireNoError(t, err)
	reservation, err = agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil, "unexpected second reservation: %#v, %v", reservation, err)
	time.Sleep(60 * time.Millisecond)
	messages, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, reservation.ID)
	require(t, err == nil && len(messages) == 0, "expired message was delivered: %#v, %v", messages, err)
	state, err = agents.runtime.Receipt(ctx, agents.plannerToken, receipt.MessageID)
	require(t, err == nil && state.State == DeliveryExpired, "commit did not preserve expiry: %#v, %v", state, err)
	receipt, err = agents.runtime.SendMessage(ctx, agents.plannerToken, SendMessageInput{
		To: "reviewer", Body: "Delivered before expiry", ExpiresInMS: 40,
	})
	requireNoError(t, err)
	reservation, err = agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil, "unexpected third reservation: %#v, %v", reservation, err)
	if _, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, reservation.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	count, err := agents.runtime.AcknowledgeMessages(ctx, agents.reviewerToken, []string{receipt.MessageID})
	require(t, err == nil && count == 0, "late acknowledgement won over expiry: %d, %v", count, err)
	state, err = agents.runtime.Receipt(ctx, agents.plannerToken, receipt.MessageID)
	require(t, err == nil && state.State == DeliveryExpired, "acknowledgement did not preserve expiry: %#v, %v", state, err)
}

func TestTaskClaimsRespectDependenciesAndOwnership(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	first, err := agents.runtime.AddTask(ctx, agents.plannerToken, AddTaskInput{Title: "Implement"})
	requireNoError(t, err)
	second, err := agents.runtime.AddTask(ctx, agents.plannerToken, AddTaskInput{Title: "Review", Dependencies: []string{first.ID}})
	requireNoError(t, err)
	_, err = agents.runtime.ClaimTask(ctx, agents.reviewerToken, second.ID)
	requireCode(t, err, CodeConflict)
	if _, err := agents.runtime.ClaimTask(ctx, agents.plannerToken, first.ID); err != nil {
		t.Fatal(err)
	}
	_, err = agents.runtime.CompleteTask(ctx, agents.reviewerToken, first.ID, "not mine")
	requireCode(t, err, CodeConflict)
	if _, err := agents.runtime.CompleteTask(ctx, agents.plannerToken, first.ID, "done"); err != nil {
		t.Fatal(err)
	}
	claimed, err := agents.runtime.ClaimTask(ctx, agents.reviewerToken, second.ID)
	require(t, err == nil && claimed.ClaimedBy == "reviewer", "unexpected claim: %#v, %v", claimed, err)
}

func TestScopeTaskBoardListsOnlyReadyWork(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	first, err := agents.runtime.AddTask(ctx, agents.scope.ScopeToken, AddTaskInput{
		Title: "Implement checkout retries", Description: "Keep idempotency keys across retries.",
	})
	requireNoError(t, err)
	require(t, first.CreatedBy == nil && first.Ready, "unexpected scope-created task: %#v", first)
	second, err := agents.runtime.AddTask(ctx, agents.plannerToken, AddTaskInput{
		Title: "Review checkout retries", Dependencies: []string{first.ID},
	})
	requireNoError(t, err)
	require(t, second.CreatedBy != nil && *second.CreatedBy == "planner" && !second.Ready, "unexpected agent-created task: %#v", second)
	ready, err := agents.runtime.ListTasks(ctx, agents.scope.ScopeToken, true)
	require(t, err == nil && len(ready) == 1 && ready[0].ID == first.ID, "unexpected ready tasks: %#v, %v", ready, err)
	if _, err := agents.runtime.ClaimTask(ctx, agents.scope.ScopeToken, first.ID); err == nil {
		t.Fatal("scope authority claimed a task")
	} else {
		requireCode(t, err, CodeUnauthenticated)
	}
	if _, err := agents.runtime.ClaimTask(ctx, agents.reviewerToken, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.runtime.CompleteTask(ctx, agents.reviewerToken, first.ID, "done"); err != nil {
		t.Fatal(err)
	}
	ready, err = agents.runtime.ListTasks(ctx, agents.plannerToken, true)
	require(t, err == nil && len(ready) == 1 && ready[0].ID == second.ID, "dependent task did not become ready: %#v, %v", ready, err)
}

func TestTaskReleaseAndExecutionReplacementRecoverClaims(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	task, err := agents.runtime.AddTask(ctx, agents.plannerToken, AddTaskInput{Title: "Review"})
	requireNoError(t, err)
	if _, err := agents.runtime.ClaimTask(ctx, agents.reviewerToken, task.ID); err != nil {
		t.Fatal(err)
	}
	released, err := agents.runtime.ReleaseTask(ctx, agents.reviewerToken, task.ID)
	require(t, err == nil && released.Status == "open" && released.ClaimedBy == "", "unexpected release: %#v, %v", released, err)
	if _, err := agents.runtime.ClaimTask(ctx, agents.reviewerToken, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.runtime.RegisterAgent(ctx, agents.scope.ScopeToken, RegisterAgentInput{ID: "reviewer", DisplayName: "Reviewer replacement"}); err != nil {
		t.Fatal(err)
	}
	recovered, err := agents.runtime.ClaimTask(ctx, agents.plannerToken, task.ID)
	require(t, err == nil && recovered.ClaimedBy == "planner", "stale execution claim was not recovered: %#v, %v", recovered, err)
}

func TestExecutionReplacementRetiresPreviousToken(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	replacement, err := agents.runtime.RegisterAgent(ctx, agents.scope.ScopeToken, RegisterAgentInput{ID: "planner", DisplayName: "Planner 2"})
	requireNoError(t, err)
	if replacement.ExecutionID == agents.planner.ExecutionID {
		t.Fatal("execution id was not replaced")
	}
	_, err = agents.runtime.ListPeers(ctx, agents.plannerToken)
	requireCode(t, err, CodeUnauthenticated)
	if _, err := agents.runtime.ListPeers(ctx, replacement.AgentToken); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineHeartbeatCannotClaimReadiness(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	_, err := agents.runtime.Heartbeat(context.Background(), agents.plannerToken, HeartbeatInput{
		Lifecycle: LifecycleOffline, Ready: true,
	})
	requireCode(t, err, CodeInvalidArgument)
}

func TestHealthSeparatesLivenessFromStorageReadiness(t *testing.T) {
	runtimeValue, err := Open(":memory:")
	requireNoError(t, err)
	server := NewServer(runtimeValue, ServerOptions{StartedAt: "2026-09-03T00:00:00Z"})

	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ready health returned HTTP %d", response.Code)
	}
	var ready Health
	requireNoError(t, json.Unmarshal(response.Body.Bytes(), &ready))
	require(t, ready.Status == "ready" && ready.Storage.Backend == StorageBackendSQLite && ready.Storage.Status == StorageAvailable, "unexpected ready health: %#v", ready)

	requireNoError(t, runtimeValue.Close())
	request = httptest.NewRequest(http.MethodGet, "/health", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready health returned HTTP %d", response.Code)
	}
	var unready Health
	requireNoError(t, json.Unmarshal(response.Body.Bytes(), &unready))
	require(t, unready.Status == "not_ready" && unready.Storage.Status == StorageUnavailable, "unexpected unready health: %#v", unready)

	request = httptest.NewRequest(http.MethodGet, "/health/live", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("liveness returned HTTP %d", response.Code)
	}
	var live Liveness
	requireNoError(t, json.Unmarshal(response.Body.Bytes(), &live))
	require(t, live.Status == "alive" && live.StartedAt != "", "unexpected liveness: %#v", live)
}

func TestShutdownRequiresAdminAuthority(t *testing.T) {
	ctx := context.Background()
	runtimeValue, err := Open(":memory:")
	requireNoError(t, err)
	server := NewServer(runtimeValue, ServerOptions{AdminToken: "admin-token"})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())

	err = (Client{Address: address, Token: "wrong-token"}).Shutdown(ctx)
	requireCode(t, err, CodeUnauthenticated)
	select {
	case <-server.ShutdownRequested():
		t.Fatal("unauthorized request triggered shutdown")
	default:
	}
	requireNoError(t, (Client{Address: address, Token: "admin-token"}).Shutdown(ctx))
	select {
	case <-server.ShutdownRequested():
	case <-time.After(time.Second):
		t.Fatal("authorized shutdown request was not surfaced")
	}
}

func TestHumanEscalationIsDurableAndScopeOwned(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	escalation, err := agents.runtime.AskHuman(ctx, agents.plannerToken, AskHumanInput{Question: "Deploy?", Options: []string{"yes", "no"}})
	requireNoError(t, err)
	values, err := agents.runtime.ListEscalations(ctx, agents.scope.ScopeToken)
	require(t, err == nil && len(values) == 1 && values[0].ID == escalation.ID, "unexpected escalations: %#v, %v", values, err)
	resolved, err := agents.runtime.ResolveEscalation(ctx, agents.scope.ScopeToken, escalation.ID, "no")
	require(t, err == nil && resolved.Status == "resolved" && resolved.Answer == "no", "unexpected resolution: %#v, %v", resolved, err)

	// An unknown escalation is NOT_FOUND; resolving again is CONFLICT and keeps the first answer.
	_, err = agents.runtime.ResolveEscalation(ctx, agents.scope.ScopeToken, "esc_missing", "yes")
	requireCode(t, err, CodeNotFound)
	_, err = agents.runtime.ResolveEscalation(ctx, agents.scope.ScopeToken, escalation.ID, "yes")
	requireCode(t, err, CodeConflict)
	values, err = agents.runtime.ListEscalations(ctx, agents.scope.ScopeToken)
	require(t, err == nil && len(values) == 1 && values[0].Answer == "no", "second resolution changed the answer: %#v, %v", values, err)
}

func TestPendingEscalationsApplyPerAgentBackpressure(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	var first HumanEscalation
	for i := 0; i < pendingEscalationCapPerAgent; i++ {
		value, err := agents.runtime.AskHuman(ctx, agents.plannerToken, AskHumanInput{Question: "Continue?"})
		require(t, err == nil, "escalation %d failed: %v", i, err)
		if i == 0 {
			first = value
		}
	}
	_, err := agents.runtime.AskHuman(ctx, agents.plannerToken, AskHumanInput{Question: "One too many"})
	requireCode(t, err, CodeBackpressure)
	if _, err := agents.runtime.ResolveEscalation(ctx, agents.scope.ScopeToken, first.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.runtime.AskHuman(ctx, agents.plannerToken, AskHumanInput{Question: "Replacement"}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLitePreservesAcceptedWorkAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.db")
	agents := setupAgents(t, path)
	ctx := context.Background()
	receipt, err := agents.runtime.SendMessage(ctx, agents.plannerToken, SendMessageInput{To: "reviewer", Mode: MessageRequest, Body: "Persist me"})
	requireNoError(t, err)
	requireNoError(t, agents.runtime.Close())
	restarted, err := Open(path)
	requireNoError(t, err)
	defer restarted.Close()
	reservation, err := restarted.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil && reservation.Messages[0].ID == receipt.MessageID, "message did not survive restart: %#v, %v", reservation, err)
}

func TestColdRestartPreservesIdempotencyAndRedelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.db")
	agents := setupAgents(t, path)
	ctx := context.Background()
	input := SendMessageInput{To: "reviewer", Mode: MessageRequest, Body: "Survive restart", IdempotencyKey: "restart-42"}
	sent, err := agents.runtime.SendMessage(ctx, agents.plannerToken, input)
	requireNoError(t, err)
	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil && len(reservation.Messages) == 1, "unexpected reservation: %#v, %v", reservation, err)
	delivered, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, reservation.ID)
	require(t, err == nil && len(delivered) == 1 && delivered[0].DeliveredAt != "", "unexpected delivery: %#v, %v", delivered, err)
	firstDeliveredAt := delivered[0].DeliveredAt
	requireNoError(t, agents.runtime.Close())

	restarted, err := Open(path)
	requireNoError(t, err)
	defer restarted.Close()
	planner, err := restarted.RegisterAgent(ctx, agents.scope.ScopeToken, RegisterAgentInput{ID: "planner", DisplayName: "Planner"})
	requireNoError(t, err)
	reviewer, err := restarted.RegisterAgent(ctx, agents.scope.ScopeToken, RegisterAgentInput{ID: "reviewer", DisplayName: "Reviewer", ConnectTo: []string{"planner"}})
	requireNoError(t, err)
	require(t, planner.ExecutionID != agents.planner.ExecutionID && reviewer.ExecutionID != agents.reviewer.ExecutionID, "restart registration did not create new executions")

	retry, err := restarted.SendMessage(ctx, planner.AgentToken, input)
	require(t, err == nil && retry.MessageID == sent.MessageID && retry.AcceptedAt == sent.AcceptedAt, "retry after restart did not return the original receipt: %#v, %v", retry, err)
	changed := input
	changed.Body = "Changed after restart"
	_, err = restarted.SendMessage(ctx, planner.AgentToken, changed)
	requireCode(t, err, CodeConflict)

	redelivery, err := restarted.ReserveInbox(ctx, reviewer.AgentToken, 10, 0)
	require(t, err == nil && redelivery != nil && len(redelivery.Messages) == 1 && redelivery.Messages[0].ID == sent.MessageID, "unacknowledged message was not redelivered once: %#v, %v", redelivery, err)
	recommitted, err := restarted.CommitInbox(ctx, reviewer.AgentToken, redelivery.ID)
	require(t, err == nil && len(recommitted) == 1 && recommitted[0].DeliveredAt == firstDeliveredAt && recommitted[0].CreatedAt == sent.AcceptedAt, "redelivery rewrote delivery history: %#v, %v", recommitted, err)

	count, err := restarted.AcknowledgeMessages(ctx, reviewer.AgentToken, []string{sent.MessageID})
	require(t, err == nil && count == 1, "unexpected acknowledgement: %d, %v", count, err)
	count, err = restarted.AcknowledgeMessages(ctx, reviewer.AgentToken, []string{sent.MessageID})
	require(t, err == nil && count == 0, "repeated acknowledgement was not a no-op: %d, %v", count, err)
	receipt, err := restarted.Receipt(ctx, planner.AgentToken, sent.MessageID)
	require(t, err == nil && receipt.State == DeliveryAcknowledged && receipt.DeliveredAt == firstDeliveredAt, "unexpected receipt: %#v, %v", receipt, err)
}

func TestOlderSchemaFailsBeforeServingWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	database, err := sql.Open("sqlite", path)
	requireNoError(t, err)
	if _, err := database.Exec(`PRAGMA user_version=1`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	requireNoError(t, database.Close())
	_, err = Open(path)
	require(t, err != nil && strings.Contains(err.Error(), "database schema 1 does not match 9"), "older schema did not fail clearly: %v", err)
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func TestPeerResolutionUsesExactCaseSensitiveAgentIDs(t *testing.T) {
	ctx := context.Background()
	runtimeValue, err := Open(":memory:")
	requireNoError(t, err)
	defer runtimeValue.Close()
	scope, err := runtimeValue.CreateScope(ctx, CreateScopeInput{ID: "peer-resolution"})
	requireNoError(t, err)
	sender, err := runtimeValue.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "sender", DisplayName: "Sender"})
	requireNoError(t, err)
	peers := []RegisterAgentInput{
		{ID: "Reviewer", DisplayName: "Primary", ConnectTo: []string{"sender"}},
		{ID: "reviewer", DisplayName: "Secondary", ConnectTo: []string{"sender"}},
		{ID: "attacker", DisplayName: "Reviewer", ConnectTo: []string{"sender"}},
		{ID: "writer-1", DisplayName: "Writer", ConnectTo: []string{"sender"}},
		{ID: "writer-2", DisplayName: "writer", ConnectTo: []string{"sender"}},
		{ID: "architect", DisplayName: "Architecture", ConnectTo: []string{"sender"}},
	}
	for _, input := range peers {
		if _, err := runtimeValue.RegisterAgent(ctx, scope.ScopeToken, input); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runtimeValue.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "unlinked", DisplayName: "Unlinked"}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(runtimeValue, ServerOptions{})

	resolved, err := server.resolvePeer(ctx, sender.AgentToken, "Reviewer")
	require(t, err == nil && resolved.ID == "Reviewer", "exact agent ID did not win over a matching display name: %#v, %v", resolved, err)
	resolved, err = server.resolvePeer(ctx, sender.AgentToken, "reviewer")
	require(t, err == nil && resolved.ID == "reviewer", "case-distinct agent ID did not resolve exactly: %#v, %v", resolved, err)
	resolved, err = server.resolvePeer(ctx, sender.AgentToken, "PRIMARY")
	require(t, err == nil && resolved.ID == "Reviewer", "case-insensitive exact display name did not resolve: %#v, %v", resolved, err)
	_, err = server.resolvePeer(ctx, sender.AgentToken, "Writer")
	requireCode(t, err, CodeConflict)
	_, err = server.resolvePeer(ctx, sender.AgentToken, "Arch")
	requireCode(t, err, CodeNotFound)
	_, err = server.resolvePeer(ctx, sender.AgentToken, "not-a-peer")
	requireCode(t, err, CodeNotFound)
	_, err = server.resolvePeer(ctx, sender.AgentToken, "unlinked")
	requireCode(t, err, CodeNotFound)
}

func TestAgentSessionHeartbeatsAndStops(t *testing.T) {
	ctx := context.Background()
	runtimeValue, err := Open(":memory:")
	requireNoError(t, err)
	server := NewServer(runtimeValue, ServerOptions{})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	scope, err := runtimeValue.CreateScope(ctx, CreateScopeInput{ID: "managed-session"})
	requireNoError(t, err)
	session, err := StartAgentSession(ctx, AgentSessionOptions{
		Address: address, ScopeToken: scope.ScopeToken,
		Registration:      RegisterAgentInput{ID: "worker", DisplayName: "Worker", LeaseMS: 30000},
		HeartbeatInterval: 10 * time.Millisecond,
	})
	requireNoError(t, err)
	owner := Client{Address: address, Token: scope.ScopeToken}
	agents, err := owner.ListAgents(ctx)
	require(t, err == nil && len(agents) == 1 && !agents[0].Ready && agents[0].Reachable && agents[0].Lifecycle == LifecycleStarting, "managed agent did not start conservatively: %#v, %v", agents, err)
	if _, err := session.SetState(ctx, LifecycleReady, true); err != nil {
		t.Fatal(err)
	}
	agents, err = owner.ListAgents(ctx)
	require(t, err == nil && len(agents) == 1 && agents[0].Ready && agents[0].Lifecycle == LifecycleReady, "managed agent state did not update: %#v, %v", agents, err)
	firstUpdate := agents[0].UpdatedAt
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		agents, err = owner.ListAgents(ctx)
		requireNoError(t, err)
		if agents[0].UpdatedAt != firstUpdate {
			break
		}
	}
	if agents[0].UpdatedAt == firstUpdate {
		t.Fatal("managed agent did not heartbeat")
	}
	closeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	requireNoError(t, session.Close(closeCtx))
	agents, err = owner.ListAgents(ctx)
	require(t, err == nil && len(agents) == 1 && !agents[0].Ready && !agents[0].Reachable && agents[0].Lifecycle == LifecycleOffline, "managed agent did not stop cleanly: %#v, %v", agents, err)
}

func TestAgentSessionReportsExecutionReplacement(t *testing.T) {
	ctx := context.Background()
	runtimeValue, err := Open(":memory:")
	requireNoError(t, err)
	server := NewServer(runtimeValue, ServerOptions{})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	scope, err := runtimeValue.CreateScope(ctx, CreateScopeInput{ID: "replaced-session"})
	requireNoError(t, err)
	session, err := StartAgentSession(ctx, AgentSessionOptions{
		Address: address, ScopeToken: scope.ScopeToken,
		Registration:      RegisterAgentInput{ID: "worker", DisplayName: "Worker", LeaseMS: 30000},
		HeartbeatInterval: 10 * time.Millisecond,
	})
	requireNoError(t, err)
	owner := Client{Address: address, Token: scope.ScopeToken}
	if _, err := owner.RegisterAgent(ctx, RegisterAgentInput{ID: "worker", DisplayName: "Replacement", LeaseMS: 30000}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("managed session did not detect execution replacement")
	}
	requireCode(t, session.Err(), CodeUnauthenticated)
}

func TestHTTPAndMCPUseTheSameAgentAuthority(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	adminToken, err := randomValue(32)
	requireNoError(t, err)
	server := NewServer(agents.runtime, ServerOptions{AdminToken: adminToken})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := Client{Address: address, Token: agents.plannerToken}
	peers, err := client.ListPeers(ctx)
	require(t, err == nil && len(peers) == 1 && peers[0].ID == "reviewer", "unexpected HTTP peers: %#v, %v", peers, err)
	_, err = (Client{Address: address, Token: "invalid"}).ListPeers(ctx)
	requireCode(t, err, CodeUnauthenticated)
	plannerClient := Client{Address: address, Token: agents.plannerToken}
	reviewerClient := Client{Address: address, Token: agents.reviewerToken}
	ownerClient := Client{Address: address, Token: agents.scope.ScopeToken}
	outputStream, err := ownerClient.CreateOutputStream(ctx, CreateOutputStreamInput{
		Name: "mcp-output", PublisherAgentIDs: []string{agents.planner.AgentID},
	})
	requireNoError(t, err)
	escalation, err := plannerClient.AskHuman(ctx, AskHumanInput{Question: "Proceed?"})
	requireNoError(t, err)
	escalations, err := ownerClient.ListEscalations(ctx)
	require(t, err == nil && len(escalations) == 1 && escalations[0].ID == escalation.ID, "scope escalation route failed: %#v, %v", escalations, err)
	if _, err := ownerClient.ResolveEscalation(ctx, escalation.ID, "yes"); err != nil {
		t.Fatal(err)
	}
	_, err = plannerClient.ListEscalations(ctx)
	requireCode(t, err, CodePermissionDenied)
	receipt, err := plannerClient.SendMessage(ctx, SendMessageInput{To: "reviewer", Body: "Reserve me"})
	requireNoError(t, err)
	reservation, err := reviewerClient.ReserveInbox(ctx, 10, 0)
	require(t, err == nil && reservation != nil, "unexpected HTTP reservation: %#v, %v", reservation, err)
	requireNoError(t, reviewerClient.ReleaseInbox(ctx, reservation.ID))
	reservation, err = reviewerClient.ReserveInbox(ctx, 10, 0)
	require(t, err == nil && reservation != nil && reservation.Messages[0].ID == receipt.MessageID, "released inbox was not available again: %#v, %v", reservation, err)
	delivered, err := reviewerClient.CommitInbox(ctx, reservation.ID)
	require(t, err == nil && len(delivered) == 1, "unexpected committed inbox: %#v, %v", delivered, err)
	if _, err := reviewerClient.AcknowledgeMessages(ctx, []string{receipt.MessageID}); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: bearerTransport{token: agents.plannerToken, base: http.DefaultTransport}, Timeout: 10 * time.Second}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: address + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	requireNoError(t, err)
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 15 {
		t.Fatalf("unexpected tools: %d, %v", len(tools.Tools), err)
	}
	for _, input := range []*mcp.CallToolParams{
		{Name: "acknowledge_messages", Arguments: map[string]any{"messageIds": "[]"}},
		{Name: "add_task", Arguments: map[string]any{"title": "strict daemon", "dependencies": "[]"}},
	} {
		result, err := session.CallTool(ctx, input)
		require(t, err != nil || result.IsError, "direct MCP accepted a stringified array")
	}
	authorized, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "message_receipt", Arguments: map[string]any{"messageId": receipt.MessageID}})
	require(t, err == nil && !authorized.IsError, "sender receipt lookup failed: %v", err)
	unrelated, err := ownerClient.RegisterAgent(ctx, RegisterAgentInput{ID: "unrelated", DisplayName: "Unrelated", ConnectTo: []string{"reviewer"}})
	requireNoError(t, err)
	privateReceipt, err := (Client{Address: address, Token: unrelated.AgentToken}).SendMessage(ctx, SendMessageInput{To: "reviewer", Body: "Not the planner's message"})
	requireNoError(t, err)
	denied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "message_receipt", Arguments: map[string]any{"messageId": privateReceipt.MessageID}})
	require(t, err != nil || denied.IsError, "unrelated agent inspected a private receipt")
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_peers", Arguments: map[string]any{}})
	require(t, err == nil && !result.IsError, "MCP list_peers failed: %#v, %v", result, err)
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("MCP list_peers structured content must be an object: %#v", result.StructuredContent)
	}
	if _, ok := structured["peers"].([]any); !ok {
		t.Fatalf("MCP list_peers must return a peers array: %#v", result.StructuredContent)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	require(t, err == nil && !result.IsError, "MCP list_tasks failed: %#v, %v", result, err)
	structured, ok = result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("MCP list_tasks structured content must be an object: %#v", result.StructuredContent)
	}
	if _, ok := structured["tasks"].([]any); !ok {
		t.Fatalf("MCP list_tasks must return a tasks array: %#v", result.StructuredContent)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "publish_output", Arguments: map[string]any{
		"streamId": outputStream.ID, "contentType": "application/json", "value": map[string]any{"status": "ready"},
	}})
	require(t, err == nil && !result.IsError, "MCP publish_output failed: %#v, %v", result, err)
	latest, err := ownerClient.LatestOutput(ctx, outputStream.ID)
	require(t, err == nil && latest != nil && latest.Sequence == 1, "MCP output was not stored: %#v, %v", latest, err)
	type callResult struct {
		result *mcp.CallToolResult
		err    error
	}
	waiting := make(chan callResult, 1)
	go func() {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "check_inbox", Arguments: map[string]any{"waitMs": 2000}})
		waiting <- callResult{result: result, err: err}
	}()
	time.Sleep(50 * time.Millisecond)
	wakeReceipt, err := reviewerClient.SendMessage(ctx, SendMessageInput{To: "planner", Body: "Wake MCP"})
	requireNoError(t, err)
	select {
	case call := <-waiting:
		if call.err != nil || call.result.IsError {
			t.Fatalf("MCP check_inbox failed: %#v, %v", call.result, call.err)
		}
		structured, ok := call.result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("MCP check_inbox structured content must be an object: %#v", call.result.StructuredContent)
		}
		messages, ok := structured["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("MCP check_inbox must return one message: %#v", call.result.StructuredContent)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := plannerClient.AcknowledgeMessages(ctx, []string{wakeReceipt.MessageID}); err != nil {
		t.Fatal(err)
	}
}

func TestServerWithoutAdminCredentialCannotCreateScopes(t *testing.T) {
	runtimeValue, err := Open(":memory:")
	requireNoError(t, err)
	server := NewServer(runtimeValue, ServerOptions{})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = (Client{Address: address, Token: " "}).CreateScope(ctx, CreateScopeInput{ID: "forbidden"})
	requireCode(t, err, CodeUnauthenticated)
}

func TestHTTPRoutesReturnDeterministicNotFoundAndMethodErrors(t *testing.T) {
	runtimeValue, err := Open(":memory:")
	requireNoError(t, err)
	server := NewServer(runtimeValue, ServerOptions{})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())

	for _, test := range []struct {
		method, path string
		status       int
		code         ErrorCode
		allow        string
	}{
		{http.MethodHead, "/health", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodGet},
		{http.MethodPost, "/health", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodGet},
		{http.MethodGet, "/v1/admin/shutdown", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodGet, "/v1/scopes", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodDelete, "/v1/agents", http.StatusMethodNotAllowed, CodeMethodNotAllowed, "GET, POST"},
		{http.MethodGet, "/v1/links", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodGet, "/v1/me/heartbeat", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPatch},
		{http.MethodPost, "/v1/peers", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodGet},
		{http.MethodGet, "/v1/messages", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodGet, "/v1/messages/ack", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodPost, "/v1/messages/message-1", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodGet},
		{http.MethodGet, "/v1/inbox/reserve", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodGet, "/v1/inbox/reservation-1/commit", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodGet, "/v1/inbox/reservation-1/release", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodDelete, "/v1/tasks", http.StatusMethodNotAllowed, CodeMethodNotAllowed, "GET, POST"},
		{http.MethodGet, "/v1/tasks/task-1/claim", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodGet, "/v1/tasks/task-1/release", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodGet, "/v1/tasks/task-1/complete", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodGet, "/v1/escalations", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodPost, "/v1/escalations/escalation-1", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodGet},
		{http.MethodPost, "/v1/scope/escalations", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodGet},
		{http.MethodGet, "/v1/scope/escalations/escalation-1/resolve", http.StatusMethodNotAllowed, CodeMethodNotAllowed, http.MethodPost},
		{http.MethodGet, "/v1/unknown", http.StatusNotFound, CodeNotFound, ""},
		{http.MethodGet, "/v1/messages/message-1/extra", http.StatusNotFound, CodeNotFound, ""},
	} {
		request, err := http.NewRequest(test.method, address+test.path, nil)
		requireNoError(t, err)
		response, err := http.DefaultClient.Do(request)
		requireNoError(t, err)
		var payload struct {
			OK    bool `json:"ok"`
			Error struct {
				Code ErrorCode `json:"code"`
			} `json:"error"`
		}
		var decodeErr error
		if test.method != http.MethodHead {
			decodeErr = json.NewDecoder(response.Body).Decode(&payload)
		}
		response.Body.Close()
		wrongCode := test.method != http.MethodHead && payload.Error.Code != test.code
		if decodeErr != nil || payload.OK || response.StatusCode != test.status || wrongCode || response.Header.Get("Allow") != test.allow || response.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected %s %s response: status=%d code=%s allow=%q decode=%v", test.method, test.path, response.StatusCode, payload.Error.Code, response.Header.Get("Allow"), decodeErr)
		}
	}
}

func TestDaemonOwnsOneRunFile(t *testing.T) {
	root := t.TempDir()
	paths := DaemonPaths{
		DataDir: filepath.Join(root, "data"), RuntimeDir: filepath.Join(root, "run"),
		Database: filepath.Join(root, "data", "bus.db"), RunFile: filepath.Join(root, "run", "bus.json"),
		LockFile: filepath.Join(root, "run", "bus.lock"),
	}
	daemon, err := StartDaemon(context.Background(), 0, &paths)
	requireNoError(t, err)
	defer daemon.Stop(context.Background())
	run, err := ReadRunFile(paths.RunFile)
	require(t, err == nil && run.AdminToken != "" && run.PID == os.Getpid(), "unexpected run file: %#v, %v", run, err)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(paths.RunFile)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("run file permissions are %v, %v", info.Mode().Perm(), err)
		}
	}
	_, err = StartDaemon(context.Background(), 0, &paths)
	if err == nil {
		t.Fatal("second daemon unexpectedly started")
	}
	requireNoError(t, daemon.Stop(context.Background()))
	if _, err := os.Stat(paths.RunFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run file was not removed: %v", err)
	}
}

func TestDaemonRejectsSymlinkedPrivateDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link creation requires additional Windows privileges")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	requireNoError(t, os.Mkdir(target, 0o700))
	runtimeDir := filepath.Join(root, "run")
	requireNoError(t, os.Symlink(target, runtimeDir))
	paths := DaemonPaths{
		DataDir: filepath.Join(root, "data"), RuntimeDir: runtimeDir,
		Database: filepath.Join(root, "data", "bus.db"), RunFile: filepath.Join(runtimeDir, "bus.json"),
		LockFile: filepath.Join(runtimeDir, "bus.lock"),
	}
	if _, err := StartDaemon(context.Background(), 0, &paths); err == nil {
		t.Fatal("daemon accepted a symlinked runtime directory")
	}
}

func TestOmarchyManifestPointsToHeadlessService(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "manifest.json"))
	requireNoError(t, err)
	var manifest struct {
		Kinds       []string          `json:"kinds"`
		EntryPoints map[string]string `json:"entryPoints"`
	}
	requireNoError(t, json.Unmarshal(data, &manifest))
	if len(manifest.Kinds) != 1 || manifest.Kinds[0] != "service" {
		t.Fatalf("unexpected plugin kinds: %#v", manifest.Kinds)
	}
	service := filepath.Join("..", manifest.EntryPoints["service"])
	if info, err := os.Stat(service); err != nil || info.IsDir() {
		t.Fatalf("service entry point is missing: %v", err)
	}
	qml, err := os.ReadFile(service)
	requireNoError(t, err)
	text := string(qml)
	if strings.Contains(text, "--foreground") || !strings.Contains(text, "healthyTimer") {
		t.Fatal("service lifecycle flags or restart backoff are stale")
	}
}
