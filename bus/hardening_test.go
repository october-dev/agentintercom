package bus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryProtectedAgentMutationFencesReplacedAndExpiredExecution(t *testing.T) {
	for _, reason := range []string{"replacement", "expiry"} {
		t.Run(reason, func(t *testing.T) {
			a := setupAgents(t, ":memory:")
			defer a.runtime.Close()
			ctx := context.Background()
			s := sqliteStore(t, a.runtime)
			p, err := s.AuthenticateAgent(ctx, a.plannerToken)
			requireNoError(t, err)
			if reason == "replacement" {
				_, err = s.RegisterAgent(ctx, p.ScopeID, RegisterAgentInput{ID: p.AgentID, DisplayName: "replacement", LeaseMS: 300000})
			} else {
				_, err = s.db.Exec("UPDATE agents SET lease_expires_at=0 WHERE scope_id=? AND agent_id=?", p.ScopeID, p.AgentID)
			}
			requireNoError(t, err)
			operations := map[string]func() error{
				"send": func() error {
					_, err := s.SendMessage(ctx, p, SendMessageInput{To: "reviewer", Body: "stale"})
					return err
				},
				"receipt":       func() error { _, err := s.Receipt(ctx, p, "missing"); return err },
				"reserve":       func() error { _, err := s.ReserveInbox(ctx, p, 1); return err },
				"commit":        func() error { _, err := s.CommitInbox(ctx, p, "missing"); return err },
				"release inbox": func() error { return s.ReleaseInbox(ctx, p, "missing") },
				"ack":           func() error { _, err := s.AcknowledgeMessages(ctx, p, []string{"missing"}); return err },
				"add task":      func() error { _, err := s.AddAgentTask(ctx, p, AddTaskInput{Title: "stale"}); return err },
				"claim":         func() error { _, err := s.ClaimTask(ctx, p, "missing"); return err },
				"complete":      func() error { _, err := s.CompleteTask(ctx, p, "missing", ""); return err },
				"release task":  func() error { _, err := s.ReleaseTask(ctx, p, "missing"); return err },
				"progress": func() error {
					_, err := s.AddTaskProgress(ctx, p, "missing", AddTaskProgressInput{Kind: "note", Text: "stale"})
					return err
				},
				"ask": func() error { _, err := s.AskHuman(ctx, p, AskHumanInput{Question: "stale"}); return err },
				"heartbeat": func() error {
					_, _, _, err := s.Heartbeat(ctx, p, HeartbeatInput{Lifecycle: LifecycleReady, Ready: true, LeaseMS: 300000})
					return err
				},
			}
			for name, operation := range operations {
				t.Run(name, func(t *testing.T) { requireCode(t, operation(), CodeUnauthenticated) })
			}
		})
	}
}

type rotateDuringScopeAuthentication struct {
	storageBackend
	once bool
}

func (s *rotateDuringScopeAuthentication) AuthenticateScope(ctx context.Context, token string) (string, error) {
	id, err := s.storageBackend.AuthenticateScope(ctx, token)
	if err == nil && !s.once {
		s.once = true
		_, err = s.storageBackend.RotateScopeToken(ctx, id)
	}
	return id, err
}

func TestScopeRotationFencesAlreadyAuthenticatedRegistration(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	a.runtime.store = &rotateDuringScopeAuthentication{storageBackend: a.runtime.store}
	_, err := a.runtime.RegisterAgent(context.Background(), a.scope.ScopeToken, RegisterAgentInput{ID: "stale", DisplayName: "stale"})
	requireCode(t, err, CodeUnauthenticated)
}

