package bus

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostedDevicePairingApprovalBoundary(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	user, stranger := hostedTestUser(t, "pair-owner"), hostedTestUser(t, "pair-stranger")
	first, err := a.CreateWorkspace(ctx, user, "First")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), first.ID)
	second, err := a.CreateWorkspace(ctx, user, "Second")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), second.ID)
	pending, err := a.BeginDevicePairing(ctx, "My laptop")
	requireNoError(t, err)
	public, err := a.DevicePairing(ctx, pending.UserCode)
	requireNoError(t, err)
	require(t, public.DeviceCode == "" && public.Name == "My laptop", "approval details exposed a device secret")
	_, err = a.PollDevicePairing(ctx, pending.UserCode)
	requireCode(t, err, CodeUnauthenticated)
	poll, err := a.PollDevicePairing(ctx, pending.DeviceCode)
	requireNoError(t, err)
	require(t, poll.Status == "pending" && poll.Device == nil, "credential issued before consent")
	_, err = a.PollDevicePairing(ctx, pending.DeviceCode)
	requireCode(t, err, CodeBackpressure)
	requireCode(t, a.ApproveDevicePairing(ctx, stranger, pending.UserCode, first.ID, true), CodePermissionDenied)
	// One code cannot authorize two devices, even if concurrent approvals choose
	// different workspace locks.
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for _, scope := range []string{first.ID, second.ID} {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- a.ApproveDevicePairing(ctx, user, pending.UserCode, scope, true) }()
	}
	wg.Wait()
	close(errors)
	success := 0
	for err := range errors {
		if err == nil {
			success++
		} else {
			requireCode(t, err, CodeConflict)
		}
	}
	require(t, success == 1, "pairing approved %d times", success)
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_pairings SET last_poll_at=0 WHERE pairing_id=?", pending.ID)
	requireNoError(t, err)
	poll, err = a.PollDevicePairing(ctx, pending.DeviceCode)
	requireNoError(t, err)
	require(t, poll.Status == "approved" && poll.Device != nil && poll.Device.Token != "", "approved credential missing")
	firstToken := poll.Device.Token
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_pairings SET last_poll_at=0 WHERE pairing_id=?", pending.ID)
	requireNoError(t, err)
	retry, err := a.PollDevicePairing(ctx, pending.DeviceCode)
	requireNoError(t, err)
	require(t, retry.Device.Token == firstToken, "lost-response retry issued another credential")
	var encrypted string
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT credential FROM hosted_pairings WHERE pairing_id=?", pending.ID).Scan(&encrypted))
	require(t, !strings.Contains(encrypted, firstToken), "pairing token stored in plaintext")
	requireNoError(t, a.RevokeDevice(ctx, user, poll.WorkspaceID, poll.Device.ID))
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_pairings SET last_poll_at=0 WHERE pairing_id=?", pending.ID)
	requireNoError(t, err)
	_, err = a.PollDevicePairing(ctx, pending.DeviceCode)
	requireCode(t, err, CodeUnauthenticated)
	declined, err := a.BeginDevicePairing(ctx, "Declined")
	requireNoError(t, err)
	defer a.store.db.ExecContext(context.Background(), "DELETE FROM hosted_pairings WHERE pairing_id=?", declined.ID)
	requireNoError(t, a.ApproveDevicePairing(ctx, user, declined.UserCode, "", false))
	poll, err = a.PollDevicePairing(ctx, declined.DeviceCode)
	requireNoError(t, err)
	require(t, poll.Status == "denied" && poll.Device == nil, "declined request issued a token")
	expired, err := a.BeginDevicePairing(ctx, "Expired")
	requireNoError(t, err)
	defer a.store.db.ExecContext(context.Background(), "DELETE FROM hosted_pairings WHERE pairing_id=?", expired.ID)
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_pairings SET expires_at=? WHERE pairing_id=?", nowMillis()-1, expired.ID)
	requireNoError(t, err)
	requireCode(t, a.ApproveDevicePairing(ctx, user, expired.UserCode, first.ID, true), CodeConflict)
	_, err = a.PollDevicePairing(ctx, expired.DeviceCode)
	requireCode(t, err, CodeUnauthenticated)
}

