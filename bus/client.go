package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	Address string
	Token   string
	HTTP    *http.Client
}

type responseEnvelope[T any] struct {
	OK     bool `json:"ok"`
	Result T    `json:"result"`
	Error  struct {
		Code    ErrorCode `json:"code"`
		Message string    `json:"message"`
	} `json:"error"`
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func request[T any](ctx context.Context, client Client, method, path string, input any) (T, error) {
	var zero T
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return zero, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, client.Address+path, body)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+client.Token)
	}
	response, err := client.httpClient().Do(req)
	if err != nil {
		return zero, err
	}
	defer response.Body.Close()
	var envelope responseEnvelope[T]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return zero, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.OK {
		if envelope.Error.Message != "" {
			return zero, Errorf(envelope.Error.Code, envelope.Error.Message)
		}
		return zero, fmt.Errorf("Agent Intercom request failed with HTTP %d", response.StatusCode)
	}
	return envelope.Result, nil
}

func (c Client) Health(ctx context.Context) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Address+"/health", nil)
	if err != nil {
		return Health{}, err
	}
	response, err := c.httpClient().Do(req)
	if err != nil {
		return Health{}, err
	}
	defer response.Body.Close()
	var health Health
	decodeErr := json.NewDecoder(response.Body).Decode(&health)
	if response.StatusCode != http.StatusOK {
		if decodeErr == nil {
			return health, fmt.Errorf("health check failed with HTTP %d", response.StatusCode)
		}
		return Health{}, fmt.Errorf("health check failed with HTTP %d", response.StatusCode)
	}
	return health, decodeErr
}

func (c Client) Liveness(ctx context.Context) (Liveness, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Address+"/health/live", nil)
	if err != nil {
		return Liveness{}, err
	}
	response, err := c.httpClient().Do(req)
	if err != nil {
		return Liveness{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Liveness{}, fmt.Errorf("liveness check failed with HTTP %d", response.StatusCode)
	}
	var liveness Liveness
	err = json.NewDecoder(response.Body).Decode(&liveness)
	return liveness, err
}

func (c Client) CreateScope(ctx context.Context, input CreateScopeInput) (CreateScopeResult, error) {
	return request[CreateScopeResult](ctx, c, http.MethodPost, "/v1/scopes", input)
}

func (c Client) Shutdown(ctx context.Context) error {
	_, err := request[map[string]bool](ctx, c, http.MethodPost, "/v1/admin/shutdown", map[string]any{})
	return err
}

func (c Client) ExportScope(ctx context.Context, scopeID string) (ScopeArchive, error) {
	return request[ScopeArchive](ctx, c, http.MethodGet, "/v1/admin/scopes/"+url.PathEscape(scopeID)+"/export", nil)
}

func (c Client) ImportScope(ctx context.Context, archive ScopeArchive) (ImportScopeResult, error) {
	return request[ImportScopeResult](ctx, c, http.MethodPost, "/v1/admin/scopes/import", archive)
}

func (c Client) RegisterAgent(ctx context.Context, input RegisterAgentInput) (RegisterAgentResult, error) {
	return request[RegisterAgentResult](ctx, c, http.MethodPost, "/v1/agents", input)
}

func (c Client) ListAgents(ctx context.Context) ([]Agent, error) {
	return request[[]Agent](ctx, c, http.MethodGet, "/v1/agents", nil)
}

func (c Client) LinkAgents(ctx context.Context, left, right string) error {
	_, err := request[map[string]bool](ctx, c, http.MethodPost, "/v1/links", map[string]string{"left": left, "right": right})
	return err
}

func (c Client) Heartbeat(ctx context.Context, input HeartbeatInput) (Agent, error) {
	return request[Agent](ctx, c, http.MethodPatch, "/v1/me/heartbeat", input)
}

func (c Client) NodeStatus(ctx context.Context) (NodeStatus, error) {
	return request[NodeStatus](ctx, c, http.MethodGet, "/v1/me", nil)
}

