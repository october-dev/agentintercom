package bus

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func a2aHistoryText(t *testing.T, message *a2a.Message) string {
	t.Helper()
	if len(message.Parts) != 1 {
		t.Fatalf("unexpected part count in %#v", message)
	}
	return message.Parts[0].Text()
}

func TestA2AGetTaskPollsSubmitDeliverReply(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	publication, issued := setupA2APrincipal(t, agents)
	server := httptest.NewServer(NewServer(agents.runtime, ServerOptions{}))
	defer server.Close()
	transport := a2aTestTransport(t, server, publication.ID)
	defer transport.Destroy()
	params := a2aTestParams(issued.Credential, string(a2a.Version))

	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Review this"), a2a.NewTextPart("Focus on security"))
	message.ID = "remote-message-1"
	result, err := transport.SendMessage(ctx, params, &a2a.SendMessageRequest{Message: message})
	requireNoError(t, err)
	submitted := result.(*a2a.Task)

	polled, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: submitted.ID})
	requireNoError(t, err)
	require(t, polled.ID == submitted.ID && polled.ContextID == submitted.ContextID, "poll lost task identity: %#v", polled)
	require(t, polled.Status.State == a2a.TaskStateSubmitted && polled.Status.Timestamp != nil, "unexpected poll status: %#v", polled.Status)
	require(t, len(polled.History) == 1, "unexpected poll history: %#v", polled.History)
	first := polled.History[0]
	require(t, first.ID == "remote-message-1" && first.Role == a2a.MessageRoleUser, "unexpected request message: %#v", first)
	require(t, first.TaskID == submitted.ID && first.ContextID == submitted.ContextID, "history message lost task link: %#v", first)
	require(t, a2aHistoryText(t, first) == "Review this\nFocus on security", "unexpected request body: %#v", first)

	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil && len(reservation.Messages) == 1, "unexpected inbox: %#v, %v", reservation, err)
	inbound := reservation.Messages[0]
	if _, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, reservation.ID); err != nil {
		t.Fatal(err)
	}
	working, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: submitted.ID})
	require(t, err == nil && working.Status.State == a2a.TaskStateWorking, "delivery did not advance poll: %#v, %v", working, err)

	reply, err := agents.runtime.SendMessage(ctx, agents.reviewerToken, SendMessageInput{
		To: issued.Principal.ID, Mode: MessageResponse, ResponseTo: inbound.ID, Body: "Found one issue", IdempotencyKey: "review-reply",
	})
	requireNoError(t, err)
	completed, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: submitted.ID})
	require(t, err == nil && completed.Status.State == a2a.TaskStateCompleted, "reply did not complete poll: %#v, %v", completed, err)
	require(t, len(completed.History) == 2, "unexpected completed history: %#v", completed.History)
	answer := completed.History[1]
	require(t, answer.Role == a2a.MessageRoleAgent && answer.ID == reply.MessageID, "unexpected agent message: %#v", answer)
	require(t, answer.TaskID == submitted.ID && answer.ContextID == submitted.ContextID, "agent message lost task link: %#v", answer)
	require(t, a2aHistoryText(t, answer) == "Found one issue", "unexpected agent body: %#v", answer)
	require(t, completed.Status.Message == nil, "status duplicated the reply: %#v", completed.Status)
}

func TestA2AGetTaskSurvivesRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "bus.db")
	agents := setupAgents(t, database)
	ctx := context.Background()
	publication, issued := setupA2APrincipal(t, agents)
	server := httptest.NewServer(NewServer(agents.runtime, ServerOptions{}))
	transport := a2aTestTransport(t, server, publication.ID)
	params := a2aTestParams(issued.Credential, string(a2a.Version))

	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Review this"))
	message.ID = "remote-message-1"
	result, err := transport.SendMessage(ctx, params, &a2a.SendMessageRequest{Message: message})
	requireNoError(t, err)
	taskID := result.(*a2a.Task).ID
	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	requireNoError(t, err)
	inbound := reservation.Messages[0]
	if _, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, reservation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.runtime.SendMessage(ctx, agents.reviewerToken, SendMessageInput{
		To: issued.Principal.ID, Mode: MessageResponse, ResponseTo: inbound.ID, Body: "Done", IdempotencyKey: "restart-reply",
	}); err != nil {
		t.Fatal(err)
	}
	transport.Destroy()
	server.Close()
	requireNoError(t, agents.runtime.Close())

	restarted, err := Open(database)
	requireNoError(t, err)
	defer restarted.Close()
	server = httptest.NewServer(NewServer(restarted, ServerOptions{}))
	defer server.Close()
	transport = a2aTestTransport(t, server, publication.ID)
	defer transport.Destroy()

	polled, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: taskID})
	require(t, err == nil && polled.Status.State == a2a.TaskStateCompleted, "restart lost poll state: %#v, %v", polled, err)
	require(t, len(polled.History) == 2, "restart lost poll history: %#v", polled.History)
	require(t, a2aHistoryText(t, polled.History[0]) == "Review this", "restart lost request body: %#v", polled.History[0])
	require(t, polled.History[1].Role == a2a.MessageRoleAgent && a2aHistoryText(t, polled.History[1]) == "Done", "restart lost agent reply: %#v", polled.History[1])
}

