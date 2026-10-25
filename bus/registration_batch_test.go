package bus

import (
	"context"
	"fmt"
	"testing"
)

func exerciseRegistrationBatch(t *testing.T, r *Runtime) {
	t.Helper()
	ctx := context.Background()
	scope, err := r.CreateScope(ctx, CreateScopeInput{})
	requireNoError(t, err)
	defer r.DeleteScope(ctx, scope.ScopeID)
	peers := make([]string, 50)
	for i := range peers {
		peers[i] = fmt.Sprintf("peer-%02d", i)
		_, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: peers[i], DisplayName: peers[i]})
		requireNoError(t, err)
	}
	before, err := r.Events(ctx, scope.ScopeToken, 0, 100, 0)
	requireNoError(t, err)
	joined, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "joined", DisplayName: "Joined", ConnectTo: append(append([]string{}, peers...), peers[0])})
	requireNoError(t, err)
	linked, err := r.ListPeers(ctx, joined.AgentToken)
	requireNoError(t, err)
	require(t, len(linked) == 50, "lost or duplicated links: %d", len(linked))
	batch, err := r.Events(ctx, scope.ScopeToken, before.CurrentRevision, 100, 0)
	requireNoError(t, err)
	require(t, len(batch.Events) == 51, "expected registration plus 50 link events, got %d", len(batch.Events))
	seen := map[string]bool{}
	for i, event := range batch.Events {
		require(t, event.Revision == before.CurrentRevision+int64(i)+1, "event revision gap")
		require(t, !seen[event.ID], "duplicate event ID")
		seen[event.ID] = true
		if i == 0 {
			require(t, event.Type == "agent.registered", "registration must precede links")
		} else {
			require(t, event.Type == "link.created", "unexpected link event")
		}
	}
	for _, invalid := range []string{"missing", "joined"} {
		_, err = r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "joined", DisplayName: "Invalid replacement", ConnectTo: append(append([]string{}, peers...), invalid)})
		if invalid == "missing" {
			requireCode(t, err, CodeNotFound)
		} else {
			requireCode(t, err, CodeInvalidArgument)
		}
		_, err = r.Principal(ctx, joined.AgentToken)
		requireNoError(t, err) // A rejected batch must not replace the current execution.
		unchanged, err := r.Events(ctx, scope.ScopeToken, batch.CurrentRevision, 100, 0)
		requireNoError(t, err)
		require(t, len(unchanged.Events) == 0 && unchanged.CurrentRevision == batch.CurrentRevision, "failed batch left events or a revision gap")
	}
	_, err = r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "joined", DisplayName: "Reconnected", ConnectTo: peers})
	requireNoError(t, err)
	retry, err := r.Events(ctx, scope.ScopeToken, batch.CurrentRevision, 100, 0)
	requireNoError(t, err)
	require(t, len(retry.Events) == 1 && retry.Events[0].Type == "agent.registered", "reconnect duplicated link events")
}

func TestRegistrationBatchAtomicityAndEvents(t *testing.T) {
	r, err := Open(":memory:")
	requireNoError(t, err)
	defer r.Close()
	exerciseRegistrationBatch(t, r)
}

func TestPostgresRegistrationBatchAtomicityAndEvents(t *testing.T) {
	_, a := hostedTestAccounts(t)
	exerciseRegistrationBatch(t, a.runtime)
}

func TestHostedEventBatchRetentionBoundary(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	w, err := a.CreateWorkspace(ctx, hostedTestUser(t, "events"), "Event retention")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), w.ID)
	tx, err := a.store.beginTx(ctx, w.ID)
	requireNoError(t, err)
	defer tx.Rollback()
	requireNoError(t, appendEvent(ctx, tx, w.ID, "scope.imported", w.ID, nil, nowMillis()))
	_, err = tx.ExecContext(ctx, "UPDATE scopes SET event_revision=100099 WHERE scope_id=?", w.ID)
	requireNoError(t, err)
	requireNoError(t, appendEvents(ctx, tx, w.ID, []pendingEvent{{"link.created", "a:b", nil}, {"link.created", "a:c", nil}, {"link.created", "a:d", nil}}, nowMillis()))
	requireNoError(t, tx.Commit())
	batch, err := a.store.Events(ctx, w.ID, 100099, 100)
	requireNoError(t, err)
	require(t, batch.MinimumCursor == 100 && batch.CurrentRevision == 100102 && len(batch.Events) == 3, "batch skipped retention boundary: %#v", batch)
	var imports int
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE scope_id=? AND event_type='scope.imported'", w.ID).Scan(&imports))
	require(t, imports == 1, "retention removed recovery marker")
}
