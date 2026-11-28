package main

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/october-dev/agentintercom/bus"
)

func TestMCPStdioLeanProfile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	address, _, senderToken, receiverToken, cleanup := startTestServer(t, ctx, "lean")
	defer cleanup()
	lean, _ := connectMCPBridge(t, ctx, address, senderToken, "--tool-profile", "lean")
	defer lean.Close()
	full, _ := connectMCPBridge(t, ctx, address, senderToken)
	defer full.Close()
	receiver := bus.Client{Address: address, Token: receiverToken}

	list, err := lean.ListTools(ctx, nil)
	requireNoError(t, err)
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	require(t, slices.Equal(names, []string{"intercom_channel", "intercom_inbox", "intercom_peers", "intercom_send", "intercom_task"}), "unexpected lean surface: %v", names)
	fullList, err := full.ListTools(ctx, nil)
	requireNoError(t, err)
	fullJSON, err := json.Marshal(fullList)
	requireNoError(t, err)
	leanJSON, err := json.Marshal(list)
	requireNoError(t, err)
	require(t, len(leanJSON) < len(fullJSON), "lean tools/list grew beyond full")
	t.Logf("tools/list JSON: lean %d bytes, full %d bytes", len(leanJSON), len(fullJSON))
	_, err = lean.CallTool(ctx, &mcp.CallToolParams{Name: "message_peer", Arguments: map[string]any{"peer": "receiver", "message": "hidden"}})
	require(t, err != nil, "lean profile allowed calling the hidden full tool")

	call := func(tool string, input map[string]any) map[string]any {
		t.Helper()
		if input == nil {
			input = map[string]any{}
		}
		result := callMCPBridgeTool(t, ctx, lean, "intercom_"+tool, input)
		object, ok := result.StructuredContent.(map[string]any)
		require(t, ok, "%s returned a non-object: %v", tool, result)
		return object
	}
	peers := call("peers", nil)
	status := callMCPBridgeTool(t, ctx, full, "get_node_status", map[string]any{}).StructuredContent.(map[string]any)
	directPeers := callMCPBridgeTool(t, ctx, full, "list_peers", map[string]any{}).StructuredContent.(map[string]any)
	require(t, reflect.DeepEqual(peers["identity"], status["identity"]) && reflect.DeepEqual(peers["peers"], directPeers["peers"]), "unexpected composite identity or peers: %v", peers)
	send := call("send", map[string]any{"peer": "receiver", "message": "Hello"})
	messages, err := receiver.PullInbox(ctx, 10, 0)
	requireNoError(t, err)
	require(t, len(messages) == 1 && messages[0].ID == send["messageId"] && messages[0].Mode == bus.MessageNotify, "unexpected delivery: %v", messages)
	_, err = receiver.AcknowledgeMessages(ctx, []string{messages[0].ID})
	requireNoError(t, err)

	request, err := receiver.SendMessage(ctx, bus.SendMessageInput{To: "sender", Mode: bus.MessageRequest, Body: "Ready?"})
	requireNoError(t, err)
	inbox := call("inbox", map[string]any{"acknowledge": false})
	items, ok := inbox["messages"].([]any)
	require(t, ok && len(items) == 1 && items[0].(map[string]any)["id"] == request.MessageID && inbox["acknowledged"] == float64(0), "unexpected deferred inbox: %v", inbox)
	receipt, err := receiver.Receipt(ctx, request.MessageID)
	requireNoError(t, err)
	require(t, receipt.State == bus.DeliveryDelivered, "deferred pull acknowledged the message: %v", receipt)
	acked := call("inbox", map[string]any{"messageIds": []string{request.MessageID}})
	require(t, acked["acknowledged"] == float64(1), "unexpected explicit acknowledgement: %v", acked)
	call("send", map[string]any{"peer": "receiver", "message": "Ready", "replyTo": request.MessageID})
	messages, err = receiver.PullInbox(ctx, 10, 0)
	requireNoError(t, err)
	require(t, len(messages) == 1 && messages[0].Mode == bus.MessageResponse && messages[0].ResponseTo == request.MessageID, "invalid linked response: %v", messages)
	queued, err := receiver.SendMessage(ctx, bus.SendMessageInput{To: "sender", Body: "Next"})
	requireNoError(t, err)
	require(t, call("inbox", nil)["acknowledged"] == float64(1), "missing default acknowledgement")
	receipt, err = receiver.Receipt(ctx, queued.MessageID)
	requireNoError(t, err)
	require(t, receipt.State == bus.DeliveryAcknowledged, "default acknowledgement not recorded: %v", receipt)
	require(t, call("inbox", nil)["acknowledged"] == float64(0), "empty inbox acknowledged messages")

	task := call("task", map[string]any{"action": "add", "title": "Review"})
	call("task", map[string]any{"action": "claim", "taskId": task["id"]})
	call("task", map[string]any{"action": "progress", "taskId": task["id"], "kind": "note", "text": "Reviewed"})
	done := call("task", map[string]any{"action": "complete", "taskId": task["id"], "note": "Done"})
	require(t, done["status"] == "done", "unexpected task completion: %v", done)
	require(t, len(call("task", map[string]any{"action": "list"})["tasks"].([]any)) == 1, "missing task")

	channel := call("channel", map[string]any{"action": "create", "name": "reviews"})
	call("channel", map[string]any{"action": "subscribe", "channelId": channel["id"], "subscribe": true})
	post := call("channel", map[string]any{"action": "post", "channelId": channel["id"], "body": "Done"})
	page := call("channel", map[string]any{"action": "read", "channelId": channel["id"]})
	posts := page["posts"].([]any)
	require(t, len(posts) == 1 && posts[0].(map[string]any)["id"] == post["id"], "missing channel post: %v", page)
	require(t, len(call("channel", map[string]any{"action": "list"})["channels"].([]any)) > 0, "missing channel")

	compareError := func(session *mcp.ClientSession, name string, input map[string]any) []byte {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
		requireNoError(t, err)
		require(t, result.IsError, "%s lost isError: %v", name, result)
		data, err := json.Marshal(result)
		requireNoError(t, err)
		return data
	}
	directError := compareError(full, "message_peer", map[string]any{"peer": "absent", "message": "Hello"})
	leanError := compareError(lean, "intercom_send", map[string]any{"peer": "absent", "message": "Hello"})
	require(t, string(leanError) == string(directError), "changed upstream error: %s vs %s", leanError, directError)
}

func TestLeanInboxStopsOnUpstreamError(t *testing.T) {
	for _, failure := range []string{"check_inbox", "acknowledge_messages"} {
		t.Run(failure, func(t *testing.T) {
			upstream := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "upstream refusal"}}}
			var calls []string
			var call leanCall = func(name string, _ any) (*mcp.CallToolResult, error) {
				calls = append(calls, name)
				if name == failure {
					return upstream, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"messages":[{"id":"one"}]}`}}}, nil
			}
			result, err := call.inbox(nil)
			requireNoError(t, err)
			want := []string{"check_inbox"}
			if failure == "acknowledge_messages" {
				want = append(want, failure)
			}
			require(t, result == upstream && slices.Equal(calls, want), "altered error or unexpected calls: %v %v", result, calls)
		})
	}
}

func TestLeanProfileRequiresItsUnderlyingTools(t *testing.T) {
	err := addLeanTools(newMCPBridgeServer(leanBridgeInstructions), map[string]*mcp.Tool{}, nil)
	require(t, err != nil, "advertised composites without their underlying tools")
	err = runMCPStdio(context.Background(), "--tool-profile", "unknown")
	require(t, err != nil && err.Error() == "--tool-profile must be full or lean", "invalid profile: %v", err)
}
