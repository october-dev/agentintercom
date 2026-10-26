package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/october-dev/agentintercom/bus"
)

func TestNativeStopGuardIsBoundedAndFailSoft(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	sender, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "sender", DisplayName: "Sender"})
	requireNoError(t, err)
	receiver, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "receiver", DisplayName: "Receiver", ConnectWorkspace: true})
	requireNoError(t, err)
	client := bus.Client{Address: owner.Address, Token: sender.AgentToken}
	_, err = client.SendMessage(ctx, bus.SendMessageInput{To: "receiver", Body: "Pending"})
	requireNoError(t, err)
	t.Setenv("OCTOBER_BUS_ADDRESS", owner.Address)
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", receiver.AgentToken)
	t.Setenv(nativeBindingFileEnv, filepath.Join(t.TempDir(), "binding.json"))
	var out bytes.Buffer
	requireNoError(t, guardNativeSession(strings.NewReader(`{"stop_hook_active":false}`), &out))
	require(t, strings.Contains(out.String(), `"decision":"block"`), "pending work not surfaced")
	out.Reset()
	requireNoError(t, guardNativeSession(strings.NewReader(`{"stop_hook_active":true}`), &out))
	require(t, out.Len() == 0, "guard blocked its own continuation")
	requireNoError(t, guardNativeSession(strings.NewReader(`not json`), &out))
	require(t, out.Len() == 0, "malformed input blocked shutdown")
	err = (bus.Client{Address: owner.Address, Token: receiver.AgentToken}).Retire(context.Background())
	requireNoError(t, err)
	requireNoError(t, guardNativeSession(strings.NewReader(`{}`), &out))
	require(t, out.Len() == 0, "revocation blocked shutdown")
}
