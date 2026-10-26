package spec_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/october-dev/agentintercom/bus"
)

func TestCodexAdapterUsesTokenFreeSelfRegistration(t *testing.T) {
	configuration, err := os.ReadFile(filepath.Join("..", "adapters", "codex", "config.toml.example"))
	requireNoError(t, err)
	text := string(configuration)
	if !strings.Contains(text, `"--scope"`) || !strings.Contains(text, `"--agent"`) {
		t.Fatal("Codex adapter must explicitly select the local scope and execution identity")
	}
	if strings.Contains(text, "TOKEN") || strings.Contains(text, "env_vars") {
		t.Fatal("self-registering Codex config must not forward any Bus credential")
	}
}

func readJSON(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	requireNoError(t, err)
	var value any
	requireNoError(t, json.Unmarshal(data, &value))
	return value
}

func resolvedSchema(t *testing.T, path, ref string) *jsonschema.Resolved {
	t.Helper()
	data, err := os.ReadFile(path)
	requireNoError(t, err)
	var schema jsonschema.Schema
	requireNoError(t, json.Unmarshal(data, &schema))
	if ref != "" {
		schema.Ref = "#/$defs/" + ref
	}
	resolved, err := schema.Resolve(nil)
	requireNoError(t, err)
	return resolved
}

func requireValid(t *testing.T, schema *jsonschema.Resolved, value any) {
	t.Helper()
	if err := schema.Validate(value); err != nil {
		t.Fatalf("expected valid value: %v", err)
	}
}

func requireInvalid(t *testing.T, schema *jsonschema.Resolved, value any) {
	t.Helper()
	if err := schema.Validate(value); err == nil {
		t.Fatal("expected schema validation failure")
	}
}

func jsonValue(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	requireNoError(t, err)
	var result any
	requireNoError(t, json.Unmarshal(data, &result))
	return result
}

