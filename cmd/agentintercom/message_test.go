package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/october-dev/agentintercom/bus"
)

func TestMessageCLIRequestReplyAndExplicitAcknowledgement(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	a, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "a", DisplayName: "First"})
	requireNoError(t, err)
	b, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "b", DisplayName: "Second", ConnectTo: []string{"a"}})
	requireNoError(t, err)
	t.Setenv("OCTOBER_BUS_ADDRESS", owner.Address)
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", a.AgentToken)
	var out bytes.Buffer
	args := []string{"--to", "b", "--body-file", "-", "--mode", "request", "--key", "cli-request", "--json"}
	requireNoError(t, sendMessageCommand(args, strings.NewReader("Review this\x1b[2J"), &out))
	var receipt bus.DeliveryReceipt
	requireNoError(t, json.Unmarshal(out.Bytes(), &receipt))
	out.Reset()
	requireNoError(t, sendMessageCommand(args, strings.NewReader("Review this\x1b[2J"), &out))
	var retry bus.DeliveryReceipt
	requireNoError(t, json.Unmarshal(out.Bytes(), &retry))
	require(t, retry.MessageID == receipt.MessageID, "CLI retry duplicated the request")
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", b.AgentToken)
	out.Reset()
	requireNoError(t, inboxCommand(nil, &out))
	require(t, !strings.Contains(out.String(), "\x1b") && strings.Contains(out.String(), receipt.MessageID), "inbox emitted terminal controls or lost the message")
	out.Reset()
	requireNoError(t, inboxCommand([]string{"--json"}, &out))
	var messages []bus.Message
	requireNoError(t, json.Unmarshal(out.Bytes(), &messages))
	require(t, len(messages) == 1 && messages[0].State == bus.DeliveryDelivered, "reading the inbox acknowledged work prematurely")
	out.Reset()
	requireNoError(t, sendMessageCommand([]string{"--to", "a", "--reply-to", receipt.MessageID, "--body", "Reviewed", "--key", "cli-reply", "--json"}, nil, &out))
	var reply bus.DeliveryReceipt
	requireNoError(t, json.Unmarshal(out.Bytes(), &reply))
	requireNoError(t, acknowledgeCommand([]string{receipt.MessageID, "--json"}, &out))
	result, err := (bus.Client{Address: owner.Address, Token: a.AgentToken}).Receipt(context.Background(), receipt.MessageID)
	requireNoError(t, err)
	require(t, result.State == bus.DeliveryAcknowledged && result.ResponseMessageID == reply.MessageID, "receipt did not preserve response and acknowledgement")
	out.Reset()
	requireNoError(t, inboxCommand([]string{"--json"}, &out))
	require(t, strings.TrimSpace(out.String()) == "[]", "acknowledged work was delivered again")
	// A scope token must never accidentally be used as execution authority.
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "")
	require(t, sendMessageCommand([]string{"--to", "a", "--body", "bad"}, nil, &out) != nil, "used an inherited owner token")
}

func TestMessageCLIRefusesUnsafeConnectionsAndOversizeInput(t *testing.T) {
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "test-only-agent-token")
	for _, address := range []string{"http://example.org", "https://user:password@example.org", "https://example.org?token=bad", "file:///tmp/bus"} {
		_, err := executionClient(address)
		require(t, err != nil, "unsafe credential destination accepted: %s", address)
	}
	_, err := messageBody("", "-", strings.NewReader(strings.Repeat("x", 65537)))
	require(t, err != nil, "accepted an oversized stdin message")
}
