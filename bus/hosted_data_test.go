package bus

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var hostedCountedTables = []string{"agents", "messages", "tasks", "task_progress", "escalations", "events", "channels", "channel_posts", "a2a_tasks", "a2a_message_correlations", "output_streams", "output_values"}

func assertHostedStorageCount(t *testing.T, a *HostedAccounts, id string) int64 {
	t.Helper()
	ctx := context.Background()
	var expected, actual int64
	for _, table := range hostedCountedTables {
		var size int64
		requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(octet_length(to_jsonb(t)::text)+128),0) FROM "+table+" t WHERE scope_id=?", id).Scan(&size))
		expected += size
	}
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT retained_bytes FROM hosted_storage_usage WHERE scope_id=?", id).Scan(&actual))
	require(t, actual == expected, "storage counter drift: %d != %d", actual, expected)
	return actual
}
func TestHostedDataPauseArchiveAndOwnerBoundaries(t *testing.T) {
	f := newHostedOAuthFixture(t)
	a := f.h.accounts
	ctx := f.ctx
	id := f.workspace.ID
	requireNoError(t, a.store.configureAttachments(filepath.Join(t.TempDir(), "files")))
	device, err := a.EnrollDevice(ctx, f.user, id, "Laptop")
	requireNoError(t, err)
	session, err := a.StartDeviceSession(ctx, device.Token, "Worker", "")
	requireNoError(t, err)
	file, err := a.runtime.UploadAttachment(ctx, session.AgentToken, UploadAttachmentInput{Name: "sample.txt"}, []byte("private file"))
	requireNoError(t, err)
	_, err = a.SendHumanMessage(ctx, f.user, id, SendMessageInput{To: session.AgentID, Body: "Keep this request", Mode: MessageRequest})
	requireNoError(t, err)
	base := "/api/workspaces/" + id
	require(t, f.call("GET", base+"/export", "account", "").Code == 409, "export did not require quiescence")
	member := hostedTestUser(t, "data-member")
	invite, err := a.Invite(ctx, f.user, id, member.Email, "admin")
	requireNoError(t, err)
	_, err = a.AcceptInvite(ctx, member, invite.Token)
	requireNoError(t, err)
	requireCode(t, a.PauseWorkspace(ctx, member, id, true), CodePermissionDenied)
	_, err = a.store.ExportScope(withHostedOwner(ctx, member), id)
	requireCode(t, err, CodePermissionDenied)
	requireNoError(t, a.PauseWorkspace(ctx, f.user, id, true))
	_, err = a.runtime.Heartbeat(ctx, session.AgentToken, HeartbeatInput{Lifecycle: LifecycleIdle})
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.StartDeviceSession(ctx, device.Token, "Blocked", "")
	requireCode(t, err, CodePermissionDenied)
	response := f.call("GET", base+"/export", "account", "")
	require(t, response.Code == 200, "export failed: %s", response.Body.String())
	require(t, strings.Contains(response.Header().Get("Content-Disposition"), "attachment"), "export not a download")
	require(t, !strings.Contains(response.Body.String(), device.Token) && !strings.Contains(response.Body.String(), session.AgentToken), "export leaked credentials")
	var archive ScopeArchive
	requireNoError(t, json.Unmarshal(response.Body.Bytes(), &archive))
	require(t, len(archive.Messages) == 1 && len(archive.Attachments) == 1 && archive.Attachments[0].ID == file.ID, "archive lost data")
	restored, err := Open(filepath.Join(t.TempDir(), "restored.db"))
	requireNoError(t, err)
	defer restored.Close()
	_, err = restored.ImportScope(ctx, archive)
	requireNoError(t, err)
	var downloaded int64
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT downloaded_bytes FROM attachment_usage WHERE scope_id=? AND month=?", id, time.Now().UTC().Format("2006-01")).Scan(&downloaded))
	require(t, downloaded == int64(len("private file")), "archive bypassed download accounting")
	requireNoError(t, a.PauseWorkspace(ctx, f.user, id, false))
	_, err = a.StartDeviceSession(ctx, device.Token, "Resumed", session.AgentID)
	requireNoError(t, err)
	// A customer pause is independent from an operator suspension.
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET disabled_at=? WHERE scope_id=?", nowMillis(), id)
	requireNoError(t, err)
	requireNoError(t, a.PauseWorkspace(ctx, f.user, id, true))
	requireNoError(t, a.PauseWorkspace(ctx, f.user, id, false))
	_, err = a.StartDeviceSession(ctx, device.Token, "Suspended", session.AgentID)
	requireCode(t, err, CodePermissionDenied)
	assertHostedStorageCount(t, a, id)
}
func TestHostedStorageThresholdAllowsCompletionAndPrune(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	owner := hostedTestUser(t, "storage")
	w, err := a.CreateWorkspace(ctx, owner, "Storage")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), w.ID)
	device, err := a.EnrollDevice(ctx, owner, w.ID, "Device")
	requireNoError(t, err)
	session, err := a.StartDeviceSession(ctx, device.Token, "Worker", "")
	requireNoError(t, err)
	task, err := a.runtime.AddTask(ctx, session.AgentToken, AddTaskInput{Title: "Finish accepted work"})
	requireNoError(t, err)
	_, err = a.runtime.ClaimTask(ctx, session.AgentToken, task.ID)
	requireNoError(t, err)
	request, err := a.SendHumanMessage(ctx, owner, w.ID, SendMessageInput{To: session.AgentID, Body: "Reply after storage fills", Mode: MessageRequest})
	requireNoError(t, err)
	inbox, err := a.runtime.ReserveInbox(ctx, session.AgentToken, 10, 0)
	requireNoError(t, err)
	_, err = a.runtime.CommitInbox(ctx, session.AgentToken, inbox.ID)
	requireNoError(t, err)
	stream, err := a.store.CreateOutputStream(withHostedOwner(ctx, owner), w.ID, CreateOutputStreamInput{Name: "Output", RetentionLimit: 2, PublisherAgentIDs: []string{session.AgentID}})
	requireNoError(t, err)
	_, err = a.runtime.PublishOutput(ctx, session.AgentToken, stream.ID, PublishOutputInput{ContentType: OutputText, Value: "stored output"})
	requireNoError(t, err)
	before := assertHostedStorageCount(t, a, w.ID)
	_, err = a.store.db.ExecContext(ctx, "DELETE FROM output_streams WHERE stream_id=?", stream.ID)
	requireNoError(t, err)
	require(t, assertHostedStorageCount(t, a, w.ID) < before, "cascaded output bytes remained charged")
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET text_limit=1 WHERE scope_id=?", w.ID)
	requireNoError(t, err)
	_, err = a.runtime.AddTask(ctx, session.AgentToken, AddTaskInput{Title: "Over limit"})
	requireCode(t, err, CodeBackpressure)
	_, err = a.SendHumanMessage(ctx, owner, w.ID, SendMessageInput{To: session.AgentID, Body: "Over limit"})
	requireCode(t, err, CodeBackpressure)
	_, err = a.runtime.AcknowledgeMessages(ctx, session.AgentToken, []string{request.MessageID})
	requireNoError(t, err)
	reply, err := a.runtime.SendMessage(ctx, session.AgentToken, SendMessageInput{To: owner.ID, Body: "Finished", Mode: MessageResponse, ResponseTo: request.MessageID})
	requireNoError(t, err)
	_, err = a.ReadHumanMessages(ctx, owner, w.ID, []string{reply.MessageID})
	requireNoError(t, err)
	_, err = a.runtime.CompleteTask(ctx, session.AgentToken, task.ID, "Done")
	requireNoError(t, err)
	old := time.Now().Add(-100 * 24 * time.Hour).UnixMilli()
	_, err = a.store.db.ExecContext(ctx, "UPDATE messages SET created_at=?,delivered_at=?,acknowledged_at=? WHERE scope_id=?", old, old, old, w.ID)
	requireNoError(t, err)
	_, err = a.store.db.ExecContext(ctx, "UPDATE tasks SET updated_at=? WHERE scope_id=?", old, w.ID)
	requireNoError(t, err)
	_, err = a.store.db.ExecContext(ctx, "UPDATE events SET created_at=? WHERE scope_id=?", old, w.ID)
	requireNoError(t, err)
	before = assertHostedStorageCount(t, a, w.ID)
	requireNoError(t, a.Maintain(ctx))
	require(t, assertHostedStorageCount(t, a, w.ID) < before, "retention did not release usage")
	var count int
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE scope_id=?", w.ID).Scan(&count))
	require(t, count == 0, "completed request/reply pair retained")
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE scope_id=?", w.ID).Scan(&count))
	require(t, count == 0, "completed task retained")
}
func TestHostedRetentionBatchesPreserveUnfinishedWorkAndEventCursor(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	owner := hostedTestUser(t, "retention")
	w, err := a.CreateWorkspace(ctx, owner, "Retention")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), w.ID)
	old := time.Now().Add(-100 * 24 * time.Hour).UnixMilli()
	_, err = a.store.db.ExecContext(ctx, `INSERT INTO messages(message_id,scope_id,from_kind,from_id,to_kind,to_id,mode,body,context_json,request_hash,state,created_at,acknowledged_at)
SELECT ?||g,?,'human','sender','human','receiver','notify','old','[]','fixture','acknowledged',?,? FROM generate_series(1,601) AS g`, w.ID+"_", w.ID, old, old)
	requireNoError(t, err)
	_, err = a.store.db.ExecContext(ctx, `INSERT INTO messages(message_id,scope_id,from_kind,from_id,to_kind,to_id,mode,body,context_json,request_hash,state,created_at,acknowledged_at,delivered_at)
VALUES(?,?,'human','sender','human','receiver','request','still owed','[]','fixture','acknowledged',?,?,?)`, w.ID+"_owed", w.ID, old, old, old)
	requireNoError(t, err)
	cutoff := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	preview, err := a.PruneWorkspace(ctx, owner, w.ID, cutoff, false)
	requireNoError(t, err)
	require(t, preview.Records.Messages == 601, "preview included unresolved request")
	result, err := a.PruneWorkspace(ctx, owner, w.ID, cutoff, true)
	requireNoError(t, err)
	require(t, result.Records.Messages == 601, "batch count changed")
	var count int
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE scope_id=?", w.ID).Scan(&count))
	require(t, count == 1, "prune deleted unfinished request")
	tx, err := a.store.beginTx(ctx, w.ID)
	requireNoError(t, err)
	_, err = tx.ExecContext(ctx, "UPDATE scopes SET event_revision=100099 WHERE scope_id=?", w.ID)
	requireNoError(t, err)
	requireNoError(t, appendEvent(ctx, tx, w.ID, "agent.lifecycle_changed", "synthetic", nil, nowMillis()))
	requireNoError(t, tx.Commit())
	events, err := a.store.Events(ctx, w.ID, 0, 10)
	requireNoError(t, err)
	require(t, events.ResyncRequired && events.MinimumCursor == 100, "bounded event log did not advance cursor floor")
	assertHostedStorageCount(t, a, w.ID)
}
