package bus

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ScopeArchiveFormat  = "october-bus.scope"
	ScopeArchiveVersion = 3
)

type ScopeArchive struct {
	Channels              []ArchivedChannel             `json:"channels,omitempty"`
	ChannelSubscriptions  []ArchivedChannelSubscription `json:"channelSubscriptions,omitempty"`
	ChannelPosts          []ArchivedChannelPost         `json:"channelPosts,omitempty"`
	Format                string                        `json:"format"`
	Version               int                           `json:"version"`
	ExportedAt            string                        `json:"exportedAt"`
	Scope                 ArchivedScope                 `json:"scope"`
	Agents                []ArchivedAgent               `json:"agents"`
	Links                 []ArchivedPeerLink            `json:"links"`
	Messages              []ArchivedMessage             `json:"messages"`
	Tasks                 []ArchivedTask                `json:"tasks"`
	TaskProgress          []ArchivedTaskProgress        `json:"taskProgress"`
	Escalations           []ArchivedEscalation          `json:"escalations"`
	AgentCardPublications []ArchivedAgentCard           `json:"agentCardPublications"`
	A2ATasks              []ArchivedA2ATask             `json:"a2aTasks"`
	A2AMessages           []ArchivedA2AMessage          `json:"a2aMessages"`
	OutputStreams         []ArchivedOutputStream        `json:"outputStreams"`
	OutputValues          []ArchivedOutputValue         `json:"outputValues"`
}

type ArchivedScope struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
}

type ArchivedAgent struct {
	ID           string            `json:"id"`
	DisplayName  string            `json:"displayName"`
	Capabilities []AgentCapability `json:"capabilities"`
	RegisteredAt string            `json:"registeredAt"`
	UpdatedAt    string            `json:"updatedAt"`
}

type ArchivedPeerLink struct {
	Left      string `json:"left"`
	Right     string `json:"right"`
	CreatedAt string `json:"createdAt"`
}

type ArchivedMessage struct {
	ID                string                 `json:"id"`
	From              string                 `json:"from"`
	FromKind          MessageParticipantKind `json:"fromKind"`
	To                string                 `json:"to"`
	ToKind            MessageParticipantKind `json:"toKind"`
	Mode              MessageMode            `json:"mode"`
	Body              string                 `json:"body"`
	Context           []ContextItem          `json:"context"`
	ResponseTo        string                 `json:"responseTo,omitempty"`
	IdempotencyKey    string                 `json:"idempotencyKey,omitempty"`
	State             DeliveryState          `json:"state"`
	CreatedAt         string                 `json:"createdAt"`
	ExpiresAt         string                 `json:"expiresAt,omitempty"`
	DeliveredAt       string                 `json:"deliveredAt,omitempty"`
	AcknowledgedAt    string                 `json:"acknowledgedAt,omitempty"`
	RepliedAt         string                 `json:"repliedAt,omitempty"`
	ResponseMessageID string                 `json:"responseMessageId,omitempty"`
}

type ArchivedA2ATask struct {
	ID            string       `json:"id"`
	ContextID     string       `json:"contextId"`
	PrincipalID   string       `json:"principalId"`
	PublicationID string       `json:"publicationId"`
	TargetAgentID string       `json:"targetAgentId"`
	State         A2ATaskState `json:"state"`
	CreatedAt     string       `json:"createdAt"`
	UpdatedAt     string       `json:"updatedAt"`
}

type ArchivedA2AMessage struct {
	PrincipalID          string `json:"principalId"`
	ClientMessageID      string `json:"clientMessageId"`
	TaskID               string `json:"taskId"`
	BusRequestMessageID  string `json:"busRequestMessageId"`
	BusResponseMessageID string `json:"busResponseMessageId,omitempty"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

type ArchivedTask struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	CreatedBy    *string  `json:"createdBy"`
	ClaimedBy    string   `json:"claimedBy,omitempty"`
	Status       string   `json:"status"`
	Dependencies []string `json:"dependencies"`
	Note         string   `json:"note,omitempty"`
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
}

type ArchivedTaskProgress struct {
	TaskID    string `json:"taskId"`
	Sequence  int64  `json:"sequence"`
	AgentID   string `json:"agentId"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
}

type ArchivedEscalation struct {
	ID         string   `json:"id"`
	AgentID    string   `json:"agentId"`
	Question   string   `json:"question"`
	Options    []string `json:"options"`
	Status     string   `json:"status"`
	Answer     string   `json:"answer,omitempty"`
	CreatedAt  string   `json:"createdAt"`
	ResolvedAt string   `json:"resolvedAt,omitempty"`
}

type ArchivedAgentCard struct {
	ID        string `json:"id"`
	AgentID   string `json:"agentId"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type ArchivedOutputStream struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	RetentionLimit    int      `json:"retentionLimit"`
	CurrentSequence   int64    `json:"currentSequence"`
	MinimumCursor     int64    `json:"minimumCursor"`
	PublisherAgentIDs []string `json:"publisherAgentIds"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
}

type ArchivedOutputValue struct {
	StreamID     string            `json:"streamId"`
	Sequence     int64             `json:"sequence"`
	ProducerType string            `json:"producerType"`
	ProducerID   string            `json:"producerId"`
	ContentType  OutputContentType `json:"contentType"`
	Value        any               `json:"value"`
	Reference    *OutputReference  `json:"reference,omitempty"`
	CreatedAt    string            `json:"createdAt"`
}

type ImportScopeResult struct {
	ScopeID    string `json:"scopeId"`
	ScopeToken string `json:"scopeToken,omitempty"`
	Imported   bool   `json:"imported"`
}

func archiveTime(value string, required bool) (int64, error) {
	if value == "" && !required {
		return 0, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, Errorf(CodeInvalidArgument, "Archive contains an invalid timestamp")
	}
	return parsed.UnixMilli(), nil
}

