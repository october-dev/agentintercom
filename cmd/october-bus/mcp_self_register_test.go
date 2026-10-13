package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/october-dev/october-bus/bus"
)

func TestStructuredArgumentCoercion(t *testing.T) {
	types := structuredArgumentTypes(json.RawMessage(`{"properties":{"ids":{"type":["null","array"]},"object":{"type":"object"},"text":{"type":"string"},"either":{"type":["string","array"]}}}`))
	require(t, len(types) == 2, "expected structured schema fields: %#v", types)
	for _, pair := range [][2]string{
		{`{"ids":"[\"one\"]","object":"{\"n\":9007199254740993}"}`, `{"ids":["one"],"object":{"n":9007199254740993}}`},
		{`{"ids":["one"],"text":"[]","either":"[]"}`, `{"ids":["one"],"text":"[]","either":"[]"}`},
		{`{"ids":"{}","object":"[]"}`, `{"ids":"{}","object":"[]"}`},
		{`{"ids":"[broken","object":"null"}`, `{"ids":"[broken","object":"null"}`},
		{`{"ids":null}`, `{"ids":null}`},
	} {
		got := coerceStructuredArguments(json.RawMessage(pair[0]), types)
		var actual, expected any
		decoder := json.NewDecoder(bytes.NewReader(got))
		decoder.UseNumber()
		requireNoError(t, decoder.Decode(&actual))
		decoder = json.NewDecoder(strings.NewReader(pair[1]))
		decoder.UseNumber()
		requireNoError(t, decoder.Decode(&expected))
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(expected)
		require(t, bytes.Equal(a, b), "coercion: got %s, want %s", a, b)
	}
}

