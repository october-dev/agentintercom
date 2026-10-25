package bus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func exerciseChannels(t *testing.T, r *Runtime) {
	t.Helper()
	ctx := context.Background()
	scope, err := r.CreateScope(ctx, CreateScopeInput{})
	requireNoError(t, err)
	defer r.DeleteScope(ctx, scope.ScopeID)
	sender, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "sender", DisplayName: "Sender"})
	requireNoError(t, err)
	subscriber, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "subscriber", DisplayName: "Subscriber"})
	requireNoError(t, err)
	outsider, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "outsider", DisplayName: "Not subscribed"})
	requireNoError(t, err)
	channel, err := r.CreateChannel(ctx, sender.AgentToken, CreateChannelInput{Name: "project", Description: "Shared project work"})
	requireNoError(t, err)
	_, err = r.CreateChannel(ctx, sender.AgentToken, CreateChannelInput{Name: "project"})
	requireCode(t, err, CodeConflict)
	requireNoError(t, r.SubscribeChannel(ctx, subscriber.AgentToken, channel.ID, true))
	requireNoError(t, r.SubscribeChannel(ctx, subscriber.AgentToken, channel.ID, true))
	input := PostChannelInput{ChannelID: channel.ID, Body: "Résumé 100% complete", IdempotencyKey: "post-once"}
	results := make(chan ChannelPost, 2)
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			post, err := r.PostChannel(ctx, sender.AgentToken, input)
			results <- post
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		requireNoError(t, err)
	}
	var root ChannelPost
	for post := range results {
		if root.ID != "" {
			require(t, root.ID == post.ID, "retry created another post")
		}
		root = post
	}
	_, err = r.PostChannel(ctx, sender.AgentToken, PostChannelInput{ChannelID: channel.ID, Body: "Changed", IdempotencyKey: "post-once"})
	requireCode(t, err, CodeConflict)
	inbox, err := r.ReserveInbox(ctx, subscriber.AgentToken, 10, 0)
	requireNoError(t, err)
	require(t, inbox != nil && len(inbox.Messages) == 1 && strings.Contains(inbox.Messages[0].Body, root.ID), "subscription did not produce exactly one durable notification")
	empty, err := r.ReserveInbox(ctx, outsider.AgentToken, 10, 0)
	requireNoError(t, err)
	require(t, empty == nil, "unsubscribed session was notified")
	peers, err := r.ListPeers(ctx, subscriber.AgentToken)
	requireNoError(t, err)
	require(t, len(peers) == 0, "channel subscription changed direct-message authorization")
	reply, err := r.PostChannel(ctx, subscriber.AgentToken, PostChannelInput{ChannelID: channel.ID, Body: "Looks good", ThreadID: root.ID})
	requireNoError(t, err)
	_, err = r.PostChannel(ctx, sender.AgentToken, PostChannelInput{ChannelID: channel.ID, Body: "Wrong nested thread", ThreadID: reply.ID})
	requireCode(t, err, CodeInvalidArgument)
	page, err := r.ChannelHistory(ctx, outsider.AgentToken, ChannelHistoryInput{ChannelID: channel.ID})
	requireNoError(t, err)
	require(t, len(page.Posts) == 1 && page.Posts[0].Replies == 1, "channel roots or reply count incorrect")
	page, err = r.ChannelHistory(ctx, sender.AgentToken, ChannelHistoryInput{ChannelID: channel.ID, ThreadID: root.ID, Limit: 1})
	requireNoError(t, err)
	require(t, len(page.Posts) == 1 && page.NextCursor != "", "thread cursor missing")
	second, err := r.ChannelHistory(ctx, sender.AgentToken, ChannelHistoryInput{ChannelID: channel.ID, ThreadID: root.ID, Limit: 1, Before: page.NextCursor})
	requireNoError(t, err)
	require(t, len(second.Posts) == 1 && second.Posts[0].ID != page.Posts[0].ID, "thread pagination repeated or skipped")
	for _, query := range []string{"RÉSUMÉ", "100%"} {
		page, err := r.ChannelHistory(ctx, sender.AgentToken, ChannelHistoryInput{Query: query})
		requireNoError(t, err)
		require(t, len(page.Posts) == 1 && page.Posts[0].ID == root.ID, "literal Unicode search failed")
	}
	other, err := r.CreateScope(ctx, CreateScopeInput{})
	requireNoError(t, err)
	defer r.DeleteScope(ctx, other.ScopeID)
	intruder, err := r.RegisterAgent(ctx, other.ScopeToken, RegisterAgentInput{ID: "intruder", DisplayName: "Intruder"})
	requireNoError(t, err)
	_, err = r.ChannelHistory(ctx, intruder.AgentToken, ChannelHistoryInput{ChannelID: channel.ID})
	requireCode(t, err, CodeNotFound)
	requireCode(t, r.SubscribeChannel(ctx, intruder.AgentToken, channel.ID, true), CodeNotFound)
	// A resumed identity keeps its subscriptions but fences the old execution.
	replacement, err := r.RegisterAgent(ctx, scope.ScopeToken, RegisterAgentInput{ID: "subscriber", DisplayName: "Resumed"})
	requireNoError(t, err)
	requireCode(t, r.SubscribeChannel(ctx, subscriber.AgentToken, channel.ID, false), CodeUnauthenticated)
	channels, err := r.Channels(ctx, replacement.AgentToken)
	requireNoError(t, err)
	require(t, len(channels) == 1 && channels[0].Subscribed, "subscription lost on resume")
	// Portable export carries both shared content and durable subscriptions.
	for _, token := range []string{sender.AgentToken, replacement.AgentToken, outsider.AgentToken} {
		requireNoError(t, r.RetireAgent(ctx, token))
	}
	archive, err := r.ExportScope(ctx, scope.ScopeID)
	requireNoError(t, err)
	require(t, archive.Version == 3 && len(archive.ChannelPosts) == 2 && len(archive.ChannelSubscriptions) == 1, "portable export dropped channel data")
	destination, err := Open(filepath.Join(t.TempDir(), "restore.db"))
	requireNoError(t, err)
	defer destination.Close()
	restored, err := destination.ImportScope(ctx, archive)
	requireNoError(t, err)
	resumed, err := destination.RegisterAgent(ctx, restored.ScopeToken, RegisterAgentInput{ID: "sender", DisplayName: "Restored"})
	requireNoError(t, err)
	duplicate, err := destination.PostChannel(ctx, resumed.AgentToken, input)
	requireNoError(t, err)
	require(t, duplicate.ID == root.ID, "import lost post retry deduplication")
	// A new reply keeps the whole thread. Only old, quiet threads are pruned.
	store := destination.store.(*Store)
	_, err = store.db.ExecContext(ctx, "UPDATE channel_posts SET created_at=? WHERE scope_id=? AND post_id=?", nowMillis()-int64(10*24*time.Hour/time.Millisecond), scope.ScopeID, root.ID)
	requireNoError(t, err)
	result, err := destination.PruneScope(ctx, restored.ScopeToken, PruneScopeInput{Before: time.Now().UTC().Format(time.RFC3339Nano), Execute: true})
	requireNoError(t, err)
	require(t, result.Records.ChannelPosts == 0, "active thread was partially pruned")
	_, err = store.db.ExecContext(ctx, "UPDATE channel_posts SET created_at=? WHERE scope_id=?", nowMillis()-int64(10*24*time.Hour/time.Millisecond), scope.ScopeID)
	requireNoError(t, err)
	result, err = destination.PruneScope(ctx, restored.ScopeToken, PruneScopeInput{Before: time.Now().UTC().Format(time.RFC3339Nano), Execute: true})
	requireNoError(t, err)
	require(t, result.Records.ChannelPosts == 2, "old thread was not pruned atomically")
}
func TestChannelsThreadsAndPortableHistory(t *testing.T) {
	r, err := Open(":memory:")
	requireNoError(t, err)
	defer r.Close()
	exerciseChannels(t, r)
}
func TestPostgresChannelsThreadsAndPortableHistory(t *testing.T) {
	_, a := hostedTestAccounts(t)
	exerciseChannels(t, a.runtime)
}

