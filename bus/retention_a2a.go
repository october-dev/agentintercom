package bus

import (
	"context"
	"database/sql"
)

// A remote task and its idempotency correlations are retained as a unit.
// A terminal task is eligible only when every correlated Bus message is eligible.
func retentionA2ACandidates(ctx context.Context, tx sqlExecutor, scopeID string, before int64, messages map[string]retentionMessage) ([]string, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.task_id,t.state,t.updated_at,c.bus_request_message_id,c.bus_response_message_id
FROM a2a_tasks t LEFT JOIN a2a_message_correlations c ON c.task_id=t.task_id
WHERE t.scope_id=? ORDER BY t.task_id,c.client_message_id`, scopeID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	type record struct {
		id           string
		eligible     bool
		messages     []string
		correlations int64
	}
	records := []*record{}
	for rows.Next() {
		var id string
		var state A2ATaskState
		var updated int64
		var request, response sql.NullString
		if err := rows.Scan(&id, &state, &updated, &request, &response); err != nil {
			return nil, 0, err
		}
		if len(records) == 0 || records[len(records)-1].id != id {
			records = append(records, &record{id: id, eligible: a2aTaskTerminal(state) && updated < before})
		}
		r := records[len(records)-1]
		if request.Valid {
			r.correlations++
		}
		for _, value := range []sql.NullString{request, response} {
			if !value.Valid {
				continue
			}
			r.messages = append(r.messages, value.String)
			if _, ok := messages[value.String]; !ok {
				r.eligible = false
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	ids := []string{}
	var count int64
	for _, r := range records {
		if r.eligible {
			ids = append(ids, r.id)
			count += r.correlations
		} else {
			for _, id := range r.messages {
				delete(messages, id)
			}
		}
	}
	return ids, count, nil
}