func TestProtocolSchemas(t *testing.T) {
	path := filepath.Join("0.1", "schemas", "protocol.schema.json")
	register := resolvedSchema(t, path, "registerAgentInput")
	requireValid(t, register, map[string]any{
		"id": "reviewer", "displayName": "Reviewer", "leaseMs": float64(30000),
		"capabilities": []any{map[string]any{"name": "review"}},
	})
	requireInvalid(t, register, map[string]any{"id": "bad id", "displayName": "Reviewer"})
	requireInvalid(t, register, map[string]any{"displayName": "Reviewer", "unknown": true})

	link := resolvedSchema(t, path, "linkAgentsInput")
	requireValid(t, link, map[string]any{"left": "planner", "right": "reviewer"})
	requireInvalid(t, link, map[string]any{"left": "planner"})
	requireInvalid(t, link, map[string]any{"left": "bad id", "right": "reviewer"})
	requireInvalid(t, link, map[string]any{"left": "planner", "right": "reviewer", "unknown": true})

	send := resolvedSchema(t, path, "sendMessageInput")
	requireValid(t, send, map[string]any{
		"to": "reviewer", "body": "Review this", "mode": "request",
		"idempotencyKey": "4e3de56b-8f21-40b4-9e94-cd29185850ce",
	})
	requireValid(t, send, map[string]any{
		"to": "planner", "body": "Found an issue", "mode": "response", "responseTo": "msg_123",
	})
	requireInvalid(t, send, map[string]any{
		"to": "planner", "body": "Missing correlation", "mode": "response",
	})
	requireInvalid(t, send, map[string]any{
		"to": "planner", "body": "Unexpected correlation", "mode": "notify", "responseTo": "msg_123",
	})

	acknowledgeInput := resolvedSchema(t, path, "acknowledgeMessagesInput")
	requireValid(t, acknowledgeInput, map[string]any{"messageIds": []any{"msg_123"}})
	requireValid(t, acknowledgeInput, map[string]any{})
	requireValid(t, acknowledgeInput, map[string]any{"messageIds": []any{}})
	tooManyMessageIDs := make([]any, 101)
	for index := range tooManyMessageIDs {
		tooManyMessageIDs[index] = "msg_123"
	}
	requireInvalid(t, acknowledgeInput, map[string]any{"messageIds": tooManyMessageIDs})
	requireInvalid(t, acknowledgeInput, map[string]any{"messageIds": []any{"bad id"}})
	requireInvalid(t, acknowledgeInput, map[string]any{"messageIds": []any{}, "unknown": true})

	acknowledgeResult := resolvedSchema(t, path, "acknowledgeMessagesResult")
	requireValid(t, acknowledgeResult, map[string]any{"acknowledged": float64(0)})
	requireInvalid(t, acknowledgeResult, map[string]any{})
	requireInvalid(t, acknowledgeResult, map[string]any{"acknowledged": float64(-1)})
	requireInvalid(t, acknowledgeResult, map[string]any{"acknowledged": 1.5})
	requireInvalid(t, acknowledgeResult, map[string]any{"acknowledged": float64(0), "unknown": true})

	for name, field := range map[string]string{
		"linkAgentsResult": "linked", "releaseInboxResult": "released",
	} {
		result := resolvedSchema(t, path, name)
		requireValid(t, result, map[string]any{field: true})
		requireInvalid(t, result, map[string]any{field: false})
		requireInvalid(t, result, map[string]any{})
		requireInvalid(t, result, map[string]any{field: true, "unknown": true})
	}

	completeTask := resolvedSchema(t, path, "completeTaskInput")
	requireValid(t, completeTask, map[string]any{})
	requireValid(t, completeTask, map[string]any{"note": ""})
	requireValid(t, completeTask, map[string]any{"note": "Reviewed and merged"})
	requireInvalid(t, completeTask, map[string]any{"note": "   "})
	requireInvalid(t, completeTask, map[string]any{"note": strings.Repeat("x", 16385)})
	requireInvalid(t, completeTask, map[string]any{"note": "done", "status": "done"})

	resolveEscalation := resolvedSchema(t, path, "resolveEscalationInput")
	requireValid(t, resolveEscalation, map[string]any{"answer": "yes"})
	requireInvalid(t, resolveEscalation, map[string]any{})
	requireInvalid(t, resolveEscalation, map[string]any{"answer": ""})
	requireInvalid(t, resolveEscalation, map[string]any{"answer": "   "})
	requireInvalid(t, resolveEscalation, map[string]any{"answer": strings.Repeat("x", 16385)})
	requireInvalid(t, resolveEscalation, map[string]any{"answer": "yes", "unknown": true})

	for _, name := range []string{"commitInboxResult", "agentList", "taskList", "taskProgressList", "escalationList"} {
		list := resolvedSchema(t, path, name)
		requireValid(t, list, []any{})
		requireInvalid(t, list, map[string]any{})
		requireInvalid(t, list, []any{map[string]any{"unknown": true}})
	}

	reserve := resolvedSchema(t, path, "reserveInboxInput")
	requireValid(t, reserve, map[string]any{"limit": float64(50), "waitMs": float64(25000)})
	requireValid(t, reserve, map[string]any{})
	requireValid(t, reserve, map[string]any{"limit": float64(0), "waitMs": float64(0)})
	requireInvalid(t, reserve, map[string]any{"waitMs": float64(25001)})
	requireInvalid(t, reserve, map[string]any{"waitMs": float64(1.5)})

	task := resolvedSchema(t, path, "task")
	requireValid(t, task, map[string]any{
		"id": "task_123", "scopeId": "scope", "title": "Review", "description": "Review the change",
		"createdBy": "planner", "claimedBy": "reviewer", "status": "done",
		"dependencies": []any{}, "ready": false, "recentProgress": []any{},
		"createdAt": "2026-08-30T00:00:00Z", "updatedAt": "2026-08-30T00:00:01Z",
	})
	requireValid(t, task, map[string]any{
		"id": "task_124", "scopeId": "scope", "title": "Plan", "description": "",
		"createdBy": nil, "status": "open", "dependencies": []any{}, "ready": true, "recentProgress": []any{},
		"createdAt": "2026-08-30T00:00:00Z", "updatedAt": "2026-08-30T00:00:01Z",
	})
	requireInvalid(t, task, map[string]any{
		"id": "task_123", "scopeId": "scope", "title": "Review", "description": "Review the change",
		"createdBy": "planner", "status": "claimed", "dependencies": []any{},
		"ready": false, "recentProgress": []any{},
		"createdAt": "2026-08-30T00:00:00Z", "updatedAt": "2026-08-30T00:00:01Z",
	})
	progress := resolvedSchema(t, path, "taskProgress")
	requireValid(t, progress, map[string]any{
		"taskId": "task_123", "sequence": float64(1), "agentId": "reviewer", "executionId": "exec_123",
		"kind": "blocker", "text": "Waiting for an API decision", "createdAt": "2026-08-30T00:00:01Z",
	})
	events := resolvedSchema(t, path, "eventBatch")
	requireValid(t, events, map[string]any{
		"scopeId": "scope", "events": []any{map[string]any{
			"id": "evt_123", "scopeId": "scope", "type": "task.created", "subjectId": "task_123",
			"revision": float64(1), "attributes": map[string]any{"status": "open"}, "createdAt": "2026-08-30T00:00:00Z",
		}},
		"nextRevision": float64(1), "currentRevision": float64(1), "minimumCursor": float64(0), "resyncRequired": false,
	})
	publication := resolvedSchema(t, path, "agentCardPublication")
	requireValid(t, publication, map[string]any{
		"id": "pub_123", "scopeId": "scope", "agentId": "reviewer", "enabled": true,
		"cardUrl":      "https://bus.example/a2a/agents/pub_123/.well-known/agent-card.json",
		"interfaceUrl": "https://bus.example/a2a/agents/pub_123",
		"createdAt":    "2026-09-02T00:00:00Z", "updatedAt": "2026-09-02T00:00:00Z",
	})
	publishInput := resolvedSchema(t, path, "publishAgentCardInput")
	requireValid(t, publishInput, map[string]any{"agentId": "reviewer"})
	requireInvalid(t, publishInput, map[string]any{"agentId": "reviewer", "scopeId": "scope"})
	principal := resolvedSchema(t, path, "a2aPrincipal")
	requireValid(t, principal, map[string]any{
		"id": "cred_123", "scopeId": "scope", "publicationId": "pub_123", "label": "Review service", "enabled": true,
		"createdAt": "2026-09-02T00:00:00Z", "updatedAt": "2026-09-02T00:00:00Z",
	})
	issuedPrincipal := resolvedSchema(t, path, "issuedA2APrincipal")
	requireValid(t, issuedPrincipal, map[string]any{
		"principal": map[string]any{
			"id": "cred_123", "scopeId": "scope", "publicationId": "pub_123", "label": "Review service", "enabled": true,
			"createdAt": "2026-09-02T00:00:00Z", "updatedAt": "2026-09-02T00:00:00Z",
		},
		"credential": "cred_123.secret",
	})
	createPrincipal := resolvedSchema(t, path, "createA2APrincipalInput")
	requireValid(t, createPrincipal, map[string]any{"publicationId": "pub_123", "label": "Review service"})
	requireInvalid(t, createPrincipal, map[string]any{"publicationId": "pub_123", "label": ""})
	principalUsage := resolvedSchema(t, path, "a2aPrincipalUsage")
	requireValid(t, principalUsage, map[string]any{
		"principalId": "cred_123", "publicationId": "pub_123", "unfinishedMessages": float64(2),
		"unfinishedBytes": float64(1024), "messageLimit": float64(1000), "byteLimit": float64(16777216),
	})
	requireInvalid(t, principalUsage, map[string]any{
		"principalId": "cred_123", "publicationId": "pub_123", "unfinishedMessages": float64(-1),
		"unfinishedBytes": float64(0), "messageLimit": float64(1000), "byteLimit": float64(16777216),
	})
	outputStream := resolvedSchema(t, path, "outputStream")
	requireValid(t, outputStream, map[string]any{
		"id": "out_123", "scopeId": "scope", "name": "site-preview", "retentionLimit": float64(1000),
		"currentSequence": float64(1), "minimumCursor": float64(0), "publisherAgentIds": []any{"reviewer"},
		"createdAt": "2026-09-02T00:00:00Z", "updatedAt": "2026-09-02T00:00:01Z",
	})
	publishOutput := resolvedSchema(t, path, "publishOutputInput")
	requireValid(t, publishOutput, map[string]any{
		"contentType": "application/json", "value": map[string]any{"status": "ready"},
		"reference": map[string]any{"uri": "https://example.test/preview", "title": "Preview"},
	})
	requireValid(t, publishOutput, map[string]any{"contentType": "text/plain", "value": "building"})
	requireInvalid(t, publishOutput, map[string]any{"contentType": "text/plain", "value": map[string]any{"status": "wrong"}})
	outputHistory := resolvedSchema(t, path, "outputHistory")
	requireValid(t, outputHistory, map[string]any{
		"streamId": "out_123", "values": []any{map[string]any{
			"streamId": "out_123", "sequence": float64(1), "producerType": "agent", "producerId": "reviewer",
			"contentType": "text/plain", "value": "ready", "createdAt": "2026-09-02T00:00:01Z",
		}},
		"nextSequence": float64(1), "currentSequence": float64(1), "minimumCursor": float64(0), "resyncRequired": false,
	})
	outputPrincipal := resolvedSchema(t, path, "issuedOutputPrincipal")
	requireValid(t, outputPrincipal, map[string]any{
		"principal": map[string]any{
			"id": "cred_456", "scopeId": "scope", "streamId": "out_123", "label": "Dashboard",
			"permissions": []any{"read"}, "enabled": true,
			"createdAt": "2026-09-02T00:00:00Z", "updatedAt": "2026-09-02T00:00:00Z",
		},
		"credential": "cred_456.secret",
	})

	prune := resolvedSchema(t, path, "pruneScopeInput")
	requireValid(t, prune, map[string]any{"before": "2026-08-01T00:00:00Z"})
	requireValid(t, prune, map[string]any{"before": "2026-08-01T00:00:00Z", "execute": true})
}

