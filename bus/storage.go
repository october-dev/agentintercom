package bus

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

func (s *Store) StorageSummary(ctx context.Context, scopeID string) (StorageSummary, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return StorageSummary{}, err
	}
	defer tx.Rollback()
	now := nowMillis()
	if err := expireMessages(ctx, tx, scopeID, now); err != nil {
		return StorageSummary{}, err
	}
	if err := releaseStaleTaskClaims(ctx, tx, scopeID); err != nil {
		return StorageSummary{}, err
	}
	summary := StorageSummary{ScopeID: scopeID, GeneratedAt: instant(now), Records: []StorageRecordSummary{}}
	queries := []struct {
		recordType string
		statement  string
	}{
		{"attachment", `SELECT 'stored',COUNT(*),COALESCE(SUM(size_bytes),0),MIN(created_at) FROM attachments WHERE scope_id=? AND deleted_at=0 HAVING COUNT(*)>0`},
		{"channelPost", `SELECT 'posted',COUNT(*),COALESCE(SUM(octet_length(body)+octet_length(search_body)),0),MIN(created_at) FROM channel_posts WHERE scope_id=? HAVING COUNT(*)>0`},
		{"channel", `SELECT 'active',COUNT(*),COALESCE(SUM(octet_length(description)),0),MIN(created_at) FROM channels WHERE scope_id=? HAVING COUNT(*)>0`},
		{"message", `SELECT state,COUNT(*),COALESCE(SUM(octet_length(body)+octet_length(context_json)),0),
MIN(CASE state WHEN 'queued' THEN created_at WHEN 'reserved' THEN created_at WHEN 'delivered' THEN delivered_at WHEN 'acknowledged' THEN acknowledged_at WHEN 'expired' THEN expires_at END)
FROM messages WHERE scope_id=? GROUP BY state ORDER BY state`},
		{"task", `SELECT status,COUNT(*),COALESCE(SUM(octet_length(title)+octet_length(description)+octet_length(dependencies_json)+COALESCE(octet_length(note),0)),0),
MIN(CASE status WHEN 'open' THEN created_at ELSE updated_at END)
FROM tasks WHERE scope_id=? GROUP BY status ORDER BY status`},
		{"taskProgress", `SELECT kind,COUNT(*),COALESCE(SUM(octet_length(text)),0),MIN(created_at)
FROM task_progress WHERE scope_id=? GROUP BY kind ORDER BY kind`},
		{"escalation", `SELECT status,COUNT(*),COALESCE(SUM(octet_length(question)+octet_length(options_json)+COALESCE(octet_length(answer),0)),0),
MIN(CASE status WHEN 'pending' THEN created_at ELSE resolved_at END)
FROM escalations WHERE scope_id=? GROUP BY status ORDER BY status`},
		{"event", `SELECT event_type,COUNT(*),COALESCE(SUM(octet_length(event_type)+octet_length(subject_id)+octet_length(attributes_json)),0),MIN(created_at)
FROM events WHERE scope_id=? GROUP BY event_type ORDER BY event_type`},
		{"a2aPublication", `SELECT CASE enabled WHEN 1 THEN 'enabled' ELSE 'disabled' END,COUNT(*),0,MIN(created_at)
FROM a2a_publications WHERE scope_id=? GROUP BY enabled ORDER BY enabled DESC`},
		{"a2aTask", `SELECT state,COUNT(*),COALESCE(SUM(octet_length(context_id)),0),MIN(created_at)
FROM a2a_tasks WHERE scope_id=? GROUP BY state ORDER BY state`},
		{"a2aMessage", `SELECT 'correlated',COUNT(*),COALESCE(SUM(octet_length(client_message_id)),0),MIN(records.created_at)
FROM a2a_message_correlations AS records JOIN a2a_tasks AS tasks ON tasks.task_id=records.task_id WHERE tasks.scope_id=? HAVING COUNT(*)>0`},
		{"credential", `SELECT CASE enabled WHEN 1 THEN 'enabled' ELSE 'disabled' END,COUNT(*),COALESCE(SUM(octet_length(label)),0),MIN(created_at)
FROM scoped_credentials WHERE scope_id=? GROUP BY enabled ORDER BY enabled DESC`},
		{"outputStream", `SELECT 'active',COUNT(*),COALESCE(SUM(octet_length(name)),0),MIN(created_at)
FROM output_streams WHERE scope_id=?`},
		{"outputValue", `SELECT records.content_type,COUNT(*),COALESCE(SUM(octet_length(records.value_json)+COALESCE(octet_length(records.reference_json),0)),0),MIN(records.created_at)
FROM output_values AS records JOIN output_streams AS streams ON streams.stream_id=records.stream_id
WHERE streams.scope_id=? GROUP BY records.content_type ORDER BY records.content_type`},
	}
	for _, query := range queries {
		rows, err := tx.QueryContext(ctx, query.statement, scopeID)
		if err != nil {
			return StorageSummary{}, err
		}
		for rows.Next() {
			var record StorageRecordSummary
			var oldest sql.NullInt64
			if err := rows.Scan(&record.State, &record.Count, &record.EstimatedBytes, &oldest); err != nil {
				rows.Close()
				return StorageSummary{}, err
			}
			record.RecordType = query.recordType
			record.OldestAt = instant(oldest.Int64)
			summary.TotalEstimatedBytes += record.EstimatedBytes
			summary.Records = append(summary.Records, record)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return StorageSummary{}, err
		}
		rows.Close()
	}
	if err := tx.Commit(); err != nil {
		return StorageSummary{}, err
	}
	return summary, nil
}