func TestMCPStdioSelfRegisteredLifecycle(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OCTOBER_BUS_RUNTIME_DIR", filepath.Join(root, "run"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	daemon, err := bus.StartDaemon(ctx, 0, nil)
	requireNoError(t, err)
	defer daemon.Stop(context.Background())
	var output bytes.Buffer
	requireNoError(t, captureStdout(&output, func() error { return createScope("self-register") }))
	owner, err := localScopeClient("self-register")
	requireNoError(t, err)
	peer, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "peer", DisplayName: "Peer"})
	requireNoError(t, err)
	connect := func() *mcp.ClientSession {
		stderr := new(bytes.Buffer)
		command := mcpBridgeCommand("", "", stderr)
		args, _ := json.Marshal([]string{"--scope", "self-register", "--agent", "editor"})
		command.Env = setEnvironment(command.Env, "OCTOBER_BUS_MCP_STDIO_TEST_ARGS", string(args))
		client := mcp.NewClient(&mcp.Implementation{Name: "self-register-test", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
		require(t, err == nil, "self-register: %v", err)
		return session
	}
	bridge := connect()
	defer bridge.Close()
	requireNoError(t, captureStdout(&output, func() error { return linkAgents([]string{"--scope", "self-register", "editor", "peer"}) }))
	peerClient := bus.Client{Address: owner.Address, Token: peer.AgentToken}
	receipt, err := peerClient.SendMessage(ctx, bus.SendMessageInput{To: "editor", Body: "Review", Mode: bus.MessageRequest})
	requireNoError(t, err)
	callMCPBridgeTool(t, ctx, bridge, "check_inbox", map[string]any{})
	ids, _ := json.Marshal([]string{receipt.MessageID})
	callMCPBridgeTool(t, ctx, bridge, "acknowledge_messages", map[string]any{"messageIds": string(ids)})
	callMCPBridgeTool(t, ctx, bridge, "message_peer", map[string]any{"peer": "peer", "message": "Reviewed", "mode": "response", "responseTo": receipt.MessageID})
	callMCPBridgeTool(t, ctx, bridge, "message_receipt", map[string]any{"messageId": receipt.MessageID})
	state, err := peerClient.Receipt(ctx, receipt.MessageID)
	require(t, err == nil && state.ResponseMessageID != "", "receipt did not link reply: %#v %v", state, err)
	// A cancelled tool must not poison the upstream HTTP connection.
	waitCtx, cancelWait := context.WithCancel(ctx)
	waitDone := make(chan error, 1)
	go func() {
		_, err := bridge.CallTool(waitCtx, &mcp.CallToolParams{Name: "check_inbox", Arguments: map[string]any{"waitMs": 25_000}})
		waitDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancelWait()
	require(t, <-waitDone != nil, "cancelled tool unexpectedly succeeded")
	callMCPBridgeTool(t, ctx, bridge, "get_node_status", map[string]any{})
	// Reconnecting the same identity fences the old execution, not the peer.
	replacement := connect()
	defer replacement.Close()
	stale, err := bridge.CallTool(ctx, &mcp.CallToolParams{Name: "list_peers", Arguments: map[string]any{}})
	require(t, err != nil || stale.IsError, "replaced bridge retained authority")
	requireNoError(t, replacement.Close())
	agents, err := owner.ListAgents(ctx)
	requireNoError(t, err)
	for _, agent := range agents {
		if agent.ID == "editor" {
			require(t, agent.Lifecycle == bus.LifecycleOffline && !agent.Reachable, "EOF did not retire: %#v", agent)
		}
		if agent.ID == "peer" {
			require(t, agent.Reachable, "unrelated peer was retired")
		}
	}
	// Rotation refreshes local discovery; deletion removes the saved credential.
	t.Setenv("OCTOBER_BUS_ADMIN_TOKEN", "")
	requireNoError(t, captureStdout(&output, func() error { return scopeAdmin("rotate-token", []string{"--id", "self-register"}) }))
	newOwner, err := localScopeClient("self-register")
	requireNoError(t, err)
	require(t, newOwner.Token != owner.Token, "rotation left stale local token")
	_, err = newOwner.ListAgents(ctx)
	requireNoError(t, err)
	requireNoError(t, captureStdout(&output, func() error {
		return scopeAdmin("delete", []string{"--id", "self-register", "--confirm", "self-register"})
	}))
	_, err = bus.ReadScopeToken(filepath.Join(root, "data"), "self-register")
	require(t, err != nil, "scope deletion retained credential")
}

// selfRegisteredBridge is a bridge subprocess whose command remains inspectable
// after the client closes it.
type selfRegisteredBridge struct {
	*mcp.ClientSession
	command *exec.Cmd
	stderr  *syncBuffer
}

// Longer than any EOF assertion: Close signals the child only after this.
const selfRegisteredBridgeTerminate = 20 * time.Second

func startSelfRegisterDaemon(t *testing.T, ctx context.Context, scope string) (*bus.RunningDaemon, bus.Client) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("OCTOBER_BUS_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OCTOBER_BUS_RUNTIME_DIR", filepath.Join(root, "run"))
	daemon, err := bus.StartDaemon(ctx, 0, nil)
	requireNoError(t, err)
	t.Cleanup(func() { daemon.Stop(context.Background()) })
	var output bytes.Buffer
	requireNoError(t, captureStdout(&output, func() error { return createScope(scope) }))
	owner, err := localScopeClient(scope)
	requireNoError(t, err)
	return daemon, owner
}

func connectSelfRegisteredBridge(t *testing.T, ctx context.Context, scope, agent string) *selfRegisteredBridge {
	t.Helper()
	stderr := new(syncBuffer)
	command := mcpBridgeCommand("", "", stderr)
	args, _ := json.Marshal([]string{"--scope", scope, "--agent", agent})
	command.Env = setEnvironment(command.Env, "OCTOBER_BUS_MCP_STDIO_TEST_ARGS", string(args))
	client := mcp.NewClient(&mcp.Implementation{Name: "self-register-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: selfRegisteredBridgeTerminate}, nil)
	require(t, err == nil, "self-register %s: %v; stderr: %s", agent, err, stderr.String())
	bridge := &selfRegisteredBridge{ClientSession: session, command: command, stderr: stderr}
	t.Cleanup(func() { bridge.Close() })
	return bridge
}

func agentByID(t *testing.T, ctx context.Context, owner bus.Client, id string) (bus.Agent, bool) {
	t.Helper()
	agents, err := owner.ListAgents(ctx)
	requireNoError(t, err)
	for _, agent := range agents {
		if agent.ID == id {
			return agent, true
		}
	}
	return bus.Agent{}, false
}

func isEndedBridgeResult(result *mcp.CallToolResult) bool {
	if result == nil || !result.IsError || len(result.Content) != 1 {
		return false
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	return ok && text.Text == mcpBridgeEndedMessage
}

// requireEndedBridge waits for the session to end, then proves the process is
// still serving the host while every call keeps failing locally.
func requireEndedBridge(t *testing.T, ctx context.Context, bridge *selfRegisteredBridge) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		result, err := bridge.CallTool(ctx, &mcp.CallToolParams{Name: "list_peers", Arguments: map[string]any{}})
		if err == nil && isEndedBridgeResult(result) {
			break
		}
		require(t, err != nil || result.IsError, "ended execution retained authority: %#v", result)
		require(t, time.Now().Before(deadline), "bridge never became terminal: %v; stderr: %s", err, bridge.stderr.String())
		time.Sleep(200 * time.Millisecond)
	}
	for range 2 {
		result, err := bridge.CallTool(ctx, &mcp.CallToolParams{Name: "check_inbox", Arguments: map[string]any{}})
		require(t, err == nil && isEndedBridgeResult(result), "terminal result changed: %#v, %v", result, err)
	}
	tools, err := bridge.ListTools(ctx, nil)
	require(t, err == nil && len(tools.Tools) == 15, "terminal bridge stopped serving tools/list: %v", err)
	// The diagnostic reaches stderr asynchronously, independent of MCP replies.
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(bridge.stderr.String(), mcpBridgeEndedMessage) && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	require(t, strings.Count(bridge.stderr.String(), mcpBridgeEndedMessage) == 1, "expected one terminal diagnostic: %s", bridge.stderr.String())
}

// requireCurrentExecution proves the successor still holds the ID after more
// heartbeats and can use its tools.
func requireCurrentExecution(t *testing.T, ctx context.Context, owner bus.Client, bridge *selfRegisteredBridge, id, executionID string) {
	t.Helper()
	agent, ok := agentByID(t, ctx, owner, id)
	require(t, ok && agent.ExecutionID == executionID && agent.Reachable, "successor lost %s: %#v", id, agent)
	callMCPBridgeTool(t, ctx, bridge.ClientSession, "list_peers", map[string]any{})
}

func TestMCPStdioReplacedBridgeStaysTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, owner := startSelfRegisterDaemon(t, ctx, "replace")
	first := connectSelfRegisteredBridge(t, ctx, "replace", "editor")
	added := callMCPBridgeTool(t, ctx, first.ClientSession, "add_task", map[string]any{"title": "held work"})
	var task bus.Task
	encoded, _ := json.Marshal(added.StructuredContent)
	requireNoError(t, json.Unmarshal(encoded, &task))
	callMCPBridgeTool(t, ctx, first.ClientSession, "claim_task", map[string]any{"taskId": task.ID})
	original, _ := agentByID(t, ctx, owner, "editor")

	second := connectSelfRegisteredBridge(t, ctx, "replace", "editor")
	successor, _ := agentByID(t, ctx, owner, "editor")
	require(t, successor.ExecutionID != "" && successor.ExecutionID != original.ExecutionID, "replacement kept old execution: %#v", successor)
	tasks, err := owner.ListTasks(ctx, false)
	requireNoError(t, err)
	require(t, len(tasks) == 1 && tasks[0].ClaimedBy == "", "replacement kept the old claim: %#v", tasks)

	requireEndedBridge(t, ctx, first)
	time.Sleep(6 * time.Second)
	requireCurrentExecution(t, ctx, owner, second, "editor", successor.ExecutionID)
	result, err := first.CallTool(ctx, &mcp.CallToolParams{Name: "list_peers", Arguments: map[string]any{}})
	require(t, err == nil && isEndedBridgeResult(result), "replaced bridge resumed: %#v, %v", result, err)

	// EOF must end the terminal bridge itself, before Close would signal it.
	started := time.Now()
	_ = first.Close()
	elapsed := time.Since(started)
	state := first.command.ProcessState
	require(t, elapsed < selfRegisteredBridgeTerminate/2, "terminal bridge ignored EOF for %s", elapsed)
	require(t, state != nil && state.Exited(), "terminal bridge did not exit normally: %v", state)
	requireCurrentExecution(t, ctx, owner, second, "editor", successor.ExecutionID)
}

func TestMCPStdioRotatedScopeBridgeStaysTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, owner := startSelfRegisterDaemon(t, ctx, "rotate")
	first := connectSelfRegisteredBridge(t, ctx, "rotate", "editor")
	callMCPBridgeTool(t, ctx, first.ClientSession, "list_peers", map[string]any{})
	original, _ := agentByID(t, ctx, owner, "editor")

	t.Setenv("OCTOBER_BUS_ADMIN_TOKEN", "")
	var output bytes.Buffer
	requireNoError(t, captureStdout(&output, func() error { return scopeAdmin("rotate-token", []string{"--id", "rotate"}) }))
	requireEndedBridge(t, ctx, first)
	rotated, err := localScopeClient("rotate")
	requireNoError(t, err)
	require(t, rotated.Token != owner.Token, "rotation left stale local token")
	agent, _ := agentByID(t, ctx, rotated, "editor")
	require(t, agent.ExecutionID == original.ExecutionID && !agent.Reachable, "ended bridge registered again: %#v", agent)

	// A deliberate new launch reads the refreshed local credential.
	second := connectSelfRegisteredBridge(t, ctx, "rotate", "editor")
	successor, _ := agentByID(t, ctx, rotated, "editor")
	require(t, successor.ExecutionID != original.ExecutionID, "fresh bridge did not register: %#v", successor)
	requireCurrentExecution(t, ctx, rotated, second, "editor", successor.ExecutionID)
}

