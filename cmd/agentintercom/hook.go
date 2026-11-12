package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

// hookBudget bounds one hook invocation. Claude gives October's hooks five
// seconds and Codex ten; a hook that overruns is killed and its receipts lost.
const hookBudget = 4 * time.Second

// hookEvents are the lifecycle events October Desktop installs for Claude and
// Codex. They keep the argv shape of Desktop's Node hook, `<event> [<flavor>]`,
// so the controller only swaps the executable when it installs this helper.
var hookEvents = map[string]bool{
	"session-start": true, "session-end": true, "pre-prompt": true, "stop": true, "stop-failure": true,
	"notify": true, "permission-request": true, "post-tool-use": true, "post-tool-use-failure": true,
	"ask-user-question": true, "ask-user-question-resolved": true,
}

var correlatedRequestPattern = regexp.MustCompile(`(?m)^\[October\]: This request is correlated as ([a-f0-9-]{36})\.`)

// runHook reports a harness lifecycle event to the managed connection's
// controller. It mirrors Desktop's `bus-hook.mjs` for the claude and codex
// flavors: identical routes, bodies and stdout envelopes, without a Node runtime.
// It never fails the harness: problems go to stderr and the caller exits 0.
func runHook(args []string, stdin io.Reader, stdinIsTerminal bool, stdout, stderr io.Writer) {
	flags := flag.NewFlagSet("hook", flag.ContinueOnError)
	flags.SetOutput(stderr)
	connectionFile := flags.String("connection-file", "", "host-managed private execution connection file; defaults to $"+managedConnectionFileEnv)
	if err := flags.Parse(args); err != nil {
		return
	}
	if flags.NArg() < 1 || flags.NArg() > 2 {
		fmt.Fprintln(stderr, "agentintercom hook: usage: hook [--connection-file <path>] <event> [<flavor>]")
		return
	}
	event, flavor := flags.Arg(0), flags.Arg(1)
	diagnostics := &bridgeDiagnostics{out: stderr, prefix: "agentintercom hook"}
	// Outside a managed execution (no file named), the hook is inert: a shared
	// harness configuration must not contact or drain any authority.
	connectionPath := resolveManagedConnectionPath(*connectionFile)
	if connectionPath == "" {
		return
	}
	if !hookEvents[event] {
		diagnostics.failed(failureConfiguration, "unsupported hook event "+quote(event))
		return
	}
	if flavor != "" && flavor != "claude" && flavor != "codex" {
		diagnostics.failed(failureConfiguration, "unsupported hook flavor "+quote(flavor)+"; the native hook supports claude and codex")
		return
	}
	connection, err := readManagedMCPConnection(connectionPath)
	if err != nil {
		diagnostics.failed(classifyTransportError(err))
		return
	}
	if connection.HookToken == "" {
		diagnostics.failed(failureConfiguration, "connection file has no hookToken; lifecycle is not reported for this execution")
		return
	}
	base, ok := hookBase(connection.Endpoint)
	if !ok {
		diagnostics.failed(failureConfiguration, "connection endpoint must end with /mcp to derive lifecycle routes")
		return
	}
	var payload map[string]any
	if !stdinIsTerminal {
		data, _ := io.ReadAll(io.LimitReader(stdin, 1024*1024))
		if len(bytes.TrimSpace(data)) > 0 {
			if json.Unmarshal(data, &payload) != nil {
				payload = nil
			}
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookBudget)
	defer cancel()
	invocation := &hookInvocation{
		ctx: ctx, connection: connection, base: base, event: event, flavor: flavor, payload: payload,
		client: &http.Client{CheckRedirect: refuseMCPRedirect}, stdout: stdout, diagnostics: diagnostics,
	}
	invocation.run()
}

func quote(value string) string { return fmt.Sprintf("%q", value) }

type hookInvocation struct {
	ctx         context.Context
	connection  managedMCPConnection
	base        string
	event       string
	flavor      string
	payload     map[string]any
	client      *http.Client
	stdout      io.Writer
	diagnostics *bridgeDiagnostics
}

type pulledInjection struct {
	text, acknowledgement, receipt string
}

func (hook *hookInvocation) run() {
	ev := hook.payload
	// Claude >= 2.1.196 exposes a native per-prompt UUID. Transcripts are
	// asynchronous snapshots and must never determine completion identity.
	providerTurnID := ""
	if hook.flavor == "" || hook.flavor == "claude" {
		if session, ok := ev["session_id"].(string); ok {
			if prompt, ok := ev["prompt_id"].(string); ok {
				encoded, _ := json.Marshal([]string{session, prompt})
				providerTurnID = string(encoded)
			}
		}
	}
	if providerTurnID == "" {
		providerTurnID, _ = ev["provider_turn_id"].(string)
	}
	requestID := ""
	if hook.flavor == "" || hook.flavor == "claude" {
		if prompt, ok := ev["prompt"].(string); ok {
			matches := correlatedRequestPattern.FindAllStringSubmatch(prompt, -1)
			if len(matches) == 1 {
				requestID = matches[0][1]
			}
		}
	}
	toolName := firstString(ev, "tool_name", "toolName")
	toolUseID := firstString(ev, "tool_use_id", "toolUseId", "tool_call_id")
	agentOrClaude := hook.flavor
	if agentOrClaude == "" {
		agentOrClaude = "claude"
	}
	agentOrUnknown := hook.flavor
	if agentOrUnknown == "" {
		agentOrUnknown = "unknown"
	}
	switch hook.event {
	case "ask-user-question":
		questions, _ := nested(ev, "tool_input")["questions"].([]any)
		message := ""
		for _, item := range questions {
			if question, ok := item.(map[string]any); ok {
				if text, ok := question["question"].(string); ok && strings.TrimSpace(text) != "" {
					more := ""
					if len(questions) > 1 {
						more = fmt.Sprintf(" (+%d more)", len(questions)-1)
					}
					message = truncate(strings.TrimSpace(text), 500-len(more)) + more
					break
				}
			}
		}
		if message == "" {
			if toolName == "ExitPlanMode" {
				message = "Review and approve the proposed plan."
			} else {
				message = "The agent has a question."
			}
		}
		hook.notify(map[string]any{"agent": "claude", "needsInput": true, "notificationType": "question", "message": message}, toolName, toolUseID)
	case "ask-user-question-resolved":
		hook.notify(map[string]any{"agent": "claude", "needsInput": false, "notificationType": "elicitation_response", "message": ""}, toolName, toolUseID)
	case "pre-prompt":
		// A fresh user prompt proves any prior elicitation in this terminal is no
		// longer blocking, including a question the user dismissed.
		body := map[string]any{"agent": agentOrClaude, "needsInput": false, "notificationType": "elicitation_response", "turnBoundary": true, "message": ""}
		if requestID != "" {
			body["requestId"] = requestID
		}
		if providerTurnID != "" {
			body["providerTurnId"] = providerTurnID
		}
		hook.notify(body, "", "")
		pulled := hook.pull("pre-prompt", providerTurnID, requestID)
		if pulled.text == "" {
			return
		}
		// Codex treats stdout opening with '[' or '{' as JSON and rejects prose,
		// so it receives the UserPromptSubmit envelope; Claude accepts raw text.
		wrote := false
		if hook.flavor == "codex" {
			wrote = hook.write(hookEnvelope("UserPromptSubmit", pulled.text))
		} else {
			wrote = hook.write(pulled.text)
		}
		if wrote {
			hook.acknowledge(pulled)
		}
	case "session-start", "session-end":
		file, _ := ev["transcript_path"].(string)
		session := firstString(ev, "session_id", "sessionId", "thread_id", "threadId")
		if session == "" && file != "" {
			if stem, ok := strings.CutSuffix(path.Base(strings.ReplaceAll(file, "\\", "/")), ".jsonl"); ok && stem != "" {
				session = stem
			}
		}
		agent := hook.flavor
		if agent == "" {
			agent = transcriptAgent(file)
		}
		cwd, _ := ev["cwd"].(string)
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		status := "live"
		if hook.event == "session-end" {
			status = "offline"
		}
		hook.post("/hook/session", map[string]any{"status": status, "session": session, "file": file, "agent": agent, "cwd": cwd})
		// Claude accepts the native SessionStart envelope on fresh, resumed and
		// reset context. The passive pull cannot start a turn.
		if hook.event == "session-start" && hook.flavor == "" && agent == "claude" {
			pulled := hook.pull("session-start", providerTurnID, requestID)
			if pulled.text != "" && hook.write(hookEnvelope("SessionStart", pulled.text)) {
				hook.acknowledge(pulled)
			}
		}
	case "post-tool-use", "post-tool-use-failure":
		notificationType := "tool_completed"
		if hook.event == "post-tool-use-failure" {
			notificationType = "tool_failed"
		}
		hook.notify(map[string]any{"agent": agentOrUnknown, "needsInput": false, "notificationType": notificationType, "message": ""}, toolName, toolUseID)
	case "permission-request":
		message, _ := ev["message"].(string)
		if message == "" {
			if toolName != "" {
				message = "Wants to use " + toolName
			} else {
				message = "The agent needs approval."
			}
		}
		hook.notify(map[string]any{"agent": agentOrUnknown, "needsInput": true, "notificationType": "permission_request", "message": message}, toolName, toolUseID)
	case "stop", "stop-failure":
		if name, _ := ev["hook_event_name"].(string); name == "SubagentStop" {
			return
		}
		if sidechain, _ := ev["isSidechain"].(bool); sidechain {
			return
		}
		file, _ := ev["transcript_path"].(string)
		agent := hook.flavor
		if agent == "" {
			agent = transcriptAgent(file)
		}
		body := map[string]any{"excerpt": nil, "agent": agent}
		if last, ok := ev["last_assistant_message"].(string); ok && hook.event == "stop" {
			prompt, _ := ev["prompt"].(string)
			excerpt := map[string]any{"toolsUsed": []any{}, "filesTouched": []any{}, "userPrompt": truncate(prompt, 6000), "assistantText": truncate(last, 12000)}
			if cwd, ok := ev["cwd"]; ok {
				excerpt["cwd"] = cwd
			}
			body["excerpt"] = excerpt
		}
		if hook.event == "stop-failure" {
			body["outcome"] = "failed"
		}
		if providerTurnID != "" {
			body["providerTurnId"] = providerTurnID
		}
		hook.post("/hook/stop", body)
	case "notify":
		// Preserve the machine-readable subtype: a generic Notification is not
		// necessarily a permission wait. Codex wires this event only to
		// PermissionRequest.
		notificationType := firstString(ev, "notification_type", "notificationType")
		if notificationType == "" && hook.flavor == "codex" {
			notificationType = "permission_request"
		}
		message, _ := ev["message"].(string)
		if message == "" {
			if toolName != "" {
				message = "Wants to use " + toolName
			} else if raw, _ := ev["notification_type"].(string); raw != "" {
				message = "Needs your attention (" + raw + ")"
			}
		}
		hook.notify(map[string]any{"agent": agentOrClaude, "notificationType": notificationType, "message": message}, toolName, toolUseID)
	}
}

func (hook *hookInvocation) identity() map[string]any {
	return map[string]any{"canvas": hook.connection.ScopeID, "node": hook.connection.AgentID, "launch": hook.connection.ExecutionID}
}

func (hook *hookInvocation) headers(request *http.Request) {
	request.Header.Set("X-October-Bus-Token", hook.connection.HookToken)
	request.Header.Set("X-October-MCP-Capability", hook.connection.AgentToken)
	if hook.connection.HTTPHost != "" {
		request.Host = hook.connection.HTTPHost
	}
}

func (hook *hookInvocation) notify(body map[string]any, toolName, toolUseID string) {
	if toolName != "" {
		body["toolName"] = toolName
	}
	if toolUseID != "" {
		body["toolUseId"] = toolUseID
	}
	hook.post("/hook/notify", body)
}

func (hook *hookInvocation) post(route string, body map[string]any) {
	for key, value := range hook.identity() {
		body[key] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return
	}
	request, err := http.NewRequestWithContext(hook.ctx, http.MethodPost, hook.base+route, bytes.NewReader(encoded))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	hook.headers(request)
	response, err := hook.client.Do(request)
	if err != nil {
		hook.diagnostics.failed(classifyTransportError(err))
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		hook.diagnostics.failed(classifyHTTPFailure(response.StatusCode, readFailureBody(response)))
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, failureBodyLimit))
}

