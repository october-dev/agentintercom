package bus

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func (a *HostedAccounts) PauseWorkspace(ctx context.Context, user HostedUser, scopeID string, paused bool) error {
	ctx = withHostedOwner(ctx, user)
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !paused {
		if err := workspaceOpen(ctx, tx, scopeID); err != nil {
			return err
		}
	}
	var current int64
	if err := tx.QueryRowContext(ctx, "SELECT paused_at FROM hosted_workspaces WHERE scope_id=?", scopeID).Scan(&current); err != nil {
		return err
	}
	if (current != 0) == paused {
		return tx.Commit()
	}
	value := int64(0)
	if paused {
		value = nowMillis()
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_workspaces SET paused_at=? WHERE scope_id=?", value, scopeID); err != nil {
		return err
	}
	if paused {
		if err := retireHostedSessions(ctx, tx, scopeID, "", ""); err != nil {
			return err
		}
	}
	kind := "workspace.resumed"
	if paused {
		kind = "workspace.paused"
	}
	if err := appendEvent(ctx, tx, scopeID, kind, scopeID, nil, nowMillis()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.runtime.signals.notifyAllScope(scopeID)
	return nil
}
func (a *HostedAccounts) PruneWorkspace(ctx context.Context, user HostedUser, scopeID, before string, execute bool) (PruneScopeResult, error) {
	cutoff, err := time.Parse(time.RFC3339Nano, before)
	if err != nil || cutoff.After(time.Now().Add(-24*time.Hour)) || cutoff.UnixMilli() <= 0 {
		return PruneScopeResult{}, Errorf(CodeInvalidArgument, "Retention cutoff must be a timestamp at least 24 hours ago")
	}
	return a.store.PruneScope(withHostedOwner(ctx, user), scopeID, cutoff.UnixMilli(), execute)
}
func (h *HostedServer) registerDataRoutes() {
	// Exports can contain 64 MiB. Bound aggregate memory independently of the
	// ordinary small account API requests and reject overload before allocating.
	exports := make(chan struct{}, 1)
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/pause", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Paused *bool `json:"paused"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		if input.Paused == nil {
			return nil, Errorf(CodeInvalidArgument, "paused is required")
		}
		return map[string]bool{"paused": *input.Paused}, h.accounts.PauseWorkspace(ctx, user, r.PathValue("workspace"), *input.Paused)
	}))
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/storage", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return h.accounts.store.StorageSummary(withHostedMember(ctx, user), r.PathValue("workspace"))
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/prune", h.userRoute(func(ctx context.Context, user HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Before  string `json:"before"`
			Execute bool   `json:"execute"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		return h.accounts.PruneWorkspace(bounded, user, r.PathValue("workspace"), input.Before, input.Execute)
	}))
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/export", func(w http.ResponseWriter, r *http.Request) {
		user, err := h.requestUser(r)
		if err != nil {
			writeFailure(w, err)
			return
		}
		select {
		case exports <- struct{}{}:
			defer func() { <-exports }()
		default:
			writeFailure(w, Errorf(CodeBackpressure, "Another export is running; retry shortly"))
			return
		}
		bounded, cancel := context.WithTimeout(withHostedOwner(r.Context(), user), 25*time.Second)
		defer cancel()
		archive, err := h.accounts.store.ExportScope(bounded, r.PathValue("workspace"))
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="october-bus-workspace.json"`)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		json.NewEncoder(w).Encode(archive)
	})
}
