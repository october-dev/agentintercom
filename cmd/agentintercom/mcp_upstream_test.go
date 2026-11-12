package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// syncBuffer collects a child's stderr while the test reads it concurrently.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type echoInput struct {
	Text string `json:"text"`
}

type echoOutput struct {
	Text string `json:"text"`
}

// fakeUpstream is an authority-shaped MCP endpoint: a Bearer check in front of a
// real go-sdk streamable server with a long-running tool. Its session table can
// be discarded to simulate an authority restart, and it can refuse credentials.
type fakeUpstream struct {
	token       atomic.Pointer[string]
	refuse      atomic.Bool
	loseSession atomic.Bool
	rejectOnce  atomic.Bool
	initializes atomic.Int32
	calls       atomic.Int32
	waiting     chan struct{}
	release     chan struct{}
	handler     atomic.Pointer[mcp.StreamableHTTPHandler]
	server      *httptest.Server
	mu          sync.Mutex
	failures    []string
}

// lastFailures returns the status and body of every non-2xx reply the fake
// produced, so a test can prove which HTTP-level refusal it exercised.
func (upstream *fakeUpstream) lastFailures() []string {
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	return append([]string(nil), upstream.failures...)
}

// newFakeUpstream mirrors the Bus daemon's handler options: stateless, JSON
// responses, request cancellation propagated into tools.
func newFakeUpstream(t *testing.T, token string) *fakeUpstream {
	t.Helper()
	upstream := &fakeUpstream{waiting: make(chan struct{}, 16), release: make(chan struct{})}
	upstream.token.Store(&token)
	upstream.resetSessions()
	upstream.server = httptest.NewServer(upstream)
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (upstream *fakeUpstream) resetSessions() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-authority", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo text"}, func(_ context.Context, _ *mcp.CallToolRequest, input echoInput) (*mcp.CallToolResult, echoOutput, error) {
		upstream.calls.Add(1)
		return nil, echoOutput{Text: input.Text}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "wait", Description: "wait for a human"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, echoOutput, error) {
		upstream.calls.Add(1)
		upstream.waiting <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, echoOutput{}, ctx.Err()
		case <-upstream.release:
			return nil, echoOutput{Text: "released"}, nil
		}
	})
	upstream.handler.Store(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, PropagateRequestCancellation: true,
	}))
}

