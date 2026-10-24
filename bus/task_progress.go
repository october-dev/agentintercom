package bus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	maxTaskProgressEvents = 1000
	maxTaskProgressBytes  = 1024 * 1024
	recentTaskProgress    = 20
	taskProgressColumns   = `task_id,sequence,agent_id,execution_id,kind,text,created_at`
)

type taskProgressQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func scanTaskProgress(row rowScanner) (TaskProgress, error) {
	var progress TaskProgress
	var createdAt int64
	err := row.Scan(&progress.TaskID, &progress.Sequence, &progress.AgentID, &progress.ExecutionID, &progress.Kind, &progress.Text, &createdAt)
	progress.CreatedAt = instant(createdAt)
	return progress, err
}

func queryTaskProgress(ctx context.Context, query taskProgressQuery, statement string, args ...any) ([]TaskProgress, error) {
	rows, err := query.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	progress := []TaskProgress{}
	for rows.Next() {
		value, err := scanTaskProgress(rows)
		if err != nil {
			return nil, err
		}
		progress = append(progress, value)
	}
	return progress, rows.Err()
}

func recentProgressForTask(ctx context.Context, query taskProgressQuery, scopeID, taskID string) ([]TaskProgress, error) {
	progress, err := queryTaskProgress(ctx, query, `SELECT `+taskProgressColumns+` FROM task_progress WHERE scope_id=? AND task_id=? ORDER BY sequence DESC LIMIT ?`, scopeID, taskID, recentTaskProgress)
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(progress)-1; left < right; left, right = left+1, right-1 {
		progress[left], progress[right] = progress[right], progress[left]
	}
	return progress, nil
}

func recentProgressForScope(ctx context.Context, query taskProgressQuery, scopeID string) (map[string][]TaskProgress, error) {
	progress, err := queryTaskProgress(ctx, query, `SELECT `+taskProgressColumns+` FROM task_progress AS progress
WHERE progress.scope_id=? AND progress.sequence>(
  SELECT COALESCE(MAX(candidate.sequence),0)-? FROM task_progress AS candidate
  WHERE candidate.scope_id=progress.scope_id AND candidate.task_id=progress.task_id
)
ORDER BY task_id,sequence`, scopeID, recentTaskProgress)
	if err != nil {
		return nil, err
	}
	byTask := map[string][]TaskProgress{}
	for _, value := range progress {
		byTask[value.TaskID] = append(byTask[value.TaskID], value)
	}
	return byTask, nil
}

func (s *Store) AddTaskProgress(ctx context.Context, principal Principal, taskID string, input AddTaskProgressInput) (TaskProgress, error) {
	tx, err := s.beginTx(ctx, principal.ScopeID)
	if err != nil {
		return TaskProgress{}, err
	}
	defer tx.Rollback()
	now := nowMillis()
	if err := requireCurrentExecution(ctx, tx, principal, now); err != nil {
		return TaskProgress{}, err
	}
	var status string
	var claimedBy, claimedExecutionID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT status,claimed_by,claimed_execution_id FROM tasks WHERE scope_id=? AND task_id=?`, principal.ScopeID, taskID).Scan(&status, &claimedBy, &claimedExecutionID)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskProgress{}, Errorf(CodeNotFound, "Task "+taskID+" was not found")
	}
	if err != nil {
		return TaskProgress{}, err
	}
	if status != "claimed" || claimedBy.String != principal.AgentID || claimedExecutionID.String != principal.ExecutionID {
		return TaskProgress{}, Errorf(CodeConflict, "Task "+taskID+" is not claimed by this execution")
	}
	var count, totalBytes, sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(octet_length(text)),0),COALESCE(MAX(sequence),0)+1 FROM task_progress WHERE task_id=?`, taskID).Scan(&count, &totalBytes, &sequence); err != nil {
		return TaskProgress{}, err
	}
	if count >= maxTaskProgressEvents || totalBytes+int64(len(input.Text)) > maxTaskProgressBytes {
		return TaskProgress{}, Errorf(CodeBackpressure, "Task progress history is full")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_progress(task_id,scope_id,sequence,agent_id,execution_id,kind,text,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		taskID, principal.ScopeID, sequence, principal.AgentID, principal.ExecutionID, input.Kind, input.Text, now)
	if err != nil {
		return TaskProgress{}, err
	}
	progress := TaskProgress{
		TaskID: taskID, Sequence: sequence, AgentID: principal.AgentID, ExecutionID: principal.ExecutionID,
		Kind: input.Kind, Text: input.Text, CreatedAt: instant(now),
	}
	if err := appendEvent(ctx, tx, principal.ScopeID, "task.progress_added", taskID, eventAttributes(
		"agentId", principal.AgentID, "executionId", principal.ExecutionID, "kind", input.Kind, "sequence", fmt.Sprintf("%d", sequence),
	), now); err != nil {
		return TaskProgress{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskProgress{}, err
	}
	return progress, nil
}

func (s *Store) ListTaskProgress(ctx context.Context, scopeID, taskID string) ([]TaskProgress, error) {
	var found int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE scope_id=? AND task_id=?`, scopeID, taskID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return nil, Errorf(CodeNotFound, "Task "+taskID+" was not found")
	} else if err != nil {
		return nil, err
	}
	return queryTaskProgress(ctx, s.db, `SELECT `+taskProgressColumns+` FROM task_progress WHERE scope_id=? AND task_id=? ORDER BY sequence`, scopeID, taskID)
}