// Credential classifies the client's bearer credential without authorizing
// anything. purpose is empty or "retire".
func (c Client) Credential(ctx context.Context, purpose string) (Credential, error) {
	path := "/v1/credential"
	if purpose != "" {
		path += "?purpose=" + url.QueryEscape(purpose)
	}
	return request[Credential](ctx, c, http.MethodGet, path, nil)
}

func (c Client) ListPeers(ctx context.Context) ([]Agent, error) {
	return request[[]Agent](ctx, c, http.MethodGet, "/v1/peers", nil)
}

func (c Client) SendMessage(ctx context.Context, input SendMessageInput) (DeliveryReceipt, error) {
	return request[DeliveryReceipt](ctx, c, http.MethodPost, "/v1/messages", input)
}

func (c Client) Receipt(ctx context.Context, messageID string) (DeliveryReceipt, error) {
	return request[DeliveryReceipt](ctx, c, http.MethodGet, "/v1/messages/"+url.PathEscape(messageID), nil)
}

func (c Client) ReserveInbox(ctx context.Context, limit int, wait time.Duration) (*InboxReservation, error) {
	waitMS := wait.Milliseconds()
	if wait > 0 && waitMS == 0 {
		waitMS = 1
	}
	return request[*InboxReservation](ctx, c, http.MethodPost, "/v1/inbox/reserve", map[string]any{"limit": limit, "waitMs": waitMS})
}

func (c Client) CommitInbox(ctx context.Context, reservationID string) ([]Message, error) {
	return request[[]Message](ctx, c, http.MethodPost, "/v1/inbox/"+url.PathEscape(reservationID)+"/commit", map[string]any{})
}

func (c Client) ReleaseInbox(ctx context.Context, reservationID string) error {
	_, err := request[map[string]bool](ctx, c, http.MethodPost, "/v1/inbox/"+url.PathEscape(reservationID)+"/release", map[string]any{})
	return err
}

func (c Client) PullInbox(ctx context.Context, limit int, wait time.Duration) ([]Message, error) {
	reservation, err := c.ReserveInbox(ctx, limit, wait)
	if err != nil || reservation == nil {
		return nil, err
	}
	return c.CommitInbox(ctx, reservation.ID)
}

func (c Client) AcknowledgeMessages(ctx context.Context, messageIDs []string) (int64, error) {
	result, err := request[map[string]int64](ctx, c, http.MethodPost, "/v1/messages/ack", map[string]any{"messageIds": messageIDs})
	return result["acknowledged"], err
}

func (c Client) AddTask(ctx context.Context, input AddTaskInput) (Task, error) {
	return request[Task](ctx, c, http.MethodPost, "/v1/tasks", input)
}

func (c Client) ListTasks(ctx context.Context, readyOnly bool) ([]Task, error) {
	path := "/v1/tasks"
	if readyOnly {
		path += "?ready=true"
	}
	return request[[]Task](ctx, c, http.MethodGet, path, nil)
}

func (c Client) StorageSummary(ctx context.Context) (StorageSummary, error) {
	return request[StorageSummary](ctx, c, http.MethodGet, "/v1/scope/storage", nil)
}

func (c Client) PruneScope(ctx context.Context, input PruneScopeInput) (PruneScopeResult, error) {
	return request[PruneScopeResult](ctx, c, http.MethodPost, "/v1/scope/storage/prune", input)
}

func (c Client) Events(ctx context.Context, after int64, limit int, wait time.Duration) (EventBatch, error) {
	if limit == 0 {
		limit = defaultEventLimit
	}
	waitMS := wait.Milliseconds()
	if wait > 0 && waitMS == 0 {
		waitMS = 1
	}
	query := url.Values{}
	query.Set("after", fmt.Sprintf("%d", after))
	query.Set("limit", fmt.Sprintf("%d", limit))
	query.Set("waitMs", fmt.Sprintf("%d", waitMS))
	return request[EventBatch](ctx, c, http.MethodGet, "/v1/events?"+query.Encode(), nil)
}

