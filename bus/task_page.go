package bus

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func (s *Store) TaskPage(ctx context.Context, scopeID, after string, limit int) (TaskPage, error) {
	tx, err := s.beginTx(ctx, scopeID)
	if err != nil {
		return TaskPage{}, err
	}
	defer tx.Rollback()
	if err := releaseStaleTaskClaims(ctx, tx, scopeID); err != nil {
		return TaskPage{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT task_id FROM tasks WHERE scope_id=? AND task_id>? ORDER BY task_id LIMIT ?", scopeID, after, limit+1)
	if err != nil {
		return TaskPage{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return TaskPage{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return TaskPage{}, err
	}
	page := TaskPage{Tasks: []Task{}}
	if len(ids) > limit {
		ids = ids[:limit]
		page.NextCursor = ids[len(ids)-1]
	}
	for _, id := range ids {
		task, err := taskFrom(ctx, tx, scopeID, id)
		if err != nil {
			return TaskPage{}, err
		}
		page.Tasks = append(page.Tasks, task)
	}
	return page, tx.Commit()
}

func (r *Runtime) TaskPage(ctx context.Context, token, after string, limit int) (TaskPage, error) {
	scopeID, p, err := r.taskAuthority(ctx, token)
	if err != nil {
		return TaskPage{}, err
	}
	if p == nil {
		ctx = withScopeCredential(ctx, token)
	} else {
		ctx = withExecution(ctx, *p)
	}
	if err := validateIdentity(after, "after", true); err != nil {
		return TaskPage{}, err
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return TaskPage{}, Errorf(CodeInvalidArgument, "limit must be between 1 and 500")
	}
	return r.store.TaskPage(ctx, scopeID, after, limit)
}

func (s *Server) taskPage(response http.ResponseWriter, request *http.Request) error {
	token, err := bearer(request)
	if err != nil {
		return err
	}
	limit := 0
	if raw := request.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			return Errorf(CodeInvalidArgument, "limit must be between 1 and 500")
		}
	}
	result, err := s.runtime.TaskPage(request.Context(), token, request.URL.Query().Get("after"), limit)
	if err != nil {
		return err
	}
	writeResult(response, http.StatusOK, result)
	return nil
}

func (c Client) TaskPage(ctx context.Context, after string, limit int) (TaskPage, error) {
	query := url.Values{"after": []string{after}, "limit": []string{strconv.Itoa(limit)}}
	if limit == 0 {
		query.Del("limit")
	}
	return request[TaskPage](ctx, c, http.MethodGet, "/v1/tasks/page?"+query.Encode(), nil)
}
