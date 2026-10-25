package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func hostedTestAccounts(t *testing.T) (context.Context, *HostedAccounts) {
	t.Helper()
	dsn := os.Getenv("OCTOBER_BUS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set OCTOBER_BUS_TEST_POSTGRES_URL to an isolated loopback october_bus_test database")
	}
	u, err := url.Parse(dsn)
	require(t, err == nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") && u.Path == "/october_bus_test", "hosted tests require an isolated database")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	requireNoError(t, MigratePostgres(ctx, dsn))
	runtime, err := OpenPostgres(ctx, dsn)
	requireNoError(t, err)
	t.Cleanup(func() { runtime.Close() })
	accounts, err := NewHostedAccounts(runtime, bytes.Repeat([]byte{42}, 32))
	requireNoError(t, err)
	return ctx, accounts
}

func hostedTestUser(t *testing.T, name string) HostedUser {
	t.Helper()
	id, err := randomID("user_")
	requireNoError(t, err)
	return HostedUser{ID: id, Email: name + "@example.test"}
}

func TestHostedOwnershipLimitCoversConcurrentTransfers(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	owner, recipient := hostedTestUser(t, "owner"), hostedTestUser(t, "recipient")
	create := func(user HostedUser) Workspace {
		w, err := a.CreateWorkspace(ctx, user, "Ownership limit")
		requireNoError(t, err)
		t.Cleanup(func() { _, _ = a.runtime.DeleteScope(context.Background(), w.ID) })
		return w
	}
	first := create(recipient)
	create(recipient)
	offers := []Workspace{create(owner), create(owner)}
	for _, w := range offers {
		invite, err := a.Invite(ctx, owner, w.ID, recipient.Email, "member")
		requireNoError(t, err)
		_, err = a.AcceptInvite(ctx, recipient, invite.Token)
		requireNoError(t, err)
	}
	// Both promotions and creation compete for the same final ownership slot.
	// A workspace lock alone cannot serialize these three different scopes.
	type result struct {
		workspace Workspace
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 3)
	for _, w := range offers {
		go func() {
			<-start
			results <- result{err: a.ChangeMember(ctx, owner, w.ID, recipient.ID, "owner")}
		}()
	}
	go func() {
		<-start
		w, err := a.CreateWorkspace(ctx, recipient, "Competing creation")
		results <- result{w, err}
	}()
	close(start)
	success := 0
	for range 3 {
		r := <-results
		if r.workspace.ID != "" {
			t.Cleanup(func() { _, _ = a.runtime.DeleteScope(context.Background(), r.workspace.ID) })
		}
		if r.err == nil {
			success++
		} else {
			requireCode(t, r.err, CodeBackpressure)
		}
	}
	require(t, success == 1, "expected exactly one ownership grant, got %d", success)
	workspaces, err := a.ListWorkspaces(ctx, recipient)
	requireNoError(t, err)
	owned := 0
	for _, w := range workspaces {
		if w.Role == "owner" {
			owned++
		} else {
			require(t, w.Role == "member", "rejected promotion changed membership")
			requireCode(t, a.ChangeMember(ctx, owner, w.ID, recipient.ID, "owner"), CodeBackpressure)
		}
	}
	require(t, owned == 3, "ownership cap exceeded: %d", owned)
	_, err = a.CreateWorkspace(ctx, recipient, "Over limit")
	requireCode(t, err, CodeBackpressure)
	// Reapplying an existing owner role does not consume another slot.
	requireNoError(t, a.ChangeMember(ctx, recipient, first.ID, recipient.ID, "owner"))
}

