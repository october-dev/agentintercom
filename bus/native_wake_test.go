package bus

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func exerciseNativeWake(t *testing.T, r *Runtime) {
	t.Helper()
	ctx := context.Background()
	scope, err := r.CreateScope(ctx, CreateScopeInput{})
	requireNoError(t, err)
	defer r.DeleteScope(context.Background(), scope.ScopeID)
	sender, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "sender", DisplayName: "Sender"})
	requireNoError(t, err)
	receiver, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "receiver", DisplayName: "Receiver", ConnectTo: []string{"sender"}})
	requireNoError(t, err)
	message, err := r.SendMessage(ctx, sender.AgentToken, SendMessageInput{To: "receiver", Body: "Wake up", Mode: MessageRequest})
	requireNoError(t, err)
	results := make(chan WakeBatch, 2)
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batch, err := r.ClaimWake(ctx, receiver.AgentToken, "claude-socket", 0)
			results <- batch
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		requireNoError(t, err)
	}
	var claimed WakeBatch
	count := 0
	for batch := range results {
		if len(batch.MessageIDs) > 0 {
			claimed = batch
			count++
		}
	}
	require(t, count == 1 && claimed.MessageIDs[0] == message.MessageID, "concurrent wake dispatch duplicated: %d", count)
	requireCode(t, r.RetryWake(ctx, receiver.AgentToken, []string{message.MessageID}), CodeConflict)
	// Native dispatch did not reserve, deliver or acknowledge the actual message.
	receipt, err := r.Receipt(ctx, sender.AgentToken, message.MessageID)
	requireNoError(t, err)
	require(t, receipt.State == DeliveryQueued, "wake claim consumed the inbox")
	requireNoError(t, r.FinishWake(ctx, receiver.AgentToken, claimed.AttemptID, "submitted"))
	requireNoError(t, r.FinishWake(ctx, receiver.AgentToken, claimed.AttemptID, "submitted"))
	receipt, err = r.Receipt(ctx, sender.AgentToken, message.MessageID)
	requireNoError(t, err)
	require(t, receipt.State == DeliveryQueued, "socket submission became model delivery")
	status, err := r.WakeStatus(ctx, receiver.AgentToken)
	requireNoError(t, err)
	require(t, status.Connected && status.LastOutcome == "submitted" && status.Pending == 1, "wake status inaccurate: %+v", status)
	// Explicit retry releases only the selected notification attempt.
	requireNoError(t, r.RetryWake(ctx, receiver.AgentToken, []string{message.MessageID}))
	store := r.store.(*Store)
	_, err = store.db.ExecContext(ctx, "UPDATE wake_connections SET last_attempt_at=0 WHERE scope_id=?", scope.ScopeID)
	requireNoError(t, err)
	retry, err := r.ClaimWake(ctx, receiver.AgentToken, "claude-socket", 0)
	requireNoError(t, err)
	require(t, len(retry.MessageIDs) == 1 && retry.AttemptID != claimed.AttemptID, "retry did not create a new attempt")
	// Crash after claim/write: the stale boundary becomes uncertain, never a send.
	_, err = store.db.ExecContext(ctx, "UPDATE wake_attempts SET updated_at=? WHERE scope_id=?", nowMillis()-60000, scope.ScopeID)
	requireNoError(t, err)
	_, err = store.db.ExecContext(ctx, "UPDATE wake_connections SET last_attempt_at=0 WHERE scope_id=?", scope.ScopeID)
	requireNoError(t, err)
	afterCrash, err := r.ClaimWake(ctx, receiver.AgentToken, "claude-socket", 0)
	requireNoError(t, err)
	require(t, len(afterCrash.MessageIDs) == 0, "ambiguous delivery was automatically replayed")
	status, err = r.WakeStatus(ctx, receiver.AgentToken)
	requireNoError(t, err)
	require(t, status.Uncertain == 1, "uncertain state was hidden")
	reservation, err := r.ReserveInbox(ctx, receiver.AgentToken, 10, 0)
	requireNoError(t, err)
	require(t, reservation != nil && len(reservation.Messages) == 1, "wake attempt lost durable inbox")
	_, err = r.CommitInbox(ctx, receiver.AgentToken, reservation.ID)
	requireNoError(t, err)
	_, err = r.AcknowledgeMessages(ctx, receiver.AgentToken, []string{message.MessageID})
	requireNoError(t, err)
	status, err = r.WakeStatus(ctx, receiver.AgentToken)
	requireNoError(t, err)
	require(t, status.Uncertain == 0 && status.Pending == 0, "acknowledged work stayed uncertain")
	next, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "receiver", DisplayName: "Replacement"})
	requireNoError(t, err)
	_, err = r.ClaimWake(ctx, receiver.AgentToken, "claude-socket", 0)
	requireCode(t, err, CodeUnauthenticated)
	requireCode(t, r.FinishWake(ctx, next.AgentToken, claimed.AttemptID, "submitted"), CodeNotFound)
}