func TestHostedPairedDeviceSessionLifecycle(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	user := hostedTestUser(t, "device-http")
	workspace, err := a.CreateWorkspace(ctx, user, "Machines")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), workspace.ID)
	h, err := NewHostedServer(a, HostedServerOptions{PublicURL: "https://bus.example.test", Identity: testHostedIdentity{"account-token": user}})
	requireNoError(t, err)
	server := httptest.NewServer(h)
	defer server.Close()
	var beats atomic.Int32
	recovered := make(chan struct{})
	transport := sessionTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/me/heartbeat" {
			switch beats.Add(1) {
			case 2:
				return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error":{"code":"INTERNAL","message":"temporary outage"}}`))}, nil
			case 3:
				close(recovered)
			}
		}
		clone := r.Clone(r.Context())
		clone.Host = "bus.example.test"
		return http.DefaultTransport.RoundTrip(clone)
	})
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	client := Client{Address: server.URL, HTTP: httpClient}
	pairing, err := client.BeginDevicePairing(ctx, "Device")
	requireNoError(t, err)
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/device/authorizations/"+pairing.UserCode, nil)
	requireNoError(t, err)
	denied, err := httpClient.Do(req)
	requireNoError(t, err)
	denied.Body.Close()
	require(t, denied.StatusCode == 401, "approval details exposed without account login")
	input, _ := json.Marshal(map[string]any{"approve": true, "workspaceId": workspace.ID})
	req, err = http.NewRequestWithContext(ctx, "POST", server.URL+"/api/device/authorizations/"+pairing.UserCode, strings.NewReader(string(input)))
	requireNoError(t, err)
	req.Header.Set("Authorization", "Bearer account-token")
	req.Header.Set("Content-Type", "application/json")
	approved, err := httpClient.Do(req)
	requireNoError(t, err)
	approved.Body.Close()
	require(t, approved.StatusCode == 200, "browser approval failed")
	result, err := client.PollDevicePairing(ctx, pairing.DeviceCode)
	requireNoError(t, err)
	options := AgentSessionOptions{Address: server.URL, DeviceToken: result.Device.Token, HTTP: httpClient, Registration: RegisterAgentInput{DisplayName: "First window", LeaseMS: 300000}, InitialLifecycle: LifecycleReady, InitialReady: true, HeartbeatInterval: 20 * time.Millisecond}
	first, err := StartAgentSession(ctx, options)
	requireNoError(t, err)
	defer first.Close(context.Background())
	select {
	case <-recovered:
	case <-time.After(3 * time.Second):
		t.Fatal("hosted session did not retry transient heartbeat failure")
	}
	options.Registration.DisplayName = "Second window"
	second, err := StartAgentSession(ctx, options)
	requireNoError(t, err)
	defer second.Close(context.Background())
	require(t, first.Registration.AgentID != second.Registration.AgentID, "device windows shared identity")
	receipt, err := first.Client.SendMessage(ctx, SendMessageInput{To: second.Registration.AgentID, Body: "Across connected sessions"})
	requireNoError(t, err)
	require(t, receipt.State == DeliveryQueued, "message not queued")
	requireNoError(t, second.Close(ctx))
	peers, err := first.Client.ListPeers(ctx)
	requireNoError(t, err)
	require(t, len(peers) == 1 && !peers[0].Reachable, "closing one window left its lease alive")
	client.Token = result.Device.Token
	requireNoError(t, client.RevokeDevice(ctx))
	select {
	case <-first.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not stop hosted session")
	}
	_, err = client.StartDeviceSession(ctx, "Rejoin", "")
	requireCode(t, err, CodeUnauthenticated)
}

