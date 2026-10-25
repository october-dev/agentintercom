package bus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func exerciseAttachments(t *testing.T, r *Runtime) {
	t.Helper()
	ctx := context.Background()
	scope, err := r.CreateScope(ctx, CreateScopeInput{})
	requireNoError(t, err)
	defer r.DeleteScope(ctx, scope.ScopeID)
	a, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "uploader", DisplayName: "Uploader"})
	requireNoError(t, err)
	b, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "reader", DisplayName: "Reader"})
	requireNoError(t, err)
	data := []byte("Résumé \x00\xff<script>untrusted</script>")
	input := UploadAttachmentInput{Name: "résumé.txt", MediaType: "text/plain", IdempotencyKey: "same-upload"}
	var wg sync.WaitGroup
	results := make(chan Attachment, 2)
	failures := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := r.UploadAttachment(ctx, a.AgentToken, input, data)
			results <- value
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		requireNoError(t, err)
	}
	var file Attachment
	for value := range results {
		if file.ID != "" {
			require(t, file.ID == value.ID, "retry duplicated the file")
		}
		file = value
	}
	page, err := r.Attachments(ctx, b.AgentToken, "", 50)
	requireNoError(t, err)
	require(t, len(page.Attachments) == 1 && page.Attachments[0].ID == file.ID, "shared file missing")
	chunk, err := r.ReadAttachment(ctx, b.AgentToken, file.ID, 0, 7)
	requireNoError(t, err)
	require(t, bytes.Equal(chunk.Data, data[:7]) && chunk.NextOffset != nil && *chunk.NextOffset == 7, "chunk boundary wrong")
	last, err := r.ReadAttachment(ctx, b.AgentToken, file.ID, 7, MaxAttachmentBytes)
	requireNoError(t, err)
	require(t, bytes.Equal(last.Data, data[7:]) && last.NextOffset == nil, "chunk continuation wrong")
	_, err = r.UploadAttachment(ctx, a.AgentToken, input, []byte("changed"))
	requireCode(t, err, CodeConflict)
	for _, name := range []string{"../secret", "a/b", "a\\b", "a\nb", "."} {
		_, err = r.UploadAttachment(ctx, a.AgentToken, UploadAttachmentInput{Name: name}, data)
		requireCode(t, err, CodeInvalidArgument)
	}
	_, err = r.UploadAttachment(ctx, a.AgentToken, UploadAttachmentInput{Name: "too-large"}, make([]byte, MaxAttachmentBytes+1))
	requireCode(t, err, CodeInvalidArgument)
	foreign, err := r.CreateScope(ctx, CreateScopeInput{})
	requireNoError(t, err)
	defer r.DeleteScope(ctx, foreign.ScopeID)
	outsider, err := r.RegisterAgent(ctx, foreign.ScopeToken, RegisterAgentInput{ID: "outsider", DisplayName: "Outsider"})
	requireNoError(t, err)
	_, err = r.ReadAttachment(ctx, outsider.AgentToken, file.ID, 0, 100)
	requireCode(t, err, CodeNotFound)
	requireCode(t, r.DeleteAttachment(ctx, b.AgentToken, file.ID), CodePermissionDenied)
	// Archive and restore include actual bytes and preserve retry identity.
	err = r.RetireAgent(ctx, a.AgentToken)
	requireNoError(t, err)
	err = r.RetireAgent(ctx, b.AgentToken)
	requireNoError(t, err)
	archive, err := r.ExportScope(ctx, scope.ScopeID)
	requireNoError(t, err)
	require(t, len(archive.Attachments) == 1 && bytes.Equal(archive.Attachments[0].Data, data), "export omitted bytes")
	destination, err := Open(":memory:")
	requireNoError(t, err)
	defer destination.Close()
	restored, err := destination.ImportScope(ctx, archive)
	requireNoError(t, err)
	resumed, err := destination.RegisterAgent(ctx, restored.ScopeToken, RegisterAgentInput{ID: "uploader", DisplayName: "Resumed"})
	requireNoError(t, err)
	copied, err := destination.ReadAttachment(ctx, resumed.AgentToken, file.ID, 0, MaxAttachmentBytes)
	requireNoError(t, err)
	require(t, bytes.Equal(copied.Data, data), "restore changed bytes")
	retry, err := destination.UploadAttachment(ctx, resumed.AgentToken, input, data)
	requireNoError(t, err)
	require(t, retry.ID == file.ID, "restore lost retry identity")
	requireNoError(t, destination.DeleteAttachment(ctx, resumed.AgentToken, file.ID))
	requireNoError(t, destination.DeleteAttachment(ctx, resumed.AgentToken, file.ID))
	_, err = destination.ReadAttachment(ctx, resumed.AgentToken, file.ID, 0, 100)
	requireCode(t, err, CodeNotFound)
	_, err = destination.UploadAttachment(ctx, resumed.AgentToken, input, data)
	requireCode(t, err, CodeConflict)
	_, err = r.ReadAttachment(ctx, a.AgentToken, file.ID, 0, 100)
	requireCode(t, err, CodeUnauthenticated)
}
func TestAttachmentsIsolationRetryAndPortableRestore(t *testing.T) {
	r, err := Open(":memory:")
	requireNoError(t, err)
	defer r.Close()
	exerciseAttachments(t, r)
}
func TestPostgresAttachmentsIsolationRetryAndPortableRestore(t *testing.T) {
	_, a := hostedTestAccounts(t)
	requireNoError(t, a.store.configureAttachments(filepath.Join(t.TempDir(), "files")))
	exerciseAttachments(t, a.runtime)
}

