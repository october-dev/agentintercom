package bus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	defaultEventLimit    = 50
	maxEventLimit        = 100
	maxScopeEventWaiters = 128
)

func eventAttributes(values ...string) map[string]string {
	attributes := make(map[string]string, len(values)/2)
	for index := 0; index+1 < len(values); index += 2 {
		if values[index+1] != "" {
			attributes[values[index]] = values[index+1]
		}
	}
	return attributes
}

func appendEvent(ctx context.Context, tx sqlExecutor, scopeID, eventType, subjectID string, attributes map[string]string, createdAt int64) error {
	return appendEvents(ctx, tx, scopeID, []pendingEvent{{eventType, subjectID, attributes}}, createdAt)
}

type pendingEvent struct {
	eventType, subjectID string
	attributes           map[string]string
}

// Reserve contiguous revisions and insert a transaction's events together.
// In particular, joining a workspace must not make a database round trip for
// each peer's revision and link event.
func appendEvents(ctx context.Context, tx sqlExecutor, scopeID string, events []pendingEvent, createdAt int64) error {
	// Retiring a connection can include historical sessions as well as the live
	// ones. Keep parameter counts bounded on both supported databases.
	for len(events) > 128 {
		if err := appendEvents(ctx, tx, scopeID, events[:128], createdAt); err != nil {
			return err
		}
		events = events[128:]
	}
	if len(events) == 0 {
		return nil
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `UPDATE scopes SET event_revision=event_revision+? WHERE scope_id=? RETURNING event_revision`, len(events), scopeID).Scan(&revision); err != nil {
		return err
	}
	args := make([]any, 0, len(events)*7)
	for i, event := range events {
		eventID, err := randomID("evt_")
		if err != nil {
			return err
		}
		if event.attributes == nil {
			event.attributes = map[string]string{}
		}
		if user, ok := ctx.Value(hostedMemberKey{}).(HostedUser); ok {
			event.attributes["actorKind"], event.attributes["actorId"] = "human", user.ID
		}
		data, err := json.Marshal(event.attributes)
		if err != nil {
			return err
		}
		args = append(args, eventID, scopeID, revision-int64(len(events))+int64(i)+1, event.eventType, event.subjectID, string(data), createdAt)
	}
	values := strings.TrimSuffix(strings.Repeat("(?,?,?,?,?,?,?),", len(events)), ",")
	_, err := tx.ExecContext(ctx, `INSERT INTO events(event_id,scope_id,revision,event_type,subject_id,attributes_json,created_at) VALUES`+values, args...)
	if err != nil {
		return err
	}
	// A bounded notification log is distinct from retained messages and tasks.
	// Clients already handle a cursor below the floor by resynchronizing.
	if isPostgresQuery(tx) && revision > 100000 && (revision-int64(len(events)))/100 < revision/100 {
		floor := revision/100*100 - 100000
		if _, err := tx.ExecContext(ctx, "DELETE FROM events WHERE scope_id=? AND revision<=? AND event_type!='scope.imported' AND EXISTS(SELECT 1 FROM hosted_workspaces WHERE scope_id=?)", scopeID, floor, scopeID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE scopes SET event_floor_revision=GREATEST(event_floor_revision,?) WHERE scope_id=? AND EXISTS(SELECT 1 FROM hosted_workspaces WHERE scope_id=?)", floor, scopeID, scopeID); err != nil {
			return err
		}
	}
	return nil
}

func scanEvent(row rowScanner) (BusEvent, error) {
	var event BusEvent
	var attributes string
	var createdAt int64
	if err := row.Scan(&event.ID, &event.ScopeID, &event.Revision, &event.Type, &event.SubjectID, &attributes, &createdAt); err != nil {
		return BusEvent{}, err
	}
	if err := json.Unmarshal([]byte(attributes), &event.Attributes); err != nil {
		return BusEvent{}, err
	}
	if event.Attributes == nil {
		event.Attributes = map[string]string{}
	}
	event.CreatedAt = instant(createdAt)
	return event, nil
}

