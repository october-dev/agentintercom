package bus

import (
	"context"
	"net/http"
)

// InboxNotice is an observation, never a delivery or acknowledgment. Content is
// bounded for terminal monitors and retains the sender's untrusted provenance.
type InboxNotice struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Preview string `json:"preview"`
}

func (s *Store) PreviewInbox(ctx context.Context, p Principal) ([]InboxNotice, error) {
	tx, err := s.beginTx(ctx, p.ScopeID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = requireCurrentExecution(ctx, tx, p, nowMillis()); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT message_id,from_id,substr(body,1,160) FROM messages WHERE scope_id=? AND to_kind='agent' AND to_id=? AND state IN ('queued','delivered') AND (expires_at IS NULL OR expires_at>?) ORDER BY created_at DESC,message_id DESC LIMIT 100`, p.ScopeID, p.AgentID, nowMillis())
	if err != nil {
		return nil, err
	}
	result := []InboxNotice{}
	for rows.Next() {
		var n InboxNotice
		if err = rows.Scan(&n.ID, &n.From, &n.Preview); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func (r *Runtime) PreviewInbox(ctx context.Context, token string) ([]InboxNotice, error) {
	p, err := r.Principal(ctx, token)
	if err != nil {
		return nil, err
	}
	return r.store.PreviewInbox(ctx, p)
}

func (s *Server) previewInbox(w http.ResponseWriter, r *http.Request) error {
	token, err := bearer(r)
	if err != nil {
		return err
	}
	result, err := s.runtime.PreviewInbox(r.Context(), token)
	if err != nil {
		return err
	}
	writeResult(w, http.StatusOK, result)
	return nil
}

func (c Client) PreviewInbox(ctx context.Context) ([]InboxNotice, error) {
	return request[[]InboxNotice](ctx, c, http.MethodGet, "/v1/inbox/preview", nil)
}
