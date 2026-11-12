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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The cloud controller writes the gateway origin alongside the execution's
// endpoint. Exercise that exact file shape over real TLS, without scope grants,
// a second daemon, a WebSocket mode or disabled certificate verification.
func TestManagedMCPCloudHTTPSConnection(t *testing.T) {
	path, connection := connectionFixture(t)
	connection.HookToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	connection.ExecutionID = "managed:environment:1"
	upstream := newFakeUpstream(t, connection.AgentToken)
	controller := newFakeController(t, connection.HookToken, connection.AgentToken)
	const prefix = "/api/compute/workers/environment"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || r.Header.Get("X-October-Caller-Pid") != "" {
			t.Error("managed helper sent local-only identity headers")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == prefix+"/mcp" {
			upstream.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, prefix+"/hook/") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
			controller.ServeHTTP(w, r)
			return
		}
		t.Errorf("request escaped the execution route: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	connection.Gateway = server.URL
	connection.Endpoint = server.URL + prefix + "/mcp"
	saveTestConnection(t, path, connection)
	caPath := filepath.Join(t.TempDir(), "test-ca.pem")
	requireNoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stderr := new(syncBuffer)
	command := mcpBridgeCommand("", "", stderr)
	args, err := json.Marshal([]string{"--connection-file", path})
	requireNoError(t, err)
	command.Env = setEnvironment(command.Env, "OCTOBER_BUS_MCP_STDIO_TEST_ARGS", string(args), "OCTOBER_BUS_MCP_TEST_CA", caPath)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "cloud-test", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	require(t, err == nil, "cloud bridge failed: %v; %s", err, stderr.String())
	defer session.Close()
	result, err := echoThrough(ctx, session, "cloud request and reply")
	require(t, err == nil && !result.IsError && strings.Contains(string(result.Content[0].(*mcp.TextContent).Text), "cloud request and reply"), "cloud round trip failed: %v", err)

	// A human wait may outlive a credential; renewal and other calls must not
	// replace the execution or close the pending request.
	waitDone := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{}})
		waitDone <- err
	}()
	select {
	case <-upstream.waiting:
	case <-ctx.Done():
		t.Fatal("human wait did not start")
	}
	connection.AgentToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	connection.HookToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	upstream.token.Store(&connection.AgentToken)
	controller.mu.Lock()
	controller.mcpToken, controller.hookToken = connection.AgentToken, connection.HookToken
	controller.mu.Unlock()
	saveTestConnection(t, path, connection)
	result, err = echoThrough(ctx, session, "renewed")
	require(t, err == nil && !result.IsError, "renewal failed: %v", err)
	close(upstream.release)
	requireNoError(t, <-waitDone)

	hook := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHookHelper$")
	hook.Env = setEnvironment(os.Environ(), "OCTOBER_BUS_HOOK_TEST_HELPER", "1", managedConnectionFileEnv, path, "OCTOBER_BUS_MCP_TEST_CA", caPath)
	hook.Stdin = strings.NewReader(`{"session_id":"cloud-session","cwd":"/work"}`)
	hook.Stderr = stderr
	output, err := hook.Output()
	requireNoError(t, err)
	require(t, strings.Contains(string(output), controller.injection), "cloud hook did not hand off context: %s", stderr.String())
	requests := controller.reset()
	require(t, len(requests) == 4 && requests[0].Body["launch"] == connection.ExecutionID && requests[0].Body["session"] == "cloud-session", "cloud lifecycle or receipts missing: %v", requests)

	expired := connection
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	saveTestConnection(t, path, expired)
	result, err = echoThrough(ctx, session, "expired")
	require(t, err != nil || result.IsError, "expired request was sent")
	saveTestConnection(t, path, connection)
	result, err = echoThrough(ctx, session, "after renewal")
	require(t, err == nil && !result.IsError, "renewed bridge did not recover: %v", err)
	requireNoError(t, os.Remove(path))
	result, err = echoThrough(ctx, session, "retired")
	require(t, err != nil || result.IsError, "withdrawn execution still sent requests")
	require(t, upstream.calls.Load() == 4, "expired/retired request reached authority: %d", upstream.calls.Load())
}
