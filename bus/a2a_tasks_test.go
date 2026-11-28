package bus

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setupA2APrincipal(t *testing.T, agents testAgents) (agentCardPublication, IssuedA2APrincipal) {
	t.Helper()
	ctx := context.Background()
	publication, err := agents.runtime.CreateAgentCardPublication(ctx, agents.scope.ScopeToken, PublishAgentCardInput{AgentID: agents.reviewer.AgentID})
	requireNoError(t, err)
	principal, err := agents.runtime.CreateA2APrincipal(ctx, agents.scope.ScopeToken, CreateA2APrincipalInput{PublicationID: publication.ID, Label: "Remote caller"})
	requireNoError(t, err)
	return publication, principal
}

func TestA2ATaskCorrelationIsDurableAndIdempotent(t *testing.T) {
	database := filepath.Join(t.TempDir(), "bus.db")
	agents := setupAgents(t, database)
	ctx := context.Background()
	publication, issued := setupA2APrincipal(t, agents)
	input := AcceptA2AMessageInput{ClientMessageID: "remote-message-1", Body: "Review this change"}
	task, err := agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, input)
	requireNoError(t, err)
	require(t, task.ID != "" && task.ContextID != "" && task.State == A2ATaskSubmitted && task.PrincipalID == issued.Principal.ID && len(task.Messages) == 1, "unexpected A2A task: %#v", task)
	correlation := task.Messages[0]
	require(t, correlation.ClientMessageID == input.ClientMessageID && correlation.BusRequestMessageID != "" && correlation.BusResponseMessageID == "", "unexpected message correlation: %#v", correlation)
	retry, err := agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, input)
	require(t, err == nil && retry.ID == task.ID && retry.Messages[0].BusRequestMessageID == correlation.BusRequestMessageID, "idempotent retry created different work: %#v, %v", retry, err)
	input.Body = "Review something else"
	_, err = agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, input)
	requireCode(t, err, CodeConflict)
	requireNoError(t, agents.runtime.Close())
	restarted, err := Open(database)
	requireNoError(t, err)
	defer restarted.Close()
	stored, _, err := restarted.A2ATaskHistory(ctx, issued.Credential, publication.ID, task.ID)
	require(t, err == nil && stored.ID == task.ID && len(stored.Messages) == 1 && stored.Messages[0].BusRequestMessageID == correlation.BusRequestMessageID, "correlation did not survive restart: %#v, %v", stored, err)
}

func TestA2ATaskTracksBusDeliveryAndResponse(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	publication, issued := setupA2APrincipal(t, agents)
	task, err := agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "remote-message-1", Body: "Review this change"})
	requireNoError(t, err)
	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil && len(reservation.Messages) == 1, "unexpected inbox: %#v, %v", reservation, err)
	inbound := reservation.Messages[0]
	require(t, inbound.FromKind == MessageParticipantA2APrincipal && inbound.From == issued.Principal.ID && inbound.ToKind == MessageParticipantAgent, "unexpected A2A Bus message: %#v", inbound)
	if _, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, reservation.ID); err != nil {
		t.Fatal(err)
	}
	working, _, err := agents.runtime.A2ATaskHistory(ctx, issued.Credential, publication.ID, task.ID)
	require(t, err == nil && working.State == A2ATaskWorking, "delivery did not advance task: %#v, %v", working, err)
	reply, err := agents.runtime.SendMessage(ctx, agents.reviewerToken, SendMessageInput{
		To: issued.Principal.ID, Mode: MessageResponse, ResponseTo: inbound.ID, Body: "Found one issue", IdempotencyKey: "review-reply",
	})
	requireNoError(t, err)
	require(t, reply.State == DeliveryAcknowledged, "A2A response was not committed durably: %#v", reply)
	completed, _, err := agents.runtime.A2ATaskHistory(ctx, issued.Credential, publication.ID, task.ID)
	require(t, err == nil && completed.State == A2ATaskCompleted && completed.Messages[0].BusResponseMessageID == reply.MessageID, "response did not complete task: %#v, %v", completed, err)
}