func TestHostedAccountsMembershipAndDeviceRevocation(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	owner, member, stranger := hostedTestUser(t, "owner"), hostedTestUser(t, "member"), hostedTestUser(t, "stranger")
	w, err := a.CreateWorkspace(ctx, owner, "Shared work")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), w.ID)
	other, err := a.CreateWorkspace(ctx, stranger, "Other workspace")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), other.ID)
	_, err = a.Members(ctx, stranger, w.ID)
	requireCode(t, err, CodePermissionDenied)
	_, err = a.EnrollDevice(ctx, stranger, w.ID, "Intruder")
	requireCode(t, err, CodePermissionDenied)
	invite, err := a.Invite(ctx, owner, w.ID, member.Email, "member")
	requireNoError(t, err)
	_, err = a.AcceptInvite(ctx, stranger, invite.Token)
	requireCode(t, err, CodePermissionDenied)
	// Single-use invitations remain single-use under concurrent acceptance.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := a.AcceptInvite(ctx, member, invite.Token); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			requireCode(t, err, CodeNotFound)
		}
	}
	require(t, success == 1, "invite accepted %d times", success)
	requireCode(t, a.ChangeMember(ctx, member, w.ID, member.ID, "owner"), CodePermissionDenied)
	requireCode(t, a.ChangeMember(ctx, owner, w.ID, owner.ID, ""), CodeConflict)
	first, err := a.EnrollDevice(ctx, owner, w.ID, "Laptop")
	requireNoError(t, err)
	second, err := a.EnrollDevice(ctx, member, w.ID, "Second machine")
	requireNoError(t, err)
	x, err := a.StartDeviceSession(ctx, first.Token, "First app", "")
	requireNoError(t, err)
	y, err := a.StartDeviceSession(ctx, second.Token, "Second app", "")
	requireNoError(t, err)
	_, err = a.StartDeviceSession(ctx, second.Token, "Impersonation", x.AgentID)
	requireCode(t, err, CodePermissionDenied)
	receipt, err := a.runtime.SendMessage(ctx, x.AgentToken, SendMessageInput{To: y.AgentID, Body: "Please review", Mode: MessageRequest, IdempotencyKey: "one"})
	requireNoError(t, err)
	task, err := a.runtime.AddTask(ctx, y.AgentToken, AddTaskInput{Title: "Owned work"})
	requireNoError(t, err)
	_, err = a.runtime.ClaimTask(ctx, y.AgentToken, task.ID)
	requireNoError(t, err)
	resumed, err := a.StartDeviceSession(ctx, first.Token, "Reconnected first app", x.AgentID)
	requireNoError(t, err)
	_, err = a.runtime.Principal(ctx, x.AgentToken)
	requireCode(t, err, CodeUnauthenticated)
	requireNoError(t, a.ChangeMember(ctx, owner, w.ID, member.ID, ""))
	_, err = a.runtime.Heartbeat(ctx, y.AgentToken, HeartbeatInput{Lifecycle: LifecycleIdle})
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.StartDeviceSession(ctx, second.Token, "Rejoin", "")
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.runtime.ClaimTask(ctx, resumed.AgentToken, task.ID)
	requireNoError(t, err)
	_, err = a.runtime.Receipt(ctx, resumed.AgentToken, receipt.MessageID)
	requireNoError(t, err)
	devices, err := a.Devices(ctx, owner, w.ID)
	requireNoError(t, err)
	data, _ := json.Marshal(devices)
	require(t, !strings.Contains(string(data), first.Token) && !strings.Contains(string(data), resumed.AgentToken), "connection list exposed credentials")
	requireNoError(t, a.RevokeDevice(ctx, owner, w.ID, first.ID))
	_, err = a.runtime.Principal(ctx, resumed.AgentToken)
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.StartDeviceSession(ctx, first.Token, "Revoked", "")
	requireCode(t, err, CodeUnauthenticated)
}

func TestHostedAllowanceCountsDeliveriesNotRetriesOrReplies(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	owner := hostedTestUser(t, "allowance")
	w, err := a.CreateWorkspace(ctx, owner, "Allowance")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), w.ID)
	d, err := a.EnrollDevice(ctx, owner, w.ID, "Machine")
	requireNoError(t, err)
	x, err := a.StartDeviceSession(ctx, d.Token, "Sender", "")
	requireNoError(t, err)
	y, err := a.StartDeviceSession(ctx, d.Token, "Recipient", "")
	requireNoError(t, err)
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET delivery_limit=1 WHERE scope_id=?", w.ID)
	requireNoError(t, err)
	input := SendMessageInput{To: y.AgentID, Body: "One request", Mode: MessageRequest, IdempotencyKey: "retry"}
	first, err := a.runtime.SendMessage(ctx, x.AgentToken, input)
	requireNoError(t, err)
	retry, err := a.runtime.SendMessage(ctx, x.AgentToken, input)
	requireNoError(t, err)
	require(t, first.MessageID == retry.MessageID, "retry duplicated work")
	_, err = a.runtime.SendMessage(ctx, x.AgentToken, SendMessageInput{To: y.AgentID, Body: "Over allowance"})
	requireCode(t, err, CodeBackpressure)
	inbox, err := a.runtime.ReserveInbox(ctx, y.AgentToken, 10, 0)
	requireNoError(t, err)
	require(t, inbox != nil, "no request delivered")
	_, err = a.runtime.CommitInbox(ctx, y.AgentToken, inbox.ID)
	requireNoError(t, err)
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET trial_ends_at=1 WHERE scope_id=?", w.ID)
	requireNoError(t, err)
	_, err = a.runtime.SendMessage(ctx, y.AgentToken, SendMessageInput{To: x.AgentID, Body: "Finished", Mode: MessageResponse, ResponseTo: first.MessageID})
	requireNoError(t, err)
	_, err = a.runtime.AcknowledgeMessages(ctx, y.AgentToken, []string{first.MessageID})
	requireNoError(t, err)
	var count int
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT SUM(deliveries) FROM hosted_usage WHERE scope_id=?", w.ID).Scan(&count))
	require(t, count == 1, "charged %d deliveries for one request", count)
	_, err = a.StartDeviceSession(ctx, d.Token, "Expired trial", "")
	requireCode(t, err, CodePermissionDenied)
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_devices SET expires_at=1 WHERE device_id=?", d.ID)
	requireNoError(t, err)
	_, err = a.runtime.Heartbeat(ctx, x.AgentToken, HeartbeatInput{Lifecycle: LifecycleIdle})
	requireCode(t, err, CodeUnauthenticated)
}