func queryArchivedAgents(ctx context.Context, tx sqlExecutor, scopeID string) ([]ArchivedAgent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT agent_id,display_name,capabilities_json,registered_at,updated_at FROM agents WHERE scope_id=? ORDER BY agent_id`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ArchivedAgent{}
	for rows.Next() {
		var value ArchivedAgent
		var capabilities string
		var registeredAt, updatedAt int64
		if err := rows.Scan(&value.ID, &value.DisplayName, &capabilities, &registeredAt, &updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(capabilities), &value.Capabilities); err != nil {
			return nil, err
		}
		if value.Capabilities == nil {
			value.Capabilities = []AgentCapability{}
		}
		value.RegisteredAt, value.UpdatedAt = instant(registeredAt), instant(updatedAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func queryArchivedLinks(ctx context.Context, tx sqlExecutor, scopeID string) ([]ArchivedPeerLink, error) {
	rows, err := tx.QueryContext(ctx, `SELECT left_agent,right_agent,created_at FROM peer_links WHERE scope_id=? ORDER BY left_agent,right_agent`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ArchivedPeerLink{}
	for rows.Next() {
		var value ArchivedPeerLink
		var createdAt int64
		if err := rows.Scan(&value.Left, &value.Right, &createdAt); err != nil {
			return nil, err
		}
		value.CreatedAt = instant(createdAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func archivedA2APrincipalID(scopeID, principalID string) string {
	if strings.HasPrefix(principalID, "a2ap_") {
		return principalID
	}
	digest := sha256.Sum256([]byte(scopeID + "\x00" + principalID))
	return "a2ap_" + hex.EncodeToString(digest[:16])
}

func queryArchivedMessages(ctx context.Context, tx sqlExecutor, scopeID string) ([]ArchivedMessage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT message_id,from_kind,from_id,to_kind,to_id,mode,body,context_json,response_to,idempotency_key,state,created_at,expires_at,delivered_at,acknowledged_at,replied_at,response_message_id FROM messages WHERE scope_id=? ORDER BY created_at,message_id`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ArchivedMessage{}
	for rows.Next() {
		var value ArchivedMessage
		var contextJSON string
		var responseTo, idempotencyKey, responseMessageID sql.NullString
		var createdAt int64
		var expiresAt, deliveredAt, acknowledgedAt, repliedAt sql.NullInt64
		if err := rows.Scan(&value.ID, &value.FromKind, &value.From, &value.ToKind, &value.To, &value.Mode, &value.Body, &contextJSON, &responseTo, &idempotencyKey, &value.State, &createdAt, &expiresAt, &deliveredAt, &acknowledgedAt, &repliedAt, &responseMessageID); err != nil {
			return nil, err
		}
		if value.FromKind == MessageParticipantA2APrincipal {
			value.From = archivedA2APrincipalID(scopeID, value.From)
		}
		if value.ToKind == MessageParticipantA2APrincipal {
			value.To = archivedA2APrincipalID(scopeID, value.To)
		}
		if err := json.Unmarshal([]byte(contextJSON), &value.Context); err != nil {
			return nil, err
		}
		if value.Context == nil {
			value.Context = []ContextItem{}
		}
		value.ResponseTo, value.IdempotencyKey, value.ResponseMessageID = responseTo.String, idempotencyKey.String, responseMessageID.String
		value.CreatedAt, value.ExpiresAt = instant(createdAt), instant(expiresAt.Int64)
		value.DeliveredAt, value.AcknowledgedAt = instant(deliveredAt.Int64), instant(acknowledgedAt.Int64)
		value.RepliedAt = instant(repliedAt.Int64)
		if value.State == DeliveryReserved {
			value.State = DeliveryQueued
			if deliveredAt.Valid {
				value.State = DeliveryDelivered
			}
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func queryArchivedA2A(ctx context.Context, tx sqlExecutor, scopeID string) ([]ArchivedA2ATask, []ArchivedA2AMessage, error) {
	taskRows, err := tx.QueryContext(ctx, `SELECT task_id,context_id,principal_id,publication_id,target_agent_id,state,created_at,updated_at FROM a2a_tasks WHERE scope_id=? ORDER BY created_at,task_id`, scopeID)
	if err != nil {
		return nil, nil, err
	}
	tasks := []ArchivedA2ATask{}
	for taskRows.Next() {
		var task ArchivedA2ATask
		var createdAt, updatedAt int64
		if err := taskRows.Scan(&task.ID, &task.ContextID, &task.PrincipalID, &task.PublicationID, &task.TargetAgentID, &task.State, &createdAt, &updatedAt); err != nil {
			taskRows.Close()
			return nil, nil, err
		}
		task.PrincipalID = archivedA2APrincipalID(scopeID, task.PrincipalID)
		task.CreatedAt, task.UpdatedAt = instant(createdAt), instant(updatedAt)
		tasks = append(tasks, task)
	}
	if err := taskRows.Err(); err != nil {
		taskRows.Close()
		return nil, nil, err
	}
	taskRows.Close()
	messageRows, err := tx.QueryContext(ctx, `SELECT correlations.principal_id,correlations.client_message_id,correlations.task_id,correlations.bus_request_message_id,correlations.bus_response_message_id,correlations.created_at,correlations.updated_at FROM a2a_message_correlations AS correlations JOIN a2a_tasks AS tasks ON tasks.task_id=correlations.task_id WHERE tasks.scope_id=? ORDER BY correlations.created_at,correlations.client_message_id`, scopeID)
	if err != nil {
		return nil, nil, err
	}
	defer messageRows.Close()
	messages := []ArchivedA2AMessage{}
	for messageRows.Next() {
		var message ArchivedA2AMessage
		var responseID sql.NullString
		var createdAt, updatedAt int64
		if err := messageRows.Scan(&message.PrincipalID, &message.ClientMessageID, &message.TaskID, &message.BusRequestMessageID, &responseID, &createdAt, &updatedAt); err != nil {
			return nil, nil, err
		}
		message.PrincipalID = archivedA2APrincipalID(scopeID, message.PrincipalID)
		message.BusResponseMessageID = responseID.String
		message.CreatedAt, message.UpdatedAt = instant(createdAt), instant(updatedAt)
		messages = append(messages, message)
	}
	return tasks, messages, messageRows.Err()
}

func queryArchivedTasks(ctx context.Context, tx sqlExecutor, scopeID string) ([]ArchivedTask, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id,title,description,created_by,claimed_by,status,dependencies_json,note,created_at,updated_at FROM tasks WHERE scope_id=? ORDER BY created_at,task_id`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ArchivedTask{}
	for rows.Next() {
		var value ArchivedTask
		var createdBy, claimedBy, note sql.NullString
		var dependencies string
		var createdAt, updatedAt int64
		if err := rows.Scan(&value.ID, &value.Title, &value.Description, &createdBy, &claimedBy, &value.Status, &dependencies, &note, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dependencies), &value.Dependencies); err != nil {
			return nil, err
		}
		if value.Dependencies == nil {
			value.Dependencies = []string{}
		}
		if createdBy.Valid {
			value.CreatedBy = &createdBy.String
		}
		value.ClaimedBy, value.Note = claimedBy.String, note.String
		if value.Status == "claimed" {
			value.Status, value.ClaimedBy = "open", ""
		}
		value.CreatedAt, value.UpdatedAt = instant(createdAt), instant(updatedAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func queryArchivedTaskProgress(ctx context.Context, tx sqlExecutor, scopeID string) ([]ArchivedTaskProgress, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id,sequence,agent_id,kind,text,created_at FROM task_progress WHERE scope_id=? ORDER BY task_id,sequence`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ArchivedTaskProgress{}
	for rows.Next() {
		var value ArchivedTaskProgress
		var createdAt int64
		if err := rows.Scan(&value.TaskID, &value.Sequence, &value.AgentID, &value.Kind, &value.Text, &createdAt); err != nil {
			return nil, err
		}
		value.CreatedAt = instant(createdAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func queryArchivedEscalations(ctx context.Context, tx sqlExecutor, scopeID string) ([]ArchivedEscalation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT escalation_id,agent_id,question,options_json,status,answer,created_at,resolved_at FROM escalations WHERE scope_id=? ORDER BY created_at,escalation_id`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ArchivedEscalation{}
	for rows.Next() {
		var value ArchivedEscalation
		var options string
		var answer sql.NullString
		var createdAt int64
		var resolvedAt sql.NullInt64
		if err := rows.Scan(&value.ID, &value.AgentID, &value.Question, &options, &value.Status, &answer, &createdAt, &resolvedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(options), &value.Options); err != nil {
			return nil, err
		}
		if value.Options == nil {
			value.Options = []string{}
		}
		value.Answer, value.CreatedAt, value.ResolvedAt = answer.String, instant(createdAt), instant(resolvedAt.Int64)
		values = append(values, value)
	}
	return values, rows.Err()
}

func queryArchivedAgentCards(ctx context.Context, tx sqlExecutor, scopeID string) ([]ArchivedAgentCard, error) {
	rows, err := tx.QueryContext(ctx, `SELECT publication_id,agent_id,created_at,updated_at FROM a2a_publications WHERE scope_id=? ORDER BY created_at,publication_id`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ArchivedAgentCard{}
	for rows.Next() {
		var value ArchivedAgentCard
		var createdAt, updatedAt int64
		if err := rows.Scan(&value.ID, &value.AgentID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		value.CreatedAt, value.UpdatedAt = instant(createdAt), instant(updatedAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func queryArchivedOutputs(ctx context.Context, tx sqlExecutor, scopeID string) ([]ArchivedOutputStream, []ArchivedOutputValue, error) {
	publishers, err := outputPublishers(ctx, tx, scopeID)
	if err != nil {
		return nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+outputStreamColumns+` FROM output_streams WHERE scope_id=? ORDER BY created_at,stream_id`, scopeID)
	if err != nil {
		return nil, nil, err
	}
	streams := []ArchivedOutputStream{}
	for rows.Next() {
		record, err := scanOutputStream(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids := publishers[record.ID]
		if ids == nil {
			ids = []string{}
		}
		streams = append(streams, ArchivedOutputStream{ID: record.ID, Name: record.Name, RetentionLimit: record.RetentionLimit, CurrentSequence: record.CurrentSequence, MinimumCursor: record.MinimumCursor, PublisherAgentIDs: ids, CreatedAt: instant(record.CreatedAt), UpdatedAt: instant(record.UpdatedAt)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	valueRows, err := tx.QueryContext(ctx, `SELECT values_.stream_id,values_.sequence,values_.producer_type,values_.producer_id,values_.content_type,values_.value_json,values_.reference_json,values_.created_at FROM output_values AS values_ JOIN output_streams AS streams ON streams.stream_id=values_.stream_id WHERE streams.scope_id=? ORDER BY values_.stream_id,values_.sequence`, scopeID)
	if err != nil {
		return nil, nil, err
	}
	defer valueRows.Close()
	values := []ArchivedOutputValue{}
	for valueRows.Next() {
		value, err := scanOutputValue(valueRows)
		if err != nil {
			return nil, nil, err
		}
		values = append(values, ArchivedOutputValue{StreamID: value.StreamID, Sequence: value.Sequence, ProducerType: value.ProducerType, ProducerID: value.ProducerID, ContentType: value.ContentType, Value: value.Value, Reference: value.Reference, CreatedAt: value.CreatedAt})
	}
	return streams, values, valueRows.Err()
}

func (s *Store) ExportScope(ctx context.Context, scopeID string) (ScopeArchive, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return ScopeArchive{}, err
	}
	defer tx.Rollback()
	var createdAt int64
	if err := tx.QueryRowContext(ctx, `SELECT created_at FROM scopes WHERE scope_id=?`, scopeID).Scan(&createdAt); errors.Is(err, sql.ErrNoRows) {
		return ScopeArchive{}, Errorf(CodeNotFound, "Scope "+scopeID+" was not found")
	} else if err != nil {
		return ScopeArchive{}, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE scope_id=? AND lifecycle!='offline' AND lease_expires_at>?`, scopeID, nowMillis()).Scan(&active); err != nil {
		return ScopeArchive{}, err
	}
	if active != 0 {
		return ScopeArchive{}, Errorf(CodeConflict, "Scope must have no active agent executions before export")
	}
	if err := expireMessages(ctx, tx, scopeID, nowMillis()); err != nil {
		return ScopeArchive{}, err
	}
	archive := ScopeArchive{Format: ScopeArchiveFormat, Version: ScopeArchiveVersion, ExportedAt: instant(nowMillis()), Scope: ArchivedScope{ID: scopeID, CreatedAt: instant(createdAt)}}
	if err := checkPortableExportBudget(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if archive.Agents, err = queryArchivedAgents(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if archive.Links, err = queryArchivedLinks(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if archive.Messages, err = queryArchivedMessages(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if archive.Tasks, err = queryArchivedTasks(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if archive.TaskProgress, err = queryArchivedTaskProgress(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if archive.Escalations, err = queryArchivedEscalations(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if archive.AgentCardPublications, err = queryArchivedAgentCards(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if archive.A2ATasks, archive.A2AMessages, err = queryArchivedA2A(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if archive.OutputStreams, archive.OutputValues, err = queryArchivedOutputs(ctx, tx, scopeID); err != nil {
		return ScopeArchive{}, err
	}
	if err := exportChannels(ctx, tx, scopeID, &archive); err != nil {
		return ScopeArchive{}, err
	}
	if err := validateArchive(&archive); err != nil {
		return ScopeArchive{}, err
	}
	if err := checkEncodedArchiveSize(archive); err != nil {
		return ScopeArchive{}, err
	}
	return archive, tx.Commit()
}

func archiveDigest(archive ScopeArchive) (string, error) {
	data, err := json.Marshal(archive)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func requireArchiveID(value, field string) error {
	if err := validateIdentity(value, field, false); err != nil {
		return Errorf(CodeInvalidArgument, "Archive "+field+" is invalid")
	}
	return nil
}

func validateArchive(archive *ScopeArchive) error {
	if archive.Format != ScopeArchiveFormat {
		return Errorf(CodeInvalidArgument, "Archive format is not supported")
	}
	if archive.Version != 2 && archive.Version != ScopeArchiveVersion {
		return Errorf(CodeInvalidArgument, "Archive version is not supported")
	}
	if _, err := archiveTime(archive.ExportedAt, true); err != nil {
		return err
	}
	if err := requireArchiveID(archive.Scope.ID, "scope.id"); err != nil {
		return err
	}
	if _, err := archiveTime(archive.Scope.CreatedAt, true); err != nil {
		return err
	}
	if archive.Agents == nil || archive.Links == nil || archive.Messages == nil || archive.Tasks == nil || archive.TaskProgress == nil || archive.Escalations == nil || archive.AgentCardPublications == nil || archive.A2ATasks == nil || archive.A2AMessages == nil || archive.OutputStreams == nil || archive.OutputValues == nil {
		return Errorf(CodeInvalidArgument, "Archive record collections are required")
	}
	agents := map[string]bool{}
	for i := range archive.Agents {
		agent := &archive.Agents[i]
		if err := requireArchiveID(agent.ID, "agent.id"); err != nil {
			return err
		}
		if agents[agent.ID] {
			return Errorf(CodeInvalidArgument, "Archive contains a duplicate agent id")
		}
		agents[agent.ID] = true
		if agent.Capabilities == nil {
			return Errorf(CodeInvalidArgument, "Archive agent capabilities are required")
		}
		if err := validateText(agent.DisplayName, "displayName", 256, false); err != nil {
			return err
		}
		if err := validateCapabilities(agent.Capabilities); err != nil {
			return err
		}
		if _, err := archiveTime(agent.RegisteredAt, true); err != nil {
			return err
		}
		if _, err := archiveTime(agent.UpdatedAt, true); err != nil {
			return err
		}
	}
	links := map[string]bool{}
	for _, link := range archive.Links {
		if !agents[link.Left] || !agents[link.Right] || link.Left >= link.Right {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid peer link")
		}
		key := link.Left + "\x00" + link.Right
		if links[key] {
			return Errorf(CodeInvalidArgument, "Archive contains a duplicate peer link")
		}
		links[key] = true
		if _, err := archiveTime(link.CreatedAt, true); err != nil {
			return err
		}
	}
	messages := map[string]*ArchivedMessage{}
	idempotencyKeys := map[string]bool{}
	activeMessages := 0
	for i := range archive.Messages {
		message := &archive.Messages[i]
		if err := requireArchiveID(message.ID, "message.id"); err != nil {
			return err
		}
		if messages[message.ID] != nil {
			return Errorf(CodeInvalidArgument, "Archive contains a duplicate message id")
		}
		messages[message.ID] = message
		if err := validateMessageParticipantKind(message.FromKind); err != nil {
			return err
		}
		if err := validateMessageParticipantKind(message.ToKind); err != nil {
			return err
		}
		if (message.FromKind == MessageParticipantAgent && !agents[message.From]) || (message.ToKind == MessageParticipantAgent && !agents[message.To]) {
			return Errorf(CodeInvalidArgument, "Archive message refers to an unknown agent")
		}
		if err := requireArchiveID(message.From, "message.from"); err != nil {
			return err
		}
		if err := requireArchiveID(message.To, "message.to"); err != nil {
			return err
		}
		if err := validateMessageMode(message.Mode); err != nil {
			return err
		}
		if err := validateText(message.Body, "body", 65536, false); err != nil {
			return err
		}
		if err := validateContext(message.Context); err != nil {
			return err
		}
		if message.Context == nil {
			return Errorf(CodeInvalidArgument, "Archive message context is required")
		}
		if err := validateText(message.IdempotencyKey, "idempotencyKey", 256, true); err != nil {
			return err
		}
		if message.IdempotencyKey != "" {
			key := string(message.FromKind) + "\x00" + message.From + "\x00" + message.IdempotencyKey
			if idempotencyKeys[key] {
				return Errorf(CodeInvalidArgument, "Archive contains a duplicate message idempotency key")
			}
			idempotencyKeys[key] = true
		}
		if message.State != DeliveryQueued && message.State != DeliveryDelivered && message.State != DeliveryAcknowledged && message.State != DeliveryExpired {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid message state")
		}
		if message.State != DeliveryAcknowledged && message.State != DeliveryExpired {
			activeMessages++
		}
		if _, err := archiveTime(message.CreatedAt, true); err != nil {
			return err
		}
		for _, value := range []string{message.ExpiresAt, message.DeliveredAt, message.AcknowledgedAt, message.RepliedAt} {
			if _, err := archiveTime(value, false); err != nil {
				return err
			}
		}
	}
	if activeMessages > messageBacklogCap {
		return Errorf(CodeInvalidArgument, "Archive message backlog exceeds the scope limit")
	}
	for _, message := range archive.Messages {
		if message.Mode == MessageResponse {
			request := messages[message.ResponseTo]
			if request == nil || request.Mode != MessageRequest || request.From != message.To || request.FromKind != message.ToKind || request.To != message.From || request.ToKind != message.FromKind || request.ResponseMessageID != message.ID || request.RepliedAt == "" {
				return Errorf(CodeInvalidArgument, "Archive contains an invalid response correlation")
			}
		} else if message.ResponseTo != "" {
			return Errorf(CodeInvalidArgument, "Archive contains responseTo on a non-response message")
		}
		if message.Mode != MessageRequest && (message.ResponseMessageID != "" || message.RepliedAt != "") {
			return Errorf(CodeInvalidArgument, "Archive contains reply state on a non-request message")
		}
		if message.ResponseMessageID != "" {
			response := messages[message.ResponseMessageID]
			if message.Mode != MessageRequest || response == nil || response.ResponseTo != message.ID || message.RepliedAt == "" {
				return Errorf(CodeInvalidArgument, "Archive contains an invalid response message id")
			}
		} else if message.RepliedAt != "" {
			return Errorf(CodeInvalidArgument, "Archive contains a reply timestamp without a response")
		}
		switch message.State {
		case DeliveryQueued:
			if message.DeliveredAt != "" || message.AcknowledgedAt != "" {
				return Errorf(CodeInvalidArgument, "Archive queued message contains delivery timestamps")
			}
		case DeliveryDelivered:
			if message.DeliveredAt == "" || message.AcknowledgedAt != "" {
				return Errorf(CodeInvalidArgument, "Archive delivered message timestamps are invalid")
			}
		case DeliveryAcknowledged:
			if message.DeliveredAt == "" || message.AcknowledgedAt == "" {
				return Errorf(CodeInvalidArgument, "Archive acknowledged message timestamps are invalid")
			}
		case DeliveryExpired:
			if message.ExpiresAt == "" || message.AcknowledgedAt != "" {
				return Errorf(CodeInvalidArgument, "Archive expired message timestamps are invalid")
			}
		}
	}
	tasks := map[string]*ArchivedTask{}
	activeTasks := 0
	for i := range archive.Tasks {
		task := &archive.Tasks[i]
		if err := requireArchiveID(task.ID, "task.id"); err != nil {
			return err
		}
		if tasks[task.ID] != nil {
			return Errorf(CodeInvalidArgument, "Archive contains a duplicate task id")
		}
		tasks[task.ID] = task
		if task.Dependencies == nil {
			return Errorf(CodeInvalidArgument, "Archive task dependencies are required")
		}
		if err := validateText(task.Title, "title", 256, false); err != nil {
			return err
		}
		if err := validateText(task.Description, "description", 16384, true); err != nil {
			return err
		}
		if task.Status != "open" && task.Status != "done" {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid task status")
		}
		if task.Status == "open" {
			activeTasks++
		}
		if task.CreatedBy != nil && !agents[*task.CreatedBy] {
			return Errorf(CodeInvalidArgument, "Archive task creator is unknown")
		}
		if task.Status == "done" && !agents[task.ClaimedBy] {
			return Errorf(CodeInvalidArgument, "Archive completed task owner is unknown")
		}
		if task.Status == "open" && task.ClaimedBy != "" {
			return Errorf(CodeInvalidArgument, "Archive open task has an owner")
		}
		if _, err := archiveTime(task.CreatedAt, true); err != nil {
			return err
		}
		if _, err := archiveTime(task.UpdatedAt, true); err != nil {
			return err
		}
	}
	if activeTasks > activeTaskCap {
		return Errorf(CodeInvalidArgument, "Archive active tasks exceed the scope limit")
	}
	for _, task := range archive.Tasks {
		seenDependencies := map[string]bool{}
		for _, dependency := range task.Dependencies {
			if dependency == task.ID || tasks[dependency] == nil || seenDependencies[dependency] {
				return Errorf(CodeInvalidArgument, "Archive task dependency is invalid")
			}
			seenDependencies[dependency] = true
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visitTask func(string) bool
	visitTask = func(taskID string) bool {
		if visiting[taskID] {
			return false
		}
		if visited[taskID] {
			return true
		}
		visiting[taskID] = true
		for _, dependency := range tasks[taskID].Dependencies {
			if !visitTask(dependency) {
				return false
			}
		}
		delete(visiting, taskID)
		visited[taskID] = true
		return true
	}
	for taskID := range tasks {
		if !visitTask(taskID) {
			return Errorf(CodeInvalidArgument, "Archive task dependencies contain a cycle")
		}
	}
	progressKeys := map[string]bool{}
	progressCount := map[string]int{}
	progressBytes := map[string]int{}
	progressMaximum := map[string]int64{}
	for _, progress := range archive.TaskProgress {
		if tasks[progress.TaskID] == nil || !agents[progress.AgentID] || progress.Sequence < 1 {
			return Errorf(CodeInvalidArgument, "Archive contains invalid task progress")
		}
		key := fmt.Sprintf("%s:%d", progress.TaskID, progress.Sequence)
		if progressKeys[key] {
			return Errorf(CodeInvalidArgument, "Archive contains duplicate task progress")
		}
		progressKeys[key] = true
		progressCount[progress.TaskID]++
		progressBytes[progress.TaskID] += len(progress.Text)
		if progress.Sequence > progressMaximum[progress.TaskID] {
			progressMaximum[progress.TaskID] = progress.Sequence
		}
		if progressCount[progress.TaskID] > maxTaskProgressEvents || progressBytes[progress.TaskID] > maxTaskProgressBytes {
			return Errorf(CodeInvalidArgument, "Archive task progress exceeds the task limit")
		}
		if progress.Kind != "progress" && progress.Kind != "note" && progress.Kind != "blocker" {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid task progress kind")
		}
		if err := validateText(progress.Text, "text", 4000, false); err != nil {
			return err
		}
		if _, err := archiveTime(progress.CreatedAt, true); err != nil {
			return err
		}
	}
	for taskID, count := range progressCount {
		if progressMaximum[taskID] != int64(count) {
			return Errorf(CodeInvalidArgument, "Archive task progress sequence has a gap")
		}
	}
	escalations := map[string]bool{}
	pendingEscalations := 0
	pendingByAgent := map[string]int{}
	for _, escalation := range archive.Escalations {
		if err := requireArchiveID(escalation.ID, "escalation.id"); err != nil {
			return err
		}
		if escalations[escalation.ID] || !agents[escalation.AgentID] {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid escalation")
		}
		escalations[escalation.ID] = true
		if escalation.Options == nil {
			return Errorf(CodeInvalidArgument, "Archive escalation options are required")
		}
		if err := validateText(escalation.Question, "question", 4000, false); err != nil {
			return err
		}
		if len(escalation.Options) == 1 || len(escalation.Options) > 4 {
			return Errorf(CodeInvalidArgument, "Archive escalation options are invalid")
		}
		for _, option := range escalation.Options {
			if err := validateText(option, "options", 256, false); err != nil {
				return err
			}
		}
		if escalation.Status != "pending" && escalation.Status != "resolved" && escalation.Status != "cancelled" {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid escalation status")
		}
		if escalation.Status == "pending" {
			pendingEscalations++
			pendingByAgent[escalation.AgentID]++
			if pendingByAgent[escalation.AgentID] > pendingEscalationCapPerAgent {
				return Errorf(CodeInvalidArgument, "Archive pending escalations exceed the agent limit")
			}
		}
		if escalation.Status == "resolved" && escalation.ResolvedAt == "" {
			return Errorf(CodeInvalidArgument, "Archive resolved escalation has no resolvedAt")
		}
		if escalation.Answer != "" {
			if err := validateText(escalation.Answer, "answer", 16384, false); err != nil {
				return err
			}
		}
		if _, err := archiveTime(escalation.CreatedAt, true); err != nil {
			return err
		}
		if _, err := archiveTime(escalation.ResolvedAt, false); err != nil {
			return err
		}
	}
	if pendingEscalations > pendingEscalationCapPerScope {
		return Errorf(CodeInvalidArgument, "Archive pending escalations exceed the scope limit")
	}
	publications := map[string]bool{}
	publishedAgents := map[string]bool{}
	for _, publication := range archive.AgentCardPublications {
		if err := requireArchiveID(publication.ID, "publication.id"); err != nil {
			return err
		}
		if publications[publication.ID] || !agents[publication.AgentID] || publishedAgents[publication.AgentID] {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid Agent Card publication")
		}
		publications[publication.ID] = true
		publishedAgents[publication.AgentID] = true
		if _, err := archiveTime(publication.CreatedAt, true); err != nil {
			return err
		}
		if _, err := archiveTime(publication.UpdatedAt, true); err != nil {
			return err
		}
	}
	a2aTasks := map[string]ArchivedA2ATask{}
	for _, task := range archive.A2ATasks {
		if err := requireArchiveID(task.ID, "a2aTask.id"); err != nil {
			return err
		}
		if err := requireArchiveID(task.ContextID, "a2aTask.contextId"); err != nil {
			return err
		}
		if err := requireArchiveID(task.PrincipalID, "a2aTask.principalId"); err != nil {
			return err
		}
		if a2aTasks[task.ID].ID != "" || !publications[task.PublicationID] || !agents[task.TargetAgentID] || !validA2ATaskState(task.State) {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid A2A task")
		}
		if _, err := archiveTime(task.CreatedAt, true); err != nil {
			return err
		}
		if _, err := archiveTime(task.UpdatedAt, true); err != nil {
			return err
		}
		a2aTasks[task.ID] = task
	}
	a2aMessageKeys := map[string]bool{}
	for _, correlation := range archive.A2AMessages {
		task, ok := a2aTasks[correlation.TaskID]
		request := messages[correlation.BusRequestMessageID]
		if !ok || task.PrincipalID != correlation.PrincipalID || request == nil || request.Mode != MessageRequest || request.FromKind != MessageParticipantA2APrincipal || request.From != correlation.PrincipalID || request.ToKind != MessageParticipantAgent || request.To != task.TargetAgentID {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid A2A message correlation")
		}
		if err := requireArchiveID(correlation.ClientMessageID, "a2aMessage.clientMessageId"); err != nil {
			return err
		}
		key := correlation.PrincipalID + "\x00" + correlation.ClientMessageID
		if a2aMessageKeys[key] {
			return Errorf(CodeInvalidArgument, "Archive contains a duplicate A2A client message id")
		}
		a2aMessageKeys[key] = true
		if correlation.BusResponseMessageID != "" {
			response := messages[correlation.BusResponseMessageID]
			if response == nil || request.ResponseMessageID != response.ID || response.ResponseTo != request.ID {
				return Errorf(CodeInvalidArgument, "Archive contains an invalid A2A response correlation")
			}
		}
		if _, err := archiveTime(correlation.CreatedAt, true); err != nil {
			return err
		}
		if _, err := archiveTime(correlation.UpdatedAt, true); err != nil {
			return err
		}
	}
	streams := map[string]*ArchivedOutputStream{}
	streamNames := map[string]bool{}
	if len(archive.OutputStreams) > maxOutputStreams {
		return Errorf(CodeInvalidArgument, "Archive output streams exceed the scope limit")
	}
	for i := range archive.OutputStreams {
		stream := &archive.OutputStreams[i]
		if err := requireArchiveID(stream.ID, "outputStream.id"); err != nil {
			return err
		}
		if streams[stream.ID] != nil || streamNames[stream.Name] || stream.RetentionLimit < 1 || stream.RetentionLimit > maxOutputRetention || stream.MinimumCursor < 0 || stream.CurrentSequence < stream.MinimumCursor {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid output stream")
		}
		streams[stream.ID] = stream
		streamNames[stream.Name] = true
		if stream.PublisherAgentIDs == nil || len(stream.PublisherAgentIDs) > maxOutputPublishers {
			return Errorf(CodeInvalidArgument, "Archive output publishers are invalid")
		}
		if err := validateIdentity(stream.Name, "outputStream.name", false); err != nil {
			return err
		}
		seenPublishers := map[string]bool{}
		for _, publisher := range stream.PublisherAgentIDs {
			if !agents[publisher] || seenPublishers[publisher] {
				return Errorf(CodeInvalidArgument, "Archive output publisher is unknown")
			}
			seenPublishers[publisher] = true
		}
		if _, err := archiveTime(stream.CreatedAt, true); err != nil {
			return err
		}
		if _, err := archiveTime(stream.UpdatedAt, true); err != nil {
			return err
		}
	}
	outputKeys := map[string]bool{}
	outputCounts := map[string]int{}
	for _, value := range archive.OutputValues {
		stream := streams[value.StreamID]
		if stream == nil || value.Sequence <= stream.MinimumCursor || value.Sequence > stream.CurrentSequence || (value.ProducerType != "agent" && value.ProducerType != "principal") {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid output value")
		}
		outputCounts[value.StreamID]++
		if outputCounts[value.StreamID] > stream.RetentionLimit {
			return Errorf(CodeInvalidArgument, "Archive output history exceeds its retention limit")
		}
		if value.ProducerType == "agent" && !agents[value.ProducerID] {
			return Errorf(CodeInvalidArgument, "Archive output producer is unknown")
		}
		if err := requireArchiveID(value.ProducerID, "outputValue.producerId"); err != nil {
			return err
		}
		if value.ContentType != OutputText && value.ContentType != OutputJSON {
			return Errorf(CodeInvalidArgument, "Archive contains an invalid output content type")
		}
		if _, _, err := validatePublishOutput(PublishOutputInput{ContentType: value.ContentType, Value: value.Value, Reference: value.Reference}); err != nil {
			return err
		}
		key := fmt.Sprintf("%s:%d", value.StreamID, value.Sequence)
		if outputKeys[key] {
			return Errorf(CodeInvalidArgument, "Archive contains a duplicate output sequence")
		}
		outputKeys[key] = true
		if _, err := archiveTime(value.CreatedAt, true); err != nil {
			return err
		}
	}
	for streamID, stream := range streams {
		if int64(outputCounts[streamID]) != stream.CurrentSequence-stream.MinimumCursor {
			return Errorf(CodeInvalidArgument, "Archive output history sequence has a gap")
		}
	}
	return validateArchivedChannels(archive, agents)
}

func insertArchive(ctx context.Context, tx sqlExecutor, archive ScopeArchive, digest, tokenHash string, importedAt int64) error {
	createdAt, _ := archiveTime(archive.Scope.CreatedAt, true)
	if _, err := tx.ExecContext(ctx, `INSERT INTO scopes(scope_id,token_hash,created_at) VALUES(?,?,?)`, archive.Scope.ID, tokenHash, createdAt); err != nil {
		return err
	}
	for _, agent := range archive.Agents {
		capabilities, _ := jsonValue(agent.Capabilities)
		registeredAt, _ := archiveTime(agent.RegisteredAt, true)
		updatedAt, _ := archiveTime(agent.UpdatedAt, true)
		executionID, err := randomID("exec_")
		if err != nil {
			return err
		}
		dormantToken, err := randomValue(32)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agents(scope_id,agent_id,display_name,capabilities_json,execution_id,token_hash,lifecycle,ready,lease_expires_at,registered_at,updated_at) VALUES(?,?,?,?,?,?,'offline',0,0,?,?)`, archive.Scope.ID, agent.ID, agent.DisplayName, capabilities, executionID, tokenDigest(dormantToken), registeredAt, updatedAt); err != nil {
			return err
		}
	}
	for _, link := range archive.Links {
		created, _ := archiveTime(link.CreatedAt, true)
		if _, err := tx.ExecContext(ctx, `INSERT INTO peer_links(scope_id,left_agent,right_agent,created_at) VALUES(?,?,?,?)`, archive.Scope.ID, link.Left, link.Right, created); err != nil {
			return err
		}
	}
	messages := append([]ArchivedMessage(nil), archive.Messages...)
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].CreatedAt < messages[j].CreatedAt })
	for _, message := range messages {
		contextJSON, _ := jsonValue(message.Context)
		created, _ := archiveTime(message.CreatedAt, true)
		expires, _ := archiveTime(message.ExpiresAt, false)
		delivered, _ := archiveTime(message.DeliveredAt, false)
		acknowledged, _ := archiveTime(message.AcknowledgedAt, false)
		replied, _ := archiveTime(message.RepliedAt, false)
		expiresIn := int64(0)
		if expires > created {
			expiresIn = expires - created
		}
		requestHash := messageRequestHash(SendMessageInput{To: message.To, Body: message.Body, ResponseTo: message.ResponseTo, ExpiresInMS: expiresIn}, message.Mode, contextJSON)
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages(message_id,scope_id,from_kind,from_id,to_kind,to_id,mode,body,context_json,response_to,idempotency_key,request_hash,state,created_at,expires_at,delivered_at,acknowledged_at,replied_at,response_message_id) VALUES(?,?,?,?,?,?,?,?,?,NULL,?,?,?,?,?,?,?,?,?)`, message.ID, archive.Scope.ID, message.FromKind, message.From, message.ToKind, message.To, message.Mode, message.Body, contextJSON, nullableString(message.IdempotencyKey), requestHash, message.State, created, nullableInt64(expires), nullableInt64(delivered), nullableInt64(acknowledged), nullableInt64(replied), nullableString(message.ResponseMessageID)); err != nil {
			return err
		}
	}
	for _, message := range messages {
		if message.ResponseTo == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET response_to=? WHERE message_id=?`, message.ResponseTo, message.ID); err != nil {
			return err
		}
	}
	for _, task := range archive.Tasks {
		dependencies, _ := jsonValue(task.Dependencies)
		created, _ := archiveTime(task.CreatedAt, true)
		updated, _ := archiveTime(task.UpdatedAt, true)
		var claimedBy, execution any
		if task.Status == "done" {
			claimedBy, execution = task.ClaimedBy, "imported"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(task_id,scope_id,title,description,created_by,claimed_by,claimed_execution_id,status,dependencies_json,note,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, task.ID, archive.Scope.ID, task.Title, task.Description, nullableStringPtr(task.CreatedBy), claimedBy, execution, task.Status, dependencies, nullableString(task.Note), created, updated); err != nil {
			return err
		}
	}
	for _, progress := range archive.TaskProgress {
		created, _ := archiveTime(progress.CreatedAt, true)
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_progress(task_id,scope_id,sequence,agent_id,execution_id,kind,text,created_at) VALUES(?,?,?,?,?,?,?,?)`, progress.TaskID, archive.Scope.ID, progress.Sequence, progress.AgentID, "imported", progress.Kind, progress.Text, created); err != nil {
			return err
		}
	}
	for _, escalation := range archive.Escalations {
		options, _ := jsonValue(escalation.Options)
		created, _ := archiveTime(escalation.CreatedAt, true)
		resolved, _ := archiveTime(escalation.ResolvedAt, false)
		if _, err := tx.ExecContext(ctx, `INSERT INTO escalations(escalation_id,scope_id,agent_id,question,options_json,status,answer,created_at,resolved_at) VALUES(?,?,?,?,?,?,?,?,?)`, escalation.ID, archive.Scope.ID, escalation.AgentID, escalation.Question, options, escalation.Status, nullableString(escalation.Answer), created, nullableInt64(resolved)); err != nil {
			return err
		}
	}
	for _, publication := range archive.AgentCardPublications {
		created, _ := archiveTime(publication.CreatedAt, true)
		updated, _ := archiveTime(publication.UpdatedAt, true)
		if _, err := tx.ExecContext(ctx, `INSERT INTO a2a_publications(publication_id,scope_id,agent_id,enabled,created_at,updated_at) VALUES(?,?,?,0,?,?)`, publication.ID, archive.Scope.ID, publication.AgentID, created, updated); err != nil {
			return err
		}
	}
	for _, task := range archive.A2ATasks {
		created, _ := archiveTime(task.CreatedAt, true)
		updated, _ := archiveTime(task.UpdatedAt, true)
		if _, err := tx.ExecContext(ctx, `INSERT INTO a2a_tasks(task_id,scope_id,context_id,principal_id,publication_id,target_agent_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, task.ID, archive.Scope.ID, task.ContextID, task.PrincipalID, task.PublicationID, task.TargetAgentID, task.State, created, updated); err != nil {
			return err
		}
	}
	for _, correlation := range archive.A2AMessages {
		created, _ := archiveTime(correlation.CreatedAt, true)
		updated, _ := archiveTime(correlation.UpdatedAt, true)
		requestHash := a2aMessageRequestHash(AcceptA2AMessageInput{TaskID: correlation.TaskID, ClientMessageID: correlation.ClientMessageID})
		if _, err := tx.ExecContext(ctx, `INSERT INTO a2a_message_correlations(principal_id,client_message_id,task_id,request_hash,bus_request_message_id,bus_response_message_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, correlation.PrincipalID, correlation.ClientMessageID, correlation.TaskID, requestHash, correlation.BusRequestMessageID, nullableString(correlation.BusResponseMessageID), created, updated); err != nil {
			return err
		}
	}
	for _, stream := range archive.OutputStreams {
		created, _ := archiveTime(stream.CreatedAt, true)
		updated, _ := archiveTime(stream.UpdatedAt, true)
		if _, err := tx.ExecContext(ctx, `INSERT INTO output_streams(stream_id,scope_id,name,retention_limit,sequence,floor_sequence,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, stream.ID, archive.Scope.ID, stream.Name, stream.RetentionLimit, stream.CurrentSequence, stream.MinimumCursor, created, updated); err != nil {
			return err
		}
		for _, publisher := range stream.PublisherAgentIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO output_stream_publishers(stream_id,scope_id,agent_id) VALUES(?,?,?)`, stream.ID, archive.Scope.ID, publisher); err != nil {
				return err
			}
		}
	}
	for _, value := range archive.OutputValues {
		valueJSON, err := jsonValue(value.Value)
		if err != nil {
			return err
		}
		var reference any
		if value.Reference != nil {
			reference, err = jsonValue(value.Reference)
			if err != nil {
				return err
			}
		}
		created, _ := archiveTime(value.CreatedAt, true)
		if _, err := tx.ExecContext(ctx, `INSERT INTO output_values(stream_id,sequence,producer_type,producer_id,content_type,value_json,reference_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, value.StreamID, value.Sequence, value.ProducerType, value.ProducerID, value.ContentType, valueJSON, reference, created); err != nil {
			return err
		}
	}
	if err := importChannels(ctx, tx, archive); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, archive.Scope.ID, "scope.imported", digest, eventAttributes("archiveVersion", fmt.Sprintf("%d", archive.Version)), importedAt); err != nil {
		return err
	}
	return nil
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
func nullableStringPtr(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

func (s *Store) ImportScope(ctx context.Context, archive ScopeArchive) (ImportScopeResult, error) {
	if err := validateArchive(&archive); err != nil {
		return ImportScopeResult{}, err
	}
	digest, err := archiveDigest(archive)
	if err != nil {
		return ImportScopeResult{}, err
	}
	tx, err := s.beginTx(ctx, archive.Scope.ID)
	if err != nil {
		return ImportScopeResult{}, err
	}
	defer tx.Rollback()
	existingScope, err := importedArchiveScope(ctx, tx, digest)
	if err != nil {
		return ImportScopeResult{}, err
	}
	if existingScope != "" {
		return ImportScopeResult{ScopeID: existingScope, Imported: false}, tx.Commit()
	}
	var found int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM scopes WHERE scope_id=?`, archive.Scope.ID).Scan(&found)
	if err == nil {
		return ImportScopeResult{}, Errorf(CodeConflict, "Scope "+archive.Scope.ID+" already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ImportScopeResult{}, err
	}
	token, err := randomValue(32)
	if err != nil {
		return ImportScopeResult{}, err
	}
	now := nowMillis()
	if err := insertArchive(ctx, tx, archive, digest, tokenDigest(token), now); err != nil {
		if isStorageConstraint(err) {
			return ImportScopeResult{}, Errorf(CodeInvalidArgument, "Archive contains conflicting records")
		}
		return ImportScopeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportScopeResult{}, err
	}
	return ImportScopeResult{ScopeID: archive.Scope.ID, ScopeToken: token, Imported: true}, nil
}

func importedArchiveScope(ctx context.Context, tx sqlExecutor, digest string) (string, error) {
	var scopeID string
	err := tx.QueryRowContext(ctx, `SELECT scope_id FROM events WHERE event_type='scope.imported' AND subject_id=?`, digest).Scan(&scopeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return scopeID, err
}

func (r *Runtime) ExportScope(ctx context.Context, scopeID string) (ScopeArchive, error) {
	if err := validateIdentity(scopeID, "scopeId", false); err != nil {
		return ScopeArchive{}, err
	}
	return r.store.ExportScope(ctx, scopeID)
}

func (r *Runtime) ImportScope(ctx context.Context, archive ScopeArchive) (ImportScopeResult, error) {
	result, err := r.store.ImportScope(ctx, archive)
	if err == nil && result.Imported {
		r.notifyScope(result.ScopeID)
	}
	return result, err
}