func TestChannelMCPAndHTTP(t *testing.T) {
	a := setupAgents(t, ":memory:")
	defer a.runtime.Close()
	ctx := context.Background()
	server := httptest.NewServer(NewServer(a.runtime, ServerOptions{}))
	defer server.Close()
	client := Client{Address: server.URL, Token: a.plannerToken}
	channel, err := client.CreateChannel(ctx, CreateChannelInput{Name: "updates"})
	requireNoError(t, err)
	httpClient := &http.Client{Transport: bearerTransport{token: a.reviewerToken, base: http.DefaultTransport}}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "channel-qualification", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient}, nil)
	requireNoError(t, err)
	defer session.Close()
	subscribed, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "subscribe_channel", Arguments: map[string]any{"channelId": channel.ID, "subscribe": true}})
	requireNoError(t, err)
	require(t, !subscribed.IsError, "MCP subscription failed")
	post, err := client.PostChannel(ctx, PostChannelInput{ChannelID: channel.ID, Body: "Shared update"})
	requireNoError(t, err)
	read, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_channel", Arguments: map[string]any{"channelId": channel.ID}})
	requireNoError(t, err)
	require(t, !read.IsError, "MCP read failed")
	reply, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "post_channel", Arguments: map[string]any{"channelId": channel.ID, "body": "Reviewed", "threadId": post.ID}})
	requireNoError(t, err)
	require(t, !reply.IsError, "MCP thread reply failed")
	page, err := client.ChannelHistory(ctx, ChannelHistoryInput{ChannelID: channel.ID, ThreadID: post.ID})
	requireNoError(t, err)
	require(t, len(page.Posts) == 2, "MCP/HTTP channel history diverged")
}