func (c Client) CreateAgentCardPublication(ctx context.Context, input PublishAgentCardInput) (AgentCardPublication, error) {
	return request[AgentCardPublication](ctx, c, http.MethodPost, "/v1/a2a/publications", input)
}

func (c Client) ListAgentCardPublications(ctx context.Context) ([]AgentCardPublication, error) {
	return request[[]AgentCardPublication](ctx, c, http.MethodGet, "/v1/a2a/publications", nil)
}

func (c Client) SetAgentCardPublicationEnabled(ctx context.Context, publicationID string, enabled bool) (AgentCardPublication, error) {
	action := "disable"
	if enabled {
		action = "enable"
	}
	return request[AgentCardPublication](ctx, c, http.MethodPost, "/v1/a2a/publications/"+url.PathEscape(publicationID)+"/"+action, map[string]any{})
}

func (c Client) CreateA2APrincipal(ctx context.Context, input CreateA2APrincipalInput) (IssuedA2APrincipal, error) {
	return request[IssuedA2APrincipal](ctx, c, http.MethodPost, "/v1/a2a/principals", input)
}

func (c Client) ListA2APrincipals(ctx context.Context) ([]A2APrincipal, error) {
	return request[[]A2APrincipal](ctx, c, http.MethodGet, "/v1/a2a/principals", nil)
}

func (c Client) ListA2APrincipalUsage(ctx context.Context) ([]A2APrincipalUsage, error) {
	return request[[]A2APrincipalUsage](ctx, c, http.MethodGet, "/v1/a2a/principals/usage", nil)
}

func (c Client) RotateA2APrincipal(ctx context.Context, principalID string) (IssuedA2APrincipal, error) {
	return request[IssuedA2APrincipal](ctx, c, http.MethodPost, "/v1/a2a/principals/"+url.PathEscape(principalID)+"/rotate", map[string]any{})
}

func (c Client) SetA2APrincipalEnabled(ctx context.Context, principalID string, enabled bool) (A2APrincipal, error) {
	action := "disable"
	if enabled {
		action = "enable"
	}
	return request[A2APrincipal](ctx, c, http.MethodPost, "/v1/a2a/principals/"+url.PathEscape(principalID)+"/"+action, map[string]any{})
}

func (c Client) CreateOutputStream(ctx context.Context, input CreateOutputStreamInput) (OutputStream, error) {
	return request[OutputStream](ctx, c, http.MethodPost, "/v1/output-streams", input)
}

func (c Client) ListOutputStreams(ctx context.Context) ([]OutputStream, error) {
	return request[[]OutputStream](ctx, c, http.MethodGet, "/v1/output-streams", nil)
}

func (c Client) OutputStream(ctx context.Context, streamID string) (OutputStream, error) {
	return request[OutputStream](ctx, c, http.MethodGet, "/v1/output-streams/"+url.PathEscape(streamID), nil)
}

func (c Client) RemoveOutputStream(ctx context.Context, streamID string) error {
	_, err := request[map[string]bool](ctx, c, http.MethodDelete, "/v1/output-streams/"+url.PathEscape(streamID), nil)
	return err
}

func (c Client) SetOutputPublisher(ctx context.Context, streamID, agentID string, allowed bool) (OutputStream, error) {
	method := http.MethodDelete
	if allowed {
		method = http.MethodPut
	}
	return request[OutputStream](ctx, c, method, "/v1/output-streams/"+url.PathEscape(streamID)+"/publishers/"+url.PathEscape(agentID), nil)
}

func (c Client) CreateOutputPrincipal(ctx context.Context, input CreateOutputPrincipalInput) (IssuedOutputPrincipal, error) {
	return request[IssuedOutputPrincipal](ctx, c, http.MethodPost, "/v1/output-principals", input)
}

func (c Client) ListOutputPrincipals(ctx context.Context) ([]OutputPrincipal, error) {
	return request[[]OutputPrincipal](ctx, c, http.MethodGet, "/v1/output-principals", nil)
}

