package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/october-dev/agentintercom/bus"
)

const ProfileLocalRuntime = "local-runtime"

type Options struct {
	Address    string
	AdminToken string
}

type Result struct {
	Profile         string    `json:"profile"`
	ProtocolVersion string    `json:"protocolVersion"`
	RuntimeVersion  string    `json:"runtimeVersion"`
	StartedAt       string    `json:"startedAt"`
	CompletedAt     string    `json:"completedAt"`
	Passed          []string  `json:"passed"`
	Failed          []Failure `json:"failed"`
}

type Failure struct {
	Check string `json:"check"`
	Error string `json:"error"`
}

type recorder struct {
	result *Result
}

func (r recorder) check(name string, fn func() error) error {
	if err := fn(); err != nil {
		r.result.Failed = append(r.result.Failed, Failure{Check: name, Error: err.Error()})
		return fmt.Errorf("%s: %w", name, err)
	}
	r.result.Passed = append(r.result.Passed, name)
	return nil
}

func requireCode(err error, code bus.ErrorCode) error {
	if err == nil {
		return fmt.Errorf("expected %s", code)
	}
	var failure *bus.BusError
	if !errors.As(err, &failure) || failure.Code != code {
		return fmt.Errorf("expected %s, got %v", code, err)
	}
	return nil
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(clone)
}

func requireStructuredArray(result *mcp.CallToolResult, field string) error {
	if result == nil || result.IsError {
		return fmt.Errorf("tool returned an error: %#v", result)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		return fmt.Errorf("structured content must be an object: %#v", result.StructuredContent)
	}
	if _, ok := structured[field].([]any); !ok {
		return fmt.Errorf("structured content must contain %s array: %#v", field, result.StructuredContent)
	}
	return nil
}

