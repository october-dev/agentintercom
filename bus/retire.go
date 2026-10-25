package bus

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// RetireAgent accepts even an expired current token, but never a replaced one.
// Keeping its digest permits idempotent cleanup without granting renewable authority.
func (s *Store) RetireAgent(ctx context.Context, token string) (Principal, error) {
	current, err := s.CurrentAgentToken(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	tx, err := s.beginTx(ctx, current.ScopeID)
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback()
	var p Principal
	err = tx.QueryRowContext(ctx, `SELECT scope_id,agent_id,execution_id,lease_expires_at FROM agents WHERE token_hash=?`, tokenDigest(token)).
		Scan(&p.ScopeID, &p.AgentID, &p.ExecutionID, &p.LeaseExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, Errorf(CodeUnauthenticated, "Invalid agent token")
	}
	if err != nil {
		return Principal{}, err
	}
	if p.LeaseExpiresAt != 0 {
		now := nowMillis()
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET lifecycle='offline',ready=0,lease_expires_at=0,updated_at=? WHERE scope_id=? AND agent_id=?`, now, p.ScopeID, p.AgentID); err != nil {
			return Principal{}, err
		}
		if err := releaseAgentReservations(ctx, tx, p.ScopeID, p.AgentID); err != nil {
			return Principal{}, err
		}
		if err := releaseStaleTaskClaims(ctx, tx, p.ScopeID); err != nil {
			return Principal{}, err
		}
		if err := appendEvent(ctx, tx, p.ScopeID, "agent.lifecycle_changed", p.AgentID, eventAttributes("executionId", p.ExecutionID, "lifecycle", "offline", "ready", "false", "reason", "retired"), now); err != nil {
			return Principal{}, err
		}
	}
	return p, tx.Commit()
}

func releaseAgentReservations(ctx context.Context, tx sqlExecutor, scopeID, agentID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT reservation_id FROM reservations WHERE scope_id=? AND agent_id=?`, scopeID, agentID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := releaseReservation(ctx, tx, scopeID, agentID, id); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) RetireAgent(ctx context.Context, token string) error {
	p, err := r.store.RetireAgent(ctx, token)
	if err == nil {
		r.notifyScope(p.ScopeID)
		r.signals.notify(signalKey{scopeID: p.ScopeID, consumerID: p.AgentID})
	}
	return err
}

func (s *Server) retireAgent(response http.ResponseWriter, request *http.Request) error {
	token, err := bearer(request)
	if err != nil {
		return err
	}
	var input struct{}
	if err := decodeBody(response, request, &input); err != nil {
		return err
	}
	if err := s.runtime.RetireAgent(request.Context(), token); err != nil {
		return err
	}
	writeResult(response, http.StatusOK, map[string]bool{"retired": true})
	return nil
}

// Retire ends this execution's authority and releases its claims and reservations.
func (c Client) Retire(ctx context.Context) error {
	_, err := request[map[string]bool](ctx, c, http.MethodPost, "/v1/me/retire", struct{}{})
	return err
}