func (c Client) RotateOutputPrincipal(ctx context.Context, principalID string) (IssuedOutputPrincipal, error) {
	return request[IssuedOutputPrincipal](ctx, c, http.MethodPost, "/v1/output-principals/"+url.PathEscape(principalID)+"/rotate", map[string]any{})
}

func (c Client) SetOutputPrincipalEnabled(ctx context.Context, principalID string, enabled bool) (OutputPrincipal, error) {
	action := "disable"
	if enabled {
		action = "enable"
	}
	return request[OutputPrincipal](ctx, c, http.MethodPost, "/v1/output-principals/"+url.PathEscape(principalID)+"/"+action, map[string]any{})
}

func (c Client) PublishOutput(ctx context.Context, streamID string, input PublishOutputInput) (OutputValue, error) {
	return request[OutputValue](ctx, c, http.MethodPost, "/outputs/"+url.PathEscape(streamID)+"/values", input)
}

func (c Client) LatestOutput(ctx context.Context, streamID string) (*OutputValue, error) {
	return request[*OutputValue](ctx, c, http.MethodGet, "/outputs/"+url.PathEscape(streamID)+"/latest", nil)
}

func (c Client) OutputHistory(ctx context.Context, streamID string, after int64, limit int) (OutputHistory, error) {
	query := url.Values{}
	query.Set("after", fmt.Sprintf("%d", after))
	query.Set("limit", fmt.Sprintf("%d", limit))
	return request[OutputHistory](ctx, c, http.MethodGet, "/outputs/"+url.PathEscape(streamID)+"/values?"+query.Encode(), nil)
}

func (c Client) WatchEvents(ctx context.Context, after int64, limit int) iter.Seq2[EventBatch, error] {
	return func(yield func(EventBatch, error) bool) {
		for ctx.Err() == nil {
			batch, err := c.Events(ctx, after, limit, 25*time.Second)
			if err != nil {
				yield(EventBatch{}, err)
				return
			}
			if batch.ResyncRequired {
				yield(batch, nil)
				return
			}
			after = batch.NextRevision
			if len(batch.Events) == 0 {
				continue
			}
			if !yield(batch, nil) {
				return
			}
		}
	}
}

func (c Client) ClaimTask(ctx context.Context, taskID string) (Task, error) {
	return request[Task](ctx, c, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/claim", map[string]any{})
}

func (c Client) ReleaseTask(ctx context.Context, taskID string) (Task, error) {
	return request[Task](ctx, c, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/release", map[string]any{})
}

func (c Client) CompleteTask(ctx context.Context, taskID, note string) (Task, error) {
	return request[Task](ctx, c, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/complete", map[string]string{"note": note})
}

func (c Client) AddTaskProgress(ctx context.Context, taskID string, input AddTaskProgressInput) (TaskProgress, error) {
	return request[TaskProgress](ctx, c, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/progress", input)
}

func (c Client) ListTaskProgress(ctx context.Context, taskID string) ([]TaskProgress, error) {
	return request[[]TaskProgress](ctx, c, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/progress", nil)
}

func (c Client) AskHuman(ctx context.Context, input AskHumanInput) (HumanEscalation, error) {
	return request[HumanEscalation](ctx, c, http.MethodPost, "/v1/escalations", input)
}

func (c Client) Escalation(ctx context.Context, id string) (HumanEscalation, error) {
	return request[HumanEscalation](ctx, c, http.MethodGet, "/v1/escalations/"+url.PathEscape(id), nil)
}

func (c Client) ListEscalations(ctx context.Context) ([]HumanEscalation, error) {
	return request[[]HumanEscalation](ctx, c, http.MethodGet, "/v1/scope/escalations", nil)
}

func (c Client) ResolveEscalation(ctx context.Context, id, answer string) (HumanEscalation, error) {
	return request[HumanEscalation](ctx, c, http.MethodPost, "/v1/scope/escalations/"+url.PathEscape(id)+"/resolve", map[string]string{"answer": answer})
}