func TestA2AGetTaskHistoryLengthSelectsRecentMessages(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	publication, issued := setupA2APrincipal(t, agents)
	server := httptest.NewServer(NewServer(agents.runtime, ServerOptions{}))
	defer server.Close()
	transport := a2aTestTransport(t, server, publication.ID)
	defer transport.Destroy()
	params := a2aTestParams(issued.Credential, string(a2a.Version))

	first := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Start"))
	first.ID = "turn-1"
	result, err := transport.SendMessage(ctx, params, &a2a.SendMessageRequest{Message: first})
	requireNoError(t, err)
	taskID := result.(*a2a.Task).ID
	principal, err := agents.runtime.AuthenticateA2APrincipal(ctx, issued.Credential, publication.ID)
	requireNoError(t, err)
	if _, err := sqliteStore(t, agents.runtime).SetA2ATaskState(ctx, principal, string(taskID), A2ATaskInputRequired); err != nil {
		t.Fatal(err)
	}
	second := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Here is the answer"))
	second.ID = "turn-2"
	second.TaskID = taskID
	if _, err := transport.SendMessage(ctx, params, &a2a.SendMessageRequest{Message: second}); err != nil {
		t.Fatal(err)
	}
	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil && len(reservation.Messages) == 2, "unexpected inbox: %#v, %v", reservation, err)
	if _, err := agents.runtime.CommitInbox(ctx, agents.reviewerToken, reservation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.runtime.SendMessage(ctx, agents.reviewerToken, SendMessageInput{
		To: issued.Principal.ID, Mode: MessageResponse, ResponseTo: reservation.Messages[1].ID, Body: "Thanks", IdempotencyKey: "turn-2-reply",
	}); err != nil {
		t.Fatal(err)
	}

	full, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: taskID})
	require(t, err == nil && len(full.History) == 3, "omitted historyLength truncated history: %#v, %v", full, err)
	zero := 0
	empty, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: taskID, HistoryLength: &zero})
	require(t, err == nil && len(empty.History) == 0, "zero historyLength kept messages: %#v, %v", empty, err)
	one := 1
	latest, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: taskID, HistoryLength: &one})
	require(t, err == nil && len(latest.History) == 1, "historyLength=1 kept %#v, %v", latest, err)
	require(t, latest.History[0].Role == a2a.MessageRoleAgent && a2aHistoryText(t, latest.History[0]) == "Thanks", "historyLength=1 missed the reply: %#v", latest.History[0])
	two := 2
	recent, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: taskID, HistoryLength: &two})
	require(t, err == nil && len(recent.History) == 2, "historyLength=2 kept %#v, %v", recent, err)
	require(t, recent.History[0].ID == "turn-2" && recent.History[1].Role == a2a.MessageRoleAgent, "historyLength=2 selected wrong messages: %#v", recent.History)
	many := 99
	all, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: taskID, HistoryLength: &many})
	require(t, err == nil && len(all.History) == 3, "oversized historyLength truncated history: %#v, %v", all, err)
	negative := -1
	_, err = transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: taskID, HistoryLength: &negative})
	require(t, errors.Is(err, a2a.ErrInvalidParams), "negative historyLength error = %v", err)
}

func TestA2AGetTaskReportsBusStates(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	publication, issued := setupA2APrincipal(t, agents)
	server := httptest.NewServer(NewServer(agents.runtime, ServerOptions{}))
	defer server.Close()
	transport := a2aTestTransport(t, server, publication.ID)
	defer transport.Destroy()
	params := a2aTestParams(issued.Credential, string(a2a.Version))
	principal, err := agents.runtime.AuthenticateA2APrincipal(ctx, issued.Credential, publication.ID)
	requireNoError(t, err)

	cases := []struct {
		state A2ATaskState
		want  a2a.TaskState
	}{
		{A2ATaskInputRequired, a2a.TaskStateInputRequired},
		{A2ATaskFailed, a2a.TaskStateFailed},
		{A2ATaskCanceled, a2a.TaskStateCanceled},
		{A2ATaskRejected, a2a.TaskStateRejected},
	}
	for i, tc := range cases {
		result, err := agents.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, AcceptA2AMessageInput{
			ClientMessageID: "state-message-" + string(rune('a'+i)),
			Body:            "case",
		})
		requireNoError(t, err)
		if _, err := sqliteStore(t, agents.runtime).SetA2ATaskState(ctx, principal, result.ID, tc.state); err != nil {
			t.Fatal(err)
		}
		polled, err := transport.GetTask(ctx, params, &a2a.GetTaskRequest{ID: a2a.TaskID(result.ID)})
		require(t, err == nil && polled.Status.State == tc.want, "state %s polled as %#v, %v", tc.state, polled, err)
	}
}