func TestA2ARotationFencesRetryReadAndStateChanges(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx := context.Background()
	s := sqliteStore(t, a.runtime)
	publication, issued := setupA2APrincipal(t, a)
	input := AcceptA2AMessageInput{ClientMessageID: "same-id", Body: "original"}
	task, err := a.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, input)
	requireNoError(t, err)
	p, err := s.AuthenticateA2APrincipal(ctx, issued.Credential, publication.ID)
	requireNoError(t, err)
	if _, err := a.runtime.RotateA2APrincipal(ctx, a.scope.ScopeToken, issued.Principal.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.AcceptA2AMessage(ctx, p, a.runtime.a2aPrincipalLimits, input)
	requireCode(t, err, CodeUnauthenticated)
	_, err = s.A2ATask(ctx, p, task.ID)
	requireCode(t, err, CodeUnauthenticated)
	_, err = s.SetA2ATaskState(ctx, p, task.ID, A2ATaskFailed)
	requireCode(t, err, CodeUnauthenticated)
}

func TestAggregateRemoteBudgetPreservesLocalMessageAdmission(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx := context.Background()
	s := sqliteStore(t, a.runtime)
	publication, issued := setupA2APrincipal(t, a)
	input := AcceptA2AMessageInput{ClientMessageID: "original", Body: "original"}
	first, err := a.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, input)
	requireNoError(t, err)
	// Fill only the backlog being tested; principal-specific correlated usage is
	// covered separately by the A2A quota suite.
	_, err = s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<5000)