func TestAttachmentPrivateFilesAndBundleRestore(t *testing.T) {
	a := setupAgents(t, filepath.Join(t.TempDir(), "bus.db"))
	defer a.runtime.Close()
	ctx := context.Background()
	file, err := a.runtime.UploadAttachment(ctx, a.plannerToken, UploadAttachmentInput{Name: "payload.bin"}, []byte{0, 1, 2, 3})
	requireNoError(t, err)
	store := a.runtime.store.(*Store)
	location := filepath.Join(store.files.directory, file.ID)
	info, err := os.Stat(location)
	requireNoError(t, err)
	require(t, info.Mode().Perm() == 0600, "file is not private")
	another, err := OpenWithOptions(":memory:", RuntimeOptions{AttachmentDir: store.files.directory})
	if another != nil {
		another.Close()
	}
	requireCode(t, err, CodeConflict)
	var bundle bytes.Buffer
	requireNoError(t, store.BackupBundle(ctx, &bundle))
	restoreDir := t.TempDir()
	requireNoError(t, os.Mkdir(filepath.Join(restoreDir, "bus.db.attachments"), 0700))
	z, err := gzip.NewReader(bytes.NewReader(bundle.Bytes()))
	requireNoError(t, err)
	defer z.Close()
	tr := tar.NewReader(z)
	seen := map[string]bool{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		requireNoError(t, err)
		data, err := io.ReadAll(tr)
		requireNoError(t, err)
		seen[header.Name] = true
		switch header.Name {
		case "bus.db":
			requireNoError(t, os.WriteFile(filepath.Join(restoreDir, "bus.db"), data, 0600))
		case "attachments/" + file.ID:
			requireNoError(t, os.WriteFile(filepath.Join(restoreDir, "bus.db.attachments", file.ID), data, 0600))
		case "manifest.json":
		default:
			t.Fatalf("unexpected bundle entry %q", header.Name)
		}
	}
	require(t, len(seen) == 3, "incomplete bundle")
	restored, err := Open(filepath.Join(restoreDir, "bus.db"))
	requireNoError(t, err)
	defer restored.Close()
	content, err := restored.ReadAttachment(ctx, a.plannerToken, file.ID, 0, 10)
	requireNoError(t, err)
	require(t, bytes.Equal(content.Data, []byte{0, 1, 2, 3}), "bundle restore lost file")
	requireNoError(t, os.WriteFile(location, []byte{4, 5, 6, 7}, 0600))
	_, err = a.runtime.ReadAttachment(ctx, a.plannerToken, file.ID, 0, 10)
	requireCode(t, err, CodeInternal)
	requireNoError(t, os.Remove(location))
	requireNoError(t, os.Symlink(filepath.Join(restoreDir, "bus.db"), location))
	_, err = a.runtime.ReadAttachment(ctx, a.plannerToken, file.ID, 0, 10)
	requireCode(t, err, CodeInternal)
}

