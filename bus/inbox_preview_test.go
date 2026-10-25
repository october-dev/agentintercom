package bus

import (
	"context"
	"path/filepath"
	"testing"
)

func TestInboxPreviewDoesNotDeliver(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "bus.db"))
	requireNoError(t, err)
	defer r.Close()
	ctx := context.Background()
	scope, err := r.CreateScope(ctx, CreateScopeInput{})
	requireNoError(t, err)
	a, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "sender", DisplayName: "sender"})
	requireNoError(t, err)
	b, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "receiver", DisplayName: "receiver", ConnectTo: []string{"sender"}})
	requireNoError(t, err)
	receipt, err := r.SendMessage(ctx, a.AgentToken, SendMessageInput{To: "receiver", Body: "hello\nsecond line"})
	requireNoError(t, err)
	for range 2 {
		preview, err := r.PreviewInbox(ctx, b.AgentToken)
		requireNoError(t, err)
		require(t, len(preview) == 1 && preview[0].ID == receipt.MessageID, "missing preview")
		state, err := r.Receipt(ctx, a.AgentToken, receipt.MessageID)
		requireNoError(t, err)
		require(t, state.State == DeliveryQueued, "preview changed delivery")
	}
	other, err := r.PreviewInbox(ctx, a.AgentToken)
	requireNoError(t, err)
	require(t, len(other) == 0, "preview leaked another inbox")
	_, err = r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "receiver", DisplayName: "replacement"})
	requireNoError(t, err)
	_, err = r.PreviewInbox(ctx, b.AgentToken)
	requireCode(t, err, CodeUnauthenticated)
}
