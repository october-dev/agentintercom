package bus

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const a2aTaskColumns = `task_id,context_id,principal_id,publication_id,target_agent_id,state,created_at,updated_at`

type a2aTaskQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanA2ATask(row rowScanner) (A2ATaskCorrelation, error) {
	var task A2ATaskCorrelation
	var createdAt, updatedAt int64
	err := row.Scan(&task.ID, &task.ContextID, &task.PrincipalID, &task.PublicationID, &task.TargetAgentID, &task.State, &createdAt, &updatedAt)
	task.CreatedAt, task.UpdatedAt = instant(createdAt), instant(updatedAt)
	task.Messages = []A2AMessageCorrelation{}
	return task, err
}

func loadA2ATask(ctx context.Context, query a2aTaskQuery, principalID, taskID string) (A2ATaskCorrelation, error) {
	task, err := scanA2ATask(query.QueryRowContext(ctx, `SELECT `+a2aTaskColumns+` FROM a2a_tasks WHERE principal_id=? AND task_id=?`, principalID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return A2ATaskCorrelation{}, Errorf(CodeNotFound, "A2A task was not found")
	}
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	rows, err := query.QueryContext(ctx, `SELECT client_message_id,bus_request_message_id,bus_response_message_id,created_at,updated_at FROM a2a_message_correlations WHERE principal_id=? AND task_id=? ORDER BY created_at,client_message_id`, principalID, taskID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var correlation A2AMessageCorrelation
		var responseID sql.NullString
		var createdAt, updatedAt int64
		if err := rows.Scan(&correlation.ClientMessageID, &correlation.BusRequestMessageID, &responseID, &createdAt, &updatedAt); err != nil {
			return A2ATaskCorrelation{}, err
		}
		correlation.BusResponseMessageID = responseID.String
		correlation.CreatedAt, correlation.UpdatedAt = instant(createdAt), instant(updatedAt)
		task.Messages = append(task.Messages, correlation)
	}
	return task, rows.Err()
}

func transitionA2ATaskForMessage(ctx context.Context, tx sqlExecutor, scopeID, messageID string, next func(A2ATaskState) (A2ATaskState, bool), now int64) error {
	var taskID, publicationID string
	var current A2ATaskState
	err := tx.QueryRowContext(ctx, `SELECT tasks.task_id,tasks.publication_id,tasks.state FROM a2a_tasks AS tasks JOIN a2a_message_correlations AS correlations ON correlations.task_id=tasks.task_id WHERE tasks.scope_id=? AND correlations.bus_request_message_id=?`, scopeID, messageID).Scan(&taskID, &publicationID, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	state, changed := next(current)
	if !changed || state == current {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE a2a_tasks SET state=?,updated_at=? WHERE scope_id=? AND task_id=? AND state=?`, state, now, scopeID, taskID, current)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return Errorf(CodeConflict, "A2A task changed concurrently")
	}
	return appendEvent(ctx, tx, scopeID, "a2a.task_state_changed", taskID, eventAttributes("publicationId", publicationID, "state", string(state)), now)
}

func a2aMessageRequestHash(input AcceptA2AMessageInput) string {
	data, _ := json.Marshal(input)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *Store) AcceptA2AMessage(ctx context.Context, principal A2APrincipal, limits A2APrincipalLimits, input AcceptA2AMessageInput) (A2ATaskCorrelation, error) {
	tx, err := s.beginTx(ctx, principal.ScopeID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentA2APrincipal(ctx, tx, principal); err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := expireMessages(ctx, tx, principal.ScopeID, nowMillis()); err != nil {
		return A2ATaskCorrelation{}, err
	}
	requestHash := a2aMessageRequestHash(input)
	var existingTaskID, existingHash string
	err = tx.QueryRowContext(ctx, `SELECT task_id,request_hash FROM a2a_message_correlations WHERE principal_id=? AND client_message_id=?`, principal.ID, input.ClientMessageID).Scan(&existingTaskID, &existingHash)
	if err == nil {
		if existingHash != requestHash {
			return A2ATaskCorrelation{}, Errorf(CodeConflict, "A2A client message ID was already used with different content")
		}
		task, err := loadA2ATask(ctx, tx, principal.ID, existingTaskID)
		if err != nil {
			return A2ATaskCorrelation{}, err
		}
		if err := tx.Commit(); err != nil {
			return A2ATaskCorrelation{}, err
		}
		return task, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return A2ATaskCorrelation{}, err
	}
	var targetAgentID string
	err = tx.QueryRowContext(ctx, `SELECT agent_id FROM a2a_publications WHERE scope_id=? AND publication_id=? AND enabled=1`, principal.ScopeID, principal.PublicationID).Scan(&targetAgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return A2ATaskCorrelation{}, Errorf(CodeUnauthenticated, "Invalid scoped credential")
	}
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	var unfinishedMessages, unfinishedBytes int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(octet_length(messages.body)),0)
FROM a2a_message_correlations AS correlations
JOIN a2a_tasks AS tasks ON tasks.task_id=correlations.task_id
JOIN messages ON messages.message_id=correlations.bus_request_message_id
WHERE correlations.principal_id=? AND tasks.principal_id=? AND tasks.state NOT IN ('completed','failed','canceled','rejected')`,
		principal.ID, principal.ID).Scan(&unfinishedMessages, &unfinishedBytes)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	if unfinishedMessages >= limits.MessageLimit {
		return A2ATaskCorrelation{}, Errorf(CodeBackpressure, "Remote principal unfinished message limit is full")
	}
	if unfinishedBytes+int64(len(input.Body)) > limits.ByteLimit {
		return A2ATaskCorrelation{}, Errorf(CodeBackpressure, "Remote principal unfinished byte limit is full")
	}
	// Reserve half the active message slots for local work even when a scope
	// owner has issued many individually bounded remote credentials.
	var remoteMessages, remoteBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(octet_length(body)),0)
FROM messages WHERE scope_id=? AND from_kind='a2aPrincipal' AND state NOT IN ('acknowledged','expired')`, principal.ScopeID).Scan(&remoteMessages, &remoteBytes); err != nil {
		return A2ATaskCorrelation{}, err
	}
	if remoteMessages >= maxRemoteBacklogMessages || remoteBytes+int64(len(input.Body)) > maxRemoteBacklogBytes {
		return A2ATaskCorrelation{}, Errorf(CodeBackpressure, "Scope remote message budget is full")
	}
	now := nowMillis()
	taskID := input.TaskID
	contextID := input.ContextID
	if taskID == "" {
		taskID, err = randomID("a2at_")
		if err != nil {
			return A2ATaskCorrelation{}, err
		}
		if contextID == "" {
			contextID, err = randomID("a2ac_")
			if err != nil {
				return A2ATaskCorrelation{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO a2a_tasks(task_id,scope_id,context_id,principal_id,publication_id,target_agent_id,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'submitted',?,?)`,
			taskID, principal.ScopeID, contextID, principal.ID, principal.PublicationID, targetAgentID, now, now); err != nil {
			return A2ATaskCorrelation{}, err
		}
		if err := appendEvent(ctx, tx, principal.ScopeID, "a2a.task_created", taskID, eventAttributes("publicationId", principal.PublicationID, "agentId", targetAgentID, "state", string(A2ATaskSubmitted)), now); err != nil {
			return A2ATaskCorrelation{}, err
		}
	} else {
		var storedContextID, storedPublicationID, storedTargetAgentID string
		var state A2ATaskState
		err := tx.QueryRowContext(ctx, `SELECT context_id,publication_id,target_agent_id,state FROM a2a_tasks WHERE principal_id=? AND task_id=?`, principal.ID, taskID).Scan(&storedContextID, &storedPublicationID, &storedTargetAgentID, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return A2ATaskCorrelation{}, Errorf(CodeNotFound, "A2A task was not found")
		}
		if err != nil {
			return A2ATaskCorrelation{}, err
		}
		if storedPublicationID != principal.PublicationID || storedTargetAgentID != targetAgentID {
			return A2ATaskCorrelation{}, Errorf(CodeNotFound, "A2A task was not found")
		}
		if a2aTaskTerminal(state) {
			return A2ATaskCorrelation{}, Errorf(CodeConflict, "A2A task is already terminal")
		}
		if state != A2ATaskInputRequired {
			return A2ATaskCorrelation{}, Errorf(CodeConflict, "A2A task is not waiting for input")
		}
		if contextID != "" && contextID != storedContextID {
			return A2ATaskCorrelation{}, Errorf(CodeConflict, "A2A context ID does not match the task")
		}
		contextID = storedContextID
		if _, err := tx.ExecContext(ctx, `UPDATE a2a_tasks SET state='working',updated_at=? WHERE principal_id=? AND task_id=? AND state='input-required'`, now, principal.ID, taskID); err != nil {
			return A2ATaskCorrelation{}, err
		}
		if err := appendEvent(ctx, tx, principal.ScopeID, "a2a.task_state_changed", taskID, eventAttributes("publicationId", principal.PublicationID, "state", string(A2ATaskWorking)), now); err != nil {
			return A2ATaskCorrelation{}, err
		}
	}
	message, err := sendMessageTx(ctx, tx, principal.ScopeID, MessageParticipantA2APrincipal, principal.ID, SendMessageInput{
		To: targetAgentID, Body: input.Body, Mode: MessageRequest, IdempotencyKey: input.ClientMessageID, Context: []ContextItem{},
	})
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO a2a_message_correlations(principal_id,client_message_id,task_id,request_hash,bus_request_message_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		principal.ID, input.ClientMessageID, taskID, requestHash, message.ID, now, now); err != nil {
		return A2ATaskCorrelation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE a2a_tasks SET updated_at=? WHERE principal_id=? AND task_id=?`, now, principal.ID, taskID); err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := appendEvent(ctx, tx, principal.ScopeID, "a2a.message_accepted", taskID, eventAttributes("publicationId", principal.PublicationID, "agentId", targetAgentID, "busMessageId", message.ID), now); err != nil {
		return A2ATaskCorrelation{}, err
	}
	task, err := loadA2ATask(ctx, tx, principal.ID, taskID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := tx.Commit(); err != nil {
		return A2ATaskCorrelation{}, err
	}
	return task, nil
}

func (s *Store) A2ATask(ctx context.Context, principal A2APrincipal, taskID string) (A2ATaskCorrelation, error) {
	tx, err := s.beginTx(ctx, principal.ScopeID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentA2APrincipal(ctx, tx, principal); err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := expireMessages(ctx, tx, principal.ScopeID, nowMillis()); err != nil {
		return A2ATaskCorrelation{}, err
	}
	task, err := loadA2ATask(ctx, tx, principal.ID, taskID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := tx.Commit(); err != nil {
		return A2ATaskCorrelation{}, err
	}
	return task, nil
}

func a2aTaskTerminal(state A2ATaskState) bool {
	switch state {
	case A2ATaskCompleted, A2ATaskFailed, A2ATaskCanceled, A2ATaskRejected:
		return true
	default:
		return false
	}
}

func validA2ATaskState(state A2ATaskState) bool {
	switch state {
	case A2ATaskSubmitted, A2ATaskWorking, A2ATaskInputRequired, A2ATaskCompleted, A2ATaskFailed, A2ATaskCanceled, A2ATaskRejected:
		return true
	default:
		return false
	}
}

func validA2ATaskTransition(from, to A2ATaskState) bool {
	if from == to {
		return true
	}
	if a2aTaskTerminal(from) {
		return false
	}
	switch from {
	case A2ATaskSubmitted:
		return to == A2ATaskWorking || to == A2ATaskInputRequired || a2aTaskTerminal(to)
	case A2ATaskWorking:
		return to == A2ATaskInputRequired || a2aTaskTerminal(to)
	case A2ATaskInputRequired:
		return to == A2ATaskWorking || a2aTaskTerminal(to)
	default:
		return false
	}
}

func (s *Store) SetA2ATaskState(ctx context.Context, principal A2APrincipal, taskID string, state A2ATaskState) (A2ATaskCorrelation, error) {
	tx, err := s.beginTx(ctx, principal.ScopeID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentA2APrincipal(ctx, tx, principal); err != nil {
		return A2ATaskCorrelation{}, err
	}
	task, err := loadA2ATask(ctx, tx, principal.ID, taskID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	if !validA2ATaskTransition(task.State, state) {
		return A2ATaskCorrelation{}, Errorf(CodeConflict, "A2A task state transition is invalid")
	}
	if task.State != state {
		now := nowMillis()
		if _, err := tx.ExecContext(ctx, `UPDATE a2a_tasks SET state=?,updated_at=? WHERE principal_id=? AND task_id=?`, state, now, principal.ID, taskID); err != nil {
			return A2ATaskCorrelation{}, err
		}
		if err := appendEvent(ctx, tx, principal.ScopeID, "a2a.task_state_changed", taskID, eventAttributes("publicationId", principal.PublicationID, "state", string(state)), now); err != nil {
			return A2ATaskCorrelation{}, err
		}
	}
	task, err = loadA2ATask(ctx, tx, principal.ID, taskID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := tx.Commit(); err != nil {
		return A2ATaskCorrelation{}, err
	}
	return task, nil
}

func (r *Runtime) AcceptA2AMessage(ctx context.Context, credential, publicationID string, input AcceptA2AMessageInput) (A2ATaskCorrelation, error) {
	principal, err := r.AuthenticateA2APrincipal(ctx, credential, publicationID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := validateIdentity(input.TaskID, "taskId", true); err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := validateIdentity(input.ContextID, "contextId", true); err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := validateIdentity(input.ClientMessageID, "clientMessageId", false); err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := validateText(input.Body, "body", 65536, false); err != nil {
		return A2ATaskCorrelation{}, err
	}
	task, err := r.store.AcceptA2AMessage(ctx, principal, r.a2aPrincipalLimits, input)
	if err == nil {
		r.signals.notify(signalKey{scopeID: principal.ScopeID, consumerID: task.TargetAgentID})
		r.notifyScope(principal.ScopeID)
	}
	return task, err
}

func (r *Runtime) A2ATask(ctx context.Context, credential, publicationID, taskID string) (A2ATaskCorrelation, error) {
	principal, err := r.AuthenticateA2APrincipal(ctx, credential, publicationID)
	if err != nil {
		return A2ATaskCorrelation{}, err
	}
	if err := validateIdentity(taskID, "taskId", false); err != nil {
		return A2ATaskCorrelation{}, err
	}
	return r.store.A2ATask(ctx, principal, taskID)
}