func TestA2ATaskAllowsFollowUpOnlyBeforeTerminalState(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	publication, issued := setupA2APrincipal(t, agents)
	task, err := agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "turn-1", Body: "Start"})
	requireNoError(t, err)
	principal, err := agents.runtime.AuthenticateA2APrincipal(ctx, issued.Credential, publication.ID)
	requireNoError(t, err)
	if _, err := sqliteStore(t, agents.runtime).SetA2ATaskState(ctx, principal, task.ID, A2ATaskInputRequired); err != nil {
		t.Fatal(err)
	}
	followUp, err := agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, AcceptA2AMessageInput{
		TaskID: task.ID, ContextID: task.ContextID, ClientMessageID: "turn-2", Body: "Here is the answer",
	})
	require(t, err == nil && followUp.State == A2ATaskWorking && len(followUp.Messages) == 2, "follow-up was not correlated: %#v, %v", followUp, err)
	if _, err := sqliteStore(t, agents.runtime).SetA2ATaskState(ctx, principal, task.ID, A2ATaskCanceled); err != nil {
		t.Fatal(err)
	}
	_, err = agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, AcceptA2AMessageInput{
		TaskID: task.ID, ClientMessageID: "turn-3", Body: "Too late",
	})
	requireCode(t, err, CodeConflict)
}

func TestA2ATaskIsIsolatedByPrincipal(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	publication, owner := setupA2APrincipal(t, agents)
	other, err := agents.runtime.CreateA2APrincipal(ctx, agents.scope.ScopeToken, CreateA2APrincipalInput{PublicationID: publication.ID, Label: "Other caller"})
	requireNoError(t, err)
	task, err := agents.runtime.AcceptA2AMessage(ctx, owner.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "private-turn", Body: "Private work"})
	requireNoError(t, err)
	_, _, err = agents.runtime.A2ATaskHistory(ctx, other.Credential, publication.ID, task.ID)
	requireCode(t, err, CodeNotFound)
	otherPrincipal, err := agents.runtime.AuthenticateA2APrincipal(ctx, other.Credential, publication.ID)
	requireNoError(t, err)
	_, err = sqliteStore(t, agents.runtime).SetA2ATaskState(ctx, otherPrincipal, task.ID, A2ATaskCanceled)
	requireCode(t, err, CodeNotFound)
}

func TestA2APrincipalMessageLimitsAreIndependentAndAtomic(t *testing.T) {
	agents := setupAgentsWithOptions(t, ":memory:", RuntimeOptions{
		A2APrincipalMessageLimit: 2,
		A2APrincipalByteLimit:    1024,
	})
	defer agents.runtime.Close()
	ctx := context.Background()
	publication, first := setupA2APrincipal(t, agents)
	second, err := agents.runtime.CreateA2APrincipal(ctx, agents.scope.ScopeToken, CreateA2APrincipalInput{
		PublicationID: publication.ID, Label: "Second caller",
	})
	requireNoError(t, err)
	firstTask, err := agents.runtime.AcceptA2AMessage(ctx, first.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "first-1", Body: "one"})
	requireNoError(t, err)
	if _, err := agents.runtime.AcceptA2AMessage(ctx, first.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "first-2", Body: "two"}); err != nil {
		t.Fatal(err)
	}
	retry, err := agents.runtime.AcceptA2AMessage(ctx, first.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "first-1", Body: "one"})
	require(t, err == nil && retry.ID == firstTask.ID, "idempotent retry consumed additional capacity: %#v, %v", retry, err)
	_, err = agents.runtime.AcceptA2AMessage(ctx, first.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "first-3", Body: "three"})
	requireCode(t, err, CodeBackpressure)
	if _, err := agents.runtime.AcceptA2AMessage(ctx, second.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "second-1", Body: "independent"}); err != nil {
		t.Fatalf("another principal lost its capacity: %v", err)
	}

	usage, err := agents.runtime.ListA2APrincipalUsage(ctx, agents.scope.ScopeToken)
	require(t, err == nil && len(usage) == 2, "unexpected principal usage: %#v, %v", usage, err)
	if _, err := agents.runtime.ListA2APrincipalUsage(ctx, agents.plannerToken); err == nil {
		t.Fatal("agent authority inspected remote principal usage")
	} else {
		requireCode(t, err, CodePermissionDenied)
	}
	byPrincipal := map[string]A2APrincipalUsage{}
	for _, item := range usage {
		byPrincipal[item.PrincipalID] = item
	}
	if got := byPrincipal[first.Principal.ID]; got.UnfinishedMessages != 2 || got.UnfinishedBytes != 6 || got.MessageLimit != 2 || got.ByteLimit != 1024 {
		t.Fatalf("unexpected first principal usage: %#v", got)
	}
	if got := byPrincipal[second.Principal.ID]; got.UnfinishedMessages != 1 || got.UnfinishedBytes != int64(len("independent")) {
		t.Fatalf("unexpected second principal usage: %#v", got)
	}
	encoded, err := json.Marshal(usage)
	requireNoError(t, err)
	for _, body := range []string{"one", "two", "independent"} {
		require(t, !strings.Contains(string(encoded), body), "usage exposed message content: %s", encoded)
	}

	var tasks, correlations, messages int
	requireNoError(t, sqliteStore(t, agents.runtime).db.QueryRow(`SELECT COUNT(*) FROM a2a_tasks`).Scan(&tasks))
	requireNoError(t, sqliteStore(t, agents.runtime).db.QueryRow(`SELECT COUNT(*) FROM a2a_message_correlations`).Scan(&correlations))
	requireNoError(t, sqliteStore(t, agents.runtime).db.QueryRow(`SELECT COUNT(*) FROM messages WHERE from_kind='a2aPrincipal'`).Scan(&messages))
	require(t, tasks == 3 && correlations == 3 && messages == 3, "limit failure left partial records: tasks=%d correlations=%d messages=%d", tasks, correlations, messages)

	principal, err := agents.runtime.AuthenticateA2APrincipal(ctx, first.Credential, publication.ID)
	requireNoError(t, err)
	if _, err := sqliteStore(t, agents.runtime).SetA2ATaskState(ctx, principal, firstTask.ID, A2ATaskCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.runtime.AcceptA2AMessage(ctx, first.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "first-3", Body: "three"}); err != nil {
		t.Fatalf("terminal work did not release capacity: %v", err)
	}
}