func TestPortableScopeArchiveSchema(t *testing.T) {
	schema := resolvedSchema(t, filepath.Join("0.1", "schemas", "scope-archive.schema.json"), "")
	archive := bus.ScopeArchive{
		Format: bus.ScopeArchiveFormat, Version: bus.ScopeArchiveVersion,
		ExportedAt: "2026-09-02T00:00:00Z",
		Scope:      bus.ArchivedScope{ID: "project", CreatedAt: "2026-09-01T00:00:00Z"},
		Agents: []bus.ArchivedAgent{{
			ID: "reviewer", DisplayName: "Reviewer", Capabilities: []bus.AgentCapability{{Name: "review"}},
			RegisteredAt: "2026-09-01T00:00:00Z", UpdatedAt: "2026-09-01T00:00:01Z",
		}},
		Links:                 []bus.ArchivedPeerLink{},
		Messages:              []bus.ArchivedMessage{},
		Tasks:                 []bus.ArchivedTask{},
		TaskProgress:          []bus.ArchivedTaskProgress{},
		Escalations:           []bus.ArchivedEscalation{},
		AgentCardPublications: []bus.ArchivedAgentCard{},
		A2ATasks:              []bus.ArchivedA2ATask{},
		A2AMessages:           []bus.ArchivedA2AMessage{},
		OutputStreams:         []bus.ArchivedOutputStream{},
		OutputValues:          []bus.ArchivedOutputValue{},
	}
	value := jsonValue(t, archive)
	requireValid(t, schema, value)
	object := value.(map[string]any)
	object["scopeToken"] = "must-not-be-portable"
	requireInvalid(t, schema, object)
}

