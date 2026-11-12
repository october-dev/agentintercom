package bus

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type MessagePage struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

type MessageHistoryInput struct {
	Before  string `json:"before,omitempty"`
	Query   string `json:"query,omitempty"`
	AgentID string `json:"agentId,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func (a *HostedAccounts) SendHumanMessage(ctx context.Context, user HostedUser, scopeID string, input SendMessageInput) (DeliveryReceipt, error) {
	if err := validateSendMessageInput(&input); err != nil {
		return DeliveryReceipt{}, err
	}
	ctx = withHostedMember(ctx, user)
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	defer tx.Rollback()
	message, err := sendMessageTx(ctx, tx, scopeID, MessageParticipantHuman, user.ID, input)
	if err != nil {
		return DeliveryReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeliveryReceipt{}, err
	}
	a.runtime.signals.notify(signalKey{scopeID: scopeID, consumerID: input.To})
	a.runtime.notifyScope(scopeID)
	return receiptFromMessage(message), nil
}

func (a *HostedAccounts) Messages(ctx context.Context, user HostedUser, scopeID string, input MessageHistoryInput) (MessagePage, error) {
	if err := validateIdentity(input.Before, "before", true); err != nil {
		return MessagePage{}, err
	}
	if err := validateIdentity(input.AgentID, "agentId", true); err != nil {
		return MessagePage{}, err
	}
	if err := validateText(input.Query, "query", 256, true); err != nil {
		return MessagePage{}, err
	}
	if input.Limit == 0 {
		input.Limit = 50
	}
	if input.Limit < 1 || input.Limit > 100 {
		return MessagePage{}, Errorf(CodeInvalidArgument, "limit must be between 1 and 100")
	}
	tx, err := a.store.beginTx(withHostedMember(ctx, user), scopeID)
	if err != nil {
		return MessagePage{}, err
	}
	defer tx.Rollback()
	var before int64
	if input.Before != "" {
		if err := tx.QueryRowContext(ctx, "SELECT created_at FROM messages WHERE scope_id=? AND message_id=?", scopeID, input.Before).Scan(&before); errors.Is(err, sql.ErrNoRows) {
			return MessagePage{}, Errorf(CodeNotFound, "History cursor is unavailable")
		} else if err != nil {
			return MessagePage{}, err
		}
	}
	if err := expireMessages(ctx, tx, scopeID, nowMillis()); err != nil {
		return MessagePage{}, err
	}
	query := "SELECT " + messageColumns + " FROM messages WHERE scope_id=?"
	args := []any{scopeID}
	if input.Before != "" {
		query += " AND (created_at<? OR (created_at=? AND message_id<?))"
		args = append(args, before, before, input.Before)
	}
	if input.AgentID != "" {
		query += " AND ((from_kind='agent' AND from_id=?) OR (to_kind='agent' AND to_id=?))"
		args = append(args, input.AgentID, input.AgentID)
	}
	if input.Query != "" {
		query += " AND lower(body) LIKE lower(?) ESCAPE '!'"
		args = append(args, "%"+strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(input.Query)+"%")
	}
	query += " ORDER BY created_at DESC,message_id DESC LIMIT ?"
	args = append(args, input.Limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return MessagePage{}, err
	}
	result := MessagePage{Messages: []Message{}}
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			rows.Close()
			return MessagePage{}, err
		}
		result.Messages = append(result.Messages, message)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return MessagePage{}, err
	}
	if len(result.Messages) > input.Limit {
		result.Messages = result.Messages[:input.Limit]
		result.NextCursor = result.Messages[len(result.Messages)-1].ID
	}
	return result, tx.Commit()
}

// Reading shared history does not claim model delivery. A human may mark only
// messages addressed to their own verified identity as read.
func (a *HostedAccounts) ReadHumanMessages(ctx context.Context, user HostedUser, scopeID string, ids []string) (int64, error) {
	if len(ids) > 100 {
		return 0, Errorf(CodeInvalidArgument, "messageIds exceeds 100 items")
	}
	for _, id := range ids {
		if err := validateIdentity(id, "messageId", false); err != nil {
			return 0, err
		}
	}
	ctx = withHostedMember(ctx, user)
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := nowMillis()
	if err := expireMessages(ctx, tx, scopeID, now); err != nil {
		return 0, err
	}
	var count int64
	for _, id := range unique(ids) {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET state='acknowledged',delivered_at=COALESCE(delivered_at,?),acknowledged_at=? WHERE scope_id=? AND message_id=? AND to_kind='human' AND to_id=? AND state IN ('queued','delivered')`, now, now, scopeID, id, user.ID)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		count += changed
		if changed != 0 {
			if err := appendEvent(ctx, tx, scopeID, "message.acknowledged", id, eventAttributes("to", user.ID, "toKind", "human", "state", "acknowledged"), now); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if count > 0 {
		a.runtime.notifyScope(scopeID)
	}
	return count, nil
}