func TestAttachmentHTTPAndMCP(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx := context.Background()
	server := httptest.NewServer(NewServer(a.runtime, ServerOptions{AdminToken: "admin"}))
	defer server.Close()
	c := Client{Address: server.URL, Token: a.plannerToken}
	file, err := c.UploadAttachment(ctx, UploadAttachmentInput{Name: "example.html", MediaType: "text/html"}, []byte("<script>untrusted</script>"))
	requireNoError(t, err)
	httpClient := &http.Client{Transport: bearerTransport{token: a.reviewerToken, base: http.DefaultTransport}}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "file-qualification", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient}, nil)
	requireNoError(t, err)
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_attachment", Arguments: map[string]any{"id": file.ID}})
	requireNoError(t, err)
	require(t, !result.IsError, "MCP read failed: %v", result.Content)
	raw, err := json.Marshal(result.StructuredContent)
	requireNoError(t, err)
	var content AttachmentContent
	requireNoError(t, json.Unmarshal(raw, &content))
	require(t, string(content.Data) == "<script>untrusted</script>", "MCP changed bytes")
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "upload_attachment", Arguments: map[string]any{"name": "answer.bin", "dataBase64": base64.StdEncoding.EncodeToString([]byte{0, 255})}})
	requireNoError(t, err)
	detail, _ := json.Marshal(result)
	require(t, !result.IsError, "MCP upload failed: %s", detail)
	req, _ := http.NewRequest("GET", server.URL+"/v1/attachments/"+file.ID+"/content", nil)
	req.Header.Set("Authorization", "Bearer "+a.reviewerToken)
	response, err := http.DefaultClient.Do(req)
	requireNoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	requireNoError(t, err)
	require(t, response.StatusCode == 200 && bytes.Equal(body, content.Data) && response.Header.Get("Content-Type") == "application/octet-stream" && strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment;") && response.Header.Get("X-Content-SHA256") == file.SHA256, "download was not safely served")
	var bundle bytes.Buffer
	requireNoError(t, (Client{Address: server.URL, Token: "admin"}).BackupBundleTo(ctx, &bundle))
	require(t, bundle.Len() > 0, "HTTP backup omitted bundle")
}