type retentionMessage struct {
	id                string
	mode              MessageMode
	state             DeliveryState
	responseTo        string
	responseMessageID string
	deliveredAt       sql.NullInt64
	expiresAt         sql.NullInt64
	acknowledgedAt    sql.NullInt64
}

func (message retentionMessage) terminalBefore(before int64) bool {
	switch message.state {
	case DeliveryAcknowledged:
		return message.acknowledgedAt.Valid && message.acknowledgedAt.Int64 < before
	case DeliveryExpired:
		return message.expiresAt.Valid && message.expiresAt.Int64 < before
	default:
		return false
	}
}

func retentionMessageCandidates(ctx context.Context, tx sqlExecutor, scopeID string, before int64) (map[string]retentionMessage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT message_id,mode,state,response_to,response_message_id,delivered_at,expires_at,acknowledged_at FROM messages WHERE scope_id=?`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]retentionMessage{}
	for rows.Next() {
		var message retentionMessage
		var responseTo, responseMessageID sql.NullString
		if err := rows.Scan(&message.id, &message.mode, &message.state, &responseTo, &responseMessageID, &message.deliveredAt, &message.expiresAt, &message.acknowledgedAt); err != nil {
			return nil, err
		}
		message.responseTo = responseTo.String
		message.responseMessageID = responseMessageID.String
		all[message.id] = message
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	candidates := map[string]retentionMessage{}
	for _, message := range all {
		if !message.terminalBefore(before) || message.mode == MessageResponse {
			continue
		}
		switch message.mode {
		case MessageNotify:
			candidates[message.id] = message
		case MessageRequest:
			if message.responseMessageID == "" {
				if message.state == DeliveryExpired && !message.deliveredAt.Valid {
					candidates[message.id] = message
				}
				continue
			}
			response, ok := all[message.responseMessageID]
			if ok && response.mode == MessageResponse && response.responseTo == message.id && response.terminalBefore(before) {
				candidates[message.id] = message
				candidates[response.id] = response
			}
		}
	}
	return candidates, nil
}

func retentionTaskCandidates(ctx context.Context, tx sqlExecutor, scopeID string, before int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id,status,dependencies_json,updated_at FROM tasks WHERE scope_id=?`, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type taskRecord struct {
		id           string
		status       string
		dependencies []string
		updatedAt    int64
	}
	tasks := []taskRecord{}
	retained := map[string]bool{}
	dependenciesByID := map[string][]string{}
	queue := []string{}
	for rows.Next() {
		var task taskRecord
		var dependenciesJSON string
		if err := rows.Scan(&task.id, &task.status, &dependenciesJSON, &task.updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dependenciesJSON), &task.dependencies); err != nil {
			return nil, err
		}
		if task.status != "done" || task.updatedAt >= before {
			retained[task.id] = true
			queue = append(queue, task.id)
		}
		dependenciesByID[task.id] = task.dependencies
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	candidates := []string{}
	// Walk each retained dependency once, including history. Re-scanning the
	// full task board per dependency depth is quadratic for long chains.
	for i := 0; i < len(queue); i++ {
		for _, dependency := range dependenciesByID[queue[i]] {
			if !retained[dependency] {
				retained[dependency] = true
				queue = append(queue, dependency)
			}
		}
	}
	for _, task := range tasks {
		if !retained[task.id] {
			candidates = append(candidates, task.id)
		}
	}
	return candidates, nil
}