func (a *HostedAccounts) CreateHumanTask(ctx context.Context, user HostedUser, scopeID string, input AddTaskInput) (Task, error) {
	if err := validateAddTaskInput(input); err != nil {
		return Task{}, err
	}
	result, err := a.store.AddTask(withHostedMember(ctx, user), scopeID, "", input)
	if err == nil {
		a.runtime.notifyScope(scopeID)
	}
	return result, err
}

func (a *HostedAccounts) Tasks(ctx context.Context, user HostedUser, scopeID, after string) (TaskPage, error) {
	if err := validateIdentity(after, "after", true); err != nil {
		return TaskPage{}, err
	}
	return a.store.TaskPage(withHostedMember(ctx, user), scopeID, after, 100)
}

type EscalationPage struct {
	Escalations []HumanEscalation `json:"escalations"`
	NextCursor  string            `json:"nextCursor,omitempty"`
}

func (a *HostedAccounts) Questions(ctx context.Context, user HostedUser, scopeID, after string) (EscalationPage, error) {
	if err := validateIdentity(after, "after", true); err != nil {
		return EscalationPage{}, err
	}
	tx, err := a.store.beginTx(withHostedMember(ctx, user), scopeID)
	if err != nil {
		return EscalationPage{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+escalationColumns+" FROM escalations WHERE scope_id=? AND status='pending' AND escalation_id>? ORDER BY escalation_id LIMIT 101", scopeID, after)
	if err != nil {
		return EscalationPage{}, err
	}
	result := EscalationPage{Escalations: []HumanEscalation{}}
	for rows.Next() {
		value, err := scanEscalation(rows)
		if err != nil {
			rows.Close()
			return EscalationPage{}, err
		}
		result.Escalations = append(result.Escalations, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return EscalationPage{}, err
	}
	if len(result.Escalations) > 100 {
		result.Escalations = result.Escalations[:100]
		result.NextCursor = result.Escalations[99].ID
	}
	return result, tx.Commit()
}

func (a *HostedAccounts) AnswerQuestion(ctx context.Context, user HostedUser, scopeID, id, answer string) (HumanEscalation, error) {
	if err := validateIdentity(id, "escalationId", false); err != nil {
		return HumanEscalation{}, err
	}
	if err := validateText(answer, "answer", 16384, false); err != nil {
		return HumanEscalation{}, err
	}
	result, err := a.store.ResolveEscalation(withHostedMember(ctx, user), scopeID, id, answer)
	if err == nil {
		a.runtime.signals.notify(signalKey{scopeID: scopeID, consumerID: result.AgentID})
		a.runtime.notifyScope(scopeID)
	}
	return result, err
}

func (h *HostedServer) registerWorkRoutes() {
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/messages", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		q := r.URL.Query()
		limit := 0
		var err error
		if q.Get("limit") != "" {
			limit, err = strconv.Atoi(q.Get("limit"))
			if err != nil {
				return nil, Errorf(CodeInvalidArgument, "Invalid history limit")
			}
		}
		return h.accounts.Messages(ctx, u, r.PathValue("workspace"), MessageHistoryInput{Before: q.Get("before"), Query: q.Get("query"), AgentID: q.Get("agentId"), Limit: limit})
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/messages", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input SendMessageInput
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		return h.accounts.SendHumanMessage(ctx, u, r.PathValue("workspace"), input)
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/messages/read", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			IDs []string `json:"messageIds"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		count, err := h.accounts.ReadHumanMessages(ctx, u, r.PathValue("workspace"), input.IDs)
		return map[string]int64{"read": count}, err
	}))
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/tasks", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return h.accounts.Tasks(ctx, u, r.PathValue("workspace"), r.URL.Query().Get("after"))
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/tasks", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input AddTaskInput
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		return h.accounts.CreateHumanTask(ctx, u, r.PathValue("workspace"), input)
	}))
	h.routes.HandleFunc("GET /api/workspaces/{workspace}/questions", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		return h.accounts.Questions(ctx, u, r.PathValue("workspace"), r.URL.Query().Get("after"))
	}))
	h.routes.HandleFunc("POST /api/workspaces/{workspace}/questions/{question}/answer", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			Answer string `json:"answer"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		return h.accounts.AnswerQuestion(ctx, u, r.PathValue("workspace"), r.PathValue("question"), input.Answer)
	}))
}