func TestNativeWakeDurableOutcomes(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "bus.db"))
	requireNoError(t, err)
	defer r.Close()
	exerciseNativeWake(t, r)
}
func TestPostgresNativeWakeDurableOutcomes(t *testing.T) {
	_, a := hostedTestAccounts(t)
	exerciseNativeWake(t, a.runtime)
}

func TestNativeWakeLongPollAndRevocation(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := make(chan WakeBatch, 1)
	fail := make(chan error, 1)
	go func() {
		batch, err := a.runtime.ClaimWake(ctx, a.reviewerToken, "claude-socket", 2*time.Second)
		ready <- batch
		fail <- err
	}()
	time.Sleep(20 * time.Millisecond)
	sent, err := a.runtime.SendMessage(ctx, a.plannerToken, SendMessageInput{To: "reviewer", Body: "Arrived during wait"})
	requireNoError(t, err)
	batch := <-ready
	requireNoError(t, <-fail)
	require(t, len(batch.MessageIDs) == 1 && batch.MessageIDs[0] == sent.MessageID, "long poll missed arrival")
	requireNoError(t, a.runtime.FinishWake(ctx, a.reviewerToken, batch.AttemptID, "submitted"))
	go func() {
		_, err := a.runtime.ClaimWake(ctx, a.reviewerToken, "claude-socket", 2*time.Second)
		fail <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_, err = a.runtime.RotateScopeToken(ctx, a.scope.ScopeID)
	requireNoError(t, err)
	requireCode(t, <-fail, CodeUnauthenticated)
}

func exerciseWorkspaceAutoLink(t *testing.T, r *Runtime) {
	ctx := context.Background()
	scope, err := r.CreateScope(ctx, CreateScopeInput{})
	requireNoError(t, err)
	defer r.DeleteScope(ctx, scope.ScopeID)
	const count = 8
	registrations := make(chan RegisterAgentResult, count)
	failures := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: fmt.Sprintf("session-%d", i), DisplayName: "Concurrent window", ConnectWorkspace: true})
			registrations <- result
			failures <- err
		}()
	}
	wg.Wait()
	close(registrations)
	close(failures)
	for err := range failures {
		requireNoError(t, err)
	}
	for result := range registrations {
		peers, err := r.ListPeers(ctx, result.AgentToken)
		requireNoError(t, err)
		require(t, len(peers) == count-1, "concurrent registration lost workspace links: %d", len(peers))
	}
}
func TestWorkspaceConcurrentAutoLink(t *testing.T) {
	r, err := Open(":memory:")
	requireNoError(t, err)
	defer r.Close()
	exerciseWorkspaceAutoLink(t, r)
}
func TestPostgresWorkspaceConcurrentAutoLink(t *testing.T) {
	_, a := hostedTestAccounts(t)
	exerciseWorkspaceAutoLink(t, a.runtime)
}