func TestMCPStdioDisconnectedBridgeStaysTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	daemon, _ := startSelfRegisterDaemon(t, ctx, "outage")
	first := connectSelfRegisteredBridge(t, ctx, "outage", "editor")
	callMCPBridgeTool(t, ctx, first.ClientSession, "list_peers", map[string]any{})
	address := daemon.RunFile.Address
	endpoint, err := url.Parse(address)
	requireNoError(t, err)
	port, err := strconv.Atoi(endpoint.Port())
	requireNoError(t, err)

	requireNoError(t, daemon.Stop(ctx))
	requireEndedBridge(t, ctx, first)

	// Restore the endpoint the ended bridge was configured with.
	restarted, err := bus.StartDaemon(ctx, port, nil)
	requireNoError(t, err)
	t.Cleanup(func() { restarted.Stop(context.Background()) })
	refreshed, err := localScopeClient("outage")
	requireNoError(t, err)
	require(t, refreshed.Address == address, "daemon moved from %s to %s", address, refreshed.Address)
	second := connectSelfRegisteredBridge(t, ctx, "outage", "editor")
	successor, ok := agentByID(t, ctx, refreshed, "editor")
	require(t, ok && successor.Reachable, "successor did not register: %#v", successor)

	time.Sleep(6 * time.Second)
	result, err := first.CallTool(ctx, &mcp.CallToolParams{Name: "list_peers", Arguments: map[string]any{}})
	require(t, err == nil && isEndedBridgeResult(result), "disconnected bridge resumed: %#v, %v", result, err)
	requireCurrentExecution(t, ctx, refreshed, second, "editor", successor.ExecutionID)
}
