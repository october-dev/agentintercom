package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/october-dev/agentintercom/bus"
)

func runCapture(t *testing.T, fn func() error) string {
	t.Helper()
	var out bytes.Buffer
	if err := captureStdout(&out, fn); err != nil {
		t.Fatalf("command failed: %v", err)
	}
	return out.String()
}

func TestWatchOncePrintsScopedEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	address, scopeToken, _, _, cleanup := startTestServer(t, ctx, "watch-once")
	defer cleanup()
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", scopeToken)

	out := runCapture(t, func() error {
		return runWatch([]string{"--address", address, "--once", "--from", "0", "--limit", "50"})
	})
	if !strings.Contains(out, "agent.registered") || !strings.Contains(out, "link.created") {
		t.Fatalf("expected registration/link events in stream, got: %s", out)
	}
}

func TestWatchFiltersByAgentAndType(t *testing.T) {
	event := bus.BusEvent{
		Type: "message.delivered", SubjectID: "msg_1",
		Attributes: map[string]string{"from": "sender", "to": "receiver", "mode": "notify"},
	}
	if !watchInclude(event, "receiver", nil) {
		t.Fatal("to-agent must match --agent")
	}
	if watchInclude(event, "bystander", nil) {
		t.Fatal("unrelated agent must not match")
	}
	if !watchInclude(event, "", []string{"message."}) {
		t.Fatal("type prefix must match")
	}
	if watchInclude(event, "", []string{"task."}) {
		t.Fatal("non-matching type prefix must exclude")
	}
	if !watchInclude(event, "receiver", []string{"message."}) {
		t.Fatal("agent + matching type prefix must include")
	}
	if watchInclude(event, "receiver", []string{"task."}) {
		t.Fatal("agent match cannot override a failing type filter")
	}
	lifecycle := bus.BusEvent{
		Type: "agent.lifecycle_changed", SubjectID: "other",
		Attributes: map[string]string{"executionId": "exec-1", "lifecycle": "ready", "ready": "true"},
	}
	if watchInclude(lifecycle, "ready", nil) {
		t.Fatal("agent name must not match lifecycle attribute values")
	}
	if watchInclude(lifecycle, "true", nil) {
		t.Fatal("agent name must not match non-agent attribute values")
	}
	registered := bus.BusEvent{
		Type: "agent.registered", SubjectID: "receiver",
		Attributes: map[string]string{"executionId": "exec-1", "lifecycle": "starting", "ready": "false"},
	}
	if !watchInclude(registered, "receiver", nil) {
		t.Fatal("agent carried as the event subject must match --agent")
	}
	channelPost := bus.BusEvent{
		Type: "channel.posted", SubjectID: "post_1",
		Attributes: map[string]string{"channelId": "general", "authorId": "receiver"},
	}
	if !watchInclude(channelPost, "receiver", nil) {
		t.Fatal("authorId must match --agent")
	}
	publication := bus.BusEvent{
		Type: "a2a.publication_created", SubjectID: "pub_1",
		Attributes: map[string]string{"agentId": "receiver", "enabled": "true"},
	}
	if !watchInclude(publication, "receiver", nil) {
		t.Fatal("agentId must match --agent")
	}
}

func TestWatchRejectsBadArgs(t *testing.T) {
	if err := runWatch([]string{"--wait", "99999"}); err == nil {
		t.Fatal("expected --wait bound error")
	}
	if err := runWatch([]string{"extra"}); err == nil {
		t.Fatal("expected positional-arg error")
	}
	if err := runWatch([]string{"--wait", "0"}); err == nil || !strings.Contains(err.Error(), "--once") {
		t.Fatalf("expected --wait 0 to require --once, got: %v", err)
	}
	// --once with --wait 0 passes validation and fails later on credentials.
	t.Setenv("OCTOBER_BUS_SCOPE_TOKEN", "")
	if err := runWatch([]string{"--wait", "0", "--once", "--from", "0"}); err == nil || strings.Contains(err.Error(), "--wait") {
		t.Fatalf("expected --once --wait 0 to pass --wait validation, got: %v", err)
	}
}

func TestInboxInjectRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	address, _, senderToken, receiverToken, cleanup := startTestServer(t, ctx, "inject-roundtrip")
	defer cleanup()

	t.Setenv("OCTOBER_BUS_ADDRESS", address)
	t.Setenv("OCTOBER_BUS_AGENT_ID", "sender")
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", senderToken)
	var sent bytes.Buffer
	requireNoError(t, sendMessageCommand([]string{"--to", "receiver", "--body", "ping", "--mode", "notify"}, nil, &sent))
	if !strings.Contains(sent.String(), "Accepted") {
		t.Fatalf("expected accepted receipt, got: %s", sent.String())
	}

	t.Setenv("OCTOBER_BUS_AGENT_ID", "receiver")
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", receiverToken)
	inbox := runCapture(t, func() error {
		return runInboxInject([]string{"--ack"})
	})
	if !strings.Contains(inbox, "sender") || !strings.Contains(inbox, "ping") {
		t.Fatalf("expected injected message from sender, got: %s", inbox)
	}
	if !strings.Contains(inbox, "receiver") {
		t.Fatalf("expected header to fall back to OCTOBER_BUS_AGENT_ID, got: %s", inbox)
	}
	if !strings.Contains(inbox, "untrusted input") {
		t.Fatalf("expected untrusted-input note in header, got: %s", inbox)
	}
	// --ack consumed it: a second drain is empty.
	again := runCapture(t, func() error {
		return runInboxInject(nil)
	})
	if again != "" {
		t.Fatalf("expected empty second drain after --ack, got: %s", again)
	}
}

func TestInboxInjectConnectionFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed connection files require Unix permissions")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	address, _, senderToken, receiverToken, cleanup := startTestServer(t, ctx, "inject-connfile")
	defer cleanup()
	sent, err := (bus.Client{Address: address, Token: senderToken}).SendMessage(ctx, bus.SendMessageInput{To: "receiver", Body: "ping", Mode: bus.MessageNotify})
	requireNoError(t, err)
	require(t, sent.MessageID != "", "expected a message id")

	root := t.TempDir()
	requireNoError(t, os.Chmod(root, 0o700))
	path := filepath.Join(root, "connection.json")
	saveTestConnection(t, path, managedMCPConnection{
		Version: 1, Endpoint: strings.TrimRight(address, "/") + "/mcp",
		ScopeID: "inject-connfile", AgentID: "receiver", ExecutionID: "run-1",
		AgentToken: receiverToken, ExpiresAt: time.Now().Add(time.Hour),
	})
	t.Setenv(managedConnectionFileEnv, "")
	t.Setenv("OCTOBER_BUS_ADDRESS", "")
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "")

	inbox := runCapture(t, func() error {
		return runInboxInject([]string{"--connection-file", path, "--ack"})
	})
	if !strings.Contains(inbox, "ping") || !strings.Contains(inbox, "receiver") {
		t.Fatalf("expected connection-file drain naming receiver, got: %s", inbox)
	}
	again := runCapture(t, func() error {
		return runInboxInject([]string{"--connection-file", path})
	})
	if again != "" {
		t.Fatalf("expected empty second drain after --ack, got: %s", again)
	}
}

func TestInboxInjectRefusesOutsideManagedExecution(t *testing.T) {
	t.Setenv(managedConnectionFileEnv, "")
	t.Setenv("OCTOBER_BUS_ADDRESS", "")
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "")
	err := runInboxInject(nil)
	if err == nil || !strings.Contains(err.Error(), "OCTOBER_BUS_AGENT_TOKEN") {
		t.Fatalf("expected managed-credential refusal, got: %v", err)
	}
}

func TestInboxInjectRefusesMixedCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed connection files require Unix permissions")
	}
	path, _ := connectionFixture(t)
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "test-only-agent-token")
	err := runInboxInject([]string{"--connection-file", path})
	if err == nil || !strings.Contains(err.Error(), "--connection-file") {
		t.Fatalf("expected mixed-credential refusal, got: %v", err)
	}
}