func TestFileWireDataAndExportMatchSchemas(t *testing.T) {
	r, err := bus.Open(":memory:")
	requireNoError(t, err)
	defer r.Close()
	ctx := context.Background()
	scope, err := r.CreateScope(ctx, bus.CreateScopeInput{})
	requireNoError(t, err)
	agent, err := r.RegisterAgent(ctx, scope.ScopeToken, bus.RegisterAgentInput{ID: "files", DisplayName: "Files"})
	requireNoError(t, err)
	file, err := r.UploadAttachment(ctx, agent.AgentToken, bus.UploadAttachmentInput{Name: "empty.bin"}, []byte{})
	requireNoError(t, err)
	protocol := filepath.Join("0.1", "schemas", "protocol.schema.json")
	requireValid(t, resolvedSchema(t, protocol, "attachment"), jsonValue(t, file))
	chunk, err := r.ReadAttachment(ctx, agent.AgentToken, file.ID, 0, 100)
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, protocol, "attachmentContent"), jsonValue(t, chunk))
	requireNoError(t, r.RetireAgent(ctx, agent.AgentToken))
	archive, err := r.ExportScope(ctx, scope.ScopeID)
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, filepath.Join("0.1", "schemas", "scope-archive.schema.json"), ""), jsonValue(t, archive))
}

