package bus

import (
	"context"
	"encoding/json"
	"sync"
)

const (
	maxConcurrentRequests          = 256
	maxInboxWaitersPerAgent        = 32
	maxInboxWaitersPerScope        = 128
	maxListedTasks                 = 10000
	maxRemoteBacklogMessages       = messageBacklogCap / 2
	maxRemoteBacklogBytes    int64 = 64 * 1024 * 1024
)

type inboxWaitBudget struct {
	mu     sync.Mutex
	counts map[signalKey]int
}

func (b *inboxWaitBudget) acquire(p Principal) (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.counts == nil {
		b.counts = map[signalKey]int{}
	}
	agent := signalKey{scopeID: p.ScopeID, consumerID: p.AgentID}
	scope := signalKey{scopeID: p.ScopeID}
	if b.counts[agent] >= maxInboxWaitersPerAgent || b.counts[scope] >= maxInboxWaitersPerScope {
		return func() {}, false
	}
	b.counts[agent]++
	b.counts[scope]++
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			for _, key := range []signalKey{agent, scope} {
				b.counts[key]--
				if b.counts[key] == 0 {
					delete(b.counts, key)
				}
			}
		})
	}, true
}

const operationalIndexesSQL = `
CREATE INDEX IF NOT EXISTS messages_expiry ON messages(scope_id,expires_at) WHERE expires_at IS NOT NULL AND state NOT IN ('acknowledged','expired');
CREATE INDEX IF NOT EXISTS messages_active ON messages(scope_id,state) WHERE state NOT IN ('acknowledged','expired');
CREATE INDEX IF NOT EXISTS messages_remote_active ON messages(scope_id,from_kind,state) WHERE state NOT IN ('acknowledged','expired');
CREATE INDEX IF NOT EXISTS tasks_active ON tasks(scope_id,status) WHERE status!='done';
CREATE INDEX IF NOT EXISTS tasks_claims ON tasks(scope_id,status,claimed_by,claimed_execution_id);
CREATE INDEX IF NOT EXISTS tasks_page ON tasks(scope_id,task_id);
`

// Refuse oversized portable exports before loading their payloads into memory.
// The conservative JSON escape/record overhead bound may reject smaller archives;
// the streaming full-database snapshot remains available at any retained size.
func checkPortableExportBudget(ctx context.Context, tx sqlExecutor, scopeID string) error {
	queries := []string{
		"SELECT COALESCE(SUM(octet_length(description)+2048),0) FROM channels WHERE scope_id=?",
		"SELECT COUNT(*)*1024 FROM channel_subscriptions WHERE scope_id=?",
		"SELECT COALESCE(SUM(octet_length(body)+2048),0) FROM channel_posts WHERE scope_id=?",
		"SELECT COALESCE(SUM(octet_length(body)+octet_length(context_json)+2048),0) FROM messages WHERE scope_id=?",
		"SELECT COALESCE(SUM(octet_length(description)+octet_length(dependencies_json)+COALESCE(octet_length(note),0)+2048),0) FROM tasks WHERE scope_id=?",
		"SELECT COALESCE(SUM(octet_length(text)+1024),0) FROM task_progress WHERE scope_id=?",
		"SELECT COALESCE(SUM(octet_length(question)+octet_length(options_json)+COALESCE(octet_length(answer),0)+1024),0) FROM escalations WHERE scope_id=?",
		"SELECT COALESCE(SUM(octet_length(capabilities_json)+2048),0) FROM agents WHERE scope_id=?",
		"SELECT COUNT(*)*1024 FROM peer_links WHERE scope_id=?",
		"SELECT COUNT(*)*1024 FROM a2a_publications WHERE scope_id=?",
		"SELECT COUNT(*)*2048 FROM a2a_tasks WHERE scope_id=?",
		"SELECT COUNT(*)*2048 FROM a2a_message_correlations c JOIN a2a_tasks t ON t.task_id=c.task_id WHERE t.scope_id=?",
		"SELECT COUNT(*)*2048 FROM output_streams WHERE scope_id=?",
		"SELECT COUNT(*)*1024 FROM output_stream_publishers WHERE scope_id=?",
		"SELECT COALESCE(SUM(octet_length(v.value_json)+COALESCE(octet_length(v.reference_json),0)+1024),0) FROM output_values v JOIN output_streams s ON s.stream_id=v.stream_id WHERE s.scope_id=?",
	}
	var total int64
	for _, query := range queries {
		var size int64
		if err := tx.QueryRowContext(ctx, query, scopeID).Scan(&size); err != nil {
			return err
		}
		total += size
		if total > maxArchiveBodyBytes/6 {
			return Errorf(CodeBackpressure, "Portable archive budget exceeded; use an admin database backup or prune retained history")
		}
	}
	var files int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM((size_bytes+2)/3*4+4096),0) FROM attachments WHERE scope_id=? AND deleted_at=0", scopeID).Scan(&files); err != nil {
		return err
	}
	if total*6+files > maxArchiveBodyBytes {
		return Errorf(CodeBackpressure, "Portable archive exceeds 64 MiB including attachments; use a database-plus-file backup")
	}
	return nil
}

type archiveSizeWriter int64

func (w *archiveSizeWriter) Write(p []byte) (int, error) {
	*w += archiveSizeWriter(len(p))
	if *w > maxArchiveBodyBytes {
		return 0, Errorf(CodeBackpressure, "Portable archive exceeds 64 MiB; use an admin database backup")
	}
	return len(p), nil
}
func checkEncodedArchiveSize(archive ScopeArchive) error {
	var size archiveSizeWriter
	return json.NewEncoder(&size).Encode(archive)
}
