package bus

import (
	"context"
	"testing"
	"time"
)

func TestHostedWorkspaceDeletionRevokesAndPreservesOtherWorkspaces(t *testing.T) {
	f := newHostedOAuthFixture(t)
	ctx, a, w := context.Background(), f.h.accounts, f.workspace
	t.Cleanup(func() { a.store.db.ExecContext(ctx, "DELETE FROM hosted_closed_workspaces WHERE scope_id=?", w.ID) })
	other, err := a.CreateWorkspace(ctx, f.user, "Keep this workspace")
	requireNoError(t, err)
	t.Cleanup(func() { a.runtime.DeleteScope(ctx, other.ID) })
	device, err := a.EnrollDevice(ctx, f.user, w.ID, "Laptop")
	requireNoError(t, err)
	session, err := a.StartDeviceSession(ctx, device.Token, "Session", "")
	requireNoError(t, err)
	_, err = a.requestWorkspaceDeletion(ctx, hostedTestUser(t, "stranger"), w.ID, w.Name, false)
	requireCode(t, err, CodePermissionDenied)
	_, err = a.requestWorkspaceDeletion(ctx, f.user, w.ID, "Wrong name", false)
	requireCode(t, err, CodeInvalidArgument)
	done, err := a.requestWorkspaceDeletion(ctx, f.user, w.ID, w.Name, false)
	requireNoError(t, err)
	require(t, !done, "request skipped deletion")
	_, err = a.StartDeviceSession(ctx, device.Token, "Session", "")
	requireCode(t, err, CodeUnauthenticated)
	_, err = a.runtime.Heartbeat(ctx, session.AgentToken, HeartbeatInput{Lifecycle: LifecycleIdle})
	requireCode(t, err, CodeUnauthenticated)
	requireCode(t, a.PauseWorkspace(ctx, f.user, w.ID, false), CodeConflict)
	_, err = a.EnrollDevice(ctx, f.user, w.ID, "Replacement")
	requireCode(t, err, CodeConflict)
	done, err = a.requestWorkspaceDeletion(ctx, f.user, w.ID, w.Name, false)
	requireNoError(t, err)
	require(t, !done, "pending deletion reported complete")
	requireNoError(t, a.Maintain(ctx))
	workspaces, err := a.ListWorkspaces(ctx, f.user)
	requireNoError(t, err)
	require(t, len(workspaces) == 1 && workspaces[0].ID == other.ID, "deletion affected another workspace")
	done, err = a.requestWorkspaceDeletion(ctx, f.user, w.ID, w.Name, false)
	requireNoError(t, err)
	require(t, done, "lost completion response cannot be retried")
	var count int
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_trial_accounts WHERE user_id=?", f.user.ID).Scan(&count))
	require(t, count == 1, "deletion reset the account trial")
}

func TestHostedWorkspaceDeletionWaitsForBillingAndRecovers(t *testing.T) {
	ctx, b, user, w, f := newBillingFixture(t)
	_, err := b.Checkout(ctx, user, w.ID, "month")
	requireNoError(t, err)
	f.pay()
	_, err = b.accounts.requestWorkspaceDeletion(ctx, user, w.ID, w.Name, false)
	requireCode(t, err, CodeConflict)
	_, err = b.accounts.requestWorkspaceDeletion(ctx, user, w.ID, w.Name, true)
	requireNoError(t, err)
	_, err = b.Checkout(ctx, user, w.ID, "year")
	requireCode(t, err, CodeConflict)
	f.mu.Lock()
	f.failReads = true
	f.mu.Unlock()
	requireCode(t, b.accounts.finishWorkspaceDeletion(ctx, w.ID, b), CodeInternal)
	items, err := b.accounts.ListWorkspaces(ctx, user)
	requireNoError(t, err)
	require(t, len(items) == 1 && items[0].Deleting && items[0].Paused, "billing failure discarded content or resumed work")
	f.mu.Lock()
	f.failReads = false
	f.loseCancellation = true
	f.mu.Unlock()
	requireCode(t, b.accounts.finishWorkspaceDeletion(ctx, w.ID, b), CodeInternal)
	requireNoError(t, b.ReconcileDue(ctx))
	items, err = b.accounts.ListWorkspaces(ctx, user)
	requireNoError(t, err)
	require(t, len(items) == 0, "maintenance did not recover lost cancellation response")
	f.mu.Lock()
	cancellations := f.cancellations
	f.mu.Unlock()
	require(t, cancellations == 1, "cancellation was duplicated")
	var customer, subscription string
	requireNoError(t, b.accounts.store.db.QueryRowContext(ctx, "SELECT customer_id,subscription_id FROM hosted_closed_workspaces WHERE scope_id=? AND closed_at>0", w.ID).Scan(&customer, &subscription))
	require(t, customer == f.customer && subscription == "sub_fixture", "payment references were lost")
	payload, signature := billingEvent(t, b, "evt_after_deletion", "customer.subscription.deleted", f.sub)
	requireNoError(t, b.HandleWebhook(ctx, payload, signature))
}

func TestHostedWorkspaceDeletionClosesUnfinishedCheckouts(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "expired-absence"}[absent], func(t *testing.T) {
			ctx, b, user, w, f := newBillingFixture(t)
			_, err := b.Checkout(ctx, user, w.ID, "month")
			requireNoError(t, err)
			if absent {
				f.mu.Lock()
				f.sessions = nil
				f.mu.Unlock()
				_, err = b.accounts.store.db.ExecContext(ctx, "UPDATE hosted_checkout_attempts SET session_id=NULL,created_at=?,expires_at=? WHERE scope_id=?", time.Now().Add(-48*time.Hour).UnixMilli(), time.Now().Add(-24*time.Hour).UnixMilli(), w.ID)
				requireNoError(t, err)
			}
			_, err = b.accounts.requestWorkspaceDeletion(ctx, user, w.ID, w.Name, true)
			requireNoError(t, err)
			requireNoError(t, b.accounts.finishWorkspaceDeletion(ctx, w.ID, b))
			if !absent {
				require(t, f.sessions[0]["status"] == "expired", "checkout survived workspace deletion")
			}
		})
	}
}