func TestA2AGetTaskHidesForeignTasksAndEnforcesCredentialState(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	ctx := context.Background()
	publication, owner := setupA2APrincipal(t, agents)
	other, err := agents.runtime.CreateA2APrincipal(ctx, agents.scope.ScopeToken, CreateA2APrincipalInput{PublicationID: publication.ID, Label: "Other caller"})
	requireNoError(t, err)
	server := httptest.NewServer(NewServer(agents.runtime, ServerOptions{}))
	defer server.Close()
	transport := a2aTestTransport(t, server, publication.ID)
	defer transport.Destroy()

	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Private work"))
	message.ID = "private-turn"
	result, err := transport.SendMessage(ctx, a2aTestParams(owner.Credential, string(a2a.Version)), &a2a.SendMessageRequest{Message: message})
	requireNoError(t, err)
	taskID := result.(*a2a.Task).ID

	// Another principal's poll is indistinguishable from an unknown task.
	_, err = transport.GetTask(ctx, a2aTestParams(other.Credential, string(a2a.Version)), &a2a.GetTaskRequest{ID: taskID})
	require(t, errors.Is(err, a2a.ErrTaskNotFound), "foreign task lookup error = %v", err)
	_, err = transport.GetTask(ctx, a2aTestParams(owner.Credential, string(a2a.Version)), &a2a.GetTaskRequest{ID: "a2at_does-not-exist"})
	require(t, errors.Is(err, a2a.ErrTaskNotFound), "unknown task lookup error = %v", err)

	// A rotated credential stops working immediately.
	if _, err := agents.runtime.RotateA2APrincipal(ctx, agents.scope.ScopeToken, owner.Principal.ID); err != nil {
		t.Fatal(err)
	}
	_, err = transport.GetTask(ctx, a2aTestParams(owner.Credential, string(a2a.Version)), &a2a.GetTaskRequest{ID: taskID})
	require(t, errors.Is(err, a2a.ErrUnauthenticated), "rotated credential lookup error = %v", err)

	// A disabled credential and a disabled publication stay denied.
	rotated, err := agents.runtime.RotateA2APrincipal(ctx, agents.scope.ScopeToken, other.Principal.ID)
	requireNoError(t, err)
	if _, err := agents.runtime.SetA2APrincipalEnabled(ctx, agents.scope.ScopeToken, other.Principal.ID, false); err != nil {
		t.Fatal(err)
	}
	_, err = transport.GetTask(ctx, a2aTestParams(rotated.Credential, string(a2a.Version)), &a2a.GetTaskRequest{ID: taskID})
	require(t, errors.Is(err, a2a.ErrUnauthenticated), "disabled credential lookup error = %v", err)
	if _, err := agents.runtime.SetA2APrincipalEnabled(ctx, agents.scope.ScopeToken, other.Principal.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.runtime.SetAgentCardPublicationEnabled(ctx, agents.scope.ScopeToken, publication.ID, false); err != nil {
		t.Fatal(err)
	}
	_, err = transport.GetTask(ctx, a2aTestParams(rotated.Credential, string(a2a.Version)), &a2a.GetTaskRequest{ID: taskID})
	require(t, errors.Is(err, a2a.ErrUnauthenticated), "disabled publication lookup error = %v", err)
	if _, err := agents.runtime.SetAgentCardPublicationEnabled(ctx, agents.scope.ScopeToken, publication.ID, true); err != nil {
		t.Fatal(err)
	}

	// Version and parameter checks still apply to task reads.
	_, err = transport.GetTask(ctx, a2aTestParams(rotated.Credential, "broken"), &a2a.GetTaskRequest{ID: taskID})
	require(t, errors.Is(err, a2a.ErrVersionNotSupported), "bad version lookup error = %v", err)
	_, err = transport.GetTask(ctx, a2aTestParams(rotated.Credential, string(a2a.Version)), &a2a.GetTaskRequest{ID: "!!! not an id !!!"})
	require(t, errors.Is(err, a2a.ErrInvalidParams), "invalid task ID lookup error = %v", err)
}
