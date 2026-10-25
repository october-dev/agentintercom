package bus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHostedHumanConversationThroughMCP(t *testing.T) {
	f := newHostedOAuthFixture(t)
	response := f.exchange(f.approve(f.authorization()), f.verifier)
	var tokens issuedOAuthTokens
	requireNoError(t, json.Unmarshal(response.Body.Bytes(), &tokens))
	server := httptest.NewServer(f.h)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "human-conversation", Version: "test"}, nil)
	session, err := client.Connect(f.ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: oauthTestTransport{tokens.Access, "bus.example.test"}}}, nil)
	requireNoError(t, err)
	defer session.Close()
	directory, err := f.h.accounts.Sessions(f.ctx, f.user, f.workspace.ID, "")
	requireNoError(t, err)
	input := SendMessageInput{To: directory.Sessions[0].ID, Body: "Please review this change", Mode: MessageRequest, IdempotencyKey: "human-request"}
	receipt, err := f.h.accounts.SendHumanMessage(f.ctx, f.user, f.workspace.ID, input)
	requireNoError(t, err)
	duplicate, err := f.h.accounts.SendHumanMessage(f.ctx, f.user, f.workspace.ID, input)
	requireNoError(t, err)
	require(t, receipt.MessageID == duplicate.MessageID, "human retry duplicated delivery")
	inbox, err := session.CallTool(f.ctx, &mcp.CallToolParams{Name: "check_inbox", Arguments: map[string]any{}})
	requireNoError(t, err)
	data, _ := json.Marshal(inbox)
	require(t, strings.Contains(string(data), `human`) && strings.Contains(string(data), f.user.ID), "human author lost: %s", data)
	reply, err := session.CallTool(f.ctx, &mcp.CallToolParams{Name: "message_peer", Arguments: map[string]any{"peer": f.user.ID, "message": "Reviewed", "mode": "response", "responseTo": receipt.MessageID}})
	requireNoError(t, err)
	require(t, !reply.IsError, "human reply refused: %+v", reply)
	history, err := f.h.accounts.Messages(f.ctx, f.user, f.workspace.ID, MessageHistoryInput{})
	requireNoError(t, err)
	require(t, len(history.Messages) == 2, "wrong conversation count")
	var responseID string
	for _, message := range history.Messages {
		if message.Mode == MessageResponse {
			responseID = message.ID
			require(t, message.ToKind == MessageParticipantHuman && message.To == f.user.ID && message.State == DeliveryQueued, "reply incorrectly acknowledged before human read")
		}
	}
	count, err := f.h.accounts.ReadHumanMessages(f.ctx, f.user, f.workspace.ID, []string{responseID, receipt.MessageID})
	requireNoError(t, err)
	require(t, count == 1, "human read acknowledged an agent's message")
	history, err = f.h.accounts.Messages(f.ctx, f.user, f.workspace.ID, MessageHistoryInput{Query: "Reviewed"})
	requireNoError(t, err)
	require(t, len(history.Messages) == 1 && history.Messages[0].State == DeliveryAcknowledged, "human read receipt missing")
	unknown := hostedTestUser(t, "stranger")
	_, err = f.h.accounts.Messages(f.ctx, unknown, f.workspace.ID, MessageHistoryInput{})
	requireCode(t, err, CodePermissionDenied)
	_, err = f.h.accounts.SendHumanMessage(f.ctx, unknown, f.workspace.ID, input)
	requireCode(t, err, CodePermissionDenied)
}