func TestHostedDeviceReplacementPreservesInboxesAndFencesAuthority(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	owner, member := hostedTestUser(t, "replace-owner"), hostedTestUser(t, "replace-member")
	w, err := a.CreateWorkspace(ctx, owner, "Recovery")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), w.ID)
	invite, err := a.Invite(ctx, owner, w.ID, member.Email, "member")
	requireNoError(t, err)
	_, err = a.AcceptInvite(ctx, member, invite.Token)
	requireNoError(t, err)
	lost, err := a.EnrollDevice(ctx, member, w.ID, "Old laptop")
	requireNoError(t, err)
	other, err := a.EnrollDevice(ctx, owner, w.ID, "Other machine")
	requireNoError(t, err)
	worker, err := a.StartDeviceSession(ctx, lost.Token, "Work to keep", "")
	requireNoError(t, err)
	sender, err := a.StartDeviceSession(ctx, other.Token, "Sender", "")
	requireNoError(t, err)
	message, err := a.runtime.SendMessage(ctx, sender.AgentToken, SendMessageInput{To: worker.AgentID, Body: "Keep this pending request", Mode: MessageRequest})
	requireNoError(t, err)
	reserved, err := a.runtime.ReserveInbox(ctx, worker.AgentToken, 10, 0)
	requireNoError(t, err)
	require(t, reserved != nil, "expected a reserved delivery")
	task, err := a.runtime.AddTask(ctx, worker.AgentToken, AddTaskInput{Title: "Release old execution claim"})
	requireNoError(t, err)
	_, err = a.runtime.ClaimTask(ctx, worker.AgentToken, task.ID)
	requireNoError(t, err)
	pending, err := a.BeginDevicePairing(ctx, "Replacement laptop")
	requireNoError(t, err)
	// Workspace ownership is not permission to impersonate another member's
	// session, and a failed takeover must leave the pairing usable by its owner.
	requireCode(t, a.completeDevicePairing(ctx, owner, pending.UserCode, w.ID, lost.ID, true), CodePermissionDenied)
	requireCode(t, a.completeDevicePairing(ctx, member, pending.UserCode, w.ID, other.ID, true), CodePermissionDenied)
	requireNoError(t, a.completeDevicePairing(ctx, member, pending.UserCode, w.ID, lost.ID, true))
	paired, err := a.PollDevicePairing(ctx, pending.DeviceCode)
	requireNoError(t, err)
	require(t, paired.Device != nil && paired.Device.ID == lost.ID && paired.Device.Token != lost.Token, "replacement did not rotate the original device")
	_, err = a.StartDeviceSession(ctx, lost.Token, "Stolen credentials", worker.AgentID)
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.runtime.Heartbeat(ctx, worker.AgentToken, HeartbeatInput{Lifecycle: LifecycleReady, Ready: true})
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.runtime.CommitInbox(ctx, worker.AgentToken, reserved.ID)
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.runtime.ClaimTask(ctx, sender.AgentToken, task.ID)
	requireNoError(t, err)
	resumed, err := a.StartDeviceSession(ctx, paired.Device.Token, "Restored session", worker.AgentID)
	requireNoError(t, err)
	inbox, err := a.runtime.ReserveInbox(ctx, resumed.AgentToken, 10, 0)
	requireNoError(t, err)
	require(t, inbox != nil && len(inbox.Messages) == 1 && inbox.Messages[0].ID == message.MessageID, "pending inbox was not preserved")
	_, err = a.runtime.CommitInbox(ctx, resumed.AgentToken, inbox.ID)
	requireNoError(t, err)
	// Explicit recovery also works after revocation (including a restore fence),
	// but still needs account consent and cannot revive the old credential.
	requireNoError(t, a.RevokeDevice(ctx, member, w.ID, lost.ID))
	next, err := a.BeginDevicePairing(ctx, "Recovered device")
	requireNoError(t, err)
	requireNoError(t, a.completeDevicePairing(ctx, member, next.UserCode, w.ID, lost.ID, true))
	recovered, err := a.PollDevicePairing(ctx, next.DeviceCode)
	requireNoError(t, err)
	_, err = a.StartDeviceSession(ctx, paired.Device.Token, "Previous replacement", worker.AgentID)
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.StartDeviceSession(ctx, recovered.Device.Token, "Recovered session", worker.AgentID)
	requireNoError(t, err)
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_pairings SET last_poll_at=0 WHERE pairing_id=?", pending.ID)
	requireNoError(t, err)
	_, err = a.PollDevicePairing(ctx, pending.DeviceCode)
	requireCode(t, err, CodeUnauthenticated)
}