// pull fetches staged peer context. Passive points may receive the one-shot
// orientation block; the reservation is acknowledged only after stdout accepted
// the bytes, which proves native handoff, not model comprehension.
func (hook *hookInvocation) pull(viaEvent, providerTurnID, requestID string) pulledInjection {
	query := url.Values{}
	query.Set("canvas", hook.connection.ScopeID)
	query.Set("node", hook.connection.AgentID)
	query.Set("event", viaEvent)
	query.Set("launch", hook.connection.ExecutionID)
	agent := hook.flavor
	if agent == "" {
		agent = "claude"
	}
	query.Set("agent", agent)
	if providerTurnID != "" {
		query.Set("providerTurnId", providerTurnID)
	}
	if requestID != "" {
		query.Set("requestId", requestID)
	}
	request, err := http.NewRequestWithContext(hook.ctx, http.MethodGet, hook.base+"/hook/pre-prompt?"+query.Encode(), nil)
	if err != nil {
		return pulledInjection{}
	}
	hook.headers(request)
	response, err := hook.client.Do(request)
	if err != nil {
		hook.diagnostics.failed(classifyTransportError(err))
		return pulledInjection{}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		hook.diagnostics.failed(classifyHTTPFailure(response.StatusCode, readFailureBody(response)))
		return pulledInjection{}
	}
	// Injection bodies are text/plain. A JSON body is a controller status reply
	// and must never reach the model as context.
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		return pulledInjection{}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return pulledInjection{}
	}
	return pulledInjection{
		text:            strings.TrimSpace(string(data)),
		acknowledgement: response.Header.Get("X-October-Resource-Ack"),
		receipt:         response.Header.Get("X-October-Inbox-Receipt"),
	}
}