func (upstream *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if upstream.refuse.Load() || r.Header.Get("Authorization") != "Bearer "+*upstream.token.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"UNAUTHENTICATED","message":"refused"}}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	// The 2026-07-28 protocol opens a session with server/discover instead of
	// initialize; either one is a fresh upstream session.
	if json.Unmarshal(body, &message) == nil && (message.Method == "initialize" || message.Method == "server/discover") {
		upstream.initializes.Add(1)
	}
	// An authority restart forgets the session: the spec answer is HTTP 404.
	if message.Method == "tools/call" && upstream.loseSession.CompareAndSwap(true, false) {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	// A 2026-07-28 authority maps JSON-RPC invalid params to HTTP 400 with the
	// JSON-RPC error as the body; Desktop Core answers its own parse errors the
	// same way.
	if message.Method == "tools/call" && upstream.rejectOnce.CompareAndSwap(true, false) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"invalid params"}}`, message.ID)
		return
	}
	recorder := httptest.NewRecorder()
	upstream.handler.Load().ServeHTTP(recorder, r)
	result := recorder.Result()
	defer result.Body.Close()
	replay, _ := io.ReadAll(result.Body)
	if result.StatusCode >= 400 {
		upstream.mu.Lock()
		upstream.failures = append(upstream.failures, fmt.Sprintf("%d %s", result.StatusCode, strings.TrimSpace(string(replay))))
		upstream.mu.Unlock()
	}
	for name, values := range result.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(result.StatusCode)
	_, _ = w.Write(replay)
}

func managedBridge(t *testing.T, ctx context.Context, path string) (*mcp.ClientSession, *syncBuffer) {
	t.Helper()
	stderr := new(syncBuffer)
	command := mcpBridgeCommand("", "", stderr)
	args, err := json.Marshal([]string{"--connection-file", path})
	requireNoError(t, err)
	command.Env = setEnvironment(command.Env, "OCTOBER_BUS_MCP_STDIO_TEST_ARGS", string(args))
	client := mcp.NewClient(&mcp.Implementation{Name: "managed-upstream-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	require(t, err == nil, "connect managed bridge: %v; %s", err, stderr.String())
	return session, stderr
}

func echoThrough(ctx context.Context, session *mcp.ClientSession, text string) (*mcp.CallToolResult, error) {
	return session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": text}})
}

func waitForStderr(t *testing.T, stderr *syncBuffer, fragment string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), fragment) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stderr never reported %q: %s", fragment, stderr.String())
}

func TestManagedMCPBridgeCancellationAndConcurrencyKeepSession(t *testing.T) {
	path, connection := connectionFixture(t)
	upstream := newFakeUpstream(t, connection.AgentToken)
	connection.Endpoint = upstream.server.URL + "/mcp"
	saveTestConnection(t, path, connection)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, stderr := managedBridge(t, ctx, path)
	defer session.Close()

	// A long human wait: cancel it while other calls proceed on the same session.
	waitCtx, cancelWait := context.WithCancel(ctx)
	waitDone := make(chan error, 1)
	go func() {
		_, err := session.CallTool(waitCtx, &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{}})
		waitDone <- err
	}()
	select {
	case <-upstream.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("wait tool never started")
	}
	var group sync.WaitGroup
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			result, err := echoThrough(ctx, session, "concurrent")
			if err != nil || result.IsError {
				failures <- err
			}
		}(i)
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent call failed during a human wait: %v; %s", err, stderr.String())
	}
	cancelWait()
	require(t, <-waitDone != nil, "cancelled wait reported success")
	result, err := echoThrough(ctx, session, "after cancel")
	require(t, err == nil && !result.IsError, "call after cancellation failed: %v %#v; %s", err, result, stderr.String())
	require(t, upstream.initializes.Load() == 1, "cancellation or concurrency forced a reconnect: %d initializes", upstream.initializes.Load())
	require(t, !strings.Contains(stderr.String(), "agentintercom mcp stdio:"), "healthy session produced diagnostics: %s", stderr.String())
}

func TestManagedMCPBridgeRecoversAfterRefusalWithoutReplay(t *testing.T) {
	path, connection := connectionFixture(t)
	upstream := newFakeUpstream(t, connection.AgentToken)
	connection.Endpoint = upstream.server.URL + "/mcp"
	saveTestConnection(t, path, connection)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, stderr := managedBridge(t, ctx, path)
	defer session.Close()
	result, err := echoThrough(ctx, session, "one")
	require(t, err == nil && !result.IsError, "initial call failed: %v; authority replies: %v", err, upstream.lastFailures())

	// The authority retires the credential: the call fails, nothing is replayed.
	upstream.refuse.Store(true)
	result, err = echoThrough(ctx, session, "refused")
	require(t, err != nil || result.IsError, "refused call reported success")
	waitForStderr(t, stderr, "refused-credential")
	result, err = echoThrough(ctx, session, "still refused")
	require(t, err != nil || result.IsError, "second refused call reported success")
	require(t, strings.Count(stderr.String(), "refused-credential") == 1, "diagnostics were not bounded: %s", stderr.String())
	require(t, upstream.calls.Load() == 1, "a refused call was executed or replayed: %d", upstream.calls.Load())

	// A renewed grant makes the next call reconnect; the refused calls stay failed.
	upstream.refuse.Store(false)
	result, err = echoThrough(ctx, session, "two")
	require(t, err == nil && !result.IsError, "call after refusal failed: %v %#v; %s", err, result, stderr.String())
	waitForStderr(t, stderr, "connection recovered")
	require(t, upstream.initializes.Load() == 2, "expected exactly one reconnect: %d initializes", upstream.initializes.Load())
	require(t, upstream.calls.Load() == 2, "reconnect replayed a call: %d", upstream.calls.Load())
}

func TestManagedMCPBridgeReconnectsAfterAuthorityRestart(t *testing.T) {
	path, connection := connectionFixture(t)
	upstream := newFakeUpstream(t, connection.AgentToken)
	connection.Endpoint = upstream.server.URL + "/mcp"
	saveTestConnection(t, path, connection)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, stderr := managedBridge(t, ctx, path)
	defer session.Close()
	result, err := echoThrough(ctx, session, "one")
	require(t, err == nil && !result.IsError, "initial call failed: %v", err)

	// The authority forgets the session (restart). The failing call surfaces
	// and the following call reconnects with a fresh initialize.
	upstream.loseSession.Store(true)
	result, err = echoThrough(ctx, session, "lost")
	require(t, err != nil || result.IsError, "call on a dead session reported success")
	waitForStderr(t, stderr, "session-lost")
	result, err = echoThrough(ctx, session, "two")
	require(t, err == nil && !result.IsError, "call after restart failed: %v %#v; %s", err, result, stderr.String())
	require(t, upstream.initializes.Load() == 2, "expected one reconnect: %d initializes", upstream.initializes.Load())
	require(t, upstream.calls.Load() == 2, "restart replayed a call: %d", upstream.calls.Load())
}

func TestManagedMCPBridgeSurvivesProtocolRefusalOnHTTPError(t *testing.T) {
	// A JSON-RPC error delivered on HTTP 400 makes the go-sdk client fail its
	// connection, even though the body looks like an ordinary protocol error.
	// Treating it as a live-session refusal would strand the worker; the bridge
	// must reconnect on the next call instead.
	path, connection := connectionFixture(t)
	upstream := newFakeUpstream(t, connection.AgentToken)
	connection.Endpoint = upstream.server.URL + "/mcp"
	saveTestConnection(t, path, connection)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, stderr := managedBridge(t, ctx, path)
	defer session.Close()

	// A tool-level refusal on a healthy session is passed through unchanged and
	// costs no reconnect.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": 12}})
	require(t, err != nil || result.IsError, "invalid arguments reported success")
	require(t, upstream.initializes.Load() == 1, "a tool-level error forced a reconnect")

	upstream.rejectOnce.Store(true)
	result, err = echoThrough(ctx, session, "rejected")
	require(t, err != nil || result.IsError, "HTTP-level refusal reported success")
	result, err = echoThrough(ctx, session, "recovered")
	require(t, err == nil && !result.IsError, "worker stranded after a protocol refusal: %v %#v; %s", err, result, stderr.String())
	require(t, upstream.initializes.Load() == 2, "expected one reconnect after the HTTP refusal: %d initializes, replies %v", upstream.initializes.Load(), upstream.lastFailures())
	// Only the recovered call ran a tool: validation stopped the first and the
	// HTTP refusal stopped the second before any handler, and neither replayed.
	require(t, upstream.calls.Load() == 1, "a refused call was replayed: %d tool executions", upstream.calls.Load())
}

func TestManagedMCPBridgeRenewsExpiredCredentialWithoutRestart(t *testing.T) {
	path, connection := connectionFixture(t)
	upstream := newFakeUpstream(t, connection.AgentToken)
	connection.Endpoint = upstream.server.URL + "/mcp"
	saveTestConnection(t, path, connection)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, stderr := managedBridge(t, ctx, path)
	defer session.Close()
	result, err := echoThrough(ctx, session, "one")
	require(t, err == nil && !result.IsError, "initial call failed: %v", err)

	expired := connection
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	saveTestConnection(t, path, expired)
	result, err = echoThrough(ctx, session, "expired")
	require(t, err != nil || result.IsError, "expired credential still sent")
	waitForStderr(t, stderr, "expired-credential")

	// The controller rotates the credential and expiry; the same bridge continues.
	renewed := connection
	renewed.AgentToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	renewed.ExpiresAt = time.Now().Add(time.Hour)
	upstream.token.Store(&renewed.AgentToken)
	saveTestConnection(t, path, renewed)
	result, err = echoThrough(ctx, session, "two")
	require(t, err == nil && !result.IsError, "renewed credential not used: %v %#v; %s", err, result, stderr.String())
	waitForStderr(t, stderr, "connection recovered")
	require(t, upstream.calls.Load() == 2, "expired call was replayed: %d", upstream.calls.Load())

	// Withdrawal: the controller removes the file. Nothing falls back to the old token.
	requireNoError(t, removeTestConnection(path))
	result, err = echoThrough(ctx, session, "withdrawn")
	require(t, err != nil || result.IsError, "withdrawn connection still sent")
	waitForStderr(t, stderr, "retired-execution")
	require(t, upstream.calls.Load() == 2, "withdrawn call reached the authority")
}

func TestManagedMCPBridgeReadsConnectionPathFromEnvironment(t *testing.T) {
	path, connection := connectionFixture(t)
	upstream := newFakeUpstream(t, connection.AgentToken)
	connection.Endpoint = upstream.server.URL + "/mcp"
	saveTestConnection(t, path, connection)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stderr := new(syncBuffer)
	command := mcpBridgeCommand("", "", stderr)
	command.Env = setEnvironment(command.Env, managedConnectionFileEnv, path)
	client := mcp.NewClient(&mcp.Implementation{Name: "managed-env-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	require(t, err == nil, "connect through environment: %v; %s", err, stderr.String())
	defer session.Close()
	result, err := echoThrough(ctx, session, "env")
	require(t, err == nil && !result.IsError, "environment-resolved bridge failed: %v", err)

	// Mixing an inherited agent credential with the managed file must refuse.
	mixed := mcpBridgeCommand("http://127.0.0.1:1", "agent-token", stderr)
	mixed.Env = setEnvironment(mixed.Env, managedConnectionFileEnv, path)
	_, err = mcp.NewClient(&mcp.Implementation{Name: "managed-env-test", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: mixed}, nil)
	require(t, err != nil && strings.Contains(stderr.String(), "without local registration flags or inherited agent credentials"), "mixed identity accepted: %v; %s", err, stderr.String())
}

func TestManagedMCPStartupDiagnosticsDoNotPrintAuthorityBodies(t *testing.T) {
	for _, phase := range []string{"connect", "list-tools"} {
		t.Run(phase, func(t *testing.T) {
			path, connection := connectionFixture(t)
			upstream := newFakeUpstream(t, connection.AgentToken)
			const secretBody = "private authority response must never be printed"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(data))
				if phase == "connect" || bytes.Contains(data, []byte(`"tools/list"`)) {
					http.Error(w, secretBody+connection.AgentToken, http.StatusUnauthorized)
					return
				}
				upstream.ServeHTTP(w, r)
			}))
			defer server.Close()
			connection.Endpoint = server.URL + "/mcp"
			saveTestConnection(t, path, connection)
			err := runMCPStdio(context.Background(), "--connection-file", path)
			require(t, err != nil && strings.Contains(err.Error(), "refused-credential"), "missing bounded startup diagnostic: %v", err)
			require(t, !strings.Contains(err.Error(), secretBody) && !strings.Contains(err.Error(), connection.AgentToken), "startup printed authority response: %v", err)
		})
	}
}
