package bus

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgresStatementBinding(t *testing.T) {
	query := `SELECT '?', 'it''s ?', "?", value FROM items WHERE a=? AND b=?`
	expected := `SELECT '?', 'it''s ?', "?", value FROM items WHERE a=$1 AND b=$2`
	require(t, sqlStatement(StorageBackendPostgres, query) == expected, "placeholder rewriting changed a quoted literal")
	require(t, sqlStatement(StorageBackendSQLite, query) == query, "SQLite query was changed")
	args := []any{true, false, "hello", 42}
	converted := sqlArguments(StorageBackendPostgres, args)
	require(t, converted[0] == 1 && converted[1] == 0 && args[0] == true, "flag binding mutated caller arguments")
}

func TestPostgresRuntimeContracts(t *testing.T) {
	dsn := os.Getenv("OCTOBER_BUS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set OCTOBER_BUS_TEST_POSTGRES_URL to an isolated local october_bus_test database")
	}
	u, err := url.Parse(dsn)
	require(t, err == nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") && u.Path == "/october_bus_test", "PostgreSQL tests require an isolated loopback october_bus_test database")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	requireNoError(t, MigratePostgres(ctx, dsn))
	requireNoError(t, MigratePostgres(ctx, dsn)) // Safe deployment retry.
	first, err := OpenPostgres(ctx, dsn)
	requireNoError(t, err)
	defer first.Close()
	second, err := OpenPostgres(ctx, dsn)
	requireNoError(t, err)
	defer second.Close()
	id, err := randomID("pg_")
	requireNoError(t, err)
	scope, err := first.CreateScope(ctx, CreateScopeInput{ID: id})
	requireNoError(t, err)
	defer first.DeleteScope(context.Background(), scope.ScopeID)
	a, err := first.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "a", DisplayName: "First"})
	requireNoError(t, err)
	b, err := second.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "b", DisplayName: "Second", ConnectTo: []string{"a"}})
	requireNoError(t, err)
	input := SendMessageInput{To: "b", Mode: MessageRequest, Body: "Unicode: 日本語 — 🚌 and a literal \\x00", IdempotencyKey: "retry-1"}

	// Concurrent identical sends across independent runtimes must commit once.
	var wg sync.WaitGroup
	results := make(chan DeliveryReceipt, 12)
	errors := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := first
			if i%2 == 1 {
				r = second
			}
			result, err := r.SendMessage(ctx, a.AgentToken, input)
			results <- result
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		requireNoError(t, err)
	}
	messageID := ""
	for result := range results {
		if messageID == "" {
			messageID = result.MessageID
		}
		require(t, result.MessageID == messageID, "retry created a second message")
	}
	input.Body = "different"
	_, err = second.SendMessage(ctx, a.AgentToken, input)
	requireCode(t, err, CodeConflict)
	reservation, err := second.ReserveInbox(ctx, b.AgentToken, 10, 0)
	require(t, err == nil && reservation != nil && len(reservation.Messages) == 1, "unexpected inbox: %#v %v", reservation, err)
	messages, err := second.CommitInbox(ctx, b.AgentToken, reservation.ID)
	require(t, err == nil && len(messages) == 1 && strings.Contains(messages[0].Body, "日本語"), "delivery lost text: %v", err)
	_, err = second.AcknowledgeMessages(ctx, b.AgentToken, []string{messageID})
	requireNoError(t, err)
	reply, err := second.SendMessage(ctx, b.AgentToken, SendMessageInput{To: "a", Mode: MessageResponse, ResponseTo: messageID, Body: "Done", IdempotencyKey: "reply"})
	requireNoError(t, err)
	receipt, err := first.Receipt(ctx, a.AgentToken, messageID)
	require(t, err == nil && receipt.ResponseMessageID == reply.MessageID, "reply receipt was not persisted: %v", err)

	task, err := first.AddTask(ctx, scope.ScopeToken, AddTaskInput{Title: "One owner"})
	requireNoError(t, err)
	claimErrors := make(chan error, 2)
	for _, token := range []string{a.AgentToken, b.AgentToken} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := second.ClaimTask(ctx, token, task.ID)
			claimErrors <- err
		}()
	}
	wg.Wait()
	close(claimErrors)
	success := 0
	for err := range claimErrors {
		if err == nil {
			success++
		} else {
			requireCode(t, err, CodeConflict)
		}
	}
	require(t, success == 1, "task was claimed by %d executions", success)
	stream, err := first.CreateOutputStream(ctx, scope.ScopeToken, CreateOutputStreamInput{Name: "status"})
	requireNoError(t, err)
	_, err = first.SetOutputPublisher(ctx, scope.ScopeToken, stream.ID, a.AgentID, true)
	requireNoError(t, err)
	_, err = second.PublishOutput(ctx, a.AgentToken, stream.ID, PublishOutputInput{ContentType: OutputText, Value: "ready"})
	requireNoError(t, err)
	latest, err := first.LatestOutput(ctx, scope.ScopeToken, stream.ID)
	require(t, err == nil && latest != nil && latest.Value == "ready", "output unavailable: %v", err)
	publication, err := first.CreateAgentCardPublication(ctx, scope.ScopeToken, PublishAgentCardInput{AgentID: a.AgentID})
	requireNoError(t, err)
	_, err = second.SetAgentCardPublicationEnabled(ctx, scope.ScopeToken, publication.ID, false)
	requireNoError(t, err)
	_, err = first.StorageSummary(ctx, scope.ScopeToken)
	requireNoError(t, err)

	// A second server's commit must wake an existing long poll within the
	// bounded database polling interval, without needing a shared event broker.
	polled := make(chan *InboxReservation, 1)
	pollErrors := make(chan error, 1)
	go func() {
		result, err := first.ReserveInbox(ctx, b.AgentToken, 10, 5000)
		polled <- result
		pollErrors <- err
	}()
	time.Sleep(100 * time.Millisecond)
	_, err = second.SendMessage(ctx, a.AgentToken, SendMessageInput{To: "b", Body: "arrived on another server"})
	requireNoError(t, err)
	require(t, <-polled != nil, "cross-server long poll missed a committed message")
	requireNoError(t, <-pollErrors)

	// Fencing still applies after a different server replaces the execution.
	replacement, err := second.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "a", DisplayName: "Replacement"})
	requireNoError(t, err)
	_, err = first.SendMessage(ctx, a.AgentToken, SendMessageInput{To: "b", Body: "stale"})
	requireCode(t, err, CodeUnauthenticated)
	err = first.RetireAgent(ctx, replacement.AgentToken)
	requireNoError(t, err)
	err = second.RetireAgent(ctx, b.AgentToken)
	requireNoError(t, err)
	archive, err := first.ExportScope(ctx, scope.ScopeID)
	requireNoError(t, err)
	local, err := Open(":memory:")
	requireNoError(t, err)
	defer local.Close()
	_, err = local.ImportScope(ctx, archive)
	requireNoError(t, err)

	health, _ := json.Marshal(first.StorageHealth(ctx))
	require(t, strings.Contains(string(health), "postgres") && !strings.Contains(string(health), "127.0.0.1"), "health leaked connection information")
}