func TestAttachmentGarbageCollectionKeepsLiveFilesAndRecentOrphans(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx := context.Background()
	live, err := a.runtime.UploadAttachment(ctx, a.plannerToken, UploadAttachmentInput{Name: "live"}, []byte("keep"))
	requireNoError(t, err)
	deleted, err := a.runtime.UploadAttachment(ctx, a.plannerToken, UploadAttachmentInput{Name: "deleted", IdempotencyKey: "deleted-key"}, []byte("remove"))
	requireNoError(t, err)
	store := a.runtime.store.(*Store)
	// Simulate a crash after metadata deletion but before unlinking.
	_, err = store.db.ExecContext(ctx, "UPDATE attachments SET deleted_at=? WHERE attachment_id=?", nowMillis()-int64(31*24*time.Hour/time.Millisecond), deleted.ID)
	requireNoError(t, err)
	oldID, _ := randomID("att_")
	newID, _ := randomID("att_")
	for _, id := range []string{oldID, newID} {
		requireNoError(t, os.WriteFile(filepath.Join(store.files.directory, id), []byte("orphan"), 0600))
	}
	old := time.Now().Add(-2 * time.Hour)
	requireNoError(t, os.Chtimes(filepath.Join(store.files.directory, oldID), old, old))
	removed, err := store.CollectAttachmentGarbage(ctx)
	requireNoError(t, err)
	require(t, removed == 2, "unexpected cleanup count: %d", removed)
	for _, id := range []string{live.ID, newID} {
		_, err := os.Stat(filepath.Join(store.files.directory, id))
		requireNoError(t, err)
	}
	for _, id := range []string{deleted.ID, oldID} {
		_, err := os.Stat(filepath.Join(store.files.directory, id))
		require(t, os.IsNotExist(err), "old file survived cleanup")
	}
	var count int
	requireNoError(t, store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM attachments WHERE attachment_id=?", deleted.ID).Scan(&count))
	require(t, count == 0, "expired deletion tombstone survived")
}

func TestHostedAttachmentQuotasAndMembership(t *testing.T) {
	f := newHostedOAuthFixture(t)
	a := f.h.accounts
	requireNoError(t, a.store.configureAttachments(filepath.Join(t.TempDir(), "files")))
	base := "/api/workspaces/" + f.workspace.ID + "/attachments"
	denied := f.call("POST", base+"?name=denied", "", strings.Repeat("x", 100))
	require(t, denied.Code == 401, "anonymous upload accepted")
	uploaded := f.call("POST", base+"?name=report.txt&idempotencyKey=hosted-file", "account", "hello")
	require(t, uploaded.Code == 200, "hosted upload: %s", uploaded.Body.String())
	var envelope responseEnvelope[Attachment]
	requireNoError(t, json.Unmarshal(uploaded.Body.Bytes(), &envelope))
	file := envelope.Result
	month := time.Now().UTC().Format("2006-01")
	var used int64
	requireNoError(t, a.store.db.QueryRowContext(f.ctx, "SELECT uploaded_bytes FROM attachment_usage WHERE scope_id=? AND month=?", f.workspace.ID, month).Scan(&used))
	require(t, used == 5, "incorrect upload metering")
	retry := f.call("POST", base+"?name=report.txt&idempotencyKey=hosted-file", "account", "hello")
	require(t, retry.Code == 200, "retry failed")
	requireNoError(t, a.store.db.QueryRowContext(f.ctx, "SELECT uploaded_bytes FROM attachment_usage WHERE scope_id=? AND month=?", f.workspace.ID, month).Scan(&used))
	require(t, used == 5, "upload retry charged twice")
	_, err := a.store.db.ExecContext(f.ctx, "UPDATE attachment_usage SET downloaded_bytes=? WHERE scope_id=? AND month=?", int64(1024*1024*1024)-4, f.workspace.ID, month)
	requireNoError(t, err)
	read := f.call("GET", base+"/"+file.ID+"/content", "account", "")
	require(t, read.Code == 429, "download exceeded trial allowance")
	_, err = a.store.db.ExecContext(f.ctx, "UPDATE hosted_workspaces SET paid_until=? WHERE scope_id=?", nowMillis()+86400000, f.workspace.ID)
	requireNoError(t, err)
	read = f.call("GET", base+"/"+file.ID+"/content", "account", "")
	require(t, read.Code == 200 && read.Body.String() == "hello", "paid allowance did not apply")
	member := hostedTestUser(t, "file-reader")
	invite, err := a.Invite(f.ctx, f.user, f.workspace.ID, member.Email, "member")
	requireNoError(t, err)
	_, err = a.AcceptInvite(f.ctx, member, invite.Token)
	requireNoError(t, err)
	_, err = a.store.ReadAttachment(withHostedMember(f.ctx, member), f.workspace.ID, file.ID, 0, 20)
	requireNoError(t, err)
	requireCode(t, a.store.DeleteAttachment(withHostedMember(f.ctx, member), f.workspace.ID, file.ID), CodePermissionDenied)
	requireNoError(t, a.ChangeMember(f.ctx, f.user, f.workspace.ID, member.ID, ""))
	_, err = a.store.ReadAttachment(withHostedMember(f.ctx, member), f.workspace.ID, file.ID, 0, 20)
	requireCode(t, err, CodePermissionDenied)
	_, err = a.store.db.ExecContext(f.ctx, "UPDATE hosted_workspaces SET paid_until=0,trial_ends_at=0 WHERE scope_id=?", f.workspace.ID)
	requireNoError(t, err)
	denied = f.call("POST", base+"?name=new.txt", "account", "new")
	require(t, denied.Code == 403, "inactive hosting accepted new upload")
	retry = f.call("POST", base+"?name=report.txt&idempotencyKey=hosted-file", "account", "hello")
	require(t, retry.Code == 200, "inactive hosting rejected safe retry")
}

func TestAttachmentDiskReserveRefusesWriteWithoutCharging(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx := context.Background()
	store := a.runtime.store.(*Store)
	original, err := a.runtime.UploadAttachment(ctx, a.plannerToken, UploadAttachmentInput{Name: "kept", IdempotencyKey: "kept"}, []byte("same"))
	requireNoError(t, err)
	store.files.minimumFreeBytes = 1 << 62
	_, err = a.runtime.UploadAttachment(ctx, a.plannerToken, UploadAttachmentInput{Name: "refused"}, []byte("new data"))
	requireCode(t, err, CodeBackpressure)
	retry, err := a.runtime.UploadAttachment(ctx, a.plannerToken, UploadAttachmentInput{Name: "kept", IdempotencyKey: "kept"}, []byte("same"))
	requireNoError(t, err)
	require(t, retry.ID == original.ID, "low disk broke idempotent recovery")
	var bytes, count int64
	requireNoError(t, store.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(uploaded_bytes),0) FROM attachment_usage WHERE scope_id=?", a.scope.ScopeID).Scan(&bytes))
	require(t, bytes == 4, "refused write consumed transfer allowance")
	requireNoError(t, store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM attachments WHERE scope_id=?", a.scope.ScopeID).Scan(&count))
	require(t, count == 1, "refused write committed metadata")
}

func TestAttachmentBackupLockPreservesBytesAndAllowsCoordination(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx := context.Background()
	store := a.runtime.store.(*Store)
	input := UploadAttachmentInput{Name: "saved", IdempotencyKey: "saved"}
	file, err := a.runtime.UploadAttachment(ctx, a.plannerToken, input, []byte("backup bytes"))
	requireNoError(t, err)
	backup, err := acquireFileLock(filepath.Join(store.files.directory, ".backup.lock"))
	requireNoError(t, err)
	defer backup.Close()
	_, err = a.runtime.UploadAttachment(ctx, a.plannerToken, UploadAttachmentInput{Name: "wait"}, []byte("new bytes"))
	requireCode(t, err, CodeBackpressure)
	retried, err := a.runtime.UploadAttachment(ctx, a.plannerToken, input, []byte("backup bytes"))
	requireNoError(t, err)
	require(t, retried.ID == file.ID, "backup interrupted retry recovery")
	read, err := a.runtime.ReadAttachment(ctx, a.plannerToken, file.ID, 0, 100)
	requireNoError(t, err)
	require(t, string(read.Data) == "backup bytes", "backup interrupted reads")
	_, err = a.runtime.SendMessage(ctx, a.plannerToken, SendMessageInput{To: "reviewer", Body: "Coordination continues"})
	requireNoError(t, err)
	requireNoError(t, a.runtime.DeleteAttachment(ctx, a.plannerToken, file.ID))
	_, err = a.runtime.ReadAttachment(ctx, a.plannerToken, file.ID, 0, 100)
	requireCode(t, err, CodeNotFound)
	_, err = os.Stat(filepath.Join(store.files.directory, file.ID))
	requireNoError(t, err) // The database dump may still reference these bytes.
	_, err = store.CollectAttachmentGarbage(ctx)
	requireCode(t, err, CodeBackpressure)
	var uploaded int64
	requireNoError(t, store.db.QueryRowContext(ctx, "SELECT SUM(uploaded_bytes) FROM attachment_usage WHERE scope_id=?", a.scope.ScopeID).Scan(&uploaded))
	require(t, uploaded == 12, "busy upload was charged")
	requireNoError(t, backup.Close())
	_, err = store.CollectAttachmentGarbage(ctx)
	requireNoError(t, err)
	_, err = os.Stat(filepath.Join(store.files.directory, file.ID))
	require(t, os.IsNotExist(err), "deferred deletion did not complete")
	_, err = a.runtime.UploadAttachment(ctx, a.plannerToken, UploadAttachmentInput{Name: "resumed"}, []byte("new bytes"))
	requireNoError(t, err)
}
