package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

type recordedHook struct {
	Method string
	Route  string
	Query  url.Values
	Body   map[string]any
}

// fakeController records lifecycle traffic the way Desktop Core admits it: a
// per-execution hook credential, the execution's MCP capability and the tunnel
// authority Host must all match before a route is served.
type fakeController struct {
	mu        sync.Mutex
	hookToken string
	mcpToken  string
	host      string
	injection string
	requests  []recordedHook
	rejected  int
	server    *httptest.Server
}

func newFakeController(t *testing.T, hookToken, mcpToken string) *fakeController {
	t.Helper()
	controller := &fakeController{hookToken: hookToken, mcpToken: mcpToken, injection: "[October] peer context: Orion touched src/app.ts"}
	controller.server = httptest.NewServer(controller)
	t.Cleanup(controller.server.Close)
	return controller
}

func (controller *fakeController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if r.Header.Get("X-October-Bus-Token") != controller.hookToken || r.Header.Get("X-October-MCP-Capability") != controller.mcpToken || (controller.host != "" && r.Host != controller.host) || r.Header.Get("X-October-Caller-Pid") != "" || r.Header.Get("Origin") != "" {
		controller.rejected++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"UNAUTHENTICATED","message":"invalid hook capability"}}`))
		return
	}
	record := recordedHook{Method: r.Method, Route: r.URL.Path, Query: r.URL.Query()}
	if r.Method == http.MethodPost {
		if r.Header.Get("Content-Type") != "application/json" {
			controller.rejected++
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &record.Body)
	}
	controller.requests = append(controller.requests, record)
	if r.URL.Path == "/hook/pre-prompt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-October-Inbox-Receipt", "receipt-1")
		w.Header().Set("X-October-Resource-Ack", "ack-1")
		_, _ = w.Write([]byte(controller.injection))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (controller *fakeController) reset() []recordedHook {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	requests := controller.requests
	controller.requests = nil
	return requests
}

func hookFixture(t *testing.T) (string, managedMCPConnection, *fakeController) {
	t.Helper()
	path, connection := connectionFixture(t)
	connection.HookToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	controller := newFakeController(t, connection.HookToken, connection.AgentToken)
	// The tunnel port differs from the authority port; Host must carry the latter.
	address, err := url.Parse(controller.server.URL)
	requireNoError(t, err)
	connection.Endpoint = controller.server.URL + "/mcp"
	connection.HTTPHost = address.Hostname() + ":65000"
	controller.host = connection.HTTPHost
	saveTestConnection(t, path, connection)
	return path, connection, controller
}

func invokeHook(t *testing.T, path string, payload any, args ...string) (string, string) {
	t.Helper()
	stdin := new(bytes.Buffer)
	if payload != nil {
		if raw, ok := payload.(string); ok {
			stdin.WriteString(raw)
		} else {
			requireNoError(t, json.NewEncoder(stdin).Encode(payload))
		}
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if path != "" {
		args = append([]string{"--connection-file", path}, args...)
	}
	runHook(args, stdin, false, stdout, stderr)
	return stdout.String(), stderr.String()
}

func routes(requests []recordedHook) []string {
	names := make([]string, 0, len(requests))
	for _, request := range requests {
		names = append(names, request.Method+" "+request.Route)
	}
	return names
}

func TestHookClaudePrePromptPullsAndAcknowledges(t *testing.T) {
	path, connection, controller := hookFixture(t)
	prompt := "[October]: This request is correlated as 0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b.\nPlease review."
	stdout, stderr := invokeHook(t, path, map[string]any{"session_id": "s-1", "prompt_id": "p-1", "prompt": prompt}, "pre-prompt")
	require(t, stdout == controller.injection, "claude must receive raw text: %q", stdout)
	require(t, stderr == "", "healthy hook wrote diagnostics: %s", stderr)
	requests := controller.reset()
	require(t, strings.Join(routes(requests), ",") == "POST /hook/notify,GET /hook/pre-prompt,POST /hook/inbox-ack,POST /hook/context-ack", "unexpected routes: %v", routes(requests))
	notify := requests[0].Body
	require(t, notify["canvas"] == connection.ScopeID && notify["node"] == connection.AgentID && notify["launch"] == connection.ExecutionID, "identity mapping: %#v", notify)
	require(t, notify["agent"] == "claude" && notify["turnBoundary"] == true && notify["needsInput"] == false && notify["notificationType"] == "elicitation_response", "turn boundary body: %#v", notify)
	require(t, notify["requestId"] == "0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b" && notify["providerTurnId"] == `["s-1","p-1"]`, "correlation: %#v", notify)
	pull := requests[1].Query
	require(t, pull.Get("canvas") == connection.ScopeID && pull.Get("node") == connection.AgentID && pull.Get("launch") == connection.ExecutionID && pull.Get("event") == "pre-prompt" && pull.Get("agent") == "claude", "pull query: %v", pull)
	require(t, pull.Get("providerTurnId") == `["s-1","p-1"]` && pull.Get("requestId") == "0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b", "pull correlation: %v", pull)
	require(t, requests[2].Body["receipt"] == "receipt-1" && requests[2].Body["launch"] == connection.ExecutionID, "inbox ack: %#v", requests[2].Body)
	require(t, requests[3].Body["acknowledgement"] == "ack-1", "context ack: %#v", requests[3].Body)
}

func TestHookCodexPrePromptUsesEnvelope(t *testing.T) {
	path, _, controller := hookFixture(t)
	stdout, _ := invokeHook(t, path, map[string]any{"session_id": "thread"}, "pre-prompt", "codex")
	var envelope map[string]map[string]any
	requireNoError(t, json.Unmarshal([]byte(stdout), &envelope))
	require(t, envelope["hookSpecificOutput"]["hookEventName"] == "UserPromptSubmit" && envelope["hookSpecificOutput"]["additionalContext"] == controller.injection, "codex envelope: %s", stdout)
	requests := controller.reset()
	require(t, requests[0].Body["agent"] == "codex" && requests[0].Body["providerTurnId"] == nil && requests[1].Query.Get("agent") == "codex", "codex identity: %#v %v", requests[0].Body, requests[1].Query)
	require(t, len(requests) == 4, "codex pull was not acknowledged: %v", routes(requests))
}

// brokenStdout accepts part of the injection and then fails, as a closed or
// full pipe to the harness would.
type brokenStdout struct{ written bytes.Buffer }

func (stdout *brokenStdout) Write(p []byte) (int, error) {
	n := min(len(p), 8)
	stdout.written.Write(p[:n])
	return n, io.ErrShortWrite
}

func TestHookDoesNotAcknowledgeWhenStdoutRejectsInjection(t *testing.T) {
	path, _, controller := hookFixture(t)
	for _, flavor := range []string{"claude", "codex"} {
		stdin := strings.NewReader(`{"session_id":"s-1"}`)
		stdout, stderr := new(brokenStdout), new(bytes.Buffer)
		runHook([]string{"--connection-file", path, "pre-prompt", flavor}, stdin, false, stdout, stderr)
		require(t, stdout.written.Len() > 0, "%s hook never attempted the handoff", flavor)
		// A failed native handoff is not acknowledged, so the controller keeps the
		// staged context and receipt for a later turn.
		requests := controller.reset()
		require(t, strings.Join(routes(requests), ",") == "POST /hook/notify,GET /hook/pre-prompt", "%s hook acknowledged a failed handoff: %v", flavor, routes(requests))
	}
}

func TestHookSessionLifecycle(t *testing.T) {
	path, _, controller := hookFixture(t)
	stdout, _ := invokeHook(t, path, map[string]any{"session_id": "claude-session", "transcript_path": "/home/u/.claude/projects/x/claude-session.jsonl", "cwd": "/work"}, "session-start")
	var envelope map[string]map[string]any
	requireNoError(t, json.Unmarshal([]byte(stdout), &envelope))
	require(t, envelope["hookSpecificOutput"]["hookEventName"] == "SessionStart", "claude session-start envelope: %s", stdout)
	requests := controller.reset()
	require(t, strings.Join(routes(requests), ",") == "POST /hook/session,GET /hook/pre-prompt,POST /hook/inbox-ack,POST /hook/context-ack", "claude session routes: %v", routes(requests))
	session := requests[0].Body
	require(t, session["status"] == "live" && session["session"] == "claude-session" && session["agent"] == "claude" && session["cwd"] == "/work", "session body: %#v", session)
	require(t, requests[1].Query.Get("event") == "session-start", "session pull event: %v", requests[1].Query)

	// Codex has no SessionStart injection; its id comes from the rollout file name.
	stdout, _ = invokeHook(t, path, map[string]any{"transcript_path": "/home/u/.codex/sessions/2026/rollout-abc.jsonl"}, "session-start", "codex")
	require(t, stdout == "", "codex session-start wrote stdout: %q", stdout)
	requests = controller.reset()
	require(t, len(requests) == 1 && requests[0].Body["session"] == "rollout-abc" && requests[0].Body["agent"] == "codex", "codex session: %#v", requests)

	stdout, _ = invokeHook(t, path, map[string]any{"session_id": "claude-session"}, "session-end")
	requests = controller.reset()
	require(t, stdout == "" && len(requests) == 1 && requests[0].Body["status"] == "offline", "session-end: %q %#v", stdout, requests)
}

func TestHookStopReportsExcerptAndSkipsSubagents(t *testing.T) {
	path, _, controller := hookFixture(t)
	invokeHook(t, path, map[string]any{"session_id": "s", "prompt_id": "p", "prompt": "Fix it", "last_assistant_message": "Done.", "cwd": "/work"}, "stop")
	requests := controller.reset()
	require(t, len(requests) == 1 && requests[0].Route == "/hook/stop", "stop routes: %v", routes(requests))
	body := requests[0].Body
	excerpt, _ := body["excerpt"].(map[string]any)
	require(t, excerpt["assistantText"] == "Done." && excerpt["userPrompt"] == "Fix it" && excerpt["cwd"] == "/work" && body["agent"] == "claude" && body["providerTurnId"] == `["s","p"]` && body["outcome"] == nil, "stop body: %#v", body)

	invokeHook(t, path, map[string]any{"hook_event_name": "SubagentStop", "last_assistant_message": "sub"}, "stop")
	invokeHook(t, path, map[string]any{"isSidechain": true}, "stop")
	require(t, len(controller.reset()) == 0, "subagent stops reached the controller")

	invokeHook(t, path, map[string]any{"last_assistant_message": "ignored"}, "stop-failure", "codex")
	requests = controller.reset()
	require(t, len(requests) == 1 && requests[0].Body["outcome"] == "failed" && requests[0].Body["excerpt"] == nil && requests[0].Body["agent"] == "codex", "stop-failure: %#v", requests)
}

func TestHookNotificationsAndQuestions(t *testing.T) {
	path, _, controller := hookFixture(t)
	invokeHook(t, path, map[string]any{"tool_name": "Bash", "tool_use_id": "t-1"}, "notify", "codex")
	invokeHook(t, path, map[string]any{"notification_type": "idle_prompt", "message": "Waiting"}, "notify")
	invokeHook(t, path, map[string]any{"tool_name": "Write", "tool_use_id": "t-2"}, "permission-request")
	invokeHook(t, path, map[string]any{"tool_name": "Write", "tool_use_id": "t-2"}, "post-tool-use")
	invokeHook(t, path, map[string]any{"tool_name": "AskUserQuestion", "tool_use_id": "t-3", "tool_input": map[string]any{"questions": []any{map[string]any{"question": " Deploy now? "}, map[string]any{"question": "Later?"}}}}, "ask-user-question")
	invokeHook(t, path, map[string]any{"tool_name": "ExitPlanMode", "tool_use_id": "t-4"}, "ask-user-question")
	invokeHook(t, path, map[string]any{"tool_name": "AskUserQuestion", "tool_use_id": "t-3"}, "ask-user-question-resolved")
	requests := controller.reset()
	require(t, len(requests) == 7, "notify routes: %v", routes(requests))
	codex := requests[0].Body
	require(t, codex["agent"] == "codex" && codex["notificationType"] == "permission_request" && codex["message"] == "Wants to use Bash" && codex["toolName"] == "Bash" && codex["toolUseId"] == "t-1", "codex notify: %#v", codex)
	claude := requests[1].Body
	require(t, claude["agent"] == "claude" && claude["notificationType"] == "idle_prompt" && claude["message"] == "Waiting" && claude["toolName"] == nil, "claude notify: %#v", claude)
	permission := requests[2].Body
	require(t, permission["needsInput"] == true && permission["notificationType"] == "permission_request" && permission["message"] == "Wants to use Write" && permission["agent"] == "unknown", "permission: %#v", permission)
	completed := requests[3].Body
	require(t, completed["needsInput"] == false && completed["notificationType"] == "tool_completed" && completed["toolUseId"] == "t-2", "post-tool-use: %#v", completed)
	question := requests[4].Body
	require(t, question["needsInput"] == true && question["notificationType"] == "question" && question["message"] == "Deploy now? (+1 more)" && question["agent"] == "claude", "question: %#v", question)
	require(t, requests[5].Body["message"] == "Review and approve the proposed plan.", "plan approval: %#v", requests[5].Body)
	resolved := requests[6].Body
	require(t, resolved["needsInput"] == false && resolved["notificationType"] == "elicitation_response" && resolved["toolUseId"] == "t-3", "resolved: %#v", resolved)
}

func TestHookIsInertOutsideManagedExecution(t *testing.T) {
	_, _, controller := hookFixture(t)
	t.Setenv(managedConnectionFileEnv, "")
	stdout, stderr := invokeHook(t, "", map[string]any{"session_id": "s"}, "session-start")
	require(t, stdout == "" && stderr == "" && len(controller.reset()) == 0, "unbound hook contacted the controller: %q %q", stdout, stderr)
}

func TestHookReportsBoundedDiagnosticsWithoutSecrets(t *testing.T) {
	path, connection, controller := hookFixture(t)
	for _, tc := range []struct {
		name   string
		mutate func(*managedMCPConnection)
		class  string
		event  string
		flavor string
	}{
		{"refused", func(c *managedMCPConnection) {
			c.HookToken = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
		}, "refused-credential", "session-start", ""},
		{"expired", func(c *managedMCPConnection) { c.ExpiresAt = c.ExpiresAt.AddDate(-1, 0, 0) }, "expired-credential", "session-start", ""},
		{"no-hook-token", func(c *managedMCPConnection) { c.HookToken = "" }, "invalid-configuration", "session-start", ""},
		{"unreachable", func(c *managedMCPConnection) { c.Endpoint = "http://127.0.0.1:1/mcp"; c.HTTPHost = "" }, "unreachable", "stop", ""},
		{"unsupported-flavor", nil, "invalid-configuration", "session-start", "gemini"},
		{"unsupported-event", nil, "invalid-configuration", "goose-status", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := connection
			if tc.mutate != nil {
				tc.mutate(&current)
			}
			saveTestConnection(t, path, current)
			stdout, stderr := invokeHook(t, path, map[string]any{"session_id": "s", "prompt": "x"}, tc.event, tc.flavor)
			require(t, stdout == "", "failed hook wrote model context: %q", stdout)
			require(t, strings.Count(stderr, "agentintercom hook: "+tc.class) == 1, "expected one %s line: %q", tc.class, stderr)
			require(t, !strings.Contains(stderr, connection.AgentToken) && !strings.Contains(stderr, connection.HookToken), "diagnostics leaked a credential")
			controller.reset()
		})
	}
	saveTestConnection(t, path, connection)
	stdout, stderr := invokeHook(t, path, "{not json", "session-start")
	require(t, stderr == "" && len(controller.reset()) == 4, "malformed stdin must behave like an empty payload: %q %q", stdout, stderr)
}

func TestHookMainNeverFails(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess")
	}
	// The dispatcher exits 0 for a hook even when the file is unusable.
	command := exec.Command(os.Args[0], "-test.run=^TestHookHelper$")
	command.Env = setEnvironment(os.Environ(), "OCTOBER_BUS_HOOK_TEST_HELPER", "1", managedConnectionFileEnv, "/nonexistent/private/connection.json")
	command.Stdin = strings.NewReader("{}")
	command.Stderr = io.Discard
	output, err := command.Output()
	require(t, err == nil && len(output) == 0, "hook helper exited non-zero or wrote stdout: %v %q", err, output)
}

func TestHookHelper(t *testing.T) {
	if os.Getenv("OCTOBER_BUS_HOOK_TEST_HELPER") != "1" {
		return
	}
	trustBridgeTestCA()
	os.Args = []string{"agentintercom", "hook", "session-start"}
	if err := run(); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