func TestAdapterManifestsMatchSchema(t *testing.T) {
	schema := resolvedSchema(t, filepath.Join("0.1", "schemas", "adapter-manifest.schema.json"), "")
	paths, err := filepath.Glob(filepath.Join("..", "adapters", "*", "adapter.json"))
	requireNoError(t, err)
	if len(paths) == 0 {
		t.Fatal("no adapter manifests found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			requireValid(t, schema, readJSON(t, path))
		})
	}
	requireInvalid(t, schema, map[string]any{
		"schemaVersion": float64(1), "id": "unproven", "harnessFamily": "Unknown",
		"adapterVersion": "0.1.0",
		"status":         "verified", "protocolVersions": []any{"0.1"}, "transport": "mcp",
		"delivery": "pull", "lifecycleEvidence": "process", "capabilities": []any{},
		"testedVersions": []any{}, "platforms": []any{"linux"}, "maintainer": "October",
		"repository": "https://github.com/october-dev/agentintercom",
	})
}

func TestCompatibilityEvidenceSchema(t *testing.T) {
	schema := resolvedSchema(t, filepath.Join("0.1", "schemas", "compatibility-evidence.schema.json"), "")
	evidence := map[string]any{
		"schemaVersion": float64(1), "harnessFamily": "Example", "harnessVersion": "1.0.0",
		"adapterId": "example-mcp", "adapterVersion": "0.1.0", "protocolVersion": "0.1",
		"runtimeVersion": "0.1.0", "operatingSystem": "linux", "architecture": "amd64",
		"profile": "mcp-adapter", "result": "passed",
		"resultDigest":     "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"verifiedAt":       "2026-08-30T00:00:00Z",
		"repositoryCommit": "0123456789abcdef0123456789abcdef01234567",
		"verificationMode": "automated", "limitations": []any{},
	}
	requireValid(t, schema, evidence)
	evidence["resultDigest"] = "unverified"
	requireInvalid(t, schema, evidence)
}

