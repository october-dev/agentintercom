package bus

import (
	"context"
	"net/http"
	"net/url"
)

type ScopeInfo struct {
	ScopeID   string `json:"scopeId"`
	CreatedAt string `json:"createdAt"`
}

func (s *Store) ListScopes(ctx context.Context) ([]ScopeInfo, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT scope_id,created_at FROM scopes ORDER BY scope_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ScopeInfo{}
	for rows.Next() {
		var value ScopeInfo
		var created int64
		if err := rows.Scan(&value.ScopeID, &created); err != nil {
			return nil, err
		}
		value.CreatedAt = instant(created)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) RotateScopeToken(ctx context.Context, scopeID string) (CreateScopeResult, error) {
	token, err := randomValue(32)
	if err != nil {
		return CreateScopeResult{}, err
	}
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return CreateScopeResult{}, err
	}
	defer tx.Rollback()
	now := nowMillis()
	result, err := tx.ExecContext(ctx, "UPDATE scopes SET token_hash=? WHERE scope_id=?", tokenDigest(token), scopeID)
	if err != nil {
		return CreateScopeResult{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return CreateScopeResult{}, err
	}
	if count != 1 {
		return CreateScopeResult{}, Errorf(CodeNotFound, "Scope was not found")
	}
	rows, err := tx.QueryContext(ctx, "SELECT agent_id FROM agents WHERE scope_id=?", scopeID)
	if err != nil {
		return CreateScopeResult{}, err
	}
	agents := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return CreateScopeResult{}, err
		}
		agents = append(agents, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return CreateScopeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE agents SET lifecycle='offline',ready=0,lease_expires_at=0,updated_at=? WHERE scope_id=?", now, scopeID); err != nil {
		return CreateScopeResult{}, err
	}
	for _, id := range agents {
		if err := releaseAgentReservations(ctx, tx, scopeID, id); err != nil {
			return CreateScopeResult{}, err
		}
	}
	if err := releaseStaleTaskClaims(ctx, tx, scopeID); err != nil {
		return CreateScopeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE scoped_credentials SET enabled=0,updated_at=? WHERE scope_id=?", now, scopeID); err != nil {
		return CreateScopeResult{}, err
	}
	if s.Backend() == StorageBackendPostgres {
		if _, err := tx.ExecContext(ctx, "UPDATE hosted_devices SET revoked_at=? WHERE scope_id=? AND revoked_at=0", now, scopeID); err != nil {
			return CreateScopeResult{}, err
		}
	}
	if err := appendEvent(ctx, tx, scopeID, "scope.credentials_rotated", scopeID, eventAttributes("executions", "retired", "scopedCredentials", "disabled"), now); err != nil {
		return CreateScopeResult{}, err
	}
	return CreateScopeResult{ScopeID: scopeID, ScopeToken: token}, tx.Commit()
}

func (s *Store) DeleteScope(ctx context.Context, scopeID string) (bool, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM a2a_tasks WHERE scope_id=?", scopeID); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM scopes WHERE scope_id=?", scopeID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return count != 0, tx.Commit()
}

func (r *Runtime) ListScopes(ctx context.Context) ([]ScopeInfo, error) {
	return r.store.ListScopes(ctx)
}

func (r *Runtime) RotateScopeToken(ctx context.Context, scopeID string) (CreateScopeResult, error) {
	if err := validateIdentity(scopeID, "scopeId", false); err != nil {
		return CreateScopeResult{}, err
	}
	result, err := r.store.RotateScopeToken(ctx, scopeID)
	if err == nil {
		r.signals.notifyAllScope(scopeID)
	}
	return result, err
}

func (r *Runtime) DeleteScope(ctx context.Context, scopeID string) (bool, error) {
	if err := validateIdentity(scopeID, "scopeId", false); err != nil {
		return false, err
	}
	deleted, err := r.store.DeleteScope(ctx, scopeID)
	if err == nil {
		r.signals.notifyAllScope(scopeID)
	}
	return deleted, err
}

func (s *Server) listScopes(response http.ResponseWriter, request *http.Request) error {
	if err := s.requireAdmin(request); err != nil {
		return err
	}
	result, err := s.runtime.ListScopes(request.Context())
	if err != nil {
		return err
	}
	writeResult(response, http.StatusOK, result)
	return nil
}

func (s *Server) rotateScopeToken(response http.ResponseWriter, request *http.Request) error {
	if err := s.requireAdmin(request); err != nil {
		return err
	}
	var input emptyInput
	if err := decodeBody(response, request, &input); err != nil {
		return err
	}
	result, err := s.runtime.RotateScopeToken(request.Context(), request.PathValue("scopeId"))
	if err != nil {
		return err
	}
	writeResult(response, http.StatusOK, result)
	return nil
}

func (s *Server) deleteScope(response http.ResponseWriter, request *http.Request) error {
	if err := s.requireAdmin(request); err != nil {
		return err
	}
	var input struct {
		ConfirmScopeID string `json:"confirmScopeId"`
	}
	if err := decodeBody(response, request, &input); err != nil {
		return err
	}
	if input.ConfirmScopeID != request.PathValue("scopeId") {
		return Errorf(CodeInvalidArgument, "confirmScopeId must match the scope being deleted")
	}
	deleted, err := s.runtime.DeleteScope(request.Context(), input.ConfirmScopeID)
	if err != nil {
		return err
	}
	writeResult(response, http.StatusOK, map[string]bool{"deleted": deleted})
	return nil
}

func (c Client) ListScopes(ctx context.Context) ([]ScopeInfo, error) {
	return request[[]ScopeInfo](ctx, c, http.MethodGet, "/v1/admin/scopes", nil)
}
func (c Client) RotateScopeToken(ctx context.Context, scopeID string) (CreateScopeResult, error) {
	return request[CreateScopeResult](ctx, c, http.MethodPost, "/v1/admin/scopes/"+url.PathEscape(scopeID)+"/rotate-token", emptyInput{})
}
func (c Client) DeleteScope(ctx context.Context, scopeID string) error {
	_, err := request[map[string]bool](ctx, c, http.MethodDelete, "/v1/admin/scopes/"+url.PathEscape(scopeID), map[string]string{"confirmScopeId": scopeID})
	return err
}
