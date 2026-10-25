//go:build unix

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/october-dev/october-bus/bus"
)

func TestAttachedClaudeUsesExactSessionAndKeepsInbox(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	root, err := os.MkdirTemp("/tmp", "ai-sock-")
	requireNoError(t, err)
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "inbox.sock")
	listener, err := net.Listen("unix", socket)
	requireNoError(t, err)
	defer listener.Close()
	requireNoError(t, os.Chmod(socket, 0600))
	t.Setenv("CLAUDE_CODE_SESSION_ID", "our-session")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", socket)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "private-socket-token")
	require(t, bindInstalledClaude("other-session") != nil, "accepted another session's hook")
	requireNoError(t, installedClaudeHook(strings.NewReader(`{"session_id":"our-session","hook_event_name":"SessionStart"}`), io.Discard))
	session, err := bus.StartAgentSession(ctx, bus.AgentSessionOptions{Address: owner.Address, ScopeToken: owner.Token, Registration: bus.RegisterAgentInput{ID: "attached", DisplayName: "Attached", ConnectWorkspace: true}, InitialReady: true, InitialLifecycle: bus.LifecycleReady})
	requireNoError(t, err)
	defer session.Close(context.Background())
	var ready atomic.Bool
	cleanup, err := attachBridge(ctx, "claude", session, ready.Load)
	requireNoError(t, err)
	defer cleanup()
	_, err = attachBridge(ctx, "claude", session, ready.Load)
	require(t, err != nil, "two bridges acquired the same session binding")
	sender, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "sender", DisplayName: "sender", ConnectWorkspace: true})
	requireNoError(t, err)
	client := bus.Client{Address: owner.Address, Token: sender.AgentToken}
	receipt, err := client.SendMessage(ctx, bus.SendMessageInput{To: "attached", Body: "PRIVATE MESSAGE BODY", Mode: bus.MessageRequest})
	requireNoError(t, err)
	status, err := session.Client.WakeStatus(ctx)
	requireNoError(t, err)
	require(t, !status.Connected, "woke before tools discovery")
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		received <- data
	}()
	ready.Store(true)
	select {
	case data := <-received:
		require(t, strings.Contains(string(data), receipt.MessageID), "nudge omitted pending ID")
		require(t, !strings.Contains(string(data), "PRIVATE MESSAGE BODY"), "forwarded untrusted content into socket")
	case <-time.After(6 * time.Second):
		t.Fatal("no attached wake")
	}
	state, err := client.Receipt(ctx, receipt.MessageID)
	requireNoError(t, err)
	require(t, state.State == bus.DeliveryQueued, "wake consumed the inbox")
	requireNoError(t, installedClaudeHook(strings.NewReader(`{"session_id":"our-session","hook_event_name":"SessionEnd"}`), io.Discard))
	dir, err := attachmentDirectory("claude:our-session")
	requireNoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "binding.json"))
	require(t, os.IsNotExist(err), "session end retained socket binding")
}

func TestCodexAttachBindsExistingExecution(t *testing.T) {
	ctx, owner := startWorkspaceTest(t)
	session, err := bus.StartAgentSession(ctx, bus.AgentSessionOptions{Address: owner.Address, ScopeToken: owner.Token, Registration: bus.RegisterAgentInput{ID: "existing", DisplayName: "Existing"}})
	requireNoError(t, err)
	defer session.Close(context.Background())
	cleanup, err := attachBridge(ctx, "codex", session, func() bool { return false })
	requireNoError(t, err)
	defer cleanup()
	dir, err := attachmentDirectory("agent:existing")
	requireNoError(t, err)
	// No provider/model is launched by attachment itself.
	requireNoError(t, attachSession([]string{"codex", "--thread", "exact name with spaces", "--agent", "existing", "--binary", "true"}, io.Discard))
	var d attachedDestination
	requireNoError(t, readPrivateSessionJSON(filepath.Join(dir, "destination.json"), &d))
	require(t, d.ExecutionID == session.Registration.ExecutionID && d.Thread == "exact name with spaces", "bound a different execution")
	agents, err := owner.ListAgents(ctx)
	requireNoError(t, err)
	require(t, len(agents) == 1, "attachment registered a disconnected second inbox")
	require(t, attachSession([]string{"codex", "--thread", "other", "--agent", "absent", "--binary", "true"}, io.Discard) != nil, "attached a missing execution")
	raw, err := os.ReadFile(filepath.Join(dir, "destination.json"))
	requireNoError(t, err)
	var fields map[string]any
	requireNoError(t, json.Unmarshal(raw, &fields))
	require(t, len(fields) == 3, "destination leaked credentials")
}