func retentionEscalationCandidates(ctx context.Context, tx sqlExecutor, scopeID string, before int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT escalation_id FROM escalations WHERE scope_id=? AND status='resolved' AND resolved_at<?`, scopeID, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) PruneScope(ctx context.Context, scopeID string, before int64, execute bool) (PruneScopeResult, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return PruneScopeResult{}, err
	}
	defer tx.Rollback()
	now := nowMillis()
	if err := expireMessages(ctx, tx, scopeID, now); err != nil {
		return PruneScopeResult{}, err
	}
	if err := releaseStaleTaskClaims(ctx, tx, scopeID); err != nil {
		return PruneScopeResult{}, err
	}
	messages, err := retentionMessageCandidates(ctx, tx, scopeID, before)
	if err != nil {
		return PruneScopeResult{}, err
	}
	a2aTasks, a2aMessages, err := retentionA2ACandidates(ctx, tx, scopeID, before, messages)
	if err != nil {
		return PruneScopeResult{}, err
	}
	tasks, err := retentionTaskCandidates(ctx, tx, scopeID, before)
	if err != nil {
		return PruneScopeResult{}, err
	}
	escalations, err := retentionEscalationCandidates(ctx, tx, scopeID, before)
	if err != nil {
		return PruneScopeResult{}, err
	}
	var currentRevision, currentFloor int64
	if err := tx.QueryRowContext(ctx, `SELECT event_revision,event_floor_revision FROM scopes WHERE scope_id=?`, scopeID).Scan(&currentRevision, &currentFloor); err != nil {
		return PruneScopeResult{}, err
	}
	var firstRetained sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(revision) FROM events WHERE scope_id=? AND created_at>=?`, scopeID, before).Scan(&firstRetained); err != nil {
		return PruneScopeResult{}, err
	}
	eventFloor := currentRevision
	if firstRetained.Valid {
		eventFloor = firstRetained.Int64 - 1
	}
	if eventFloor < currentFloor {
		eventFloor = currentFloor
	}
	var eventCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE scope_id=? AND revision<=? AND event_type!='scope.imported'`, scopeID, eventFloor).Scan(&eventCount); err != nil {
		return PruneScopeResult{}, err
	}
	var taskProgressCount int64
	for first := 0; first < len(tasks); first += 500 {
		ids := tasks[first:min(first+500, len(tasks))]
		args := []any{scopeID}
		for _, id := range ids {
			args = append(args, id)
		}
		var count int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM task_progress WHERE scope_id=? AND task_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", args...).Scan(&count); err != nil {
			return PruneScopeResult{}, err
		}
		taskProgressCount += count
	}
	channelRoots, channelCount, err := channelRetentionCandidates(ctx, tx, scopeID, before)
	if err != nil {
		return PruneScopeResult{}, err
	}
	result := PruneScopeResult{
		ScopeID: scopeID, Before: time.UnixMilli(before).UTC().Format(time.RFC3339Nano), DryRun: !execute,
		Records: RetentionCounts{ChannelPosts: channelCount,
			A2ATasks: int64(len(a2aTasks)), A2AMessages: a2aMessages,
			Messages: int64(len(messages)), Tasks: int64(len(tasks)), TaskProgress: taskProgressCount, Escalations: int64(len(escalations)), Events: eventCount,
		},
	}
	if execute {
		// Batch deletes keep a large retention pass from needing one database round
		// trip per record. References are removed before their parents.
		var replies, requests []string
		for _, message := range messages {
			if message.mode == MessageResponse {
				replies = append(replies, message.id)
			} else {
				requests = append(requests, message.id)
			}
		}
		batches := []struct {
			table, column string
			ids           []string
		}{
			{"channel_posts", "thread_id", channelRoots}, {"channel_posts", "post_id", channelRoots},
			{"a2a_tasks", "task_id", a2aTasks}, {"messages", "message_id", replies}, {"messages", "message_id", requests},
			{"tasks", "task_id", tasks}, {"escalations", "escalation_id", escalations},
		}
		for _, batch := range batches {
			if err := deleteRetentionRows(ctx, tx, scopeID, batch.table, batch.column, batch.ids); err != nil {
				return PruneScopeResult{}, err
			}
		}
		if eventCount > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE scope_id=? AND revision<=? AND event_type!='scope.imported'`, scopeID, eventFloor); err != nil {
				return PruneScopeResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE scopes SET event_floor_revision=CASE WHEN event_floor_revision>? THEN event_floor_revision ELSE ? END WHERE scope_id=?`, eventFloor, eventFloor, scopeID); err != nil {
				return PruneScopeResult{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return PruneScopeResult{}, err
	}
	return result, nil
}

// Table and column names are static call-site constants, never request input.
func deleteRetentionRows(ctx context.Context, tx sqlExecutor, scopeID, table, column string, ids []string) error {
	for first := 0; first < len(ids); first += 500 {
		batch := ids[first:min(first+500, len(ids))]
		args := []any{scopeID}
		for _, id := range batch {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE scope_id=? AND "+column+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+")", args...); err != nil {
			return err
		}
	}
	return nil
}
