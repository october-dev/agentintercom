package bus

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/october-dev/agentintercom/deploy/hosted/migrations"
)

// SQLite and PostgreSQL use exactly the same additive wake tables. BIGINT has
// integer affinity on SQLite. These tables contain no provider socket secrets.
var nativeWakeSchemaSQL = func() string {
	data, err := migrations.Files.ReadFile("006_native_wake.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}()

type WakeBatch struct {
	AttemptID  string   `json:"attemptId,omitempty"`
	MessageIDs []string `json:"messageIds"`
	retryAt    int64
}

type WakeStatus struct {
	Pending       int64  `json:"pending"`
	Provider      string `json:"provider,omitempty"`
	Connected     bool   `json:"connected"`
	LastSeenAt    string `json:"lastSeenAt,omitempty"`
	LastAttemptAt string `json:"lastAttemptAt,omitempty"`
	LastOutcome   string `json:"lastOutcome,omitempty"`
	Uncertain     int64  `json:"uncertain"`
}

func (s *Store) ClaimWake(ctx context.Context, p Principal, provider string) (WakeBatch, error) {
	tx, err := s.beginTx(ctx, p.ScopeID)
	if err != nil {
		return WakeBatch{}, err
	}
	defer tx.Rollback()
	now := nowMillis()
	if err := requireCurrentExecution(ctx, tx, p, now); err != nil {
		return WakeBatch{}, err
	}
	// A crashed dispatcher may have written to the provider before dying. Never
	// automatically repeat that ambiguous boundary, even after the process restarts.
	if _, err := tx.ExecContext(ctx, "UPDATE wake_attempts SET outcome='uncertain',updated_at=? WHERE scope_id=? AND agent_id=? AND execution_id=? AND outcome='dispatching' AND updated_at<?", now, p.ScopeID, p.AgentID, p.ExecutionID, now-30000); err != nil {
		return WakeBatch{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO wake_connections(scope_id,agent_id,execution_id,provider,last_seen_at) VALUES(?,?,?,?,?)
ON CONFLICT(scope_id,agent_id) DO UPDATE SET execution_id=excluded.execution_id,provider=excluded.provider,last_seen_at=excluded.last_seen_at,
last_attempt_at=CASE WHEN wake_connections.execution_id=excluded.execution_id THEN wake_connections.last_attempt_at ELSE 0 END,
last_outcome=CASE WHEN wake_connections.execution_id=excluded.execution_id THEN wake_connections.last_outcome ELSE 'waiting' END
WHERE wake_connections.execution_id<>excluded.execution_id OR wake_connections.provider<>excluded.provider OR wake_connections.last_seen_at<?`, p.ScopeID, p.AgentID, p.ExecutionID, provider, now, now-30000)
	if err != nil {
		return WakeBatch{}, err
	}
	var lastAttempt int64
	if err := tx.QueryRowContext(ctx, "SELECT last_attempt_at FROM wake_connections WHERE scope_id=? AND agent_id=? AND execution_id=?", p.ScopeID, p.AgentID, p.ExecutionID).Scan(&lastAttempt); err != nil {
		return WakeBatch{}, err
	}
	if lastAttempt > now-2000 {
		return WakeBatch{MessageIDs: []string{}, retryAt: lastAttempt + 2000}, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.message_id FROM messages m LEFT JOIN wake_attempts w ON w.scope_id=m.scope_id AND w.agent_id=m.to_id AND w.execution_id=? AND w.message_id=m.message_id
WHERE m.scope_id=? AND m.to_kind='agent' AND m.to_id=? AND m.state IN ('queued','delivered') AND (m.expires_at IS NULL OR m.expires_at>?)
AND (w.message_id IS NULL OR (w.outcome='unavailable' AND w.tries<5 AND w.updated_at<?)) ORDER BY m.created_at,m.message_id LIMIT 20`, p.ExecutionID, p.ScopeID, p.AgentID, now, now-30000)
	if err != nil {
		return WakeBatch{}, err
	}
	result := WakeBatch{MessageIDs: []string{}}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return WakeBatch{}, err
		}
		result.MessageIDs = append(result.MessageIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return WakeBatch{}, err
	}
	if len(result.MessageIDs) > 0 {
		result.AttemptID, err = randomID("wake_")
		if err != nil {
			return WakeBatch{}, err
		}
		for _, id := range result.MessageIDs {
			_, err = tx.ExecContext(ctx, `INSERT INTO wake_attempts(scope_id,agent_id,execution_id,message_id,attempt_id,outcome,tries,updated_at) VALUES(?,?,?,?,?,'dispatching',1,?)
ON CONFLICT(scope_id,agent_id,execution_id,message_id) DO UPDATE SET attempt_id=excluded.attempt_id,outcome='dispatching',tries=wake_attempts.tries+1,updated_at=excluded.updated_at`, p.ScopeID, p.AgentID, p.ExecutionID, id, result.AttemptID, now)
			if err != nil {
				return WakeBatch{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE wake_connections SET last_attempt_at=?,last_outcome='dispatching' WHERE scope_id=? AND agent_id=? AND execution_id=?", now, p.ScopeID, p.AgentID, p.ExecutionID); err != nil {
			return WakeBatch{}, err
		}
	}
	return result, tx.Commit()
}

func (s *Store) FinishWake(ctx context.Context, p Principal, id, outcome string) error {
	tx, err := s.beginTx(ctx, p.ScopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, p, nowMillis()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE wake_attempts SET outcome=?,updated_at=? WHERE scope_id=? AND agent_id=? AND execution_id=? AND attempt_id=? AND outcome='dispatching'", outcome, nowMillis(), p.ScopeID, p.AgentID, p.ExecutionID, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var previous string
		err := tx.QueryRowContext(ctx, "SELECT outcome FROM wake_attempts WHERE scope_id=? AND agent_id=? AND execution_id=? AND attempt_id=? LIMIT 1", p.ScopeID, p.AgentID, p.ExecutionID, id).Scan(&previous)
		if errors.Is(err, sql.ErrNoRows) {
			return Errorf(CodeNotFound, "Wake attempt is unavailable")
		}
		if err != nil {
			return err
		}
		if previous != outcome {
			return Errorf(CodeConflict, "Wake outcome is already recorded; inspect the inbox before retrying")
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE wake_connections SET last_outcome=? WHERE scope_id=? AND agent_id=? AND execution_id=?", outcome, p.ScopeID, p.AgentID, p.ExecutionID); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, p.ScopeID, "agent.wake_submitted", id, eventAttributes("agentId", p.AgentID, "executionId", p.ExecutionID, "outcome", outcome), nowMillis()); err != nil {
		return err
	}
	return tx.Commit()
}

func wakeStatusFrom(ctx context.Context, q sqlExecutor, scopeID, agentID, executionID string) (WakeStatus, error) {
	var status WakeStatus
	var seen, attempt int64
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE scope_id=? AND to_kind='agent' AND to_id=? AND state IN ('queued','delivered') AND (expires_at IS NULL OR expires_at>?)", scopeID, agentID, nowMillis()).Scan(&status.Pending); err != nil {
		return status, err
	}
	err := q.QueryRowContext(ctx, "SELECT provider,last_seen_at,last_attempt_at,last_outcome FROM wake_connections WHERE scope_id=? AND agent_id=? AND execution_id=?", scopeID, agentID, executionID).Scan(&status.Provider, &seen, &attempt, &status.LastOutcome)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	status.Connected = seen > nowMillis()-90000
	status.LastSeenAt = instant(seen)
	status.LastAttemptAt = instant(attempt)
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM wake_attempts w JOIN messages m ON m.message_id=w.message_id WHERE w.scope_id=? AND w.agent_id=? AND w.execution_id=? AND (w.outcome='uncertain' OR (w.outcome='dispatching' AND w.updated_at<?)) AND m.state IN ('queued','delivered') AND (m.expires_at IS NULL OR m.expires_at>?)`, scopeID, agentID, executionID, nowMillis()-30000, nowMillis()).Scan(&status.Uncertain)
	if status.LastOutcome == "dispatching" && attempt < nowMillis()-30000 {
		status.LastOutcome = "uncertain"
	}
	return status, err
}

func (s *Store) WakeStatus(ctx context.Context, p Principal) (WakeStatus, error) {
	tx, err := s.beginTx(ctx, p.ScopeID)
	if err != nil {
		return WakeStatus{}, err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, p, nowMillis()); err != nil {
		return WakeStatus{}, err
	}
	result, err := wakeStatusFrom(ctx, tx, p.ScopeID, p.AgentID, p.ExecutionID)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) RetryWake(ctx context.Context, p Principal, ids []string) error {
	tx, err := s.beginTx(ctx, p.ScopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireCurrentExecution(ctx, tx, p, nowMillis()); err != nil {
		return err
	}
	for _, id := range unique(ids) {
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM wake_attempts WHERE scope_id=? AND agent_id=? AND execution_id=? AND message_id=? AND outcome='dispatching' AND updated_at>=?", p.ScopeID, p.AgentID, p.ExecutionID, id, nowMillis()-30000).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return Errorf(CodeConflict, "A wake submission is still in progress; wait before retrying")
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM wake_attempts WHERE scope_id=? AND agent_id=? AND execution_id=? AND message_id=? AND (outcome<>'dispatching' OR updated_at<?)", p.ScopeID, p.AgentID, p.ExecutionID, id, nowMillis()-30000); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Runtime) ClaimWake(ctx context.Context, token, provider string, wait time.Duration) (WakeBatch, error) {
	if provider != "claude-socket" && provider != "codex-app-server" {
		return WakeBatch{}, Errorf(CodeInvalidArgument, "Unknown native wake provider")
	}
	if wait < 0 || wait > 25*time.Second {
		return WakeBatch{}, Errorf(CodeInvalidArgument, "wait must be between 0 and 25 seconds")
	}
	p, err := r.Principal(ctx, token)
	if err != nil {
		return WakeBatch{}, err
	}
	release, ok := r.inboxWaiters.acquire(p)
	if !ok {
		return WakeBatch{}, Errorf(CodeBackpressure, "Too many delivery listeners")
	}
	defer release()
	deadline := time.Now().Add(wait)
	for {
		signal, unsubscribe := r.signals.subscribe(signalKey{scopeID: p.ScopeID, consumerID: p.AgentID})
		p, err = r.Principal(ctx, token)
		if err != nil {
			unsubscribe()
			return WakeBatch{}, err
		}
		result, err := r.store.ClaimWake(ctx, p, provider)
		if err != nil || len(result.MessageIDs) > 0 || !time.Now().Before(deadline) {
			unsubscribe()
			return result, err
		}
		wakeAt := deadline
		if result.retryAt > 0 {
			wakeAt = minTime(wakeAt, time.UnixMilli(result.retryAt))
		}
		if r.store.Backend() == StorageBackendPostgres {
			wakeAt = minTime(wakeAt, time.Now().Add(time.Second))
		}
		wakeAt = minTime(wakeAt, time.UnixMilli(p.LeaseExpiresAt))
		timer := time.NewTimer(time.Until(wakeAt))
		select {
		case <-ctx.Done():
			stopTimer(timer)
			unsubscribe()
			return WakeBatch{}, ctx.Err()
		case <-signal:
			stopTimer(timer)
		case <-timer.C:
		}
		unsubscribe()
	}
}
func (r *Runtime) FinishWake(ctx context.Context, token, id, outcome string) error {
	if err := validateIdentity(id, "attemptId", false); err != nil {
		return err
	}
	if outcome != "submitted" && outcome != "unavailable" && outcome != "uncertain" {
		return Errorf(CodeInvalidArgument, "Invalid wake outcome")
	}
	p, err := r.Principal(ctx, token)
	if err != nil {
		return err
	}
	err = r.store.FinishWake(ctx, p, id, outcome)
	if err == nil {
		r.notifyScope(p.ScopeID)
	}
	return err
}
func (r *Runtime) WakeStatus(ctx context.Context, token string) (WakeStatus, error) {
	p, err := r.Principal(ctx, token)
	if err != nil {
		return WakeStatus{}, err
	}
	return r.store.WakeStatus(ctx, p)
}
func (r *Runtime) RetryWake(ctx context.Context, token string, ids []string) error {
	if len(ids) == 0 || len(ids) > 100 {
		return Errorf(CodeInvalidArgument, "Select between 1 and 100 messages to retry")
	}
	for _, id := range ids {
		if err := validateIdentity(id, "messageId", false); err != nil {
			return err
		}
	}
	p, err := r.Principal(ctx, token)
	if err != nil {
		return err
	}
	err = r.store.RetryWake(ctx, p, ids)
	if err == nil {
		r.signals.notify(signalKey{scopeID: p.ScopeID, consumerID: p.AgentID})
	}
	return err
}

func (s *Server) claimWake(w http.ResponseWriter, r *http.Request) error {
	token, err := bearer(r)
	if err != nil {
		return err
	}
	var input struct {
		Provider string `json:"provider"`
		WaitMS   int64  `json:"waitMs"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	if input.WaitMS < 0 || input.WaitMS > 25000 {
		return Errorf(CodeInvalidArgument, "waitMs must be between 0 and 25000")
	}
	ctx, cancel := s.inboxWaitContext(r.Context())
	defer cancel()
	result, err := s.runtime.ClaimWake(ctx, token, input.Provider, time.Duration(input.WaitMS)*time.Millisecond)
	if err != nil {
		return err
	}
	writeResult(w, 200, result)
	return nil
}
func (s *Server) finishWake(w http.ResponseWriter, r *http.Request) error {
	token, err := bearer(r)
	if err != nil {
		return err
	}
	var input struct {
		Outcome string `json:"outcome"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	if err := s.runtime.FinishWake(r.Context(), token, r.PathValue("attemptId"), input.Outcome); err != nil {
		return err
	}
	writeResult(w, 200, map[string]bool{"recorded": true})
	return nil
}
func (s *Server) wakeStatus(w http.ResponseWriter, r *http.Request) error {
	token, err := bearer(r)
	if err != nil {
		return err
	}
	result, err := s.runtime.WakeStatus(r.Context(), token)
	if err != nil {
		return err
	}
	writeResult(w, 200, result)
	return nil
}
func (s *Server) retryWake(w http.ResponseWriter, r *http.Request) error {
	token, err := bearer(r)
	if err != nil {
		return err
	}
	var input struct {
		IDs []string `json:"messageIds"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	if err := s.runtime.RetryWake(r.Context(), token, input.IDs); err != nil {
		return err
	}
	writeResult(w, 200, map[string]bool{"retryScheduled": true})
	return nil
}
func (c Client) ClaimWake(ctx context.Context, provider string, wait time.Duration) (WakeBatch, error) {
	return request[WakeBatch](ctx, c, http.MethodPost, "/v1/me/wake/claim", map[string]any{"provider": provider, "waitMs": wait.Milliseconds()})
}
func (c Client) FinishWake(ctx context.Context, id, outcome string) error {
	_, err := request[map[string]bool](ctx, c, http.MethodPost, "/v1/me/wake/attempts/"+id, map[string]string{"outcome": outcome})
	return err
}
func (c Client) WakeStatus(ctx context.Context) (WakeStatus, error) {
	return request[WakeStatus](ctx, c, http.MethodGet, "/v1/me/wake", nil)
}
func (c Client) RetryWake(ctx context.Context, ids []string) error {
	_, err := request[map[string]bool](ctx, c, http.MethodPost, "/v1/me/wake/retry", map[string]any{"messageIds": ids})
	return err
}