func (hook *hookInvocation) acknowledge(pulled pulledInjection) {
	if pulled.receipt != "" {
		hook.post("/hook/inbox-ack", map[string]any{"receipt": pulled.receipt})
	}
	if pulled.acknowledgement != "" {
		hook.post("/hook/context-ack", map[string]any{"acknowledgement": pulled.acknowledgement})
	}
}

func (hook *hookInvocation) write(value string) bool {
	_, err := io.WriteString(hook.stdout, value)
	return err == nil
}

func hookEnvelope(eventName, text string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": eventName, "additionalContext": text}})
	return strings.TrimSuffix(buffer.String(), "\n")
}

// transcriptAgent tells Codex from Claude by transcript path shape when no
// flavor was given: Codex rollouts live under sessions/ as rollout-*.jsonl.
func transcriptAgent(file string) string {
	normalized := strings.ReplaceAll(file, "\\", "/")
	if strings.Contains(normalized, "/sessions/") || (strings.HasPrefix(path.Base(normalized), "rollout-") && strings.HasSuffix(normalized, ".jsonl")) {
		return "codex"
	}
	return "claude"
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}

func nested(values map[string]any, key string) map[string]any {
	if inner, ok := values[key].(map[string]any); ok {
		return inner
	}
	return map[string]any{}
}

func truncate(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