func TestHostedChannelFanoutIsAtomicAndMemberBound(t *testing.T) {
	ctx, a := hostedTestAccounts(t)
	user := hostedTestUser(t, "channel-owner")
	workspace, err := a.CreateWorkspace(ctx, user, "Shared channels")
	requireNoError(t, err)
	defer a.runtime.DeleteScope(context.Background(), workspace.ID)
	device, err := a.EnrollDevice(ctx, user, workspace.ID, "Machine")
	requireNoError(t, err)
	first, err := a.StartDeviceSession(ctx, device.Token, "First", "")
	requireNoError(t, err)
	second, err := a.StartDeviceSession(ctx, device.Token, "Second", "")
	requireNoError(t, err)
	channel, err := a.store.CreateChannel(withHostedMember(ctx, user), workspace.ID, CreateChannelInput{Name: "shared"})
	requireNoError(t, err)
	for _, token := range []string{first.AgentToken, second.AgentToken} {
		requireNoError(t, a.runtime.SubscribeChannel(ctx, token, channel.ID, true))
	}
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET delivery_limit=1 WHERE scope_id=?", workspace.ID)
	requireNoError(t, err)
	input := PostChannelInput{ChannelID: channel.ID, Body: "Notify both sessions", IdempotencyKey: "human-post"}
	_, err = a.store.PostChannel(withHostedMember(ctx, user), workspace.ID, input)
	requireCode(t, err, CodeBackpressure)
	var posts, deliveries int
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM channel_posts WHERE scope_id=?", workspace.ID).Scan(&posts))
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(deliveries),0) FROM hosted_usage WHERE scope_id=?", workspace.ID).Scan(&deliveries))
	require(t, posts == 0 && deliveries == 0, "failed fanout left partial content or charges")
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET delivery_limit=3 WHERE scope_id=?", workspace.ID)
	requireNoError(t, err)
	post, err := a.store.PostChannel(withHostedMember(ctx, user), workspace.ID, input)
	requireNoError(t, err)
	require(t, post.AuthorKind == MessageParticipantHuman && post.AuthorID == user.ID, "human attribution lost")
	_, err = a.store.PostChannel(withHostedMember(ctx, user), workspace.ID, input)
	requireNoError(t, err)
	requireNoError(t, a.store.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(deliveries),0) FROM hosted_usage WHERE scope_id=?", workspace.ID).Scan(&deliveries))
	require(t, deliveries == 2, "retry billed notifications twice")
	outsider := hostedTestUser(t, "channel-outsider")
	_, err = a.store.ChannelHistory(withHostedMember(ctx, outsider), workspace.ID, ChannelHistoryInput{ChannelID: channel.ID})
	requireCode(t, err, CodePermissionDenied)
	_, err = a.store.db.ExecContext(ctx, "UPDATE hosted_workspaces SET disabled_at=? WHERE scope_id=?", nowMillis(), workspace.ID)
	requireNoError(t, err)
	_, err = a.store.CreateChannel(withHostedMember(ctx, user), workspace.ID, CreateChannelInput{Name: "inactive"})
	requireCode(t, err, CodePermissionDenied)
	_, err = a.store.PostChannel(withHostedMember(ctx, user), workspace.ID, input)
	requireNoError(t, err) // Safe retry still resolves.
	_, err = a.store.PostChannel(withHostedMember(ctx, user), workspace.ID, PostChannelInput{ChannelID: channel.ID, Body: "New unpaid work"})
	requireCode(t, err, CodePermissionDenied)
	_, err = a.store.ChannelHistory(withHostedMember(ctx, user), workspace.ID, ChannelHistoryInput{ChannelID: channel.ID})
	requireNoError(t, err)
}