func TestCompatibilityRegistrySchema(t *testing.T) {
	schema := resolvedSchema(t, filepath.Join("0.1", "schemas", "compatibility-registry.schema.json"), "")
	repositoryRoot := filepath.Join("..")
	registry := readJSON(t, filepath.Join(repositoryRoot, "compatibility", "registry.json"))
	requireValid(t, schema, registry)

	value, ok := registry.(map[string]any)
	if !ok {
		t.Fatal("compatibility registry must be an object")
	}
	paths, ok := value["verified"].([]any)
	if !ok {
		t.Fatal("compatibility registry verified field must be an array")
	}
	evidenceSchema := resolvedSchema(t, filepath.Join("0.1", "schemas", "compatibility-evidence.schema.json"), "")
	for _, entry := range paths {
		path, ok := entry.(string)
		if !ok {
			t.Fatal("compatibility registry entries must be strings")
		}
		requireValid(t, evidenceSchema, readJSON(t, filepath.Join(repositoryRoot, "compatibility", path)))
	}
}

func TestReferenceRuntimeResponsesMatchProtocolSchemas(t *testing.T) {
	ctx := context.Background()
	runtimeValue, err := bus.Open(":memory:")
	requireNoError(t, err)
	server := bus.NewServer(runtimeValue, bus.ServerOptions{AdminToken: "schema-admin-token"})
	address, err := server.Start()
	requireNoError(t, err)
	defer server.Stop(context.Background())
	client := bus.Client{Address: address, Token: "schema-admin-token"}
	path := filepath.Join("0.1", "schemas", "protocol.schema.json")

	health, err := client.Health(ctx)
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "health"), jsonValue(t, health))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/health/live", nil)
	requireNoError(t, err)
	response, err := http.DefaultClient.Do(request)
	requireNoError(t, err)
	var liveness bus.Liveness
	decodeErr := json.NewDecoder(response.Body).Decode(&liveness)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil {
		t.Fatalf("unexpected liveness response: HTTP %d, %v", response.StatusCode, decodeErr)
	}
	requireValid(t, resolvedSchema(t, path, "liveness"), jsonValue(t, liveness))
	scope, err := client.CreateScope(ctx, bus.CreateScopeInput{ID: "schema"})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "createScopeResult"), jsonValue(t, scope))
	scopes, err := client.ListScopes(ctx)
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "scopeInfo"), jsonValue(t, scopes[0]))
	requireValid(t, resolvedSchema(t, path, "retireResult"), map[string]any{"retired": true})
	requireValid(t, resolvedSchema(t, path, "deleteScopeInput"), map[string]any{"confirmScopeId": scope.ScopeID})
	requireValid(t, resolvedSchema(t, path, "deleteScopeResult"), map[string]any{"deleted": true})
	owner := bus.Client{Address: address, Token: scope.ScopeToken}
	credential, err := owner.Credential(ctx, "")
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "credential"), jsonValue(t, credential))
	page, err := owner.TaskPage(ctx, "", 10)
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "taskPage"), jsonValue(t, page))
	plannerRegistration, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "planner", DisplayName: "Planner"})
	requireNoError(t, err)
	status, err := (bus.Client{Address: address, Token: plannerRegistration.AgentToken}).NodeStatus(ctx)
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "nodeStatus"), jsonValue(t, status))
	reviewerRegistration, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "reviewer", DisplayName: "Reviewer", ConnectTo: []string{"planner"}})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "registerAgentResult"), jsonValue(t, reviewerRegistration))
	publication, err := owner.CreateAgentCardPublication(ctx, bus.PublishAgentCardInput{AgentID: "reviewer"})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "agentCardPublication"), jsonValue(t, publication))
	principal, err := owner.CreateA2APrincipal(ctx, bus.CreateA2APrincipalInput{PublicationID: publication.ID, Label: "Review service"})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "issuedA2APrincipal"), jsonValue(t, principal))
	principalUsage, err := owner.ListA2APrincipalUsage(ctx)
	require(t, err == nil && len(principalUsage) == 1, "unexpected principal usage: %#v, %v", principalUsage, err)
	requireValid(t, resolvedSchema(t, path, "a2aPrincipalUsage"), jsonValue(t, principalUsage[0]))
	outputStream, err := owner.CreateOutputStream(ctx, bus.CreateOutputStreamInput{
		Name: "site-preview", PublisherAgentIDs: []string{"reviewer"},
	})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "outputStream"), jsonValue(t, outputStream))
	outputPrincipal, err := owner.CreateOutputPrincipal(ctx, bus.CreateOutputPrincipalInput{
		StreamID: outputStream.ID, Label: "Dashboard", Permissions: []bus.OutputPermission{bus.OutputRead},
	})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "issuedOutputPrincipal"), jsonValue(t, outputPrincipal))
	planner := bus.Client{Address: address, Token: plannerRegistration.AgentToken}
	reviewer := bus.Client{Address: address, Token: reviewerRegistration.AgentToken}
	output, err := reviewer.PublishOutput(ctx, outputStream.ID, bus.PublishOutputInput{ContentType: bus.OutputText, Value: "ready"})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "outputValue"), jsonValue(t, output))
	history, err := (bus.Client{Address: address, Token: outputPrincipal.Credential}).OutputHistory(ctx, outputStream.ID, 0, 50)
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "outputHistory"), jsonValue(t, history))
	agent, err := planner.Heartbeat(ctx, bus.HeartbeatInput{Lifecycle: bus.LifecycleReady, Ready: true})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "agent"), jsonValue(t, agent))
	receipt, err := planner.SendMessage(ctx, bus.SendMessageInput{To: "reviewer", Body: "Review", Mode: bus.MessageRequest})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "deliveryReceipt"), jsonValue(t, receipt))
	messages, err := reviewer.PullInbox(ctx, 10, 0)
	require(t, err == nil && len(messages) == 1, "unexpected messages: %#v, %v", messages, err)
	requireValid(t, resolvedSchema(t, path, "message"), jsonValue(t, messages[0]))
	acknowledgeSchema := resolvedSchema(t, path, "acknowledgeMessagesResult")
	acknowledge := func(expected float64) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, address+"/v1/messages/ack", strings.NewReader(`{"messageIds":["`+messages[0].ID+`"]}`))
		requireNoError(t, err)
		request.Header.Set("Authorization", "Bearer "+reviewerRegistration.AgentToken)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		requireNoError(t, err)
		var envelope struct {
			OK     bool           `json:"ok"`
			Result map[string]any `json:"result"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&envelope)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil || !envelope.OK {
			t.Fatalf("unexpected acknowledgement response: HTTP %d, %#v, %v", response.StatusCode, envelope, decodeErr)
		}
		requireValid(t, acknowledgeSchema, envelope.Result)
		if envelope.Result["acknowledged"] != expected {
			t.Fatalf("acknowledged = %#v, want %v", envelope.Result["acknowledged"], expected)
		}
	}
	acknowledge(1)
	acknowledge(0)
	task, err := planner.AddTask(ctx, bus.AddTaskInput{Title: "Review"})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "task"), jsonValue(t, task))
	if _, err := planner.ClaimTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	progressEntry, err := planner.AddTaskProgress(ctx, task.ID, bus.AddTaskProgressInput{Kind: "progress", Text: "Review started"})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "taskProgress"), jsonValue(t, progressEntry))
	progressHistory, err := owner.ListTaskProgress(ctx, task.ID)
	require(t, err == nil && len(progressHistory) == 1, "unexpected progress history: %#v, %v", progressHistory, err)
	events, err := owner.Events(ctx, 0, 100, 0)
	require(t, err == nil && len(events.Events) != 0, "unexpected scope events: %#v, %v", events, err)
	requireValid(t, resolvedSchema(t, path, "eventBatch"), jsonValue(t, events))
	storage, err := owner.StorageSummary(ctx)
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "storageSummary"), jsonValue(t, storage))
	pruneResult, err := owner.PruneScope(ctx, bus.PruneScopeInput{Before: "2026-08-01T00:00:00Z"})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "pruneScopeResult"), jsonValue(t, pruneResult))
	escalation, err := reviewer.AskHuman(ctx, bus.AskHumanInput{Question: "Proceed?", Options: []string{"yes", "no"}})
	requireNoError(t, err)
	requireValid(t, resolvedSchema(t, path, "humanEscalation"), jsonValue(t, escalation))

	// Results the Go client discards are read from the raw response envelope.
	postResult := func(route, token string, body any) any {
		t.Helper()
		payload, err := json.Marshal(body)
		requireNoError(t, err)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, address+route, bytes.NewReader(payload))
		requireNoError(t, err)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		requireNoError(t, err)
		defer response.Body.Close()
		var envelope struct {
			OK     bool `json:"ok"`
			Result any  `json:"result"`
		}
		requireNoError(t, json.NewDecoder(response.Body).Decode(&envelope))
		require(t, response.StatusCode == http.StatusOK && envelope.OK, "POST %s: HTTP %d, %#v", route, response.StatusCode, envelope)
		return envelope.Result
	}
	linkInput := map[string]any{"left": "planner", "right": "reviewer"}
	requireValid(t, resolvedSchema(t, path, "linkAgentsInput"), linkInput)
	requireValid(t, resolvedSchema(t, path, "linkAgentsResult"), postResult("/v1/links", scope.ScopeToken, linkInput))

	agents, err := owner.ListAgents(ctx)
	require(t, err == nil && len(agents) == 2, "unexpected agents: %#v, %v", agents, err)
	requireValid(t, resolvedSchema(t, path, "agentList"), jsonValue(t, agents))
	peers, err := planner.ListPeers(ctx)
	require(t, err == nil && len(peers) == 1, "unexpected peers: %#v, %v", peers, err)
	requireValid(t, resolvedSchema(t, path, "agentList"), jsonValue(t, peers))

	for _, body := range []string{"Commit me", "Release me"} {
		_, err := planner.SendMessage(ctx, bus.SendMessageInput{To: "reviewer", Body: body})
		requireNoError(t, err)
		reservation, err := reviewer.ReserveInbox(ctx, 1, 0)
		require(t, err == nil && reservation != nil, "unexpected reservation: %#v, %v", reservation, err)
		if body == "Commit me" {
			committed, err := reviewer.CommitInbox(ctx, reservation.ID)
			require(t, err == nil && len(committed) == 1, "unexpected commit: %#v, %v", committed, err)
			requireValid(t, resolvedSchema(t, path, "commitInboxResult"), jsonValue(t, committed))
			continue
		}
		released := postResult("/v1/inbox/"+reservation.ID+"/release", reviewerRegistration.AgentToken, map[string]any{})
		requireValid(t, resolvedSchema(t, path, "releaseInboxResult"), released)
	}

	tasks, err := owner.ListTasks(ctx, false)
	require(t, err == nil && len(tasks) == 1, "unexpected tasks: %#v, %v", tasks, err)
	requireValid(t, resolvedSchema(t, path, "taskList"), jsonValue(t, tasks))
	requireValid(t, resolvedSchema(t, path, "taskProgressList"), jsonValue(t, progressHistory))
	completeInput := map[string]any{"note": "Reviewed"}
	requireValid(t, resolvedSchema(t, path, "completeTaskInput"), completeInput)
	completed := postResult("/v1/tasks/"+task.ID+"/complete", plannerRegistration.AgentToken, completeInput)
	requireValid(t, resolvedSchema(t, path, "task"), completed)

	escalations, err := owner.ListEscalations(ctx)
	require(t, err == nil && len(escalations) == 1, "unexpected escalations: %#v, %v", escalations, err)
	requireValid(t, resolvedSchema(t, path, "escalationList"), jsonValue(t, escalations))
	resolveInput := map[string]any{"answer": "yes"}
	requireValid(t, resolvedSchema(t, path, "resolveEscalationInput"), resolveInput)
	resolved := postResult("/v1/scope/escalations/"+escalation.ID+"/resolve", scope.ScopeToken, resolveInput)
	requireValid(t, resolvedSchema(t, path, "humanEscalation"), resolved)
}