INSERT INTO messages(message_id,scope_id,from_kind,from_id,to_kind,to_id,mode,body,context_json,request_hash,state,created_at)
SELECT printf('remote_%04d',x),?,'a2aPrincipal',?,'agent','reviewer','request','remote','[]','hash','queued',? FROM n`, a.scope.ScopeID, issued.Principal.ID, nowMillis())
	requireNoError(t, err)
	_, err = a.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, AcceptA2AMessageInput{ClientMessageID: "overflow", Body: "overflow"})
	requireCode(t, err, CodeBackpressure)
	retry, err := a.runtime.AcceptA2AMessage(ctx, issued.Credential, publication.ID, input)
	require(t, err == nil && retry.ID == first.ID, "quota blocked safe retry: %+v %v", retry, err)
	if _, err := a.runtime.SendMessage(ctx, a.plannerToken, SendMessageInput{To: "reviewer", Body: "local"}); err != nil {
		t.Fatalf("remote work exhausted local admission: %v", err)
	}
}

func TestAdminScopeRecoveryAndDeletion(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx := context.Background()
	server := httptest.NewServer(NewServer(a.runtime, ServerOptions{AdminToken: "admin"}))
	defer server.Close()
	admin := Client{Address: server.URL, Token: "admin"}
	owner := Client{Address: server.URL, Token: a.scope.ScopeToken}
	if _, err := owner.ListScopes(ctx); err == nil {
		t.Fatal("owner gained admin listing")
	}
	values, err := admin.ListScopes(ctx)
	require(t, err == nil && len(values) == 1, "list: %+v %v", values, err)
	task, err := a.runtime.AddTask(ctx, a.plannerToken, AddTaskInput{Title: "claim"})
	requireNoError(t, err)
	if _, err := a.runtime.ClaimTask(ctx, a.plannerToken, task.ID); err != nil {
		t.Fatal(err)
	}
	rotated, err := admin.RotateScopeToken(ctx, a.scope.ScopeID)
	requireNoError(t, err)
	if rotated.ScopeToken == a.scope.ScopeToken {
		t.Fatal("token unchanged")
	}
	_, err = owner.ListAgents(ctx)
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.runtime.Heartbeat(ctx, a.plannerToken, HeartbeatInput{Lifecycle: LifecycleReady, LeaseMS: 300000})
	requireCode(t, err, CodeUnauthenticated)
	owner.Token = rotated.ScopeToken
	tasks, err := owner.ListTasks(ctx, false)
	require(t, err == nil && tasks[0].Status == "open", "claims not released: %+v %v", tasks, err)
	requireNoError(t, admin.DeleteScope(ctx, a.scope.ScopeID))
	if err := admin.DeleteScope(ctx, a.scope.ScopeID); err != nil {
		t.Fatalf("delete retry: %v", err)
	}
	values, err = admin.ListScopes(ctx)
	require(t, err == nil && len(values) == 0, "scope remains: %+v %v", values, err)
}

func TestTaskPaginationDoesNotLoseRows(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		if _, err := a.runtime.AddTask(ctx, a.scope.ScopeToken, AddTaskInput{Title: fmt.Sprintf("task %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	for {
		page, err := a.runtime.TaskPage(ctx, a.plannerToken, after, 2)
		requireNoError(t, err)
		if len(page.Tasks) > 2 {
			t.Fatal("page limit exceeded")
		}
		for _, task := range page.Tasks {
			if seen[task.ID] {
				t.Fatal("duplicate task")
			}
			seen[task.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		after = page.NextCursor
	}
	if len(seen) != 7 {
		t.Fatalf("missing tasks: %d", len(seen))
	}
}

func TestInboxAndHTTPBudgetsReserveControlCapacity(t *testing.T) {
	var budget inboxWaitBudget
	releases := []func(){}
	for agent := 0; agent < 4; agent++ {
		p := Principal{AgentIdentity: AgentIdentity{ScopeID: "scope", AgentID: fmt.Sprint(agent)}}
		for i := 0; i < maxInboxWaitersPerAgent; i++ {
			release, ok := budget.acquire(p)
			if !ok {
				t.Fatal("early rejection")
			}
			releases = append(releases, release)
		}
		if _, ok := budget.acquire(p); ok {
			t.Fatal("agent budget exceeded")
		}
	}
	if _, ok := budget.acquire(Principal{AgentIdentity: AgentIdentity{ScopeID: "scope", AgentID: "extra"}}); ok {
		t.Fatal("scope budget exceeded")
	}
	for _, release := range releases {
		release()
		release()
	}
	if len(budget.counts) != 0 {
		t.Fatal("waiter budget leaked")
	}
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	server := NewServer(a.runtime, ServerOptions{})
	for i := 0; i < maxConcurrentRequests; i++ {
		server.requests <- struct{}{}
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/tasks", nil))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("request admission: %d", response.Code)
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("control plane starved: %d", response.Code)
	}
}

func TestLargeDatabaseBackupRestoresBeyondPortableLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("large-state recovery test")
	}
	a := setupAgents(t, filepath.Join(t.TempDir(), "source.db"))
	defer a.runtime.Close()
	s := sqliteStore(t, a.runtime)
	ctx := context.Background()
	body := strings.Repeat("x", 65536)
	_, err := s.db.ExecContext(ctx, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1100)
INSERT INTO messages(message_id,scope_id,from_kind,from_id,to_kind,to_id,mode,body,context_json,request_hash,state,created_at)
SELECT printf('msg_large_%04d',x),?,'agent','planner','agent','reviewer','notify',?,'[]','hash','queued',? FROM n`, a.scope.ScopeID, body, nowMillis())
	requireNoError(t, err)
	offlineAgents(t, a)
	_, err = a.runtime.ExportScope(ctx, a.scope.ScopeID)
	requireCode(t, err, CodeBackpressure)
	server := httptest.NewServer(NewServer(a.runtime, ServerOptions{AdminToken: "admin"}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "backup.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	requireNoError(t, err)
	err = (Client{Address: server.URL, Token: "admin"}).BackupTo(ctx, file)
	file.Close()
	requireNoError(t, err)
	info, err := os.Stat(path)
	require(t, err == nil && !(info.Size() <= maxArchiveBodyBytes), "large fixture missing: %v %v", info, err)
	restored, err := OpenStore(path)
	requireNoError(t, err)
	defer restored.Close()
	var count int
	if err := restored.db.QueryRow("SELECT COUNT(*) FROM messages WHERE length(body)=65536").Scan(&count); err != nil || count != 1100 {
		t.Fatalf("restored messages: %d %v", count, err)
	}
	var integrity string
	if err := restored.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity: %q %v", integrity, err)
	}
	if _, err := restored.AuthenticateScope(ctx, a.scope.ScopeToken); err != nil {
		t.Fatal("backup lost credentials")
	}
}