func TestHostedSharedWorkAuthorityAndQuestions(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	owner := hostedTestUser(t, "owner")
	workspace, err := a.CreateWorkspace(ctx, owner, "Shared work")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), workspace.ID)
	member := hostedTestUser(t, "member")
	invite, err := a.Invite(ctx, owner, workspace.ID, member.Email, "member")
	requireNoError(t, err)
	_, err = a.AcceptInvite(ctx, member, invite.Token)
	requireNoError(t, err)
	device, err := a.EnrollDevice(ctx, owner, workspace.ID, "Machine")
	requireNoError(t, err)
	agent, err := a.StartDeviceSession(ctx, device.Token, "Worker", "")
	requireNoError(t, err)
	task, err := a.CreateHumanTask(ctx, member, workspace.ID, AddTaskInput{Title: "Human task", Description: "An agent should claim this"})
	requireNoError(t, err)
	claimed, err := a.runtime.ClaimTask(ctx, agent.AgentToken, task.ID)
	requireNoError(t, err)
	require(t, claimed.Status == "claimed", "human task could not be claimed")
	question, err := a.runtime.AskHuman(ctx, agent.AgentToken, AskHumanInput{Question: "Proceed?", Options: []string{"Yes", "No"}})
	requireNoError(t, err)
	pending, err := a.Questions(ctx, member, workspace.ID, "")
	requireNoError(t, err)
	require(t, len(pending.Escalations) == 1, "missing question")
	answered, err := a.AnswerQuestion(ctx, member, workspace.ID, question.ID, "Yes")
	requireNoError(t, err)
	require(t, answered.Answer == "Yes", "answer lost")
	inbox, err := a.runtime.ReserveInbox(ctx, agent.AgentToken, 10, 0)
	requireNoError(t, err)
	require(t, inbox != nil && len(inbox.Messages) == 1 && strings.Contains(inbox.Messages[0].Body, "Yes") && inbox.Messages[0].FromKind == MessageParticipantHuman, "answer was not sent to the agent inbox")
	_, err = a.AnswerQuestion(ctx, owner, workspace.ID, question.ID, "No")
	requireCode(t, err, CodeConflict)
	events, err := a.store.Events(ctx, workspace.ID, 0, 100)
	requireNoError(t, err)
	attributed := 0
	for _, event := range events.Events {
		if event.SubjectID == task.ID && event.Type == "task.created" || event.SubjectID == question.ID && event.Type == "escalation.resolved" {
			require(t, event.Attributes["actorKind"] == "human" && event.Attributes["actorId"] == member.ID, "human actor missing from event")
			attributed++
		}
	}
	require(t, attributed == 2, "missing audit events")
	requireNoError(t, a.ChangeMember(ctx, owner, workspace.ID, member.ID, ""))
	_, err = a.Tasks(ctx, member, workspace.ID, "")
	requireCode(t, err, CodePermissionDenied)
	_, err = a.CreateHumanTask(ctx, member, workspace.ID, AddTaskInput{Title: "Removed member"})
	requireCode(t, err, CodePermissionDenied)
	_, err = a.AnswerQuestion(ctx, member, workspace.ID, question.ID, "No")
	requireCode(t, err, CodePermissionDenied)
}

func TestHostedMessageHistoryPaginationAndLiteralSearch(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	user := hostedTestUser(t, "history")
	workspace, err := a.CreateWorkspace(ctx, user, "History")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), workspace.ID)
	device, err := a.EnrollDevice(ctx, user, workspace.ID, "Machine")
	requireNoError(t, err)
	agent, err := a.StartDeviceSession(ctx, device.Token, "Worker", "")
	requireNoError(t, err)
	for _, body := range []string{"100% done", "plain text", "underscore_value"} {
		_, err = a.SendHumanMessage(ctx, user, workspace.ID, SendMessageInput{To: agent.AgentID, Body: body})
		requireNoError(t, err)
	}
	first, err := a.Messages(ctx, user, workspace.ID, MessageHistoryInput{Limit: 2})
	requireNoError(t, err)
	require(t, len(first.Messages) == 2 && first.NextCursor != "", "history pagination missing")
	second, err := a.Messages(ctx, user, workspace.ID, MessageHistoryInput{Limit: 2, Before: first.NextCursor})
	requireNoError(t, err)
	require(t, len(second.Messages) == 1 && second.NextCursor == "", "history repeated or lost messages")
	require(t, second.Messages[0].ID != first.Messages[0].ID && second.Messages[0].ID != first.Messages[1].ID, "duplicate history page")
	matches, err := a.Messages(ctx, user, workspace.ID, MessageHistoryInput{Query: "%"})
	requireNoError(t, err)
	require(t, len(matches.Messages) == 1 && matches.Messages[0].Body == "100% done", "search interpreted user wildcard")
}