func (s *Store) Events(ctx context.Context, scopeID string, after int64, limit int) (EventBatch, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return EventBatch{}, err
	}
	defer tx.Rollback()
	batch := EventBatch{ScopeID: scopeID, Events: []BusEvent{}, NextRevision: after}
	if err := tx.QueryRowContext(ctx, `SELECT event_revision,event_floor_revision FROM scopes WHERE scope_id=?`, scopeID).
		Scan(&batch.CurrentRevision, &batch.MinimumCursor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EventBatch{}, Errorf(CodeNotFound, "Scope "+scopeID+" was not found")
		}
		return EventBatch{}, err
	}
	if after > batch.CurrentRevision {
		return EventBatch{}, Errorf(CodeInvalidArgument, "after exceeds the current event revision")
	}
	if after < batch.MinimumCursor {
		batch.ResyncRequired = true
		batch.NextRevision = batch.CurrentRevision
		if err := tx.Commit(); err != nil {
			return EventBatch{}, err
		}
		return batch, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_id,scope_id,revision,event_type,subject_id,attributes_json,created_at FROM events WHERE scope_id=? AND revision>? ORDER BY revision LIMIT ?`, scopeID, after, limit)
	if err != nil {
		return EventBatch{}, err
	}
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			rows.Close()
			return EventBatch{}, err
		}
		batch.Events = append(batch.Events, event)
		batch.NextRevision = event.Revision
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return EventBatch{}, err
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return EventBatch{}, err
	}
	return batch, nil
}

func normalizedEventLimit(limit int) (int, error) {
	if limit == 0 {
		return defaultEventLimit, nil
	}
	if limit < 1 || limit > maxEventLimit {
		return 0, Errorf(CodeInvalidArgument, "limit must be between 1 and 100")
	}
	return limit, nil
}

func (r *Runtime) notifyScope(scopeID string) {
	r.signals.notify(signalKey{scopeID: scopeID})
}

func (r *Runtime) Events(ctx context.Context, scopeToken string, after int64, limit int, wait time.Duration) (EventBatch, error) {
	if after < 0 {
		return EventBatch{}, Errorf(CodeInvalidArgument, "after must not be negative")
	}
	limit, err := normalizedEventLimit(limit)
	if err != nil {
		return EventBatch{}, err
	}
	if wait < 0 {
		return EventBatch{}, Errorf(CodeInvalidArgument, "wait must be between 0 and 25 seconds")
	}
	waitMS := wait.Milliseconds()
	if wait > 0 && waitMS == 0 {
		waitMS = 1
	}
	if waitMS < 0 || waitMS > maxEventWaitMS {
		return EventBatch{}, Errorf(CodeInvalidArgument, "wait must be between 0 and 25 seconds")
	}
	scopeID, err := r.scopeAuthority(ctx, scopeToken)
	ctx = withScopeCredential(ctx, scopeToken)
	if err != nil {
		return EventBatch{}, err
	}
	if waitMS == 0 {
		return r.store.Events(ctx, scopeID, after, limit)
	}
	deadline := time.Now().Add(time.Duration(waitMS) * time.Millisecond)
	key := signalKey{scopeID: scopeID}
	for {
		signal, unsubscribe, subscribed := r.signals.subscribeLimited(key, maxScopeEventWaiters)
		if !subscribed {
			return EventBatch{}, Errorf(CodeBackpressure, "Scope event wait limit is full")
		}
		if _, err := r.scopeAuthority(ctx, scopeToken); err != nil {
			unsubscribe()
			return EventBatch{}, err
		}
		batch, err := r.store.Events(ctx, scopeID, after, limit)
		if err != nil || batch.ResyncRequired || len(batch.Events) > 0 || !time.Now().Before(deadline) {
			unsubscribe()
			return batch, err
		}
		wakeAt := deadline
		if r.store.Backend() == StorageBackendPostgres {
			wakeAt = minTime(wakeAt, time.Now().Add(time.Second))
		}
		timer := time.NewTimer(time.Until(wakeAt))
		select {
		case <-ctx.Done():
			stopTimer(timer)
			unsubscribe()
			return EventBatch{}, ctx.Err()
		case <-signal:
			stopTimer(timer)
		case <-timer.C:
		}
		unsubscribe()
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