func TestA2APrincipalByteLimitAndExpiryRelease(t *testing.T) {
	agents := setupAgentsWithOptions(t, ":memory:", RuntimeOptions{
		A2APrincipalMessageLimit: 10,
		A2APrincipalByteLimit:    8,
	})
	defer agents.runtime.Close()
	ctx := context.Background()
	publication, issued := setupA2APrincipal(t, agents)
	task, err := agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "bytes-1", Body: "12345"})
	requireNoError(t, err)
	_, err = agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "bytes-2", Body: "6789"})
	requireCode(t, err, CodeBackpressure)
	if _, err := sqliteStore(t, agents.runtime).db.Exec(`UPDATE messages SET expires_at=? WHERE message_id=?`, time.Now().Add(-time.Second).UnixMilli(), task.Messages[0].BusRequestMessageID); err != nil {
		t.Fatal(err)
	}
	usage, err := agents.runtime.ListA2APrincipalUsage(ctx, agents.scope.ScopeToken)
	require(t, err == nil && len(usage) == 1 && usage[0].UnfinishedMessages == 0 && usage[0].UnfinishedBytes == 0, "expired work retained capacity: %#v, %v", usage, err)
	if _, err := agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "bytes-2", Body: "6789"}); err != nil {
		t.Fatalf("expiry did not release byte capacity: %v", err)
	}
}

func TestConcurrentA2ARequestsCannotBypassPrincipalLimit(t *testing.T) {
	agents := setupAgentsWithOptions(t, ":memory:", RuntimeOptions{
		A2APrincipalMessageLimit: 1,
		A2APrincipalByteLimit:    1024,
	})
	defer agents.runtime.Close()
	publication, issued := setupA2APrincipal(t, agents)
	start := make(chan struct{})
	errorsByRequest := make(chan error, 2)
	var wait sync.WaitGroup
	for _, id := range []string{"concurrent-1", "concurrent-2"} {
		wait.Add(1)
		go func(messageID string) {
			defer wait.Done()
			<-start
			_, err := agents.runtime.AcceptA2AMessage(context.Background(), issued.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: messageID, Body: "work"})
			errorsByRequest <- err
		}(id)
	}
	close(start)
	wait.Wait()
	close(errorsByRequest)
	succeeded, limited := 0, 0
	for err := range errorsByRequest {
		if err == nil {
			succeeded++
			continue
		}
		if AsBusError(err).Code == CodeBackpressure {
			limited++
			continue
		}
		t.Fatalf("unexpected concurrent result: %v", err)
	}
	require(t, succeeded == 1 && limited == 1, "concurrent results: succeeded=%d limited=%d", succeeded, limited)
}

func TestA2APrincipalLimitConfiguration(t *testing.T) {
	for _, options := range []RuntimeOptions{
		{A2APrincipalMessageLimit: messageBacklogCap},
		{A2APrincipalByteLimit: int64(messageBacklogCap) * 65536},
		{A2APrincipalMessageLimit: -1},
		{A2APrincipalByteLimit: -1},
	} {
		if runtimeValue, err := OpenWithOptions(":memory:", options); err == nil {
			runtimeValue.Close()
			t.Fatalf("invalid options were accepted: %#v", options)
		} else {
			requireCode(t, err, CodeInvalidArgument)
		}
	}
}