// Run exercises the local runtime profile through the public HTTP and MCP interfaces.
func Run(ctx context.Context, options Options) (result Result, runErr error) {
	result = Result{
		Profile: ProfileLocalRuntime, StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Passed: []string{}, Failed: []Failure{},
	}
	defer func() { result.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano) }()
	record := recorder{result: &result}
	admin := bus.Client{Address: options.Address, Token: options.AdminToken}
	var health bus.Health
	if err := record.check("health-and-version", func() error {
		var err error
		health, err = admin.Health(ctx)
		if err != nil {
			return err
		}
		if health.ProtocolVersion != bus.ProtocolVersion || health.Status != "ready" {
			return fmt.Errorf("unexpected health response: %#v", health)
		}
		return nil
	}); err != nil {
		return result, err
	}
	result.ProtocolVersion = health.ProtocolVersion
	result.RuntimeVersion = health.RuntimeVersion

	scopeID := fmt.Sprintf("conformance-%d", time.Now().UnixNano())
	var scope bus.CreateScopeResult
	if err := record.check("scope-authority", func() error {
		var err error
		scope, err = admin.CreateScope(ctx, bus.CreateScopeInput{ID: scopeID})
		if err != nil {
			return err
		}
		_, err = (bus.Client{Address: options.Address}).CreateScope(ctx, bus.CreateScopeInput{})
		return requireCode(err, bus.CodeUnauthenticated)
	}); err != nil {
		return result, err
	}
	owner := bus.Client{Address: options.Address, Token: scope.ScopeToken}

	if err := record.check("portable-scope-archive", func() error {
		archive := bus.ScopeArchive{
			Format: bus.ScopeArchiveFormat, Version: bus.ScopeArchiveVersion,
			ExportedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Scope:      bus.ArchivedScope{ID: scopeID + "-archive", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
			Agents:     []bus.ArchivedAgent{}, Links: []bus.ArchivedPeerLink{}, Messages: []bus.ArchivedMessage{},
			Tasks: []bus.ArchivedTask{}, TaskProgress: []bus.ArchivedTaskProgress{}, Escalations: []bus.ArchivedEscalation{},
			AgentCardPublications: []bus.ArchivedAgentCard{}, A2ATasks: []bus.ArchivedA2ATask{}, A2AMessages: []bus.ArchivedA2AMessage{},
			OutputStreams: []bus.ArchivedOutputStream{}, OutputValues: []bus.ArchivedOutputValue{},
		}
		imported, err := admin.ImportScope(ctx, archive)
		if err != nil || !imported.Imported || imported.ScopeID != archive.Scope.ID || imported.ScopeToken == "" {
			return fmt.Errorf("unexpected archive import: %#v, %v", imported, err)
		}
		retry, err := admin.ImportScope(ctx, archive)
		if err != nil || retry.Imported || retry.ScopeID != imported.ScopeID || retry.ScopeToken != "" {
			return fmt.Errorf("archive retry was not idempotent: %#v, %v", retry, err)
		}
		exported, err := admin.ExportScope(ctx, imported.ScopeID)
		if err != nil || exported.Scope.ID != imported.ScopeID || exported.Format != bus.ScopeArchiveFormat || exported.Version != bus.ScopeArchiveVersion {
			return fmt.Errorf("unexpected archive export: %#v, %v", exported, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	var plannerRegistration, reviewerRegistration bus.RegisterAgentResult
	if err := record.check("registration-and-peer-link", func() error {
		var err error
		plannerRegistration, err = owner.RegisterAgent(ctx, bus.RegisterAgentInput{
			ID: "planner", DisplayName: "Planner", LeaseMS: 30000,
			Capabilities: []bus.AgentCapability{{Name: "planning"}},
		})
		if err != nil {
			return err
		}
		reviewerRegistration, err = owner.RegisterAgent(ctx, bus.RegisterAgentInput{
			ID: "reviewer", DisplayName: "Reviewer", LeaseMS: 30000, ConnectTo: []string{"planner"},
			Capabilities: []bus.AgentCapability{{Name: "review"}},
		})
		return err
	}); err != nil {
		return result, err
	}
	planner := bus.Client{Address: options.Address, Token: plannerRegistration.AgentToken}
	reviewer := bus.Client{Address: options.Address, Token: reviewerRegistration.AgentToken}
	if err := record.check("scope-route-authority-errors", func() error {
		if _, err := planner.ListAgents(ctx); requireCode(err, bus.CodePermissionDenied) != nil {
			return fmt.Errorf("agent credential on scope route: %v", err)
		}
		_, err := (bus.Client{Address: options.Address}).ListAgents(ctx)
		return requireCode(err, bus.CodeUnauthenticated)
	}); err != nil {
		return result, err
	}

	var reviewerPublication bus.AgentCardPublication
	if err := record.check("owner-controlled-agent-card-publication", func() error {
		publication, err := owner.CreateAgentCardPublication(ctx, bus.PublishAgentCardInput{AgentID: "reviewer"})
		if err != nil || !publication.Enabled || publication.ID == "" {
			return fmt.Errorf("unexpected publication: %#v, %v", publication, err)
		}
		reviewerPublication = publication
		listed, err := owner.ListAgentCardPublications(ctx)
		if err != nil || len(listed) != 1 || listed[0].ID != publication.ID {
			return fmt.Errorf("unexpected publications: %#v, %v", listed, err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, publication.CardURL, nil)
		if err != nil {
			return err
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		var card a2a.AgentCard
		decodeErr := json.NewDecoder(response.Body).Decode(&card)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil || card.Name != "Reviewer" || len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].URL != publication.InterfaceURL {
			return fmt.Errorf("unexpected published card: status=%d card=%#v error=%v", response.StatusCode, card, decodeErr)
		}
		disabled, err := owner.SetAgentCardPublicationEnabled(ctx, publication.ID, false)
		if err != nil || disabled.Enabled || disabled.CardURL != publication.CardURL {
			return fmt.Errorf("unexpected disabled publication: %#v, %v", disabled, err)
		}
		request, _ = http.NewRequestWithContext(ctx, http.MethodGet, publication.CardURL, nil)
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("disabled card returned HTTP %d", response.StatusCode)
		}
		enabled, err := owner.SetAgentCardPublicationEnabled(ctx, publication.ID, true)
		if err != nil || !enabled.Enabled || enabled.CardURL != publication.CardURL {
			return fmt.Errorf("unexpected enabled publication: %#v, %v", enabled, err)
		}
		_, err = planner.CreateAgentCardPublication(ctx, bus.PublishAgentCardInput{AgentID: "planner"})
		return requireCode(err, bus.CodePermissionDenied)
	}); err != nil {
		return result, err
	}

	if err := record.check("scoped-a2a-principal-credentials", func() error {
		issued, err := owner.CreateA2APrincipal(ctx, bus.CreateA2APrincipalInput{
			PublicationID: reviewerPublication.ID, Label: "Conformance caller",
		})
		if err != nil || issued.Principal.ID == "" || issued.Credential == "" || !issued.Principal.Enabled {
			return fmt.Errorf("unexpected issued principal: %#v, %v", issued, err)
		}
		listed, err := owner.ListA2APrincipals(ctx)
		if err != nil || len(listed) != 1 || listed[0].ID != issued.Principal.ID {
			return fmt.Errorf("unexpected principals: %#v, %v", listed, err)
		}
		remote := bus.Client{Address: options.Address, Token: issued.Credential}
		if _, err := remote.ListAgents(ctx); requireCode(err, bus.CodePermissionDenied) != nil {
			return fmt.Errorf("scoped credential accessed Bus APIs: %v", err)
		}
		disabled, err := owner.SetA2APrincipalEnabled(ctx, issued.Principal.ID, false)
		if err != nil || disabled.Enabled {
			return fmt.Errorf("unexpected disabled principal: %#v, %v", disabled, err)
		}
		rotated, err := owner.RotateA2APrincipal(ctx, issued.Principal.ID)
		if err != nil || rotated.Credential == "" || rotated.Credential == issued.Credential || rotated.Principal.Enabled {
			return fmt.Errorf("unexpected rotated principal: %#v, %v", rotated, err)
		}
		enabled, err := owner.SetA2APrincipalEnabled(ctx, issued.Principal.ID, true)
		if err != nil || !enabled.Enabled {
			return fmt.Errorf("unexpected enabled principal: %#v, %v", enabled, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	var outputStream bus.OutputStream
	if err := record.check("addressable-output-streams", func() error {
		stream, err := owner.CreateOutputStream(ctx, bus.CreateOutputStreamInput{
			Name: "build-status", RetentionLimit: 2, PublisherAgentIDs: []string{"reviewer"},
		})
		if err != nil || stream.ID == "" || stream.RetentionLimit != 2 {
			return fmt.Errorf("unexpected output stream: %#v, %v", stream, err)
		}
		outputStream = stream
		reader, err := owner.CreateOutputPrincipal(ctx, bus.CreateOutputPrincipalInput{
			StreamID: stream.ID, Label: "Dashboard", Permissions: []bus.OutputPermission{bus.OutputRead},
		})
		if err != nil || reader.Credential == "" {
			return fmt.Errorf("unexpected output reader: %#v, %v", reader, err)
		}
		for _, text := range []string{"queued", "building", "ready"} {
			if _, err := reviewer.PublishOutput(ctx, stream.ID, bus.PublishOutputInput{ContentType: bus.OutputText, Value: text}); err != nil {
				return err
			}
		}
		outputReader := bus.Client{Address: options.Address, Token: reader.Credential}
		latest, err := outputReader.LatestOutput(ctx, stream.ID)
		if err != nil || latest == nil || latest.Sequence != 3 || latest.Value != "ready" {
			return fmt.Errorf("unexpected latest output: %#v, %v", latest, err)
		}
		history, err := outputReader.OutputHistory(ctx, stream.ID, 1, 10)
		if err != nil || history.ResyncRequired || len(history.Values) != 2 {
			return fmt.Errorf("unexpected output history: %#v, %v", history, err)
		}
		stale, err := outputReader.OutputHistory(ctx, stream.ID, 0, 10)
		if err != nil || !stale.ResyncRequired || stale.MinimumCursor != 1 {
			return fmt.Errorf("unexpected stale output cursor: %#v, %v", stale, err)
		}
		if _, err := outputReader.PublishOutput(ctx, stream.ID, bus.PublishOutputInput{ContentType: bus.OutputText, Value: "denied"}); requireCode(err, bus.CodeUnauthenticated) != nil {
			return fmt.Errorf("read credential published output: %v", err)
		}
		if _, err := planner.PublishOutput(ctx, stream.ID, bus.PublishOutputInput{ContentType: bus.OutputText, Value: "denied"}); requireCode(err, bus.CodePermissionDenied) != nil {
			return fmt.Errorf("unapproved agent published output: %v", err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	if err := record.check("presence-and-discovery", func() error {
		if _, err := planner.Heartbeat(ctx, bus.HeartbeatInput{Lifecycle: bus.LifecycleReady, Ready: true, LeaseMS: 30000}); err != nil {
			return err
		}
		if _, err := reviewer.Heartbeat(ctx, bus.HeartbeatInput{Lifecycle: bus.LifecycleReady, Ready: true, LeaseMS: 30000}); err != nil {
			return err
		}
		plannerPeers, err := planner.ListPeers(ctx)
		if err != nil || len(plannerPeers) != 1 || plannerPeers[0].ID != "reviewer" || !plannerPeers[0].Ready {
			return fmt.Errorf("unexpected planner peers: %#v, %v", plannerPeers, err)
		}
		reviewerPeers, err := reviewer.ListPeers(ctx)
		if err != nil || len(reviewerPeers) != 1 || reviewerPeers[0].ID != "planner" {
			return fmt.Errorf("unexpected reviewer peers: %#v, %v", reviewerPeers, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	var request bus.DeliveryReceipt
	if err := record.check("durable-request-and-idempotency", func() error {
		input := bus.SendMessageInput{
			To: "reviewer", Mode: bus.MessageRequest, Body: "Review the retry path",
			IdempotencyKey: "conformance-request", Context: []bus.ContextItem{{Kind: "text", Title: "Scope", Text: "Retry logic only"}},
		}
		var err error
		request, err = planner.SendMessage(ctx, input)
		if err != nil {
			return err
		}
		retry, err := planner.SendMessage(ctx, input)
		if err != nil || retry.MessageID != request.MessageID {
			return fmt.Errorf("retry did not return original receipt: %#v, %v", retry, err)
		}
		input.Body = "Different content"
		_, err = planner.SendMessage(ctx, input)
		return requireCode(err, bus.CodeConflict)
	}); err != nil {
		return result, err
	}

	if err := record.check("reservation-delivery-and-acknowledgement", func() error {
		reservation, err := reviewer.ReserveInbox(ctx, 10, 0)
		if err != nil || reservation == nil || len(reservation.Messages) != 1 || reservation.Messages[0].ID != request.MessageID {
			return fmt.Errorf("unexpected reservation: %#v, %v", reservation, err)
		}
		messages, err := reviewer.CommitInbox(ctx, reservation.ID)
		if err != nil || len(messages) != 1 || messages[0].State != bus.DeliveryDelivered {
			return fmt.Errorf("unexpected committed messages: %#v, %v", messages, err)
		}
		count, err := reviewer.AcknowledgeMessages(ctx, []string{request.MessageID})
		if err != nil || count != 1 {
			return fmt.Errorf("unexpected acknowledgement: %d, %v", count, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	if err := record.check("correlated-response", func() error {
		response, err := reviewer.SendMessage(ctx, bus.SendMessageInput{
			To: "planner", Mode: bus.MessageResponse, ResponseTo: request.MessageID,
			Body: "The retry path is correct", IdempotencyKey: "conformance-response",
		})
		if err != nil {
			return err
		}
		messages, err := planner.PullInbox(ctx, 10, 0)
		if err != nil || len(messages) != 1 || messages[0].ID != response.MessageID {
			return fmt.Errorf("unexpected response inbox: %#v, %v", messages, err)
		}
		if _, err := planner.AcknowledgeMessages(ctx, []string{response.MessageID}); err != nil {
			return err
		}
		receipt, err := planner.Receipt(ctx, request.MessageID)
		if err != nil || receipt.ResponseMessageID != response.MessageID || receipt.RepliedAt == "" {
			return fmt.Errorf("request did not link response: %#v, %v", receipt, err)
		}
		_, err = reviewer.SendMessage(ctx, bus.SendMessageInput{
			To: "planner", Mode: bus.MessageResponse, ResponseTo: request.MessageID, Body: "Second response",
		})
		return requireCode(err, bus.CodeConflict)
	}); err != nil {
		return result, err
	}

	if err := record.check("message-expiry", func() error {
		receipt, err := planner.SendMessage(ctx, bus.SendMessageInput{To: "reviewer", Body: "Expire", ExpiresInMS: 30})
		if err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
		messages, err := reviewer.PullInbox(ctx, 10, 0)
		if err != nil || len(messages) != 0 {
			return fmt.Errorf("expired message was delivered: %#v, %v", messages, err)
		}
		state, err := planner.Receipt(ctx, receipt.MessageID)
		if err != nil || state.State != bus.DeliveryExpired {
			return fmt.Errorf("unexpected expiry receipt: %#v, %v", state, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	if err := record.check("bounded-inbox-wait", func() error {
		type waitResult struct {
			messages []bus.Message
			err      error
		}
		waiting := make(chan waitResult, 1)
		go func() {
			messages, err := reviewer.PullInbox(ctx, 10, 2*time.Second)
			waiting <- waitResult{messages: messages, err: err}
		}()
		time.Sleep(50 * time.Millisecond)
		receipt, err := planner.SendMessage(ctx, bus.SendMessageInput{To: "reviewer", Body: "Wake waiting inbox"})
		if err != nil {
			return err
		}
		select {
		case waited := <-waiting:
			if waited.err != nil || len(waited.messages) != 1 || waited.messages[0].ID != receipt.MessageID {
				return fmt.Errorf("unexpected waited inbox: %#v, %v", waited.messages, waited.err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		count, err := reviewer.AcknowledgeMessages(ctx, []string{receipt.MessageID})
		if err != nil || count != 1 {
			return fmt.Errorf("unexpected waited acknowledgement: %d, %v", count, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	var firstTask, blockedTask bus.Task
	if err := record.check("task-dependencies-release-and-completion", func() error {
		var err error
		firstTask, err = planner.AddTask(ctx, bus.AddTaskInput{Title: "Review"})
		if err != nil {
			return err
		}
		blockedTask, err = planner.AddTask(ctx, bus.AddTaskInput{Title: "Apply", Dependencies: []string{firstTask.ID}})
		if err != nil {
			return err
		}
		_, err = reviewer.ClaimTask(ctx, blockedTask.ID)
		if err := requireCode(err, bus.CodeConflict); err != nil {
			return err
		}
		if _, err := reviewer.ClaimTask(ctx, firstTask.ID); err != nil {
			return err
		}
		if _, err := reviewer.ReleaseTask(ctx, firstTask.ID); err != nil {
			return err
		}
		if _, err := reviewer.ClaimTask(ctx, firstTask.ID); err != nil {
			return err
		}
		if _, err := reviewer.CompleteTask(ctx, firstTask.ID, "Reviewed"); err != nil {
			return err
		}
		if _, err := planner.ClaimTask(ctx, blockedTask.ID); err != nil {
			return err
		}
		completed, err := planner.CompleteTask(ctx, blockedTask.ID, "Applied")
		if err != nil || completed.Status != "done" {
			return fmt.Errorf("unexpected completed task: %#v, %v", completed, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	if err := record.check("durable-task-progress", func() error {
		task, err := planner.AddTask(ctx, bus.AddTaskInput{Title: "Report progress"})
		if err != nil {
			return err
		}
		if _, err := reviewer.ClaimTask(ctx, task.ID); err != nil {
			return err
		}
		entry, err := reviewer.AddTaskProgress(ctx, task.ID, bus.AddTaskProgressInput{Kind: "progress", Text: "Review started"})
		if err != nil || entry.Sequence != 1 || entry.AgentID != "reviewer" {
			return fmt.Errorf("unexpected task progress: %#v, %v", entry, err)
		}
		if _, err := reviewer.CompleteTask(ctx, task.ID, "Reviewed"); err != nil {
			return err
		}
		history, err := owner.ListTaskProgress(ctx, task.ID)
		if err != nil || len(history) != 1 || history[0].Text != "Review started" {
			return fmt.Errorf("unexpected retained task progress: %#v, %v", history, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	if err := record.check("execution-replacement-and-stale-claim-recovery", func() error {
		task, err := planner.AddTask(ctx, bus.AddTaskInput{Title: "Recover claim"})
		if err != nil {
			return err
		}
		if _, err := reviewer.ClaimTask(ctx, task.ID); err != nil {
			return err
		}
		replacement, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{ID: "reviewer", DisplayName: "Reviewer", LeaseMS: 30000})
		if err != nil {
			return err
		}
		_, err = reviewer.ListTasks(ctx, false)
		if err := requireCode(err, bus.CodeUnauthenticated); err != nil {
			return err
		}
		_, err = reviewer.AddTaskProgress(ctx, task.ID, bus.AddTaskProgressInput{Kind: "note", Text: "stale execution"})
		if err := requireCode(err, bus.CodeUnauthenticated); err != nil {
			return err
		}
		reviewer = bus.Client{Address: options.Address, Token: replacement.AgentToken}
		if _, err := reviewer.Heartbeat(ctx, bus.HeartbeatInput{Lifecycle: bus.LifecycleReady, Ready: true, LeaseMS: 30000}); err != nil {
			return err
		}
		if _, err := reviewer.ClaimTask(ctx, task.ID); err != nil {
			return err
		}
		_, err = reviewer.CompleteTask(ctx, task.ID, "Recovered")
		return err
	}); err != nil {
		return result, err
	}

	if err := record.check("human-escalation-boundary", func() error {
		escalation, err := planner.AskHuman(ctx, bus.AskHumanInput{Question: "Proceed?", Options: []string{"yes", "no"}})
		if err != nil {
			return err
		}
		_, err = planner.ResolveEscalation(ctx, escalation.ID, "yes")
		if err := requireCode(err, bus.CodePermissionDenied); err != nil {
			return err
		}
		escalations, err := owner.ListEscalations(ctx)
		if err != nil || len(escalations) != 1 || escalations[0].ID != escalation.ID {
			return fmt.Errorf("unexpected owner escalations: %#v, %v", escalations, err)
		}
		resolved, err := owner.ResolveEscalation(ctx, escalation.ID, "yes")
		if err != nil || resolved.Status != "resolved" || resolved.Answer != "yes" {
			return fmt.Errorf("unexpected resolution: %#v, %v", resolved, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	if err := record.check("resumable-scope-events", func() error {
		initial, err := owner.Events(ctx, 0, 100, 0)
		if err != nil || initial.ResyncRequired || len(initial.Events) == 0 || initial.NextRevision != initial.CurrentRevision {
			return fmt.Errorf("unexpected initial event batch: %#v, %v", initial, err)
		}
		for _, event := range initial.Events {
			for _, value := range event.Attributes {
				if value == "The retry path is correct" || value == "Proceed?" || value == "Reviewed" {
					return fmt.Errorf("event exposed record content: %#v", event)
				}
			}
		}
		type eventResult struct {
			batch bus.EventBatch
			err   error
		}
		waiting := make(chan eventResult, 1)
		go func() {
			batch, err := owner.Events(ctx, initial.NextRevision, 100, 2*time.Second)
			waiting <- eventResult{batch: batch, err: err}
		}()
		time.Sleep(50 * time.Millisecond)
		if _, err := planner.Heartbeat(ctx, bus.HeartbeatInput{Lifecycle: bus.LifecycleWorking, Ready: true, LeaseMS: 30000}); err != nil {
			return err
		}
		select {
		case value := <-waiting:
			if value.err != nil || len(value.batch.Events) != 1 || value.batch.Events[0].Type != "agent.lifecycle_changed" {
				return fmt.Errorf("unexpected waited event batch: %#v, %v", value.batch, value.err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err = planner.Events(ctx, 0, 10, 0)
		return requireCode(err, bus.CodePermissionDenied)
	}); err != nil {
		return result, err
	}

	if err := record.check("storage-diagnostics-and-retention", func() error {
		summary, err := owner.StorageSummary(ctx)
		if err != nil || len(summary.Records) == 0 || summary.TotalEstimatedBytes == 0 {
			return fmt.Errorf("unexpected storage summary: %#v, %v", summary, err)
		}
		_, err = planner.StorageSummary(ctx)
		if err := requireCode(err, bus.CodePermissionDenied); err != nil {
			return err
		}
		before := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
		dryRun, err := owner.PruneScope(ctx, bus.PruneScopeInput{Before: before})
		if err != nil || !dryRun.DryRun {
			return fmt.Errorf("unexpected retention dry run: %#v, %v", dryRun, err)
		}
		if dryRun.Records.Messages+dryRun.Records.Tasks+dryRun.Records.Escalations == 0 {
			return errors.New("retention dry run found no terminal records")
		}
		executed, err := owner.PruneScope(ctx, bus.PruneScopeInput{Before: before, Execute: true})
		if err != nil || executed.DryRun || executed.Records != dryRun.Records {
			return fmt.Errorf("unexpected retention execution: %#v, %v", executed, err)
		}
		stale, err := owner.Events(ctx, 0, 100, 0)
		if err != nil || !stale.ResyncRequired {
			return fmt.Errorf("pruned event cursor did not require resync: %#v, %v", stale, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	if err := record.check("scope-isolation", func() error {
		otherScope, err := admin.CreateScope(ctx, bus.CreateScopeInput{ID: scopeID + "-other"})
		if err != nil {
			return err
		}
		intruderRegistration, err := (bus.Client{Address: options.Address, Token: otherScope.ScopeToken}).RegisterAgent(ctx, bus.RegisterAgentInput{
			ID: "intruder", DisplayName: "Intruder", LeaseMS: 30000,
		})
		if err != nil {
			return err
		}
		intruder := bus.Client{Address: options.Address, Token: intruderRegistration.AgentToken}
		_, err = intruder.SendMessage(ctx, bus.SendMessageInput{To: "planner", Body: "Cross scope"})
		return requireCode(err, bus.CodePermissionDenied)
	}); err != nil {
		return result, err
	}

	if err := record.check("mcp-tool-surface", func() error {
		httpClient := &http.Client{
			Transport: bearerTransport{token: reviewer.Token, base: http.DefaultTransport},
			Timeout:   10 * time.Second,
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "agentintercom-conformance", Version: bus.Version}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: options.Address + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			return err
		}
		defer session.Close()
		tools, err := session.ListTools(ctx, nil)
		if err != nil {
			return err
		}
		if len(tools.Tools) != 23 {
			return fmt.Errorf("unexpected MCP tool count: %d", len(tools.Tools))
		}
		peers, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_peers", Arguments: map[string]any{}})
		if err != nil {
			return err
		}
		if err := requireStructuredArray(peers, "peers"); err != nil {
			return err
		}
		tasks, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
		if err != nil {
			return err
		}
		if err := requireStructuredArray(tasks, "tasks"); err != nil {
			return err
		}
		published, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "publish_output", Arguments: map[string]any{
			"streamId": outputStream.ID, "contentType": "text/plain", "value": "published through MCP",
		}})
		if err != nil || published.IsError {
			return fmt.Errorf("MCP publish_output failed: %#v, %v", published, err)
		}
		return nil
	}); err != nil {
		return result, err
	}

	if err := record.check("clean-offline-lifecycle", func() error {
		if _, err := planner.Heartbeat(ctx, bus.HeartbeatInput{Lifecycle: bus.LifecycleOffline, Ready: false, LeaseMS: 30000}); err != nil {
			return err
		}
		_, err := reviewer.Heartbeat(ctx, bus.HeartbeatInput{Lifecycle: bus.LifecycleOffline, Ready: false, LeaseMS: 30000})
		return err
	}); err != nil {
		return result, err
	}

	return result, nil
}
