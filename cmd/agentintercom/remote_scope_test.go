package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/october-dev/october-bus/bus"
)

type remoteTestOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *remoteTestOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *remoteTestOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestRemoteScopeCredentialValidationAndRedirectRefusal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scope.token")
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	requireNoError(t, os.WriteFile(path, []byte(token+"\n"), 0o600))
	for _, address := range []string{"http://bus.example", "https://name:password@bus.example", "https://bus.example?key=secret", "https://bus.example/#fragment"} {
		_, err := remoteScopeClient(address, path)
		require(t, err != nil, "unsafe remote address accepted: %s", address)
	}
	if runtime.GOOS != "windows" {
		requireNoError(t, os.Chmod(path, 0o644))
		_, err := remoteScopeClient("https://bus.example/bus", path)
		require(t, err != nil, "shared credential accepted")
		requireNoError(t, os.Chmod(path, 0o600))
		link := filepath.Join(dir, "scope-link")
		requireNoError(t, os.Symlink(path, link))
		_, err = remoteScopeClient("https://bus.example/bus", link)
		require(t, err != nil, "symlinked credential accepted")
	}
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, err := remoteScopeClient(source.URL, path)
	requireNoError(t, err)
	client.HTTP.Transport = source.Client().Transport
	_, err = client.RegisterAgent(context.Background(), bus.RegisterAgentInput{ID: "remote", DisplayName: "Remote"})
	require(t, err != nil && !leaked.Load(), "credential-bearing redirect followed")
	requireNoError(t, os.WriteFile(path, []byte("invalid"), 0o600))
	_, err = remoteScopeClient("https://bus.example/bus", path)
	require(t, err != nil, "malformed credential accepted")
}

func TestRemoteMCPBridgeHTTPSLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	runtimeValue, err := bus.Open(":memory:")
	requireNoError(t, err)
	defer runtimeValue.Close()
	server := httptest.NewTLSServer(bus.NewServer(runtimeValue, bus.ServerOptions{}))
	defer server.Close()
	scope, err := runtimeValue.CreateScope(ctx, bus.CreateScopeInput{ID: "remote"})
	requireNoError(t, err)
	peer, err := runtimeValue.RegisterAgent(ctx, scope.ScopeToken, bus.RegisterAgentInput{ID: "peer", DisplayName: "Peer"})
	requireNoError(t, err)
	dir := t.TempDir()
	tokenPath, caPath := filepath.Join(dir, "scope.token"), filepath.Join(dir, "test-ca.pem")
	requireNoError(t, os.WriteFile(tokenPath, []byte(scope.ScopeToken+"\n"), 0o600))
	requireNoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
	stderr := new(remoteTestOutput)
	command := mcpBridgeCommand("", "", stderr)
	args, _ := json.Marshal([]string{"--remote", server.URL, "--scope-token-file", tokenPath, "--agent", "laptop", "--connect-to", "peer"})
	command.Env = setEnvironment(command.Env, "OCTOBER_BUS_MCP_STDIO_TEST_ARGS", string(args), "OCTOBER_BUS_MCP_TEST_CA", caPath)
	client := mcp.NewClient(&mcp.Implementation{Name: "remote-harness-test", Version: "1"}, nil)
	bridge, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	require(t, err == nil, "remote bridge: %v; %s", err, stderr.String())
	defer bridge.Close()
	receipt, err := runtimeValue.SendMessage(ctx, peer.AgentToken, bus.SendMessageInput{To: "laptop", Body: "Check the change", Mode: bus.MessageRequest})
	requireNoError(t, err)
	callMCPBridgeTool(t, ctx, bridge, "check_inbox", map[string]any{})
	callMCPBridgeTool(t, ctx, bridge, "message_peer", map[string]any{"peer": "peer", "message": "Checked", "mode": "response", "responseTo": receipt.MessageID})
	replies, err := runtimeValue.ReserveInbox(ctx, peer.AgentToken, 10, 0)
	requireNoError(t, err)
	require(t, replies != nil && len(replies.Messages) == 1 && replies.Messages[0].Body == "Checked", "missing remote reply")
	requireNoError(t, bridge.Close())
	agents, err := runtimeValue.ListAgents(ctx, scope.ScopeToken)
	requireNoError(t, err)
	for _, agent := range agents {
		if agent.ID == "laptop" {
			require(t, !agent.Reachable && agent.Lifecycle == bus.LifecycleOffline, "remote bridge did not retire")
		}
	}
	require(t, !strings.Contains(stderr.String(), scope.ScopeToken), "scope credential appeared in bridge output")
}

func TestRemoteMCPBridgeRejectsMixedModes(t *testing.T) {
	t.Setenv("OCTOBER_BUS_AGENT_TOKEN", "")
	for _, args := range [][]string{
		{"--remote", "https://bus.example/bus", "--agent", "a"},
		{"--scope-token-file", "missing", "--agent", "a"},
		{"--remote", "https://bus.example/bus", "--scope-token-file", "missing", "--agent", "a", "--scope", "local"},
	} {
		err := runMCPStdio(context.Background(), args...)
		require(t, err != nil && strings.Contains(err.Error(), "remote registration requires"), "mixed registration mode accepted: %v", err)
	}
}
